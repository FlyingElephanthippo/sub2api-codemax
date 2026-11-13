import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import VideoGenView from '../VideoGenView.vue'

const mocks = vi.hoisted(() => ({
  listKeys: vi.fn(), listModels: vi.fn(), create: vi.fn(), getTask: vi.fn(), content: vi.fn(), delay: vi.fn(), readReference: vi.fn(),
}))
const auth = reactive({ isAuthenticated: true, user: { id: 1 } })
vi.mock('@/api/keys', () => ({ keysAPI: { list: mocks.listKeys } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))
vi.mock('@/api/videoGateway', async importOriginal => ({
  ...await importOriginal<typeof import('@/api/videoGateway')>(),
  createVideo: mocks.create, getVideoTask: mocks.getTask, fetchVideoContent: mocks.content,
  listVideoModels: mocks.listModels, videoPollDelay: mocks.delay, readVideoReference: mocks.readReference,
}))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

function apiKey(id: number, platform = 'grok', extra: Record<string, unknown> = {}) {
  return { id, name: `Key ${id}`, key: `sk-user-${id}`, status: 'active', group: { platform, name: `Group ${id}` }, ...extra }
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
const wrappers: Array<ReturnType<typeof mount>> = []
function mountView() {
  const wrapper = mount(VideoGenView)
  wrappers.push(wrapper)
  return wrapper
}
async function submit(wrapper: ReturnType<typeof mount>) {
  await wrapper.get('#video-prompt').setValue('A cat in the garden')
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

beforeEach(() => {
  vi.clearAllMocks()
  window.history.replaceState({}, '', '/video-gen')
  auth.isAuthenticated = true
  auth.user.id = 1
  mocks.listKeys.mockResolvedValue({ items: [apiKey(1), apiKey(2)], pages: 1 })
  mocks.listModels.mockResolvedValue(['muse-video'])
  mocks.create.mockResolvedValue({ id: 'task_one', status: 'pending', progress: 10 })
  mocks.getTask.mockResolvedValue({ id: 'task_one', status: 'done', progress: 100 })
  mocks.content.mockResolvedValue(new Blob(['video'], { type: 'video/mp4' }))
  mocks.delay.mockResolvedValue(undefined)
  mocks.readReference.mockResolvedValue('data:image/png;base64,test')
  vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:protected-video'), revokeObjectURL: vi.fn() }))
})
afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  vi.unstubAllGlobals()
})

describe('VideoGenView', () => {
  it('uses only usable video platform keys and offers models advertised by the chosen key', async () => {
    mocks.listKeys.mockResolvedValue({ items: [
      apiKey(1), apiKey(2, 'composite'), apiKey(3, 'openai'),
      apiKey(4, 'grok', { status: 'expired' }), apiKey(5, 'grok', { quota: 1, quota_used: 1 }),
    ] })
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('#video-key').findAll('option').map(item => item.attributes('value'))).toEqual(['', '1', '2'])
    expect(mocks.listModels).toHaveBeenCalledWith('sk-user-1', expect.any(AbortSignal))
    expect(wrapper.get('#video-model').findAll('option').map(item => item.attributes('value'))).toEqual(['', 'muse-video'])
  })
  it('disables generation if no model is advertised instead of using a static fallback', async () => {
    mocks.listModels.mockResolvedValue([])
    const wrapper = mountView()
    await flushPromises()
    await submit(wrapper)
    expect(wrapper.text()).toContain('videoGen.noModels')
    expect(mocks.create).not.toHaveBeenCalled()
  })
  it('ignores a late model response from the previous key', async () => {
    const old = deferred<string[]>()
    mocks.listModels.mockImplementation((key: string) => key === 'sk-user-1' ? old.promise : Promise.resolve(['grok-imagine-video']))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('#video-key').setValue('2')
    await flushPromises()
    old.resolve(['muse-video'])
    await flushPromises()
    expect(wrapper.get('#video-model').findAll('option').map(item => item.attributes('value'))).toEqual(['', 'grok-imagine-video'])
  })
  it('submits once, polls and previews protected video bytes without exposing a key in the URL', async () => {
    const wrapper = mountView()
    await flushPromises()
    await submit(wrapper)
    expect(mocks.create).toHaveBeenCalledOnce()
    expect(mocks.create).toHaveBeenCalledWith('sk-user-1', {
      model: 'muse-video', prompt: 'A cat in the garden', duration: 6, aspect_ratio: '16:9',
    }, expect.any(AbortSignal))
    expect(mocks.getTask).toHaveBeenCalledWith('sk-user-1', 'task_one', expect.any(AbortSignal))
    expect(mocks.content).toHaveBeenCalledWith('sk-user-1', 'task_one', expect.any(AbortSignal), expect.any(Function))
    expect(wrapper.get('video').attributes('src')).toBe('blob:protected-video')
    expect(wrapper.html()).not.toContain('sk-user-1')
  })
  it('distinguishes loading a completed video from generating and allows stopping the download', async () => {
    const transfer = deferred<Blob>()
    mocks.content.mockReturnValue(transfer.promise)
    const wrapper = mountView()
    await flushPromises()
    await submit(wrapper)
    expect(wrapper.text()).toContain('videoGen.loadingCompletedVideo')
    expect(wrapper.get('button[type="submit"]').text()).toBe('videoGen.loadingVideo')
    expect(wrapper.find('video').exists()).toBe(false)
    const signal = mocks.content.mock.calls[0][2] as AbortSignal
    const stop = wrapper.findAll('button').find(button => button.text() === 'videoGen.stopLoading')!
    await stop.trigger('click')
    expect(signal.aborted).toBe(true)
    expect(wrapper.text()).toContain('videoGen.loadVideo')
    transfer.resolve(new Blob(['late'], { type: 'video/mp4' }))
    await flushPromises()
    expect(wrapper.find('video').exists()).toBe(false)
    expect(mocks.create).toHaveBeenCalledOnce()
  })
  it('retains a completed task after a download timeout and retries only the content request', async () => {
    mocks.content.mockRejectedValueOnce(new Error('videoGen.downloadTimeout'))
    const wrapper = mountView()
    await flushPromises()
    await submit(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toBe('videoGen.downloadTimeout')
    expect(wrapper.text()).toContain('task_one')
    const load = wrapper.findAll('button').find(button => button.text() === 'videoGen.loadVideo')!
    await load.trigger('click')
    await flushPromises()
    expect(mocks.content).toHaveBeenCalledTimes(2)
    expect(mocks.getTask).toHaveBeenCalledOnce()
    expect(mocks.create).toHaveBeenCalledOnce()
    expect(wrapper.find('video').exists()).toBe(true)
  })
  it('restores an existing task with a currently available key without submitting a generation', async () => {
    window.history.replaceState({}, '', '/video-gen?task_id=task_one&key_id=2')
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('#video-key').element).toHaveProperty('value', '2')
    expect(mocks.getTask).toHaveBeenCalledWith('sk-user-2', 'task_one', expect.any(AbortSignal))
    expect(mocks.content).toHaveBeenCalledWith('sk-user-2', 'task_one', expect.any(AbortSignal), expect.any(Function))
    expect(mocks.create).not.toHaveBeenCalled()
    expect(wrapper.find('video').exists()).toBe(true)
    expect(window.location.search).not.toContain('sk-user')
  })
  it.each([
    '?task_id=task_one&key_id=42', '?task_id=../../admin&key_id=1',
    '?task_id=task_one', '?key_id=1', '?task_id=task_one&task_id=other&key_id=1',
  ])('rejects an unavailable key or invalid recovery link %s without requesting the task', async query => {
    window.history.replaceState({}, '', '/video-gen' + query)
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('videoGen.recoveryError')
    expect(mocks.create).not.toHaveBeenCalled()
    expect(mocks.getTask).not.toHaveBeenCalled()
    expect(mocks.content).not.toHaveBeenCalled()
  })
  it('does not duplicate submission while create is pending', async () => {
    const accepted = deferred<{ id: string; status: string; progress: number }>()
    mocks.create.mockReturnValue(accepted.promise)
    const wrapper = mountView()
    await flushPromises()
    await submit(wrapper)
    await wrapper.get('form').trigger('submit')
    expect(mocks.create).toHaveBeenCalledOnce()
    accepted.resolve({ id: 'task_one', status: 'pending', progress: 10 })
    await flushPromises()
  })
  it('clears the previous task and ignores a stale accepted response on key change', async () => {
    const accepted = deferred<{ id: string; status: string; progress: number }>()
    mocks.create.mockReturnValue(accepted.promise)
    const wrapper = mountView()
    await flushPromises()
    await submit(wrapper)
    const signal = mocks.create.mock.calls[0][2] as AbortSignal
    await wrapper.get('#video-key').setValue('2')
    await flushPromises()
    expect(signal.aborted).toBe(true)
    accepted.resolve({ id: 'task_old', status: 'pending', progress: 10 })
    await flushPromises()
    expect(wrapper.text()).not.toContain('task_old')
    expect(mocks.getTask).not.toHaveBeenCalled()
    expect(mocks.content).not.toHaveBeenCalled()
  })
  it('shows terminal errors without automatically retrying generation or downloading a result', async () => {
    mocks.getTask.mockResolvedValue({ id: 'task_one', status: 'failed', progress: 0, error: 'Muse login expired' })
    const wrapper = mountView()
    await flushPromises()
    await submit(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toBe('Muse login expired')
    expect(mocks.create).toHaveBeenCalledOnce()
    expect(mocks.content).not.toHaveBeenCalled()
  })
  it('stops polling and revokes video URLs when the login identity changes', async () => {
    const wrapper = mountView()
    await flushPromises()
    await submit(wrapper)
    expect(wrapper.find('video').exists()).toBe(true)
    auth.isAuthenticated = false
    await flushPromises()
    expect(wrapper.find('video').exists()).toBe(false)
    expect(wrapper.get('#video-key').findAll('option')).toHaveLength(1)
    expect(wrapper.get('#video-prompt').element).toHaveProperty('value', '')
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:protected-video')
  })
  it('retains an accepted task across a cached route switch and does not submit again', async () => {
    const accepted = deferred<{ id: string; status: string; progress: number }>()
    mocks.create.mockReturnValue(accepted.promise)
    const Host = defineComponent({
      components: { VideoGenView }, data: () => ({ show: true }),
      template: '<div><button id="navigate" @click="show = !show">navigate</button><KeepAlive include="VideoGenView"><VideoGenView v-if="show" /><aside v-else>Another page</aside></KeepAlive></div>',
    })
    const wrapper = mount(Host)
    wrappers.push(wrapper)
    await flushPromises()
    await submit(wrapper)
    await wrapper.get('#navigate').trigger('click')
    accepted.resolve({ id: 'task_one', status: 'pending', progress: 10 })
    await flushPromises()
    await wrapper.get('#navigate').trigger('click')
    await flushPromises()
    expect(mocks.create).toHaveBeenCalledOnce()
    expect(wrapper.get('video').attributes('src')).toBe('blob:protected-video')
  })
})
