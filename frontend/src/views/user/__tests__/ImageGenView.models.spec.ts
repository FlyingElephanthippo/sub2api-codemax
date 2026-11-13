import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ImageGenView from '../ImageGenView.vue'
import type { ImageProvider } from '@/constants/imageGen'
import type { ImageGenerationEstimate } from '@/api/imageGenerations'

const { getCapabilities, listKeys, listModels, estimate, listHistory, listLocalImages, generate, generateBatch, showWarning } = vi.hoisted(() => ({
  getCapabilities: vi.fn(),
  listKeys: vi.fn(), listModels: vi.fn(), estimate: vi.fn(), listHistory: vi.fn(),
  listLocalImages: vi.fn(),
  generate: vi.fn(), generateBatch: vi.fn(),
  showWarning: vi.fn(),
}))
vi.mock('@/utils/localImageHistory', async importOriginal => ({
  ...await importOriginal<typeof import('@/utils/localImageHistory')>(), listLocalImages, deleteLocalImage: vi.fn(),
}))

vi.mock('@/api/imageGenerations', () => ({
  getCapabilities,
  listModels, estimate, listHistory,
  fetchHistoryContent: vi.fn(), deleteHistory: vi.fn(),
}))
vi.mock('@/api/keys', () => ({ keysAPI: { list: listKeys } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isAuthenticated: true, user: { id: 1 } }) }))
vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: { template: '<main><slot /></main>' },
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showWarning }),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/composables/useImageGeneration', async () => {
  const { ref } = await import('vue')
  return {
    useImageGeneration: () => ({
      status: ref('idle'), currentResult: ref(null), batchItems: ref([]),
      errorMessage: ref(''), progress: ref(''),
      generate, generateBatch, archive: vi.fn(), resumePending: vi.fn(),
      cancelGeneration: vi.fn(), clearAllResults: vi.fn(), readFileAsBase64: vi.fn(),
    }),
  }
})

const SelectStub = defineComponent({
  name: 'ModelSelectStub',
  props: ['modelValue', 'options', 'disabled', 'placeholder'],
  emits: ['update:modelValue'],
  template: '<div><slot /></div>',
})

function apiKey(id: number, platform = 'openai') {
  return {
    id, name: `Key ${id}`, key: `sk-model-test-${id}`, status: 'active',
    group: { id, name: `Group ${id}`, platform, allow_image_generation: true },
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

describe('ImageGenView group model availability', () => {
  const wrappers: Array<ReturnType<typeof mount>> = []

  function mountView(props: { providerFilter?: ImageProvider; fixedPlatform?: string } = {}) {
    const wrapper = mount(ImageGenView, {
      props,
      global: { stubs: { Select: SelectStub, EmptyState: true, LoadingSpinner: true } },
    })
    wrappers.push(wrapper)
    return wrapper
  }

  function keySelect(wrapper: ReturnType<typeof mountView>) {
    return wrapper.findAllComponents(SelectStub)[0]
  }

  function modelSelect(wrapper: ReturnType<typeof mountView>) {
    return wrapper.findAllComponents(SelectStub)[1]
  }

  function modelIDs(wrapper: ReturnType<typeof mountView>) {
    return (modelSelect(wrapper).props('options') as Array<{ value: string }>).map(option => option.value)
  }

  function button(wrapper: ReturnType<typeof mountView>, text: string) {
    const found = wrapper.findAll('button').find(item => item.text() === text)
    expect(found, `button ${text}`).toBeDefined()
    return found!
  }

  async function chooseKey(wrapper: ReturnType<typeof mountView>, id: number) {
    keySelect(wrapper).vm.$emit('update:modelValue', id)
    await flushPromises()
  }

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    vi.clearAllMocks()
    vi.stubGlobal('URL', Object.assign(class extends URL {}, {
      createObjectURL: vi.fn(() => 'blob:model-history'), revokeObjectURL: vi.fn(),
    }))
    getCapabilities.mockResolvedValue({ history_enabled: true, retention_days: 90 })
    listKeys.mockReset().mockResolvedValue({ items: [apiKey(1), apiKey(2), apiKey(3)] })
    listModels.mockReset().mockResolvedValue(['gpt-image-2'])
    listHistory.mockReset().mockResolvedValue({ items: [], total: 0 })
    listLocalImages.mockReset().mockResolvedValue([])
    estimate.mockReset().mockResolvedValue({
      estimated_cost: 0.1, total_cost: 0.1, unit_cost: 0.1,
      image_count: 1, billing_tier: '1k', rate_multiplier: 1,
    })
    generate.mockReset().mockResolvedValue(null)
    generateBatch.mockReset().mockResolvedValue(undefined)
    vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  })

  afterEach(() => {
    for (const wrapper of wrappers.splice(0)) wrapper.unmount()
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('offers only concrete image IDs advertised by the selected Key group', async () => {
    listModels.mockResolvedValue(['gpt-image-2', 'gpt-5.6-sol', 'my-image-alias', 'GPT Image 2', 'gpt-image-*'])
    const wrapper = mountView()
    await flushPromises()

    expect(listModels).toHaveBeenCalledWith('sk-model-test-1', expect.any(AbortSignal))
    expect(modelIDs(wrapper)).toEqual(['gpt-image-2'])
    expect(modelSelect(wrapper).props('modelValue')).toBe('gpt-image-2')
  })

  it('submits the native Muse model and ratio without unsupported resolution or GPT options', async () => {
    listKeys.mockResolvedValue({ items: [apiKey(1, 'grok')] })
    listModels.mockResolvedValue(['muse-spark', 'muse-image', 'muse-video'])
    const wrapper = mountView()
    await flushPromises()

    expect(modelIDs(wrapper)).toEqual(['muse-image'])
    expect(wrapper.text()).not.toContain('imageGen.imageSize')
    expect(wrapper.text()).not.toContain('imageGen.quality')
    const ratio = wrapper.findAllComponents(SelectStub).find(select =>
      (select.props('options') as Array<{ value: string }>).some(option => option.value === '16:9'))
    expect(ratio).toBeDefined()
    ratio!.vm.$emit('update:modelValue', '16:9')
    await wrapper.get('textarea').setValue('A teacup')
    await flushPromises()
    await button(wrapper, 'imageGen.generate').trigger('click')

    expect(generate).toHaveBeenCalledWith(expect.objectContaining({
      model: 'muse-image', size: '16:9', aspectRatio: '16:9', apiKey: 'sk-model-test-1',
      imageSize: undefined, quality: undefined, background: undefined,
      outputFormat: undefined, outputCompression: undefined,
    }))
  })

  it('retains the production group image aliases and submits the selected public ID unchanged', async () => {
    const advertised = ['gpt-image-2.0', 'gpt-image-2.0-不超分', 'gpt-image-2.5-flare']
    listModels.mockResolvedValue(advertised)
    const wrapper = mountView()
    await flushPromises()

    expect(modelIDs(wrapper)).toEqual(advertised)
    expect(modelIDs(wrapper)).not.toContain('gpt-image-2')
    expect(modelIDs(wrapper)).not.toContain('w-nano-banana-2')
    expect(modelIDs(wrapper)).not.toContain('dreamina-seedream-5.0-pro')
    expect(modelSelect(wrapper).props('modelValue')).toBe('gpt-image-2.0')
    await wrapper.get('textarea').setValue('A teacup')

    for (const advertisedID of advertised) {
      modelSelect(wrapper).vm.$emit('update:modelValue', advertisedID)
      await flushPromises()
      await vi.advanceTimersByTimeAsync(300)
      expect(estimate).toHaveBeenLastCalledWith(expect.objectContaining({
        api_key_id: 1, model: advertisedID,
      }), expect.any(AbortSignal))
      await button(wrapper, 'imageGen.generate').trigger('click')
      expect(generate).toHaveBeenLastCalledWith(expect.objectContaining({
        model: advertisedID, apiKey: 'sk-model-test-1', apiKeyID: 1,
      }))
    }
  })

  it.each([
    { platform: 'aggregate_w', supported: ['w-nano-banana-2', 'w-gpt-image-2'], expected: ['w-gpt-image-2', 'w-nano-banana-2'] },
    { platform: 'jimeng_intl', supported: ['dreamina-seedream-5.0-pro'], expected: ['dreamina-seedream-5.0-pro'] },
  ])('shows the advertised $platform models without unrelated providers', async ({ platform, supported, expected }) => {
    listKeys.mockResolvedValue({ items: [apiKey(1, platform)] })
    listModels.mockResolvedValue(supported)
    const wrapper = mountView()
    await flushPromises()

    expect(modelIDs(wrapper)).toEqual(expected)
    expect(modelSelect(wrapper).props('modelValue')).toBe(expected[0])
    await wrapper.get('textarea').setValue('A teacup')
    await button(wrapper, 'imageGen.generate').trigger('click')
    expect(generate).toHaveBeenCalledWith(expect.objectContaining({
      model: expected[0], apiKey: 'sk-model-test-1', apiKeyID: 1,
    }))
  })

  it('selects a supported model when changing to a group without the current model', async () => {
    listModels.mockImplementation((key: string) => Promise.resolve(
      key === 'sk-model-test-1' ? ['gpt-image-2'] : ['w-nano-banana-2'],
    ))
    const wrapper = mountView()
    await flushPromises()
    await chooseKey(wrapper, 2)

    expect(modelIDs(wrapper)).toEqual(['w-nano-banana-2'])
    expect(modelSelect(wrapper).props('modelValue')).toBe('w-nano-banana-2')
    await wrapper.get('textarea').setValue('A teacup')
    await button(wrapper, 'imageGen.generate').trigger('click')
    expect(generate).toHaveBeenCalledWith(expect.objectContaining({
      model: 'w-nano-banana-2', apiKey: 'sk-model-test-2', apiKeyID: 2,
    }))
  })

  it('preserves the chosen model when it is also supported by the next Key group', async () => {
    listModels.mockResolvedValue(['gpt-image-2', 'w-nano-banana-2'])
    const wrapper = mountView()
    await flushPromises()
    modelSelect(wrapper).vm.$emit('update:modelValue', 'w-nano-banana-2')
    await flushPromises()
    await chooseKey(wrapper, 2)

    expect(modelSelect(wrapper).props('modelValue')).toBe('w-nano-banana-2')
  })

  it.each([
    { models: [] }, { models: ['claude-sonnet-4-6'] }, { models: ['gpt-image-*'] },
  ])('keeps an empty supported list closed without falling back: $models', async ({ models }) => {
    listModels.mockResolvedValue(models)
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('textarea').setValue('A teacup')
    await vi.advanceTimersByTimeAsync(1200)

    expect(modelIDs(wrapper)).toEqual([])
    expect(wrapper.text()).toContain('imageGen.noModels')
    expect(button(wrapper, 'imageGen.generate').attributes('disabled')).toBeDefined()
    await button(wrapper, 'imageGen.generate').trigger('click')
    expect(estimate).not.toHaveBeenCalled()
    expect(generate).not.toHaveBeenCalled()
  })

  it('shows a failed model lookup without fallback and retries with the same Key', async () => {
    listModels.mockRejectedValueOnce(new Error('temporary failure')).mockResolvedValue(['w-nano-banana-2'])
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('textarea').setValue('A teacup')

    expect(modelIDs(wrapper)).toEqual([])
    expect(wrapper.text()).toContain('imageGen.modelsLoadFailed')
    expect(button(wrapper, 'imageGen.generate').attributes('disabled')).toBeDefined()
    await button(wrapper, 'imageGen.retry').trigger('click')
    await flushPromises()

    expect(listModels).toHaveBeenCalledTimes(2)
    expect(listModels.mock.calls[1][0]).toBe('sk-model-test-1')
    expect(modelIDs(wrapper)).toEqual(['w-nano-banana-2'])
    expect(button(wrapper, 'imageGen.generate').attributes('disabled')).toBeUndefined()
  })

  it('waits for model discovery before estimating or generating', async () => {
    const pending = deferred<string[]>()
    listModels.mockReturnValue(pending.promise)
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('textarea').setValue('A teacup')
    await vi.advanceTimersByTimeAsync(1200)

    expect(modelSelect(wrapper).props('placeholder')).toBe('imageGen.modelsLoading')
    expect(modelIDs(wrapper)).toEqual([])
    expect(button(wrapper, 'imageGen.generate').attributes('disabled')).toBeDefined()
    await button(wrapper, 'imageGen.generate').trigger('click')
    expect(estimate).not.toHaveBeenCalled()
    expect(generate).not.toHaveBeenCalled()

    pending.resolve(['w-nano-banana-2'])
    await flushPromises()
    await vi.advanceTimersByTimeAsync(1200)
    expect(estimate).toHaveBeenCalledWith(expect.objectContaining({
      api_key_id: 1, model: 'w-nano-banana-2',
    }), expect.any(AbortSignal))
    await button(wrapper, 'imageGen.generate').trigger('click')
    expect(generate).toHaveBeenCalledWith(expect.objectContaining({ model: 'w-nano-banana-2' }))
  })

  it.each(['resolve', 'reject'] as const)('ignores a previous Key lookup that later %s after switching Keys', async (outcome) => {
    const previous = deferred<string[]>()
    listModels.mockImplementation((key: string) => key === 'sk-model-test-1'
      ? previous.promise
      : Promise.resolve(['w-nano-banana-2']))
    const wrapper = mountView()
    await flushPromises()
    const oldSignal = listModels.mock.calls[0][1] as AbortSignal
    await chooseKey(wrapper, 2)
    expect(oldSignal.aborted).toBe(true)
    expect(modelIDs(wrapper)).toEqual(['w-nano-banana-2'])

    if (outcome === 'resolve') previous.resolve(['gpt-image-2'])
    else previous.reject(new Error('old Key failure'))
    await flushPromises()

    expect(modelIDs(wrapper)).toEqual(['w-nano-banana-2'])
    expect(modelSelect(wrapper).props('modelValue')).toBe('w-nano-banana-2')
    expect(wrapper.text()).not.toContain('imageGen.modelsLoadFailed')
  })

  it('clears the previous group list and blocks requests while the next group loads', async () => {
    const pending = deferred<string[]>()
    listModels.mockResolvedValueOnce(['gpt-image-2']).mockReturnValue(pending.promise)
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('textarea').setValue('A teacup')
    await chooseKey(wrapper, 2)
    await vi.advanceTimersByTimeAsync(1200)

    expect(modelIDs(wrapper)).toEqual([])
    expect(button(wrapper, 'imageGen.generate').attributes('disabled')).toBeDefined()
    expect(estimate).not.toHaveBeenCalled()
    expect(generate).not.toHaveBeenCalled()
  })

  it('does not replace the new group price with an estimate started for the old Key', async () => {
    const previous = deferred<ImageGenerationEstimate>()
    const currentPrice = {
      estimated_cost: 0.2, total_cost: 0.2, unit_cost: 0.2,
      image_count: 1, billing_tier: 'current-group-price', rate_multiplier: 1,
    }
    estimate.mockReturnValueOnce(previous.promise).mockResolvedValue(currentPrice)
    listModels.mockImplementation((key: string) => Promise.resolve(
      key === 'sk-model-test-1' ? ['gpt-image-2'] : ['w-nano-banana-2'],
    ))
    const wrapper = mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(300)
    const oldSignal = estimate.mock.calls[0][1] as AbortSignal
    await chooseKey(wrapper, 2)
    expect(oldSignal.aborted).toBe(true)
    await vi.advanceTimersByTimeAsync(300)
    expect(wrapper.text()).toContain('current-group-price')

    previous.resolve({ ...currentPrice, billing_tier: 'old-group-price' })
    await flushPromises()
    expect(wrapper.text()).toContain('current-group-price')
    expect(wrapper.text()).not.toContain('old-group-price')
  })

  it('retains the dedicated provider filter even when the Key advertises other image providers', async () => {
    listModels.mockResolvedValue(['gpt-image-2', 'w-nano-banana-2', 'dreamina-seedream-5.0-pro'])
    const wrapper = mountView({ providerFilter: 'aggregate-w-images' })
    await flushPromises()

    expect(modelIDs(wrapper)).toEqual(['w-nano-banana-2'])
    expect(modelSelect(wrapper).props('modelValue')).toBe('w-nano-banana-2')
  })

  it('does not submit an unsupported model restored from history', async () => {
    listLocalImages.mockResolvedValue([{
      id: 'local-11', userID: 1, model: 'dreamina-seedream-5.0-pro', provider: 'dreamina-canvas', prompt: 'Old prompt',
      blob: new Blob(['png'], { type: 'image/png' }), timestamp: 1,
      mimeType: 'image/png', size: '2K', format: 'png', extension: 'png', normalizedParams: {},
    }])
    const wrapper = mountView()
    await flushPromises()
    await button(wrapper, 'imageGen.history.reuse').trigger('click')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(1200)
    await button(wrapper, 'imageGen.generate').trigger('click')

    expect(generate).not.toHaveBeenCalled()
    expect(estimate.mock.calls.every(([request]) => request.model === 'gpt-image-2')).toBe(true)
  })

  it('restores edit parameters while requiring the original reference to be uploaded again', async () => {
    listLocalImages.mockResolvedValue([{
      id: 'local-edit', userID: 1, model: 'gpt-image-2', provider: 'openai-responses', prompt: 'Keep the original scene',
      blob: new Blob(['result png'], { type: 'image/png' }), timestamp: 1, mimeType: 'image/png',
      size: '1536x1024', format: 'png', extension: 'png', normalizedParams: { size: '1536x1024' },
      referenceSnapshot: [{ index: 1, name: 'original.png', content_type: 'image/png' }],
    }])
    const wrapper = mountView()
    await flushPromises()
    await button(wrapper, 'imageGen.history.reuse').trigger('click')
    await flushPromises()
    expect(wrapper.get('textarea').element.value).toBe('Keep the original scene')
    expect(button(wrapper, 'imageGen.generate').attributes('disabled')).toBeDefined()
    await button(wrapper, 'imageGen.generate').trigger('click')
    expect(showWarning).toHaveBeenCalledWith('imageGen.history.referencesReupload')
    expect(generate).not.toHaveBeenCalled()
    expect(listHistory).not.toHaveBeenCalled()
    expect(wrapper.find('img[alt="original.png"]').exists()).toBe(false)
  })

  it('blocks an unsupported model even if a stale selector emits it', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('textarea').setValue('A teacup')
    modelSelect(wrapper).vm.$emit('update:modelValue', 'dreamina-seedream-5.0-pro')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(1200)
    await button(wrapper, 'imageGen.generate').trigger('click')

    expect(button(wrapper, 'imageGen.generate').attributes('disabled')).toBeDefined()
    expect(generate).not.toHaveBeenCalled()
    expect(estimate).not.toHaveBeenCalled()
  })
})
