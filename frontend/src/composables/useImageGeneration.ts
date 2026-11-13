import { ref, shallowRef } from 'vue'
import {
  describeTaskError,
  generateImage,
  normalizedParamsFor,
  restorePendingTasks,
  type ImageReference,
  type ImageResult,
} from '@/api/imageGateway'
import { saveLocalImage } from '@/utils/localImageHistory'
import { resolveImageModel } from '@/constants/imageGen'

export type ImageGenStatus = 'idle' | 'generating' | 'success' | 'error'

export interface ImageGenRequest {
  apiBase: string
  apiKey: string
  apiKeyID?: number
  model: string
  prompt: string
  size?: string
  imageSize?: string
  aspectRatio?: string
  quality?: string
  background?: string
  outputFormat?: string
  outputCompression?: number
  references: ImageReference[]
  /** 参考图只登记元数据、不上传字节时使用 */
  referenceSnapshot?: unknown
}

export interface GeneratedImage {
  id: string
  blob: Blob
  blobUrl: string
  prompt: string
  timestamp: number
  size: string
  format: string
  mimeType: string
  extension: string
  model: string
  provider: string
  normalizedParams: unknown
  /** 已保存到当前用户的浏览器历史。 */
  localSaved: boolean
  apiKeyID?: number
  referenceSnapshot?: unknown
}

export interface BatchItem {
  prompt: string
  status: 'idle' | 'generating' | 'success' | 'error'
  result?: GeneratedImage
  error?: string
}

/** 上层读的是 i18n key，这里只负责产生 key，翻译交给页面。 */
function errorFrom(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

const MIME_EXTENSIONS: Record<string, string> = {
  'image/jpeg': 'jpg',
  'image/jpg': 'jpg',
  'image/webp': 'webp',
  'image/gif': 'gif',
  'image/avif': 'avif',
}

function extensionFor(mimeType: string): string {
  return MIME_EXTENSIONS[mimeType.toLowerCase()] || 'png'
}

function mimeFor(format: string): string {
  if (format === 'jpeg') return 'image/jpeg'
  if (format === 'webp') return 'image/webp'
  return 'image/png'
}

/**
 * 上游并不总说实话：这里按魔数再判一次真实类型。
 * 类型错了会让浏览器把图片当成八进制流下载，所以值得多这几行。
 */
function sniffMimeType(bytes: Uint8Array, fallback: string): string {
  if (bytes.length >= 8 && bytes[0] === 137 && bytes[1] === 80 && bytes[2] === 78 && bytes[3] === 71) {
    return 'image/png'
  }
  if (bytes.length >= 3 && bytes[0] === 255 && bytes[1] === 216 && bytes[2] === 255) {
    return 'image/jpeg'
  }
  if (
    bytes.length >= 12 &&
    bytes[0] === 82 && bytes[1] === 73 && bytes[2] === 70 && bytes[3] === 70 &&
    bytes[8] === 87 && bytes[9] === 69 && bytes[10] === 66 && bytes[11] === 80
  ) {
    return 'image/webp'
  }
  if (
    bytes.length >= 6 &&
    bytes[0] === 71 && bytes[1] === 73 && bytes[2] === 70 &&
    bytes[3] === 56 && (bytes[4] === 55 || bytes[4] === 57) && bytes[5] === 97
  ) {
    return 'image/gif'
  }
  return fallback
}

function base64ToBytes(base64: string): Uint8Array {
  const normalized = base64
    .replace(/\s+/g, '')
    .replace(/-/g, '+')
    .replace(/_/g, '/')
  const padded = normalized.length % 4 === 0 ? normalized : normalized + '='.repeat(4 - (normalized.length % 4))
  const binary = atob(padded)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index)
  }
  return bytes
}

export function imageResultToBlob(result: ImageResult, format: string): { blob: Blob; mimeType: string } {
  const bytes = base64ToBytes(result.data)
  const mimeType = sniffMimeType(bytes, result.mimeType || mimeFor(format))
  return { blob: new Blob([bytes], { type: mimeType }), mimeType }
}

/** 限制同时进行的请求数，避免一次批量把上游打挂。 */
class ConcurrencyGate {
  private current = 0
  private queue: Array<() => void> = []

  constructor(private readonly max: number) {}

  async acquire(): Promise<void> {
    if (this.current < this.max) {
      this.current += 1
      return
    }
    return new Promise(resolve => {
      this.queue.push(resolve)
    })
  }

  release(): void {
    this.current -= 1
    const next = this.queue.shift()
    if (next) {
      this.current += 1
      next()
    }
  }
}

export function useImageGeneration() {
  const status = ref<ImageGenStatus>('idle')
  const currentResult = shallowRef<GeneratedImage | null>(null)
  const batchItems = ref<BatchItem[]>([])
  const errorMessage = ref('')
  const progress = ref('')

  const blobUrls = new Set<string>()
  const archiveInFlight = new Map<string, Promise<void>>()
  const archivingIDs = ref(new Set<string>())
  let controller: AbortController | null = null
  let runToken = 0

  function clearAllResults(): void {
    for (const url of blobUrls) URL.revokeObjectURL(url)
    blobUrls.clear()
    currentResult.value = null
    batchItems.value = []
  }

  function toGeneratedImage(result: ImageResult, req: ImageGenRequest, format: string): GeneratedImage {
    const { blob, mimeType } = imageResultToBlob(result, format)
    const url = URL.createObjectURL(blob)
    blobUrls.add(url)
    const spec = resolveImageModel(req.model)
    return {
      id: `img-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
      blob,
      blobUrl: url,
      prompt: req.prompt,
      timestamp: Date.now(),
      size: req.size || req.imageSize || '',
      format,
      mimeType,
      extension: extensionFor(mimeType),
      model: req.model,
      provider: spec.provider,
      normalizedParams: normalizedParamsFor(spec.provider, req),
      localSaved: false,
      apiKeyID: req.apiKeyID,
      referenceSnapshot: req.referenceSnapshot ?? req.references.map((reference, index) => ({
        index: index + 1,
        name: reference.name,
        content_type: reference.mimeType || 'image/png',
      })),
    }
  }

  async function run(
    req: ImageGenRequest,
    signal: AbortSignal,
    onAccepted?: () => void,
    format = req.outputFormat || 'png'
  ): Promise<GeneratedImage> {
    return generateImage(
      {
        apiBase: req.apiBase,
        apiKey: req.apiKey,
        model: req.model,
        prompt: req.prompt,
        size: req.size,
        imageSize: req.imageSize,
        aspectRatio: req.aspectRatio,
        quality: req.quality,
        background: req.background,
        outputFormat: req.outputFormat,
        outputCompression: req.outputCompression,
        references: req.references,
        referenceSnapshot: req.referenceSnapshot,
        onAccepted: () => onAccepted?.(),
      },
      signal
    ).then(result => {
      if (signal.aborted) throw new DOMException('The operation was aborted.', 'AbortError')
      return toGeneratedImage(result, req, format)
    })
  }

  /** 单张生成（含图生图）。返回 null 表示被取消或已被更新的请求取代。 */
  async function generate(
    req: ImageGenRequest,
    onAccepted?: () => void
  ): Promise<GeneratedImage | null> {
    controller?.abort()
    clearAllResults()
    status.value = 'generating'
    errorMessage.value = ''

    const token = ++runToken
    controller = new AbortController()
    const { signal } = controller
    try {
      const image = await run(req, signal, onAccepted)
      if (signal.aborted || token !== runToken) return null
      currentResult.value = image
      status.value = 'success'
      return image
    } catch (error) {
      if (signal.aborted || token !== runToken) return null
      errorMessage.value = errorFrom(error)
      status.value = 'error'
      return null
    } finally {
      if (token === runToken) controller = null
    }
  }

  /**
   * 批量生成。每条提示词单独计费、单独归档，其中一条失败不影响其余。
   */
  async function generateBatch(req: ImageGenRequest & { prompts: string[]; concurrency: number }): Promise<void> {
    controller?.abort()
    clearAllResults()
    status.value = 'generating'
    errorMessage.value = ''
    progress.value = 'imageGen.status.batchProgress'

    const token = ++runToken
    controller = new AbortController()
    const { signal } = controller
    const gate = new ConcurrencyGate(Math.max(1, req.concurrency))
    batchItems.value = req.prompts.map(prompt => ({ prompt, status: 'idle' as const }))

    const tasks = batchItems.value.map(async item => {
      if (signal.aborted || token !== runToken) return
      await gate.acquire()
      if (signal.aborted || token !== runToken) {
        gate.release()
        return
      }
      try {
        item.status = 'generating'
        const image = await run({ ...req, prompt: item.prompt }, signal)
        if (signal.aborted || token !== runToken) return
        item.result = image
        item.status = 'success'
      } catch (error) {
        if (signal.aborted || token !== runToken) return
        item.error = errorFrom(error)
        item.status = 'error'
      } finally {
        gate.release()
      }
    })

    await Promise.allSettled(tasks)
    if (signal.aborted || token !== runToken) return

    if (batchItems.value.some(item => item.status === 'success')) {
      status.value = 'success'
    } else {
      status.value = 'error'
      errorMessage.value = 'imageGen.error.allFailed'
    }
    progress.value = ''
    if (token === runToken) controller = null
  }

  function cancelGeneration(): void {
    controller?.abort()
    controller = null
    runToken += 1
    status.value = 'idle'
    progress.value = ''
    errorMessage.value = ''
  }

  /**
   * 保存到本机浏览器；失败时保留生成结果供下载或重试。
   */
  async function archive(
    images: GeneratedImage[],
    options: {
      userID: number
      apiKeyID?: number
      references: ImageReference[]
      referenceSnapshot?: unknown
      onError?: (error: unknown) => void
    }
  ): Promise<void> {
    const pending = images.filter(image => !image.localSaved)
    if (!pending.length) return

    const snapshot =
      options.referenceSnapshot ??
      options.references.map((ref, index) => ({
        index: index + 1,
        name: ref.name,
        storage_object_id: undefined,
        content_type: ref.mimeType || 'image/png',
      }))

    await Promise.allSettled(
      pending.map(image => {
        const existing = archiveInFlight.get(image.id)
        if (existing) return existing
        archivingIDs.value.add(image.id)
        const saved = (async () => {
          try {
            await saveLocalImage(options.userID, {
              id: image.id,
              blob: image.blob,
              prompt: image.prompt,
              timestamp: image.timestamp,
              size: image.size,
              format: image.format,
              mimeType: image.mimeType,
              extension: image.extension,
              model: image.model,
              provider: image.provider,
              normalizedParams: image.normalizedParams,
              referenceSnapshot: image.referenceSnapshot ?? snapshot,
            })
            image.localSaved = true
          } catch (error) {
            options.onError?.(error)
          } finally {
            archivingIDs.value.delete(image.id)
            archiveInFlight.delete(image.id)
          }
        })()
        archiveInFlight.set(image.id, saved)
        return saved
      })
    )
  }

  /** 刷新页面后把没跑完的任务接回来，不重新扣费。 */
  async function resumePending(apiBase: string, apiKey: string): Promise<number> {
    const probe = new AbortController()
    try {
      return await restorePendingTasks({ apiBase, apiKey, signal: probe.signal })
    } catch {
      return 0
    }
  }

  return {
    status,
    currentResult,
    batchItems,
    errorMessage,
    progress,
    generate,
    generateBatch,
    archive,
    isArchiving: (image: GeneratedImage) => archivingIDs.value.has(image.id),
    resumePending,
    cancelGeneration,
    clearAllResults,
    describeTaskError,
    readFileAsBase64,
  }
}

/** 参考图上传时读成 data URL；上游拿不到直链时就靠它兜底。 */
export function readFileAsBase64(file: File): Promise<string> {
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
    reader.readAsDataURL(file)
  })
}
