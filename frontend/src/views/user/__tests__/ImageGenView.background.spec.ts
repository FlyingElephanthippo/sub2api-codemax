import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, reactive } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from '@/App.vue'
import ImageGenView from '../ImageGenView.vue'
import type { ImageGenerateRequest, ImageResult } from '@/api/imageGateway'
import type { LocalImageHistoryInput, LocalImageHistoryRecord } from '@/utils/localImageHistory'

const mocks = vi.hoisted(() => ({
  auth: { user: { id: 1 } as { id: number } | null, isAuthenticated: true, isAdmin: false },
  app: {
    siteName: 'Image test', siteLogo: '',
    cachedPublicSettings: { subscription_enabled: false, custom_menu_items: [] },
    fetchPublicSettings: vi.fn(), showError: vi.fn(), showSuccess: vi.fn(), showWarning: vi.fn(),
  },
  subscription: { clear: vi.fn(), fetchActiveSubscriptions: vi.fn(), startPolling: vi.fn() },
  announcement: { fetchAnnouncements: vi.fn(), reset: vi.fn() },
  compliance: { fetchStatus: vi.fn(), requireAcknowledgement: vi.fn(), reset: vi.fn() },
  adminSettings: { customMenuItems: [] },
  generateImage: vi.fn(), archiveHistory: vi.fn(), listHistory: vi.fn(), listKeys: vi.fn(), listModels: vi.fn(),
  saveLocalImage: vi.fn(), listLocalImages: vi.fn(), deleteLocalImage: vi.fn(),
  getCapabilities: vi.fn(), fetchHistoryContent: vi.fn(), estimate: vi.fn(), getSetupStatus: vi.fn(),
}))

vi.mock('@/stores', () => ({
  useAppStore: () => mocks.app, useAuthStore: () => mocks.auth,
  useSubscriptionStore: () => mocks.subscription, useAnnouncementStore: () => mocks.announcement,
  useAdminComplianceStore: () => mocks.compliance, useAdminSettingsStore: () => mocks.adminSettings,
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => mocks.app }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mocks.auth }))
vi.mock('@/api/setup', () => ({ getSetupStatus: mocks.getSetupStatus }))
vi.mock('@/router/title', () => ({ resolveRouteDocumentTitle: () => 'Image test' }))
vi.mock('@/utils/branding', () => ({ updateFavicon: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/common/Toast.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/common/NavigationProgress.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/common/AnnouncementPopup.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/admin/AdminComplianceDialog.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/api/keys', () => ({ keysAPI: { list: mocks.listKeys } }))
vi.mock('@/api/client', () => ({ buildGatewayUrl: (path: string) => path }))
vi.mock('@/api/imageGenerations', () => ({
  getCapabilities: mocks.getCapabilities, estimate: mocks.estimate,
  listModels: mocks.listModels, listHistory: mocks.listHistory, archiveHistory: mocks.archiveHistory,
  fetchHistoryContent: mocks.fetchHistoryContent,
  deleteHistory: vi.fn(),
}))
vi.mock('@/api/imageGateway', async importOriginal => ({
  ...await importOriginal<typeof import('@/api/imageGateway')>(),
  generateImage: mocks.generateImage,
}))
vi.mock('@/utils/localImageHistory', async importOriginal => ({
  ...await importOriginal<typeof import('@/utils/localImageHistory')>(),
  saveLocalImage: mocks.saveLocalImage, listLocalImages: mocks.listLocalImages,
  deleteLocalImage: mocks.deleteLocalImage,
}))

const SelectStub = defineComponent({
  name: 'ImageSelectStub', props: ['modelValue', 'options', 'disabled', 'placeholder'],
  emits: ['update:modelValue'], template: '<div><slot /></div>',
})

function apiKey(id: number) {
  return { id, name: `Key ${id}`, key: `sk-background-test-${id}`, status: 'active',
    group: { id, name: `Group ${id}`, platform: 'openai', allow_image_generation: true } }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

const pngResult: ImageResult = { data: 'iVBORw0KGgo=', mimeType: 'image/png' }

describe('ImageGenView background generation through the application router', () => {
  const wrappers: Array<ReturnType<typeof mount>> = []
  let requests: Array<{ request: ImageGenerateRequest; signal: AbortSignal; pending: ReturnType<typeof deferred<ImageResult>> }>
  let history: LocalImageHistoryRecord[]
  let nextURL = 0

  async function mountApp() {
    const router = createRouter({ history: createMemoryHistory(), routes: [
      { path: '/image-gen', name: 'ImageGen', component: ImageGenView },
      { path: '/other', name: 'Other', component: { template: '<p>Other page</p>' } },
    ] })
    await router.push('/image-gen')
    await router.isReady()
    const wrapper = mount(App, { global: { plugins: [router], stubs: {
      Select: SelectStub, EmptyState: true, LoadingSpinner: true,
    } } })
    wrappers.push(wrapper)
    await flushPromises()
    return { wrapper, router }
  }

  function button(wrapper: ReturnType<typeof mount>, label: string) {
    const found = wrapper.findAll('button').find(item => item.text() === label)
    expect(found, `button ${label}`).toBeDefined()
    return found!
  }

  async function startSingle(wrapper: ReturnType<typeof mount>, prompt = 'A background teacup') {
    await wrapper.get('textarea').setValue(prompt)
    await button(wrapper, 'imageGen.generate').trigger('click')
    await flushPromises()
    expect(requests).toHaveLength(1)
  }

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    vi.clearAllMocks()
    mocks.auth = reactive({ user: { id: 1 }, isAuthenticated: true, isAdmin: false })
    requests = []
    history = []
    nextURL = 0
    vi.stubGlobal('URL', Object.assign(class extends URL {}, {
      createObjectURL: vi.fn(() => `blob:background-${++nextURL}`), revokeObjectURL: vi.fn(),
    }))
    mocks.getSetupStatus.mockReset().mockResolvedValue({ needs_setup: false })
    mocks.getCapabilities.mockReset().mockResolvedValue({ history_enabled: true, retention_days: 90 })
    mocks.fetchHistoryContent.mockReset().mockResolvedValue(new Blob(['png'], { type: 'image/png' }))
    mocks.estimate.mockReset().mockResolvedValue({ estimated_cost: 0.1, total_cost: 0.1, unit_cost: 0.1,
      image_count: 1, billing_tier: '1k', rate_multiplier: 1 })
    mocks.listKeys.mockReset().mockImplementation(() => Promise.resolve({ items: [apiKey(1), apiKey(2)] }))
    mocks.listModels.mockReset().mockResolvedValue(['gpt-image-2'])
    mocks.listHistory.mockReset().mockResolvedValue({ items: [], total: 0 })
    mocks.archiveHistory.mockReset()
    mocks.listLocalImages.mockReset().mockImplementation((userID: number) => Promise.resolve(history.filter(item => item.userID === userID)))
    mocks.saveLocalImage.mockReset().mockImplementation(async (userID: number, payload: LocalImageHistoryInput) => {
      history.push({ ...payload, userID })
    })
    mocks.deleteLocalImage.mockReset().mockResolvedValue(undefined)
    mocks.generateImage.mockReset().mockImplementation((request: ImageGenerateRequest, signal: AbortSignal) => {
      const pending = deferred<ImageResult>()
      requests.push({ request, signal, pending })
      return pending.promise
    })
    vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  })

  afterEach(() => {
    expect(mocks.archiveHistory).not.toHaveBeenCalled()
    expect(mocks.listHistory).not.toHaveBeenCalled()
    expect(mocks.fetchHistoryContent).not.toHaveBeenCalled()
    for (const wrapper of wrappers.splice(0)) wrapper.unmount()
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('preserves an in-flight request and its progress when navigating away and back without resubmitting', async () => {
    const { wrapper, router } = await mountApp()
    await startSingle(wrapper)
    await router.push('/other')
    await flushPromises()
    expect(wrapper.text()).toContain('Other page')
    expect(requests[0].signal.aborted).toBe(false)
    await router.push('/image-gen')
    await flushPromises()

    expect(requests[0].signal.aborted).toBe(false)
    expect(mocks.generateImage).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('imageGen.status.generating')
    expect(wrapper.get('textarea').element.value).toBe('A background teacup')
    requests[0].pending.resolve(pngResult)
    await flushPromises()
    expect(wrapper.find('img[alt="A background teacup"]').exists()).toBe(true)
    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(1)
  })

  it('finishes and archives while another page is visible and restores the completed image on return', async () => {
    const { wrapper, router } = await mountApp()
    await startSingle(wrapper)
    await router.push('/other')
    requests[0].pending.resolve(pngResult)
    await flushPromises()

    expect(wrapper.text()).toContain('Other page')
    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(1)
    await router.push('/image-gen')
    await flushPromises()
    expect(wrapper.findAll('img[alt="A background teacup"]')).toHaveLength(2)
    expect(mocks.generateImage).toHaveBeenCalledTimes(1)
    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).not.toContain('imageGen.status.generating')
  })

  it('retains a background failure and permits an intentional retry after returning', async () => {
    const { wrapper, router } = await mountApp()
    await startSingle(wrapper)
    await router.push('/other')
    requests[0].pending.reject(new Error('upstream image failed'))
    await flushPromises()
    expect(mocks.app.showError).toHaveBeenCalledWith('upstream image failed')
    await router.push('/image-gen')
    await flushPromises()

    expect(mocks.generateImage).toHaveBeenCalledTimes(1)
    expect(mocks.saveLocalImage).not.toHaveBeenCalled()
    expect(button(wrapper, 'imageGen.generate').attributes('disabled')).toBeUndefined()
    await button(wrapper, 'imageGen.generate').trigger('click')
    await flushPromises()
    expect(mocks.generateImage).toHaveBeenCalledTimes(2)
  })

  it('continues queued batch requests off-page and restores all completed results', async () => {
    const { wrapper, router } = await mountApp()
    await button(wrapper, 'imageGen.tabBatch').trigger('click')
    await wrapper.get('textarea').setValue('One\nTwo\nThree\nFour\nFive')
    await button(wrapper, 'imageGen.batchGenerate').trigger('click')
    await flushPromises()
    expect(requests).toHaveLength(3)
    await router.push('/other')
    requests[0].pending.resolve(pngResult)
    await flushPromises()
    expect(requests).toHaveLength(4)
    requests[1].pending.resolve(pngResult)
    await flushPromises()
    expect(requests).toHaveLength(5)
    for (const task of requests.slice(2)) task.pending.resolve(pngResult)
    await flushPromises()
    expect(requests.every(task => !task.signal.aborted)).toBe(true)
    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(5)
    await router.push('/image-gen')
    await flushPromises()

    for (const prompt of ['One', 'Two', 'Three', 'Four', 'Five']) {
      expect(wrapper.findAll(`img[alt="${prompt}"]`)).toHaveLength(2)
    }
    expect(wrapper.text()).toContain('5 / 5')
    expect(mocks.generateImage).toHaveBeenCalledTimes(5)
    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(5)
  })

  it.each(['logout', 'switch-account'] as const)('clears the old account task on %s and rejects its late result', async change => {
    const { wrapper, router } = await mountApp()
    await startSingle(wrapper, 'Private old account prompt')
    await router.push('/other')
    if (change === 'logout') {
      mocks.auth.isAuthenticated = false
      mocks.auth.user = null
    } else {
      mocks.auth.user = { id: 2 }
    }
    await flushPromises()
    expect(requests[0].signal.aborted).toBe(true)
    requests[0].pending.resolve(pngResult)
    await flushPromises()
    expect(mocks.saveLocalImage).not.toHaveBeenCalled()
    expect(mocks.app.showSuccess).not.toHaveBeenCalled()
    expect(URL.createObjectURL).not.toHaveBeenCalled()
    if (change === 'logout') {
      mocks.auth.user = { id: 1 }
      mocks.auth.isAuthenticated = true
    }
    await router.push('/image-gen')
    await flushPromises()

    expect(wrapper.get('textarea').element.value).toBe('')
    expect(wrapper.find('img[alt="Private old account prompt"]').exists()).toBe(false)
    expect(mocks.generateImage).toHaveBeenCalledTimes(1)
  })

  it('saves for the same signed-in user when the selected Key changes during generation', async () => {
    const { wrapper } = await mountApp()
    await startSingle(wrapper)
    wrapper.findAllComponents(SelectStub)[0].vm.$emit('update:modelValue', 2)
    await flushPromises()
    expect(mocks.listModels).toHaveBeenLastCalledWith('sk-background-test-2', expect.any(AbortSignal))
    requests[0].pending.resolve(pngResult)
    await flushPromises()

    expect(requests[0].request.apiKey).toBe('sk-background-test-1')
    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(1)
    expect(mocks.saveLocalImage).toHaveBeenCalledWith(1, expect.objectContaining({ prompt: 'A background teacup' }))
  })

  it.each(['resolve', 'reject'] as const)('keeps an old account local save isolated when it later %s', async outcome => {
    const saving = deferred<void>()
    mocks.saveLocalImage.mockReturnValueOnce(saving.promise)
    const { wrapper } = await mountApp()
    await startSingle(wrapper, 'Old account saved image')
    requests[0].pending.resolve(pngResult)
    await flushPromises()
    expect(mocks.saveLocalImage).toHaveBeenCalledWith(1, expect.objectContaining({ prompt: 'Old account saved image' }))
    mocks.auth.user = { id: 2 }
    await flushPromises()
    if (outcome === 'resolve') saving.resolve(undefined)
    else saving.reject(new Error('private old account save error'))
    await flushPromises()
    expect(wrapper.find('img[alt="Old account saved image"]').exists()).toBe(false)
    expect(mocks.saveLocalImage).toHaveBeenCalledOnce()
    expect(mocks.app.showWarning).not.toHaveBeenCalled()
    expect(mocks.listLocalImages).toHaveBeenLastCalledWith(2)
  })

  it('does not cancel an in-flight image when switching generation modes', async () => {
    const { wrapper } = await mountApp()
    await startSingle(wrapper)
    for (const mode of ['imageGen.tabEdit', 'imageGen.tabBatch', 'imageGen.tabGenerate']) {
      await button(wrapper, mode).trigger('click')
      expect(requests[0].signal.aborted).toBe(false)
    }
    requests[0].pending.resolve(pngResult)
    await flushPromises()

    expect(wrapper.find('img[alt="A background teacup"]').exists()).toBe(true)
    expect(mocks.generateImage).toHaveBeenCalledTimes(1)
    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(1)
  })

  it('honors explicit cancellation and does not publish a late image after returning', async () => {
    const { wrapper, router } = await mountApp()
    await startSingle(wrapper)
    await button(wrapper, 'imageGen.cancel').trigger('click')
    expect(requests[0].signal.aborted).toBe(true)
    await router.push('/other')
    requests[0].pending.resolve(pngResult)
    await flushPromises()
    await router.push('/image-gen')
    await flushPromises()

    expect(wrapper.find('img[alt="A background teacup"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('imageGen.status.generating')
    expect(mocks.saveLocalImage).not.toHaveBeenCalled()
    expect(mocks.app.showSuccess).not.toHaveBeenCalled()
    expect(URL.createObjectURL).not.toHaveBeenCalled()
    expect(button(wrapper, 'imageGen.generate').attributes('disabled')).toBeUndefined()
  })

  it('stops queued batch submissions on explicit cancellation without showing a success toast', async () => {
    const { wrapper, router } = await mountApp()
    await button(wrapper, 'imageGen.tabBatch').trigger('click')
    await wrapper.get('textarea').setValue('One\nTwo\nThree\nFour\nFive')
    await button(wrapper, 'imageGen.batchGenerate').trigger('click')
    await flushPromises()
    expect(requests).toHaveLength(3)
    await button(wrapper, 'imageGen.cancel').trigger('click')
    await router.push('/other')
    for (const task of requests) task.pending.resolve(pngResult)
    await flushPromises()

    expect(requests).toHaveLength(3)
    expect(requests.every(task => task.signal.aborted)).toBe(true)
    expect(mocks.saveLocalImage).not.toHaveBeenCalled()
    expect(mocks.app.showSuccess).not.toHaveBeenCalled()
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })

  it('invalidates the visible account task synchronously when logout and completion happen together', async () => {
    const { wrapper } = await mountApp()
    await startSingle(wrapper, 'Private visible account prompt')
    mocks.auth.user = null
    mocks.auth.isAuthenticated = false
    requests[0].pending.resolve(pngResult)
    await flushPromises()

    expect(requests[0].signal.aborted).toBe(true)
    expect(wrapper.find('img[alt="Private visible account prompt"]').exists()).toBe(false)
    expect(mocks.saveLocalImage).not.toHaveBeenCalled()
    expect(mocks.app.showSuccess).not.toHaveBeenCalled()
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })

  it('retains a reference upload across navigation and archives its submission snapshot after changing modes', async () => {
    const { wrapper, router } = await mountApp()
    const readers: FileReader[] = []
    vi.spyOn(FileReader.prototype, 'readAsDataURL').mockImplementation(function(this: FileReader) {
      readers.push(this)
    })
    const reference = new File(['png'], 'original-reference.png', { type: 'image/png' })
    const input = wrapper.get('input[type="file"]')
    Object.defineProperty(input.element, 'files', { configurable: true, value: [reference] })
    await input.trigger('change')
    expect(wrapper.text()).toContain('imageGen.referenceStatus.reading')
    expect(readers).toHaveLength(1)
    const reader = readers[0]
    await router.push('/other')
    Object.defineProperty(reader, 'result', { configurable: true, value: 'data:image/png;base64,iVBORw0KGgo=' })
    reader.onload!.call(reader, new ProgressEvent('load'))
    await flushPromises()
    await router.push('/image-gen')
    await flushPromises()

    expect(wrapper.find('img[alt="original-reference.png"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('imageGen.referenceStatus.reading')
    await wrapper.get('textarea').setValue('Use the uploaded reference')
    await button(wrapper, 'imageGen.generateEdit').trigger('click')
    await flushPromises()
    expect(requests).toHaveLength(1)
    expect(requests[0].request.references).toEqual([expect.objectContaining({
      name: 'original-reference.png', dataUrl: 'data:image/png;base64,iVBORw0KGgo=',
    })])
    await button(wrapper, 'imageGen.tabGenerate').trigger('click')
    expect(wrapper.find('img[alt="original-reference.png"]').exists()).toBe(false)
    expect(requests[0].signal.aborted).toBe(false)
    requests[0].pending.resolve(pngResult)
    await flushPromises()

    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(1)
    expect(mocks.saveLocalImage).toHaveBeenCalledWith(1, expect.objectContaining({
      referenceSnapshot: [expect.objectContaining({ name: 'original-reference.png', content_type: 'image/png' })],
    }))
  })

  it('keeps a failed save available for one explicit retry and disables the button while uploading', async () => {
    const firstSave = deferred<void>()
    const retrySave = deferred<void>()
    mocks.saveLocalImage.mockReturnValueOnce(firstSave.promise).mockReturnValueOnce(retrySave.promise)
    const { wrapper } = await mountApp()
    await startSingle(wrapper)
    requests[0].pending.resolve(pngResult)
    await flushPromises()
    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(1)
    expect(button(wrapper, 'imageGen.history.archiving').attributes('disabled')).toBeDefined()
    await button(wrapper, 'imageGen.history.archiving').trigger('click')
    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(1)

    firstSave.reject({ status: 400, code: 400, reason: 'IMAGE_GENERATION_HISTORY_UPLOAD_INCOMPLETE',
      message: 'private database_password=secret' })
    await flushPromises()
    expect(mocks.app.showWarning).toHaveBeenCalledWith('imageGen.history.localSaveFailed')
    expect(wrapper.find('img[alt="A background teacup"]').exists()).toBe(true)
    expect(button(wrapper, 'imageGen.longStorage').attributes('disabled')).toBeUndefined()
    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(1)
    await button(wrapper, 'imageGen.longStorage').trigger('click')
    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(2)
    expect(button(wrapper, 'imageGen.history.archiving').attributes('disabled')).toBeDefined()
    retrySave.resolve(undefined)
    await flushPromises()

    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(2)
    expect(wrapper.findAll('button').some(item => item.text() === 'imageGen.longStorage')).toBe(false)
    expect(wrapper.findAll('button').some(item => item.text() === 'imageGen.history.archiving')).toBe(false)
    expect(mocks.app.showWarning).toHaveBeenCalledTimes(1)
  })

  it('provides a storage retry for a failed batch archive without regenerating the image', async () => {
    mocks.saveLocalImage.mockRejectedValueOnce({ status: 0, code: 'ECONNABORTED', message: 'unsafe raw detail' })
    const { wrapper } = await mountApp()
    await button(wrapper, 'imageGen.tabBatch').trigger('click')
    await wrapper.get('textarea').setValue('First save fails\nSecond save succeeds')
    await button(wrapper, 'imageGen.batchGenerate').trigger('click')
    await flushPromises()
    for (const task of requests) task.pending.resolve(pngResult)
    await flushPromises()

    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(2)
    expect(mocks.app.showWarning).toHaveBeenCalledWith('imageGen.history.localSaveFailed')
    expect(wrapper.findAll('button').filter(item => item.text() === 'imageGen.longStorage')).toHaveLength(1)
    await button(wrapper, 'imageGen.longStorage').trigger('click')
    await flushPromises()

    expect(mocks.saveLocalImage).toHaveBeenCalledTimes(3)
    expect(mocks.generateImage).toHaveBeenCalledTimes(2)
    expect(wrapper.findAll('button').some(item => item.text() === 'imageGen.longStorage')).toBe(false)
  })
})
