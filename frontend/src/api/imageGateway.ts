/**
 * 生图网关调用。各 provider 的请求体与结果形态都不一样，全部收在这里，
 * 页面只面对 generateImage() 一个入口。
 *
 * 与面板接口的区别：这些请求走 /v1/* 网关鉴权，凭据是 API Key 而不是面板 token，
 * 所以用裸 fetch + buildGatewayUrl，与 batchImage.ts 保持同一写法。
 */

import { buildGatewayUrl } from './client'
import { resolveImageModel, type ImageProvider } from '@/constants/imageGen'

export interface ImageReference {
  id: string
  name: string
  /** 上游可直接拉取的 URL；取不到时退化成 dataUrl */
  url?: string
  dataUrl?: string
  loadDataUrl?: () => Promise<string>
  mimeType?: string
}

export interface NormalizedImageParams {
  size?: string
  quality?: string
  background?: string
  output_format?: string
  output_compression?: number
  aspect_ratio?: string
  image_size?: string
}

export interface ImageGenerateRequest {
  apiBase: string
  apiKey: string
  model: string
  prompt: string
  size?: string
  imageSize?: string
  aspectRatio?: string
  quality?: string
  background?: string
  outputFormat?: string
  outputCompression?: number
  references?: ImageReference[]
  /** 只登记元数据、不带字节时用 */
  referenceSnapshot?: unknown
  requestID?: string
  onAccepted?: (task: ImageTaskHandle) => void
}

export interface ImageResult {
  /** base64（不含 data: 前缀）或可直接下载的 URL */
  data: string
  mimeType?: string
}

export interface ImageTaskHandle {
  taskID: string
  pollURL: string
  requestID?: string
}

/** 任务句柄存在 localStorage，刷新页面后还能接着轮询。 */
const DREAMINA_TASK_KEY = 'sub2api:dreamina-image-tasks'
const DREAMINA_ARCHIVED_KEY = 'sub2api:dreamina-image-tasks-archived'
const DREAMINA_TASK_LIMIT = 20
const POLL_INTERVAL_MS = 1500
const DREAMINA_POLL_TIMEOUT_MS = 30 * 60 * 1000
const SYNC_POLL_TIMEOUT_MS = 18 * 60 * 1000
/** 提交后网关可能还没把任务登记完，重试一次再放弃 */
const SUBMIT_RETRY_DELAY_MS = 1000

interface PersistedTask extends ImageTaskHandle {
  createdAt: number
  prompt: string
  model: string
  imageSize?: string
  aspectRatio?: string
  /** 只由 /v1/images/tasks 的列表接口回填，本地持久化时不写 */
  status?: string
}

function makeRequestID(): string {
  return `img-${Date.now()}-${Math.random().toString(36).slice(2, 14)}`
}

function gatewayBase(apiBase: string): string {
  return apiBase.replace(/\/+$/, '')
}

function withOrigin(path: string, apiBase: string): string {
  try {
    return new URL(path, apiBase).toString()
  } catch {
    return path
  }
}

async function delay(signal: AbortSignal, ms: number): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(new DOMException('The operation was aborted.', 'AbortError'))
      return
    }
    const timer = window.setTimeout(() => {
      signal.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    const onAbort = () => {
      window.clearTimeout(timer)
      signal.removeEventListener('abort', onAbort)
      reject(new DOMException('The operation was aborted.', 'AbortError'))
    }
    signal.addEventListener('abort', onAbort, { once: true })
  })
}

async function readErrorMessage(response: Response): Promise<string> {
  const text = await response.text()
  try {
    const body = JSON.parse(text)
    return String(body?.error?.message || body?.message || text)
  } catch {
    return text
  }
}

export function describeTaskError(task: unknown): string {
  const error = (task as { error?: unknown })?.error
  if (error && typeof error === 'object') {
    const nested = error as { message?: string; error?: { message?: string } }
    return String(nested.message || nested.error?.message || 'image generation failed')
  }
  if (typeof error === 'string') {
    try {
      const parsed = JSON.parse(error)
      return String(parsed?.message || parsed?.error?.message || error)
    } catch {
      return error
    }
  }
  return 'image generation failed'
}

/**
 * 参考图解析成上游能取的形态。上游优先用 URL（省一次 base64 传输），
 * URL 拿不到时退回 dataUrl，两条都失败才算错误。
 */
export async function resolveReferences(references: ImageReference[]): Promise<ImageReference[]> {
  return Promise.all(
    references.map(async ref => ({
      ...ref,
      url: undefined,
      dataUrl: ref.dataUrl || (await ref.loadDataUrl?.()) || undefined,
    }))
  )
}

function hasInlineFallback(refs: ImageReference[]): boolean {
  return refs.some(ref => !ref.url || ref.dataUrl || ref.loadDataUrl)
}

/** 网关对带参考图的同步请求可能返回这些状态，说明应该改用 dataUrl 重试 */
function shouldRetryInline(status: number, refs: ImageReference[]): boolean {
  return hasInlineFallback(refs) && [400, 404, 409, 415, 422, 424].includes(status)
}

/* ---------------------------------- 请求体 --------------------------------- */

/** /v1beta/models/{model}:generateContent：参考图是 parts 里的 fileData 或 inlineData */
function buildGeminiPayload(req: ImageGenerateRequest): Record<string, unknown> {
  const parts: Array<Record<string, unknown>> = []
  for (const ref of req.references ?? []) {
    if (ref.url) {
      parts.push({ fileData: { fileUri: ref.url, mimeType: ref.mimeType || 'image/png' } })
      continue
    }
    const match = /^data:([^;,]+);base64,(.*)$/s.exec(String(ref.dataUrl || '').trim())
    if (match) {
      parts.push({ inlineData: { mimeType: ref.mimeType || match[1] || 'image/png', data: match[2] } })
    }
  }
  parts.push({ text: req.prompt })

  const imageConfig: Record<string, unknown> = {}
  if (req.aspectRatio) imageConfig.aspectRatio = req.aspectRatio
  if (req.imageSize) imageConfig.imageSize = req.imageSize

  return {
    contents: [{ role: 'user', parts }],
    generationConfig: { responseModalities: ['TEXT', 'IMAGE'], imageConfig },
  }
}

/** 即梦画布的同步请求体：尺寸走 resolution，宽高比单独一个字段 */
function buildDreaminaSyncPayload(req: ImageGenerateRequest, images: string[]): Record<string, unknown> {
  return {
    model: req.model,
    prompt: req.prompt,
    size: req.imageSize || req.size || '2K',
    aspect_ratio: req.aspectRatio,
    resolution: req.imageSize,
    n: 1,
    response_format: 'b64_json',
    reference_snapshot: req.referenceSnapshot,
    ...(images.length ? { images: images.map(imageUrl => ({ image_url: imageUrl })) } : {}),
  }
}

function buildAggregateSyncPayload(req: ImageGenerateRequest, images: string[]): Record<string, unknown> {
  return {
    model: req.model,
    prompt: req.prompt,
    size: req.size || '1024x1024',
    n: 1,
    response_format: 'b64_json',
    ...(images.length ? { images: images.map(imageUrl => ({ image_url: imageUrl })) } : {}),
  }
}

function buildOpenAIEditFormData(req: ImageGenerateRequest, references: ImageReference[]): FormData {
  const form = new FormData()
  const fields = {
    model: req.model,
    prompt: req.prompt,
    size: req.size || '1024x1024',
    n: 1,
    response_format: 'b64_json',
    quality: req.quality,
    background: req.background,
    output_format: req.outputFormat,
    output_compression: req.outputCompression,
    reference_snapshot: req.referenceSnapshot === undefined ? undefined : JSON.stringify(req.referenceSnapshot),
  }
  for (const [name, value] of Object.entries(fields)) {
    if (value !== undefined) form.append(name, String(value))
  }

  for (const [index, reference] of references.entries()) {
    const match = /^data:(image\/[a-z0-9.+-]+);base64,([\s\S]+)$/i.exec(String(reference.dataUrl || '').trim())
    if (!match) throw new Error('failed to read reference image, please upload again')
    let bytes: Uint8Array
    try {
      const binary = atob(match[2])
      if (!binary.length) throw new Error('empty image')
      bytes = Uint8Array.from(binary, character => character.charCodeAt(0))
    } catch {
      throw new Error('failed to read reference image, please upload again')
    }
    const field = references.length === 1 ? 'image' : 'image[]'
    form.append(field, new Blob([bytes], { type: match[1] }), reference.name || `reference-${index + 1}`)
  }
  return form
}

/* ---------------------------------- 结果解析 -------------------------------- */

/** Gemini 会返回多个 candidate/part，取最后一张图（首张常是说明文字） */
export function extractGeminiImage(body: unknown): ImageResult | null {
  if (!body || typeof body !== 'object') return null
  const candidates = Array.isArray((body as { candidates?: unknown[] }).candidates)
    ? (body as { candidates: unknown[] }).candidates
    : []
  for (const candidate of candidates) {
    const content = (candidate as { content?: { parts?: unknown[] } })?.content
    const parts = Array.isArray(content?.parts) ? content.parts : []
    for (let index = parts.length - 1; index >= 0; index -= 1) {
      const part = parts[index] as {
        inlineData?: { data?: string; mimeType?: string }
        inline_data?: { data?: string; mime_type?: string }
      }
      const inline = part?.inlineData || part?.inline_data
      const data = String(inline?.data || '').trim()
      if (data) {
        const mimeType = (inline as { mimeType?: string; mime_type?: string })?.mimeType
          || (inline as { mime_type?: string })?.mime_type
        return { data, mimeType: String(mimeType || 'image/png') }
      }
    }
  }
  return null
}

function extractGeminiFailure(body: unknown): string | null {
  const typed = body as {
    error?: { message?: string }
    promptFeedback?: { blockReason?: string }
  }
  const message = typed?.error?.message || typed?.promptFeedback?.blockReason
  return message ? String(message) : null
}

/** 结果可能是 b64_json，也可能是需要再下载一次的 URL */
async function extractOpenAIStyleImage(
  data: unknown,
  signal: AbortSignal
): Promise<ImageResult | null> {
  const first = (data as { data?: Array<Record<string, unknown>> })?.data?.[0] || {}
  const b64 = (first as { b64_json?: unknown }).b64_json
  if (typeof b64 === 'string' && b64) {
    return { data: b64, mimeType: (first as { mime_type?: string }).mime_type }
  }
  const url = (first as { url?: unknown }).url
  if (typeof url === 'string' && url) {
    return downloadAsBase64(url, signal)
  }
  return null
}

/** 结果 URL 在网关同源下，带 Cookie 走一次即可拿到字节 */
export async function downloadAsBase64(url: string, signal?: AbortSignal): Promise<ImageResult> {
  const response = await fetch(withOrigin(url, window.location.origin), {
    signal,
    credentials: 'same-origin',
  })
  if (!response.ok) throw new Error(`image download failed (${response.status})`)
  const blob = await response.blob()
  return { data: await blobToBase64(blob), mimeType: blob.type || undefined }
}

export function blobToBase64(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const result = String(reader.result || '')
      const comma = result.indexOf(',')
      if (comma < 0) {
        reject(new Error('invalid image payload'))
        return
      }
      resolve(result.slice(comma + 1))
    }
    reader.onerror = () => reject(reader.error || new Error('failed to read image'))
    reader.readAsDataURL(blob)
  })
}

/* ---------------------------------- 任务轮询 -------------------------------- */

function persistTask(task: ImageTaskHandle, extra: Partial<PersistedTask>): void {
  try {
    const raw = JSON.parse(localStorage.getItem(DREAMINA_TASK_KEY) || '[]')
    const list = Array.isArray(raw) ? raw.filter(item => item?.taskID !== task.taskID) : []
    list.unshift({
      taskID: task.taskID,
      pollURL: task.pollURL,
      requestID: task.requestID,
      createdAt: Date.now(),
      prompt: extra.prompt || '',
      model: extra.model || '',
      imageSize: extra.imageSize,
      aspectRatio: extra.aspectRatio,
    })
    localStorage.setItem(DREAMINA_TASK_KEY, JSON.stringify(list.slice(0, DREAMINA_TASK_LIMIT)))
  } catch {
    // 存不进去只影响刷新后恢复进度，不影响本次生成
  }
}

function dropTask(taskID: string): void {
  try {
    const raw = JSON.parse(localStorage.getItem(DREAMINA_TASK_KEY) || '[]')
    if (!Array.isArray(raw)) return
    localStorage.setItem(
      DREAMINA_TASK_KEY,
      JSON.stringify(raw.filter(item => item?.taskID !== taskID))
    )
  } catch {
    // ignore
  }
}

function markArchived(taskID: string): void {
  try {
    const raw = JSON.parse(localStorage.getItem(DREAMINA_ARCHIVED_KEY) || '[]')
    const list = Array.isArray(raw) ? raw.filter(id => id !== taskID) : []
    list.unshift(taskID)
    localStorage.setItem(DREAMINA_ARCHIVED_KEY, JSON.stringify(list.slice(0, 100)))
  } catch {
    // ignore
  }
}

function buildTaskHandle(
  body: { task_id?: string; id?: string; poll_url?: string; request_id?: string },
  apiBase: string,
  fallbackRequestID?: string
): ImageTaskHandle {
  const taskID = String(body.task_id || body.id || '').trim()
  if (!taskID) throw new Error('image task did not return a task id')
  const pollURL = withOrigin(
    body.poll_url || `/v1/images/tasks/${encodeURIComponent(taskID)}`,
    apiBase
  )
  return { taskID, pollURL, requestID: body.request_id || fallbackRequestID }
}

/**
 * 提交请求被中断或超时后，网关可能已经建好任务。用 request id 反查一次，
 * 避免用户重复提交并重复扣费。
 */
async function findByRequestID(
  apiBase: string,
  apiKey: string,
  requestID: string,
  signal: AbortSignal
): Promise<ImageTaskHandle | null> {
  try {
    const response = await fetch(
      `${gatewayBase(apiBase)}/v1/images/tasks/by-request-id/${encodeURIComponent(requestID)}`,
      { method: 'GET', headers: { Authorization: `Bearer ${apiKey}` }, signal }
    )
    if (response.status === 404 || !response.ok) return null
    return buildTaskHandle(await response.json(), apiBase, requestID)
  } catch (error) {
    if (signal.aborted) throw error
    return null
  }
}

interface PollOptions {
  apiKey: string
  signal: AbortSignal
  timeoutMs: number
  /** 失败信息里带上渠道名，用户才知道该找谁 */
  channelLabel: string
}

/** 按 1.5s 轮询任务，直到完成、失败或超时。408/429/5xx 视为瞬时错误继续等。 */
async function pollTask(handle: ImageTaskHandle, options: PollOptions): Promise<ImageResult> {
  const deadline = Date.now() + options.timeoutMs
  let first = true
  while (Date.now() < deadline) {
    if (!first) await delay(options.signal, POLL_INTERVAL_MS)
    first = false

    let response: Response
    try {
      response = await fetch(handle.pollURL, {
        method: 'GET',
        headers: { Authorization: `Bearer ${options.apiKey}` },
        signal: options.signal,
      })
    } catch (error) {
      if (options.signal.aborted) throw error
      continue
    }

    if (!response.ok) {
      if (response.status === 408 || response.status === 429 || response.status >= 500) continue
      throw new Error(
        `${options.channelLabel} task lookup failed (${response.status}): ${await readErrorMessage(response)}`
      )
    }

    const task = (await response.json()) as { status?: string; result?: unknown }
    if (task.status === 'processing' || task.status === 'queued') continue
    if (task.status !== 'completed') {
      dropTask(handle.taskID)
      throw new Error(`${describeTaskError(task)} (task ${handle.taskID})`)
    }

    const result = typeof task.result === 'string' ? JSON.parse(task.result) : task.result
    const image = await extractOpenAIStyleImage(result, options.signal)
    if (!image) {
      dropTask(handle.taskID)
      throw new Error(`${options.channelLabel} returned no image (task ${handle.taskID})`)
    }
    dropTask(handle.taskID)
    return image
  }
  throw new Error(
    `${options.channelLabel} task timed out (task ${handle.taskID}); it may still finish later`
  )
}

/* ---------------------------------- 提交入口 -------------------------------- */

/**
 * OpenAI 系与聚合渠道 W 都优先走异步接口：生图经常超过同步请求的超时。
 * 上游没开异步（404/405）时退回同步接口，其余错误直接抛出。
 */
async function submitAsync(
  req: ImageGenerateRequest,
  requestID: string,
  signal: AbortSignal,
  provider: ImageProvider,
  channelLabel: string
): Promise<ImageResult> {
  const references = await resolveReferences(req.references ?? [])
  if (signal.aborted) throw new DOMException('The operation was aborted.', 'AbortError')
  const dataUrls = references.map(ref => String(ref.dataUrl || '').trim()).filter(Boolean)
  if (dataUrls.length !== references.length) {
    throw new Error('failed to read reference image, please upload again')
  }

  const path = `${gatewayBase(req.apiBase)}/v1/images/${dataUrls.length ? 'edits' : 'generations'}/async`
  const multipart = provider === 'openai-responses' && references.length > 0
  const payload = {
    ...buildAggregateSyncPayload(req, dataUrls),
    quality: req.quality,
    background: req.background,
    output_format: req.outputFormat,
    output_compression: req.outputCompression,
    reference_snapshot: req.referenceSnapshot,
  }
  const init: RequestInit = {
    method: 'POST',
    headers: {
      ...(!multipart ? { 'Content-Type': 'application/json' } : {}),
      Authorization: `Bearer ${req.apiKey}`,
      'Idempotency-Key': requestID,
      'X-Request-ID': requestID,
    },
    body: multipart ? buildOpenAIEditFormData(req, references) : JSON.stringify(payload),
    signal,
  }

  let response: Response
  try {
    response = await fetch(path, init)
  } catch (error) {
    if (signal.aborted) throw error
    const recovered = await findByRequestID(req.apiBase, req.apiKey, requestID, signal)
    if (recovered) {
      req.onAccepted?.(recovered)
      return pollTask(recovered, { apiKey: req.apiKey, signal, timeoutMs: SYNC_POLL_TIMEOUT_MS, channelLabel })
    }
    await delay(signal, SUBMIT_RETRY_DELAY_MS)
    response = await fetch(path, init)
  }

  if (!response.ok) throw new Error(`API ${response.status}: ${await readErrorMessage(response)}`)

  const handle = buildTaskHandle(await response.json(), req.apiBase, requestID)
  req.onAccepted?.(handle)
  return pollTask(handle, {
    apiKey: req.apiKey,
    signal,
    timeoutMs: SYNC_POLL_TIMEOUT_MS,
    channelLabel,
  })
}

async function submitDreamina(
  req: ImageGenerateRequest,
  requestID: string,
  signal: AbortSignal
): Promise<ImageResult> {
  const references = await resolveReferences(req.references ?? [])
  const dataUrls = references.map(ref => {
    const value = String(ref.url || ref.dataUrl || '').trim()
    return value
  })
  if (dataUrls.some(value => !value)) {
    throw new Error('failed to read reference image, please upload again')
  }

  const build = (images: string[]) =>
    fetch(`${gatewayBase(req.apiBase)}/v1/images/${images.length ? 'edits' : 'generations'}/async`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${req.apiKey}`,
        'Idempotency-Key': requestID,
        'X-Request-ID': requestID,
      },
      body: JSON.stringify(buildDreaminaSyncPayload(req, images)),
      signal,
    })

  let response: Response
  try {
    response = await build(dataUrls)
  } catch (error) {
    if (signal.aborted) throw error
    const recovered = await findByRequestID(req.apiBase, req.apiKey, requestID, signal)
    if (recovered) return resumeDreaminaTask(recovered, req.apiKey, signal)
    await delay(signal, SUBMIT_RETRY_DELAY_MS)
    response = await build(dataUrls)
  }

  if (!response.ok) throw new Error(`API ${response.status}: ${await readErrorMessage(response)}`)

  const handle = buildTaskHandle(await response.json(), req.apiBase, requestID)
  persistTask(handle, {
    prompt: req.prompt,
    model: req.model,
    imageSize: req.imageSize,
    aspectRatio: req.aspectRatio,
  })
  return pollTask(handle, {
    apiKey: req.apiKey,
    signal,
    timeoutMs: DREAMINA_POLL_TIMEOUT_MS,
    channelLabel: 'channel 2',
  })
}

async function resumeDreaminaTask(
  handle: ImageTaskHandle,
  apiKey: string,
  signal: AbortSignal
): Promise<ImageResult> {
  persistTask(handle, {})
  return pollTask(handle, {
    apiKey,
    signal,
    timeoutMs: DREAMINA_POLL_TIMEOUT_MS,
    channelLabel: 'channel 2',
  })
}

/** 页面加载时恢复未完成的任务，避免刷新丢掉进行中的生成 */
export async function restorePendingTasks(options: {
  apiBase: string
  apiKey: string
  signal: AbortSignal
}): Promise<number> {
  const tasks: PersistedTask[] = []
  try {
    const raw = JSON.parse(localStorage.getItem(DREAMINA_TASK_KEY) || '[]')
    if (Array.isArray(raw)) tasks.push(...raw)
  } catch {
    return 0
  }

  const known = new Map(tasks.map(task => [String(task.taskID), task]))
  try {
    const response = await fetch(`${gatewayBase(options.apiBase)}/v1/images/tasks?limit=20`, {
      headers: { Authorization: `Bearer ${options.apiKey}` },
      signal: options.signal,
    })
    if (response.ok) {
      const body = await response.json()
      for (const item of Array.isArray(body?.data) ? body.data : []) {
        const taskID = String(item?.task_id || item?.id || '').trim()
        if (!taskID) continue
        const previous = known.get(taskID) || ({} as PersistedTask)
        known.set(taskID, {
          ...previous,
          ...item,
          taskID,
          pollURL:
            item.poll_url ||
            previous.pollURL ||
            `${gatewayBase(options.apiBase)}/v1/images/tasks/${encodeURIComponent(taskID)}`,
        })
      }
    }
  } catch {
    return 0
  }

  const pending = Array.from(known.values()).filter(
    task => task.status !== 'failed' && task.status !== 'canceled'
  )
  let completed = 0
  for (const task of pending.slice(0, DREAMINA_TASK_LIMIT)) {
    const archived = isArchived(task.taskID)
    try {
      const result = await pollTask(
        { taskID: task.taskID, pollURL: task.pollURL, requestID: task.requestID },
        { apiKey: options.apiKey, signal: options.signal, timeoutMs: 0, channelLabel: 'channel 2' }
      )
      void result
      if (!archived) markArchived(task.taskID)
      completed += 1
    } catch {
      // 超时或已失败的任务留在 localStorage，下次再试
    }
  }
  return completed
}

function isArchived(taskID: string): boolean {
  try {
    const raw = JSON.parse(localStorage.getItem(DREAMINA_ARCHIVED_KEY) || '[]')
    return Array.isArray(raw) && raw.includes(taskID)
  } catch {
    return false
  }
}

/**
 * 同步通道：Gemini 与即梦。两条路径的响应形态不同，但都不需要轮询。
 */
async function submitSynchronous(
  req: ImageGenerateRequest,
  signal: AbortSignal,
  provider: ImageProvider
): Promise<ImageResult> {
  const base = gatewayBase(req.apiBase)
  const references = await resolveReferences(req.references ?? [])
  if (signal.aborted) throw new DOMException('The operation was aborted.', 'AbortError')
  const multipart = provider === 'openai-responses' && references.length > 0

  const send = async (useInline: boolean): Promise<Response> => {
    if (multipart) {
      return fetch(`${base}/v1/images/edits`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${req.apiKey}` },
        body: buildOpenAIEditFormData(req, references),
        signal,
      })
    }
    if (provider === 'gemini-native') {
      return fetch(`${base}/v1beta/models/${encodeURIComponent(req.model)}:generateContent`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${req.apiKey}` },
        body: JSON.stringify(
          buildGeminiPayload({
            ...req,
            references: useInline
              ? references.map(ref => ({ ...ref, url: undefined }))
              : references,
          })
        ),
        signal,
      })
    }
    const images = references
      .map(ref => (useInline ? ref.dataUrl : ref.url || ref.dataUrl) || '')
      .filter(Boolean)
    return fetch(`${base}/v1/images/${images.length ? 'edits' : 'generations'}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${req.apiKey}` },
      body: JSON.stringify(buildDreaminaSyncPayload(req, images)),
      signal,
    })
  }

  let response = await send(false)
  if (!response.ok && !multipart && shouldRetryInline(response.status, references)) {
    await response.body?.cancel().catch(() => {})
    response = await send(true)
  }
  if (!response.ok) {
    throw new Error(`API ${response.status}: ${await readErrorMessage(response)}`)
  }

  const channelLabel = provider === 'gemini-native' ? 'Gemini' : 'channel 2'
  const body = await response.json()

  if (provider === 'gemini-native') {
    const image = extractGeminiImage(body)
    if (!image) throw new Error(extractGeminiFailure(body) || 'No image data in response')
    return image
  }

  const image = await extractOpenAIStyleImage(body, signal)
  if (image) return image

  const taskHandle = buildTaskHandleSafe(body, req.apiBase)
  if (taskHandle) {
    return pollTask(taskHandle, {
      apiKey: req.apiKey,
      signal,
      timeoutMs: DREAMINA_POLL_TIMEOUT_MS,
      channelLabel,
    })
  }

  throw new Error(`${channelLabel} returned no image`)
}

function buildTaskHandleSafe(
  body: { task_id?: string; id?: string; poll_url?: string; request_id?: string },
  apiBase: string
): ImageTaskHandle | null {
  try {
    const taskID = String(body.task_id || body.id || '').trim()
    // 上游同步结果也可能带 task_id；只有本地任务或明确的轮询地址才能继续查询。
    if (!taskID.startsWith('imgtask_') && !body.poll_url) return null
    return buildTaskHandle(body, apiBase)
  } catch {
    return null
  }
}

async function submitMuseImage(req: ImageGenerateRequest, signal: AbortSignal): Promise<ImageResult> {
  if ((req.references?.length || 0) > 4) throw new Error('Muse supports at most 4 reference images')
  const references = await resolveReferences(req.references || [])
  let totalBytes = 0
  const images = references.map(reference => {
    const url = reference.dataUrl || ''
    const match = /^data:image\/(?:png|jpeg|webp);base64,([A-Za-z0-9+/=\r\n]+)$/i.exec(url)
    if (!match) throw new Error('failed to read reference image, please upload again')
    const encoded = match[1].replace(/\s/g, '')
    const bytes = Math.floor(encoded.length * 3 / 4) - (encoded.endsWith('==') ? 2 : encoded.endsWith('=') ? 1 : 0)
    if (bytes > 10 * 1024 * 1024) throw new Error('Muse reference image exceeds 10 MiB')
    try {
      if (!atob(encoded).length) throw new Error('empty image')
    } catch {
      throw new Error('failed to read reference image, please upload again')
    }
    totalBytes += bytes
    return { url }
  })
  if (totalBytes > 24 * 1024 * 1024) throw new Error('Muse reference images exceed 24 MiB in total')
  if (signal.aborted) throw new DOMException('Aborted', 'AbortError')
  const response = await fetch(`${gatewayBase(req.apiBase)}/v1/images/${images.length ? 'edits' : 'generations'}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${req.apiKey}` },
    body: JSON.stringify({
      model: req.model, prompt: req.prompt, size: req.aspectRatio || '1:1',
      n: 1, response_format: 'b64_json', ...(images.length ? { images } : {}),
    }),
    signal,
    credentials: 'omit',
    redirect: 'error',
  })
  if (!response.ok) throw new Error(`API ${response.status}: ${await readErrorMessage(response)}`)
  const body = await response.json()
  const first = body?.data?.[0]
  if (typeof first?.b64_json !== 'string' || !first.b64_json) throw new Error('Muse returned no inline image')
  return { data: first.b64_json, mimeType: first.mime_type }
}

/** 统一入口，按 provider 分派请求并将结果规整成 {data, mimeType}。 */
export async function generateImage(
  req: ImageGenerateRequest,
  signal: AbortSignal
): Promise<ImageResult> {
  const provider = resolveImageModel(req.model).provider
  const requestID = req.requestID || makeRequestID()
  const withID = { ...req, requestID }

  if (provider === 'muse-native') return submitMuseImage(withID, signal)

  if (provider === 'openai-responses' || provider === 'aggregate-w-images') {
    try {
      return await submitAsync(withID, requestID, signal, provider, 'aggregate channel W')
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      // 只有「异步接口不存在」才值得退回同步；其余错误说明上游确实拒绝了这次请求。
      if (!/^API (404|405):/.test(message)) throw error
    }
  }

  if (provider === 'dreamina-canvas') {
    return submitDreamina(withID, requestID, signal)
  }

  return submitSynchronous(withID, signal, provider)
}

/** 生成归一化参数，用于归档与历史复用 */
export function normalizedParamsFor(
  provider: ImageProvider,
  req: Pick<
    ImageGenerateRequest,
    'size' | 'quality' | 'background' | 'outputFormat' | 'outputCompression' | 'aspectRatio' | 'imageSize'
  >
): NormalizedImageParams {
  if (provider === 'muse-native') return { aspect_ratio: req.aspectRatio }
  if (provider === 'gemini-native' || provider === 'dreamina-canvas') {
    return { aspect_ratio: req.aspectRatio, image_size: req.imageSize }
  }
  return {
    size: req.size,
    quality: req.quality,
    background: req.background,
    output_format: req.outputFormat,
    output_compression: req.outputFormat && req.outputFormat !== 'png' ? req.outputCompression : undefined,
  }
}

export { buildGatewayUrl }
