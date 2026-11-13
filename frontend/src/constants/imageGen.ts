/**
 * 生图页的模型目录与参数约束。
 *
 * 各 provider 的请求体形态差别很大（见 buildImageRequest），所以每个模型都要
 * 声明自己支持哪些参数、能带多少张参考图、可选的宽高比与分辨率档位。UI 据此
 * 隐藏不适用的控件，而不是让用户提交一个上游会拒绝的组合。
 */

export type ImageProvider =
  | 'openai-responses'
  | 'gemini-native'
  | 'aggregate-w-images'
  | 'dreamina-canvas'
  | 'muse-native'

export interface ImageModelSpec {
  /** 提交给网关的模型名 */
  value: string
  label: string
  provider: ImageProvider
  maxReferences: number
  /** 空数组表示该模型不暴露宽高比选项，用固定尺寸代替 */
  aspectRatios: string[]
  /** 空数组表示该模型不暴露分辨率档位，用固定尺寸代替 */
  imageSizes: string[]
  supportsTransparentBackground: boolean
  supportsOutputFormat: boolean
}

export interface ImageSizePreset {
  value: string
  label: string
  status: 'verified' | 'unstable'
}

/** 支持 imageConfig.imageSize 的模型（Gemini 系） */
export const GEMINI_ASPECT_RATIOS = [
  '1:1', '2:3', '3:2', '3:4', '4:3', '4:5', '5:4', '9:16', '16:9', '21:9',
]

/** Gemini 2 系列额外支持四个极端长宽比 */
export const GEMINI_ASPECT_RATIOS_EXTENDED = [
  '1:1', '1:4', '1:8', '2:3', '3:2', '3:4', '4:1', '4:3', '4:5', '5:4', '8:1', '9:16', '16:9', '21:9',
]

/** 即梦画布的宽高比选项，比 Gemini 少两级 */
export const DREAMINA_ASPECT_RATIOS = [
  '1:1', '2:3', '3:2', '3:4', '4:3', '9:16', '16:9', '21:9',
]

export const OPENAI_RESPONSES_MODELS: ImageModelSpec[] = [
  {
    value: 'gpt-image-2',
    label: 'GPT Image 2',
    provider: 'openai-responses',
    maxReferences: 16,
    aspectRatios: [],
    imageSizes: [],
    supportsTransparentBackground: false,
    supportsOutputFormat: true,
  },
]

export const GEMINI_NATIVE_MODELS: ImageModelSpec[] = [
  {
    value: 'gemini-3.1-flash-image',
    label: 'Nano Banana 2',
    provider: 'gemini-native',
    maxReferences: 14,
    aspectRatios: GEMINI_ASPECT_RATIOS_EXTENDED,
    imageSizes: ['0.5K', '1K', '2K', '4K'],
    supportsTransparentBackground: false,
    supportsOutputFormat: false,
  },
  {
    value: 'gemini-3-pro-image',
    label: 'Nano Banana Pro',
    provider: 'gemini-native',
    maxReferences: 14,
    aspectRatios: GEMINI_ASPECT_RATIOS,
    imageSizes: ['1K', '2K', '4K'],
    supportsTransparentBackground: false,
    supportsOutputFormat: false,
  },
  {
    value: 'gemini-3.1-flash-lite-image',
    label: 'Nano Banana Lite',
    provider: 'gemini-native',
    maxReferences: 14,
    aspectRatios: GEMINI_ASPECT_RATIOS,
    imageSizes: ['1K'],
    supportsTransparentBackground: false,
    supportsOutputFormat: false,
  },
]

/** 聚合渠道 W：上游按模型名分渠道，参考图上限统一按 14 处理 */
export const AGGREGATE_W_MODELS: ImageModelSpec[] = [
  ['w-gpt-image-2', 'GPT Image 2'],
  ['w-gpt-image-2.5', 'GPT Image 2.5'],
  ['w-nano-banana-2', 'Nano Banana 2'],
  ['w-nano-banana-pro', 'Nano Banana Pro'],
].map(([value, label]) => ({
  value,
  label,
  provider: 'aggregate-w-images' as const,
  maxReferences: 14,
  aspectRatios: [],
  imageSizes: [],
  supportsTransparentBackground: false,
  supportsOutputFormat: false,
}))

const DREAMINA_MODEL_DEFS: Array<[string, string, number, string[]]> = [
  ['dreamina-gpt-image-2.5-flare', 'GPT Image 2.5 Flare', 16, ['1K', '2K', '4K']],
  ['dreamina-gpt-image-2.5-sunburst', 'GPT Image 2.5 Sunburst', 16, ['1K', '2K', '4K']],
  ['dreamina-seedream-5.0-pro', 'Seedream 5 Pro', 10, ['1.5K', '2K', '4K']],
]

/** 官转渠道二（即梦国际站画布） */
export const DREAMINA_MODELS: ImageModelSpec[] = DREAMINA_MODEL_DEFS.map(
  ([value, label, maxReferences, imageSizes]) => ({
    value,
    label,
    provider: 'dreamina-canvas' as const,
    maxReferences,
    aspectRatios: DREAMINA_ASPECT_RATIOS,
    imageSizes,
    supportsTransparentBackground: false,
    supportsOutputFormat: false,
  })
)

export const MUSE_IMAGE_MODELS: ImageModelSpec[] = [{
  value: 'muse-image',
  label: 'Muse Image',
  provider: 'muse-native',
  maxReferences: 4,
  aspectRatios: ['1:1', '16:9', '9:16', '4:3', '3:4'],
  imageSizes: [],
  supportsTransparentBackground: false,
  supportsOutputFormat: false,
}]
export const IMAGE_MODELS: ImageModelSpec[] = [
  ...OPENAI_RESPONSES_MODELS,
  ...AGGREGATE_W_MODELS,
  ...DREAMINA_MODELS,
  ...MUSE_IMAGE_MODELS,
]

export const DEFAULT_IMAGE_MODEL = 'gpt-image-2'

/**
 * GPT Image 2 与 W 家族沿用对应的参数约束，并保留分组返回的完整模型 ID。
 * 未识别的名称仅在参数解析时回落默认规格；可选列表仍由下方函数严格筛选。
 */
export function resolveImageModel(model: string): ImageModelSpec {
  if (/^gpt-image-2(?:[.-][^\s/*?#]+)*$/.test(model)) {
    return { ...OPENAI_RESPONSES_MODELS[0], value: model, label: model === DEFAULT_IMAGE_MODEL ? 'GPT Image 2' : model }
  }
  if (/^w-(gpt-image|nano-banana)[a-z0-9.-]+$/.test(model)) {
    return {
      ...AGGREGATE_W_MODELS[0],
      value: model,
      label: model.replace(/^w-/, '').replace(/-/g, ' '),
    }
  }
  return (
    [...OPENAI_RESPONSES_MODELS, ...GEMINI_NATIVE_MODELS, ...AGGREGATE_W_MODELS, ...DREAMINA_MODELS, ...MUSE_IMAGE_MODELS].find(
      spec => spec.value === model
    ) ?? OPENAI_RESPONSES_MODELS[0]
  )
}

/** 目录提供参数约束，实际提交 ID 始终取所选 Key 返回的公开模型名。 */
export function getAvailableImageModels(modelIDs: string[], providerFilter?: ImageProvider): ImageModelSpec[] {
  const ids = new Set(modelIDs)
  const models = IMAGE_MODELS.filter(model => ids.has(model.value))
  for (const id of ids) {
    if (models.some(model => model.value === id)) continue
    if (!/^gpt-image-2(?:[.-][^\s/*?#]+)*$/.test(id) && !/^w-(gpt-image|nano-banana)[a-z0-9.-]+$/.test(id)) continue
    models.push(resolveImageModel(id))
  }
  return providerFilter ? models.filter(model => model.provider === providerFilter) : models
}

/**
 * 固定尺寸与分辨率档位是两套互斥的输入方式：Gemini 与即梦按档位出图，OpenAI
 * 系按像素出图。标签用通用比例名而非具体像素，避免和实际输出不一致。
 */
export const IMAGE_SIZE_PRESETS: ImageSizePreset[] = [
  { value: '1024x1024', label: 'Square 1:1', status: 'verified' },
  { value: '1536x1024', label: 'Landscape 3:2', status: 'verified' },
  { value: '1024x1536', label: 'Portrait 2:3', status: 'verified' },
  { value: '2048x1536', label: 'Landscape 4:3', status: 'verified' },
  { value: '1536x2048', label: 'Portrait 3:4', status: 'verified' },
  { value: '2048x2048', label: 'Square 2K', status: 'verified' },
  { value: '2304x2304', label: 'Square 2K+', status: 'verified' },
  { value: '2560x1440', label: 'Landscape 16:9', status: 'verified' },
  { value: '1440x2560', label: 'Portrait 9:16', status: 'verified' },
  { value: '2560x2560', label: 'Square 2.5K', status: 'verified' },
  { value: '1792x3200', label: 'Portrait Tall', status: 'verified' },
  { value: '2016x3584', label: 'Portrait Ultra', status: 'verified' },
  { value: '3584x2016', label: 'Landscape 16:9 HD', status: 'verified' },
  { value: '3840x2160', label: 'Landscape 4K', status: 'verified' },
  { value: '3840x1280', label: 'Ultrawide', status: 'verified' },
  { value: '1280x3840', label: 'Ultra Tall', status: 'verified' },
  // 以下档位上游对部分模型会返回错误，保留可用但默认折叠并提示风险。
  { value: '2816x2816', label: 'Square 2.8K', status: 'unstable' },
  { value: '2880x2880', label: 'Square 2.9K', status: 'unstable' },
  { value: '3200x1792', label: 'Landscape 2.9K', status: 'unstable' },
  { value: '2160x3840', label: 'Portrait 4K', status: 'unstable' },
]

export const VERIFIED_IMAGE_SIZES = IMAGE_SIZE_PRESETS.filter(p => p.status === 'verified')
export const UNSTABLE_IMAGE_SIZES = IMAGE_SIZE_PRESETS.filter(p => p.status === 'unstable')

export const IMAGE_QUALITY_OPTIONS = [
  { value: 'auto', label: 'Auto' },
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
]

export const IMAGE_BACKGROUND_OPTIONS = [
  { value: 'auto', label: 'Auto' },
  { value: 'opaque', label: 'Opaque' },
]

export const IMAGE_FORMAT_OPTIONS = [
  { value: 'png', label: 'PNG' },
  { value: 'jpeg', label: 'JPEG' },
  { value: 'webp', label: 'WebP' },
]

/** 自定义尺寸的合法区间，与网关侧校验保持一致 */
export const IMAGE_SIZE_LIMITS = {
  maxEdge: 3840,
  minEdge: 256,
  maxPixels: 3840 * 2160,
}

export const IMAGE_REFERENCE_MAX_BYTES = 10 * 1024 * 1024
export const IMAGE_REFERENCE_DEFAULT_LIMIT = 16

export const IMAGE_GEN_DEFAULTS = {
  model: DEFAULT_IMAGE_MODEL,
  size: '1024x1024',
  quality: 'auto',
  background: 'auto',
  format: 'png' as const,
  compression: 80,
  aspectRatio: '1:1',
  imageSize: '1K',
  batchConcurrency: 3,
  maxBatchResults: 20,
}

/**
 * 返回 i18n key 而不是成句的错误，调用方才有机会带上自己的参数。
 * 两个上限含义不同：单边越界是尺寸非法，面积越界是总像素超限。
 */
export function validateCustomSize(width: number, height: number): string | null {
  if (!Number.isInteger(width) || !Number.isInteger(height)) return 'imageGen.error.invalidSize'
  if (
    width < IMAGE_SIZE_LIMITS.minEdge ||
    height < IMAGE_SIZE_LIMITS.minEdge ||
    width > IMAGE_SIZE_LIMITS.maxEdge ||
    height > IMAGE_SIZE_LIMITS.maxEdge
  ) {
    return 'imageGen.error.invalidSize'
  }
  if (width * height > IMAGE_SIZE_LIMITS.maxPixels) return 'imageGen.error.sizeTooLarge'
  return null
}
