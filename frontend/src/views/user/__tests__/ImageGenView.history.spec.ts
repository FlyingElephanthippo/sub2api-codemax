import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ImageGenView from '../ImageGenView.vue'
import type { ImageGenerationHistoryItem } from '@/api/imageGenerations'
import type { LocalImageHistoryRecord } from '@/utils/localImageHistory'

const { getCapabilities, listHistory, fetchHistoryContent, deleteHistory, listLocalImages, deleteLocalImage, auth, showWarning } = vi.hoisted(() => ({
  getCapabilities: vi.fn(),
  listHistory: vi.fn(),
  fetchHistoryContent: vi.fn(),
  deleteHistory: vi.fn(),
  listLocalImages: vi.fn(), deleteLocalImage: vi.fn(), showWarning: vi.fn(),
  auth: { isAuthenticated: true, user: { id: 1 } as { id: number } | null },
}))

vi.mock('@/api/imageGenerations', () => ({
  getCapabilities,
  estimate: vi.fn(),
  listHistory,
  fetchHistoryContent,
  deleteHistory,
}))
vi.mock('@/api/keys', () => ({ keysAPI: { list: vi.fn().mockResolvedValue({ items: [] }) } }))
vi.mock('@/utils/localImageHistory', async importOriginal => ({
  ...await importOriginal<typeof import('@/utils/localImageHistory')>(), listLocalImages, deleteLocalImage,
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => reactive(auth) }))
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
      generate: vi.fn(), generateBatch: vi.fn(), archive: vi.fn(), resumePending: vi.fn(),
      cancelGeneration: vi.fn(), clearAllResults: vi.fn(), readFileAsBase64: vi.fn(),
    }),
  }
})

function historyItem(overrides: Partial<ImageGenerationHistoryItem> = {}): ImageGenerationHistoryItem {
  return {
    id: 11, model: 'gpt-image-2', provider: 'openai', prompt: 'a cat', content_type: 'image/png',
    size_bytes: 128, status: 'completed', content_available: true, created_at: '2026-10-17T00:00:00Z',
    ...overrides,
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}

function localItem(overrides: Partial<LocalImageHistoryRecord> = {}): LocalImageHistoryRecord {
  return {
    id: 'local-11', userID: 1, blob: new Blob(['local png'], { type: 'image/png' }),
    prompt: 'local cat', timestamp: 1, size: '1024x1024', format: 'png', mimeType: 'image/png',
    extension: 'png', model: 'gpt-image-2', provider: 'openai-responses', normalizedParams: {},
    ...overrides,
  }
}

describe('ImageGenView local and explicitly opened cloud history', () => {
  const wrappers: Array<ReturnType<typeof mount>> = []
  let nextUrl = 0

  async function mountView(openCloud = true) {
    const wrapper = mount(ImageGenView, {
      global: { stubs: { Select: true, EmptyState: true, LoadingSpinner: true } },
    })
    wrappers.push(wrapper)
    if (openCloud) {
      const details = wrapper.get('details')
      ;(details.element as HTMLDetailsElement).open = true
    }
    await new Promise<void>(resolve => setTimeout(resolve, 0))
    await flushPromises()
    return wrapper
  }

  beforeEach(() => {
    vi.clearAllMocks()
    auth.user = { id: 1 }
    auth.isAuthenticated = true
    nextUrl = 0
    vi.stubGlobal('URL', Object.assign(class extends URL {}, {
      createObjectURL: vi.fn(() => `blob:history-${++nextUrl}`),
      revokeObjectURL: vi.fn(),
    }))
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    getCapabilities.mockResolvedValue({ history_enabled: true, retention_days: 90 })
    listHistory.mockReset().mockResolvedValue({ items: [historyItem()], total: 1 })
    fetchHistoryContent.mockReset().mockResolvedValue(new Blob(['png'], { type: 'image/png' }))
    deleteHistory.mockResolvedValue(undefined)
    listLocalImages.mockReset().mockResolvedValue([])
    deleteLocalImage.mockReset().mockResolvedValue(undefined)
  })

  afterEach(() => {
    for (const wrapper of wrappers.splice(0)) wrapper.unmount()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('restores local image bytes after remounting without any cloud history requests', async () => {
    listLocalImages.mockResolvedValue([localItem()])
    const first = await mountView(false)
    await flushPromises()
    expect(listLocalImages).toHaveBeenCalledWith(1)
    expect(first.get('img[alt="local cat"]').attributes('src')).toBe('blob:history-1')
    expect(first.get('a[download]').attributes('download')).toBe('image-local-11.png')
    expect(getCapabilities).not.toHaveBeenCalled()
    expect(listHistory).not.toHaveBeenCalled()
    expect(fetchHistoryContent).not.toHaveBeenCalled()
    first.unmount()
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:history-1')

    const second = await mountView(false)
    await flushPromises()
    expect(second.get('img[alt="local cat"]').attributes('src')).toBe('blob:history-2')
    expect(listLocalImages).toHaveBeenCalledTimes(2)
    expect(listHistory).not.toHaveBeenCalled()
  })

  it('refreshes and deletes local records without changing old cloud history', async () => {
    listLocalImages.mockResolvedValue([localItem()])
    const wrapper = await mountView(false)
    await flushPromises()
    listLocalImages.mockResolvedValue([localItem({ id: 'local-12', prompt: 'local dog' })])
    await wrapper.findAll('button').find(button => button.text() === 'imageGen.history.reload')!.trigger('click')
    await flushPromises()
    expect(wrapper.find('img[alt="local cat"]').exists()).toBe(false)
    expect(wrapper.get('img[alt="local dog"]').attributes('src')).toBe('blob:history-2')
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:history-1')
    await wrapper.findAll('button').find(button => button.text() === '×')!.trigger('click')
    await flushPromises()
    expect(deleteLocalImage).toHaveBeenCalledWith(1, 'local-12')
    expect(wrapper.find('img[alt="local dog"]').exists()).toBe(false)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:history-2')
    expect(deleteHistory).not.toHaveBeenCalled()
    expect(listHistory).not.toHaveBeenCalled()
  })

  it('shows a local storage read failure and lets the user retry without loading cloud records', async () => {
    listLocalImages.mockRejectedValueOnce(new Error('browser storage unavailable')).mockResolvedValue([localItem()])
    const wrapper = await mountView(false)
    await flushPromises()
    expect(wrapper.text()).toContain('imageGen.history.localLoadFailed')
    await wrapper.findAll('button').find(button => button.text() === 'imageGen.history.reload')!.trigger('click')
    await flushPromises()
    expect(wrapper.get('img[alt="local cat"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('imageGen.history.localLoadFailed')
    expect(listHistory).not.toHaveBeenCalled()
  })

  it('discards an earlier account local list when it resolves after switching accounts', async () => {
    const oldList = deferred<LocalImageHistoryRecord[]>()
    listLocalImages.mockImplementation((userID: number) => userID === 1 ? oldList.promise : Promise.resolve([
      localItem({ id: 'account-2', userID: 2, prompt: 'new account image' }),
    ]))
    const wrapper = await mountView(false)
    await flushPromises()
    reactive(auth).user = { id: 2 }
    await flushPromises()
    expect(listLocalImages).toHaveBeenLastCalledWith(2)
    expect(wrapper.get('img[alt="new account image"]').exists()).toBe(true)
    oldList.resolve([localItem({ prompt: 'old private image' })])
    await flushPromises()
    expect(wrapper.find('img[alt="old private image"]').exists()).toBe(false)
    expect(wrapper.get('img[alt="new account image"]').exists()).toBe(true)
    expect(URL.createObjectURL).toHaveBeenCalledTimes(1)
    expect(listHistory).not.toHaveBeenCalled()
  })

  it('does not remove a new account record when the old account deletion completes late', async () => {
    const oldDelete = deferred<void>()
    listLocalImages.mockResolvedValue([localItem()])
    deleteLocalImage.mockReturnValue(oldDelete.promise)
    const wrapper = await mountView(false)
    await wrapper.findAll('button').find(button => button.text() === '×')!.trigger('click')
    expect(deleteLocalImage).toHaveBeenCalledWith(1, 'local-11')
    listLocalImages.mockResolvedValue([localItem({ userID: 2, prompt: 'new owner image' })])
    reactive(auth).user = { id: 2 }
    await flushPromises()
    oldDelete.resolve(undefined)
    await flushPromises()
    expect(wrapper.get('img[alt="new owner image"]').exists()).toBe(true)
    expect(wrapper.find('img[alt="local cat"]').exists()).toBe(false)
  })

  it('fetches cloud history only after the user explicitly opens its section', async () => {
    const wrapper = await mountView(false)
    await flushPromises()
    expect(listHistory).not.toHaveBeenCalled()
    expect(fetchHistoryContent).not.toHaveBeenCalled()
    const details = wrapper.get('details')
    ;(details.element as HTMLDetailsElement).open = true
    await new Promise<void>(resolve => setTimeout(resolve, 0))
    await flushPromises()
    expect(listHistory).toHaveBeenCalledOnce()
    expect(wrapper.get('img[alt="a cat"]').exists()).toBe(true)
  })

  it('clears an old account preview immediately and ignores its late cloud response', async () => {
    const oldContent = deferred<Blob>()
    listLocalImages.mockResolvedValue([localItem()])
    fetchHistoryContent.mockReturnValue(oldContent.promise)
    const wrapper = await mountView()
    await flushPromises()
    expect(wrapper.get('img[alt="local cat"]').exists()).toBe(true)
    listLocalImages.mockResolvedValue([])
    reactive(auth).user = { id: 2 }
    await flushPromises()
    expect(wrapper.find('img[alt="local cat"]').exists()).toBe(false)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:history-1')
    oldContent.resolve(new Blob(['old cloud image'], { type: 'image/png' }))
    await flushPromises()
    expect(wrapper.find('img[alt="a cat"]').exists()).toBe(false)
    expect(URL.createObjectURL).toHaveBeenCalledTimes(1)
    expect((wrapper.get('details').element as HTMLDetailsElement).open).toBe(false)
  })

  it('fetches protected image bytes and restores them when the user returns to the page', async () => {
    const first = await mountView()
    await flushPromises()
    expect(fetchHistoryContent).toHaveBeenCalledWith(11)
    expect(first.get('img[alt="a cat"]').attributes('src')).toBe('blob:history-1')
    first.unmount()
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:history-1')

    const second = await mountView()
    await flushPromises()
    expect(second.get('img[alt="a cat"]').attributes('src')).toBe('blob:history-2')
    expect(listHistory).toHaveBeenCalledTimes(2)
  })

  it('retains history previews when switching generation modes and avoids refetching unchanged images', async () => {
    const wrapper = await mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'imageGen.tabEdit')!.trigger('click')
    await wrapper.get('details').findAll('button').find(button => button.text() === 'imageGen.history.reload')!.trigger('click')
    await flushPromises()
    expect(wrapper.get('img[alt="a cat"]').attributes('src')).toBe('blob:history-1')
    expect(fetchHistoryContent).toHaveBeenCalledTimes(1)
  })

  it('keeps content failures distinct from expired images and allows reloading', async () => {
    fetchHistoryContent.mockRejectedValueOnce(new Error('temporary failure'))
    const wrapper = await mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('imageGen.history.loadFailed')
    expect(wrapper.text()).not.toContain('imageGen.history.expired')
    await wrapper.get('details').findAll('button').find(button => button.text() === 'imageGen.history.reload')!.trigger('click')
    await flushPromises()
    expect(wrapper.get('img[alt="a cat"]').attributes('src')).toBe('blob:history-1')
  })

  it.each(['https://images.example.com/cat.png', 'http://images.example.com/cat.png'])(
    'displays a URL-only history image from %s without fetching it with panel credentials',
    async (contentUrl) => {
      vi.stubGlobal('fetch', vi.fn())
      listHistory.mockResolvedValue({ items: [historyItem({ content_url: contentUrl })] })
      fetchHistoryContent.mockRejectedValue({ status: 404 })
      const wrapper = await mountView()
      await flushPromises()

      expect(fetchHistoryContent).toHaveBeenCalledWith(11)
      expect(wrapper.get('img[alt="a cat"]').attributes('src')).toBe(contentUrl)
      expect(wrapper.get('a[download]').attributes('href')).toBe(contentUrl)
      expect(fetch).not.toHaveBeenCalled()
      expect(URL.createObjectURL).not.toHaveBeenCalled()

      wrapper.unmount()
      expect(URL.revokeObjectURL).not.toHaveBeenCalled()
    },
  )

  it('prefers stored image bytes when a history record also has an external URL', async () => {
    listHistory.mockResolvedValue({ items: [historyItem({ content_url: 'https://expired.example.com/cat.png' })] })
    const wrapper = await mountView()
    await flushPromises()

    expect(wrapper.get('img[alt="a cat"]').attributes('src')).toBe('blob:history-1')
    expect(wrapper.get('a[download]').attributes('href')).toBe('blob:history-1')
    expect(fetchHistoryContent).toHaveBeenCalledTimes(1)
  })

  it.each([
    'javascript:alert(1)',
    'data:image/svg+xml,<svg/>',
    'blob:https://images.example.com/image',
    'ftp://images.example.com/cat.png',
    '/relative-image.png',
    'not a URL',
  ])('does not expose an unsafe external history URL: %s', async (contentUrl) => {
    listHistory.mockResolvedValue({ items: [historyItem({ content_url: contentUrl })] })
    fetchHistoryContent.mockRejectedValue({ status: 404, reason: 'IMAGE_GENERATION_HISTORY_NO_CONTENT' })
    const wrapper = await mountView()
    await flushPromises()

    expect(wrapper.find('img[alt="a cat"]').exists()).toBe(false)
    expect(wrapper.find('a[download]').exists()).toBe(false)
    expect(wrapper.text()).toContain('imageGen.history.loadFailed')
  })

  it.each([
    { status: 401 },
    { status: 403 },
    { status: 500 },
    { status: 404, reason: 'IMAGE_GENERATION_HISTORY_NOT_FOUND' },
  ])('does not bypass a content error by using an external URL: %j', async (error) => {
    listHistory.mockResolvedValue({ items: [historyItem({ content_url: 'https://images.example.com/cat.png' })] })
    fetchHistoryContent.mockRejectedValue(error)
    const wrapper = await mountView()
    await flushPromises()

    expect(wrapper.find('img[alt="a cat"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('imageGen.history.loadFailed')
  })

  it('shows a retryable loading error if an external preview cannot be displayed', async () => {
    listHistory.mockResolvedValue({ items: [historyItem({ content_url: 'https://images.example.com/cat.png' })] })
    fetchHistoryContent.mockRejectedValue({ status: 404 })
    const wrapper = await mountView()
    await flushPromises()

    await wrapper.get('img[alt="a cat"]').trigger('error')
    expect(wrapper.find('img[alt="a cat"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('imageGen.history.loadFailed')
    expect(URL.revokeObjectURL).not.toHaveBeenCalled()

    await wrapper.get('details').findAll('button').find(button => button.text() === 'imageGen.history.reload')!.trigger('click')
    await flushPromises()
    expect(wrapper.get('img[alt="a cat"]').attributes('src')).toBe('https://images.example.com/cat.png')
  })

  it('does not fetch unavailable content and releases a deleted image preview', async () => {
    listHistory.mockResolvedValue({ items: [historyItem(), historyItem({ id: 12, prompt: 'expired', content_available: false })] })
    const wrapper = await mountView()
    await flushPromises()
    expect(fetchHistoryContent).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('imageGen.history.expired')
    await wrapper.findAll('button').find(button => button.text() === '×')!.trigger('click')
    await flushPromises()
    expect(deleteHistory).toHaveBeenCalledWith(11)
    expect(wrapper.find('img[alt="a cat"]').exists()).toBe(false)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:history-1')
  })

  it('ignores an image response arriving after unmount', async () => {
    const pending = deferred<Blob>()
    fetchHistoryContent.mockReturnValue(pending.promise)
    const wrapper = await mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('common.loading')
    expect(wrapper.text()).not.toContain('imageGen.history.expired')
    wrapper.unmount()
    pending.resolve(new Blob(['png'], { type: 'image/png' }))
    await flushPromises()
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })

  it('discards a stale image response when a newer history reload has replaced the record', async () => {
    const pending = deferred<Blob>()
    fetchHistoryContent.mockImplementation((id: number) => id === 11
      ? pending.promise
      : Promise.resolve(new Blob(['png'], { type: 'image/png' })))
    const wrapper = await mountView()
    await flushPromises()
    listHistory.mockResolvedValue({ items: [historyItem({ id: 12, prompt: 'a dog' })] })
    await wrapper.get('details').findAll('button').find(button => button.text() === 'imageGen.history.reload')!.trigger('click')
    await flushPromises()
    pending.resolve(new Blob(['old'], { type: 'image/png' }))
    await flushPromises()
    expect(wrapper.find('img[alt="a cat"]').exists()).toBe(false)
    expect(wrapper.get('img[alt="a dog"]').attributes('src')).toBe('blob:history-1')
    expect(URL.createObjectURL).toHaveBeenCalledTimes(1)
  })
})
