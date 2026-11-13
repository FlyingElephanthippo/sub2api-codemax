import { buildGatewayUrl } from './url'

export type VideoTaskStatus = 'pending' | 'done' | 'failed'
export interface VideoTask {
  id: string
  status: VideoTaskStatus
  progress: number
  error?: string
}
export interface VideoGenerateRequest {
  model: string
  prompt: string
  duration: number
  aspect_ratio: string
  resolution?: string
  image?: { url: string }
}

const MAX_REFERENCE_BYTES = 10 * 1024 * 1024
const MAX_VIDEO_BYTES = 200 * 1024 * 1024
const VIDEO_ID = /^[a-zA-Z0-9_-]{1,200}$/
export const VIDEO_CONTENT_TIMEOUT_MS = 180_000
export interface VideoContentProgress { received: number; total?: number }

export function isVideoModel(id: string): boolean {
  return id === 'muse-video' || /^grok-imagine-video(?:[-.][\w.-]+)?$/i.test(id)
}

function taskID(value: unknown): string {
  if (typeof value !== 'string' || !VIDEO_ID.test(value)) throw new Error('Invalid video task ID')
  return value
}

export function normalizeVideoTask(value: unknown, expectedID?: string): VideoTask {
  if (!value || typeof value !== 'object') throw new Error('Invalid video task response')
  const data = value as Record<string, unknown>
  const id = taskID(data.request_id ?? data.id ?? data.task_id ?? expectedID)
  if (expectedID && id !== expectedID) throw new Error('Video task ID mismatch')
  const raw = String(data.status || '').toLowerCase()
  let status: VideoTaskStatus
  if (['pending', 'queued', 'processing', 'in_progress'].includes(raw)) status = 'pending'
  else if (['done', 'completed', 'succeeded'].includes(raw)) status = 'done'
  else if (['failed', 'expired', 'cancelled', 'canceled'].includes(raw)) status = 'failed'
  else if (!raw && !expectedID) status = 'pending' // xAI create returns only request_id.
  else throw new Error('Invalid video task status')
  const error = typeof data.error === 'string' ? data.error
    : data.error && typeof data.error === 'object' ? String((data.error as { message?: unknown }).message || '') : ''
  return {
    id, status,
    progress: status === 'done' ? 100 : Math.max(0, Math.min(99, Number(data.progress) || 0)),
    ...(error ? { error } : {}),
  }
}

async function gatewayFetch(path: string, key: string, options: RequestInit = {}): Promise<Response> {
  const response = await fetch(buildGatewayUrl(path), {
    ...options,
    credentials: 'omit',
    redirect: 'error',
    headers: { ...options.headers, Authorization: `Bearer ${key}` },
  })
  if (!response.ok) {
    let message = `HTTP ${response.status}`
    try {
      const data = await response.json()
      message = String(data?.error?.message || data?.message || message)
    } catch { /* Keep the HTTP status when the response is not JSON. */ }
    throw new Error(message)
  }
  return response
}

export async function listVideoModels(key: string, signal?: AbortSignal): Promise<string[]> {
  const response = await gatewayFetch('/v1/models?purpose=video', key, { signal })
  const body = await response.json()
  if (!Array.isArray(body?.data)) throw new Error('Invalid video model list')
  return [...new Set<string>(body.data.map((item: { id?: unknown }) => item?.id)
    .filter((id: unknown): id is string => typeof id === 'string' && isVideoModel(id)))]
}

export async function createVideo(key: string, request: VideoGenerateRequest, signal?: AbortSignal): Promise<VideoTask> {
  const response = await gatewayFetch('/v1/videos', key, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(request), signal,
  })
  return normalizeVideoTask(await response.json())
}

export async function getVideoTask(key: string, id: string, signal?: AbortSignal): Promise<VideoTask> {
  const response = await gatewayFetch(`/v1/videos/${encodeURIComponent(taskID(id))}`, key, { signal })
  return normalizeVideoTask(await response.json(), id)
}

export async function fetchVideoContent(key: string, id: string, signal?: AbortSignal, onProgress?: (progress: VideoContentProgress) => void): Promise<Blob> {
  // Always build the protected gateway path locally. Never fetch a URL returned by an upstream.
  const path = `/v1/videos/${encodeURIComponent(taskID(id))}/content`
  if (signal?.aborted) throw new DOMException('Aborted', 'AbortError')
  const current = new AbortController()
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined
  let rejectInterrupted!: (reason: Error) => void
  const interrupted = new Promise<never>((_, reject) => { rejectInterrupted = reject })
  const interrupt = (reason: Error) => {
    rejectInterrupted(reason)
    current.abort()
    // Cancellation of an underlying stream can itself stall. Do not await it.
    void reader?.cancel().catch(() => {})
  }
  const onAbort = () => interrupt(new DOMException('Aborted', 'AbortError'))
  signal?.addEventListener('abort', onAbort, { once: true })
  const timer = setTimeout(() => interrupt(new Error('videoGen.downloadTimeout')), VIDEO_CONTENT_TIMEOUT_MS)
  const download = async (): Promise<Blob> => {
    const response = await gatewayFetch(path, key, { signal: current.signal })
    const mime = (response.headers.get('content-type') || '').split(';')[0]?.trim()
    if (!mime?.startsWith('video/')) throw new Error('Invalid video content type')
    const length = Number(response.headers.get('content-length'))
    if (length > MAX_VIDEO_BYTES) throw new Error('Video is too large to preview')
    const total = Number.isFinite(length) && length > 0 ? length : undefined
    if (!response.body) throw new Error('Missing video content')
    reader = response.body.getReader()
    const chunks: Uint8Array[] = []
    let size = 0
    onProgress?.({ received: 0, total })
    try {
      while (true) {
        const item = await reader.read()
        if (current.signal.aborted) throw new DOMException('Aborted', 'AbortError')
        if (item.done) break
        size += item.value.byteLength
        if (size > MAX_VIDEO_BYTES) throw new Error('Video is too large to preview')
        chunks.push(item.value)
        onProgress?.({ received: size, total })
      }
    } finally { reader.releaseLock() }
    if (!size) throw new Error('Empty video content')
    if (total && size !== total) throw new Error('videoGen.downloadIncomplete')
    return new Blob(chunks as BlobPart[], { type: mime })
  }
  try {
    return await Promise.race([download(), interrupted])
  } finally {
    clearTimeout(timer)
    signal?.removeEventListener('abort', onAbort)
    current.abort()
  }
}

export function videoPollDelay(signal: AbortSignal, ms = 3000): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) { reject(new DOMException('Aborted', 'AbortError')); return }
    const onAbort = () => { clearTimeout(timer); reject(new DOMException('Aborted', 'AbortError')) }
    const timer = setTimeout(() => { signal.removeEventListener('abort', onAbort); resolve() }, ms)
    signal.addEventListener('abort', onAbort, { once: true })
  })
}

export async function readVideoReference(file: File): Promise<string> {
  if (!['image/png', 'image/jpeg', 'image/webp'].includes(file.type) || !file.size || file.size > MAX_REFERENCE_BYTES) {
    throw new Error('videoGen.referenceError')
  }
  const data = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result || ''))
    reader.onerror = () => reject(new Error('videoGen.referenceError'))
    reader.readAsDataURL(file)
  })
  const bytes = Uint8Array.from(atob(data.split(',')[1] || '').slice(0, 12), c => c.charCodeAt(0))
  const valid = file.type === 'image/png'
    ? [137, 80, 78, 71, 13, 10, 26, 10].every((b, i) => bytes[i] === b)
    : file.type === 'image/jpeg' ? bytes[0] === 255 && bytes[1] === 216 && bytes[2] === 255
      : String.fromCharCode(...bytes.slice(0, 4)) === 'RIFF' && String.fromCharCode(...bytes.slice(8, 12)) === 'WEBP'
  if (!valid) throw new Error('videoGen.referenceError')
  return data
}
