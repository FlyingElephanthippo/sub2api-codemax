/**
 * 生图页面板接口 + 异步任务列表。
 *
 * 面板接口（/image-generations/*）走 apiClient，响应体由拦截器解包。
 * 任务列表挂在网关鉴权下（/v1/images/tasks），需要用裸 fetch 带 API Key，
 * 与 batchImage.ts 保持同一写法。
 */

import { apiClient, buildGatewayUrl } from './client'
import type { PaginatedResponse } from '@/types'

export type ImageTaskStatus = 'processing' | 'completed' | 'failed' | string

export interface ImageGenerationCapabilities {
  history_enabled: boolean
  retention_days: number
  max_image_count: number
}

export interface ImageGenerationEstimateRequest {
  api_key_id: number
  model: string
  image_size?: string
  image_count?: number
}

export interface ImageGenerationEstimate {
  /** 应用分组倍率后用户实际会被扣掉的金额 */
  estimated_cost: number
  /** 不含倍率的标准价 */
  total_cost: number
  unit_cost: number
  image_count: number
  billing_tier: string
  rate_multiplier: number
}

export interface ImageGenerationHistoryItem {
  id: number
  task_id?: string
  model: string
  provider: string
  prompt: string
  normalized_params?: Record<string, unknown>
  reference_snapshot?: unknown[]
  content_type: string
  size_bytes: number
  content_url?: string
  storage_object_id?: string
  library_asset_id?: string
  status: string
  error?: string
  /** 服务端是否仍持有图片字节；false 时前端只能显示占位 */
  content_available: boolean
  created_at: string
}

export interface ImageGenerationTask {
  id: string
  task_id: string
  object: string
  status: ImageTaskStatus
  http_status?: number
  image_url?: string
  result?: unknown
  error?: unknown
  created_at: number
  completed_at?: number
  expires_at: number
}

export interface ImageGenerationArchivePayload {
  task_id?: string
  model: string
  provider: string
  prompt: string
  content_type?: string
  content_url?: string
  storage_object_id?: string
  library_asset_id?: string
  status?: string
  error?: string
  api_key_id?: number
  normalized_params?: unknown
  reference_snapshot?: unknown
  /** 图片字节；不传则只登记元数据，图片留在对象存储上 */
  file?: Blob | File
}

export async function getCapabilities(): Promise<ImageGenerationCapabilities> {
  const { data } = await apiClient.get<ImageGenerationCapabilities>('/image-generations/capabilities')
  return data
}

export async function estimate(
  payload: ImageGenerationEstimateRequest,
  signal?: AbortSignal
): Promise<ImageGenerationEstimate> {
  const { data } = await apiClient.post<ImageGenerationEstimate>(
    '/image-generations/estimate',
    payload,
    { signal }
  )
  return data
}

export async function listHistory(
  page = 1,
  pageSize = 20
): Promise<PaginatedResponse<ImageGenerationHistoryItem>> {
  const { data } = await apiClient.get<PaginatedResponse<ImageGenerationHistoryItem>>(
    '/image-generations/history',
    { params: { page, page_size: pageSize } }
  )
  return data
}

export async function archiveHistory(
  payload: ImageGenerationArchivePayload
): Promise<ImageGenerationHistoryItem> {
  const form = new FormData()
  const appendIfPresent = (key: string, value: unknown) => {
    if (value === undefined || value === null || value === '') return
    form.append(key, typeof value === 'string' ? value : JSON.stringify(value))
  }
  appendIfPresent('task_id', payload.task_id)
  appendIfPresent('model', payload.model)
  appendIfPresent('provider', payload.provider)
  appendIfPresent('prompt', payload.prompt)
  appendIfPresent('content_type', payload.content_type)
  appendIfPresent('content_url', payload.content_url)
  appendIfPresent('storage_object_id', payload.storage_object_id)
  appendIfPresent('library_asset_id', payload.library_asset_id)
  appendIfPresent('status', payload.status)
  appendIfPresent('error', payload.error)
  appendIfPresent('api_key_id', payload.api_key_id)
  appendIfPresent('normalized_params', payload.normalized_params)
  appendIfPresent('reference_snapshot', payload.reference_snapshot)
  if (payload.file) form.append('file', payload.file)

  const { data } = await apiClient.post<ImageGenerationHistoryItem>(
    '/image-generations/history',
    form,
    { timeout: 5 * 60 * 1000, headers: { 'Content-Type': 'multipart/form-data' } }
  )
  return data
}

export async function deleteHistory(id: number): Promise<void> {
  await apiClient.delete(`/image-generations/history/${id}`)
}

/** 面板图片需要 JWT，取回后使用临时 Blob URL 展示。 */
export async function fetchHistoryContent(id: number): Promise<Blob> {
  const { data } = await apiClient.get<Blob>(`/image-generations/history/${id}/content`, {
    responseType: 'blob',
  })
  return data
}

function parseGatewayError(response: Response): Promise<Error> {
  return response
    .json()
    .then(body => {
      const error = new Error(body?.error?.message || response.statusText)
      ;(error as any).code = body?.error?.code || response.status
      ;(error as any).status = response.status
      return error
    })
    .catch(() => {
      const error = new Error(response.statusText || `HTTP ${response.status}`)
      ;(error as any).status = response.status
      return error
    })
}

/** 按所选 API Key 的分组获取网关实际支持的模型。 */
export async function listModels(apiKey: string, signal?: AbortSignal): Promise<string[]> {
  const response = await fetch(buildGatewayUrl('/v1/models?purpose=image-generation'), {
    headers: { Authorization: `Bearer ${apiKey}` },
    signal,
  })
  if (!response.ok) throw await parseGatewayError(response)
  const body = await response.json()
  if (!Array.isArray(body?.data)) throw new Error('Invalid model list response')

  return [...new Set<string>(body.data
    .map((model: unknown) => (model as { id?: unknown } | null)?.id)
    .filter((id: unknown): id is string => typeof id === 'string' && id.trim().length > 0))]
}

/**
 * 列出当前 API Key 最近的任务。走网关鉴权，用裸 fetch 而不是 apiClient ——
 * 这两条路径的凭据完全不同（API Key vs 面板 token）。
 */
export async function listTasks(apiKey: string, limit = 100): Promise<ImageGenerationTask[]> {
  const params = new URLSearchParams()
  if (limit > 0) params.set('limit', String(limit))
  const response = await fetch(buildGatewayUrl(`/v1/images/tasks?${params.toString()}`), {
    headers: { Authorization: `Bearer ${apiKey}` },
  })
  if (!response.ok) throw await parseGatewayError(response)
  const body = await response.json()
  return Array.isArray(body?.data) ? body.data : []
}
