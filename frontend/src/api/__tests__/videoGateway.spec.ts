import { afterEach, describe, expect, it, vi } from 'vitest'
import { buildGatewayUrl } from '../url'
import { createVideo, fetchVideoContent, getVideoTask, listVideoModels, normalizeVideoTask, readVideoReference, videoPollDelay, VIDEO_CONTENT_TIMEOUT_MS } from '../videoGateway'

function json(value: unknown, status = 200) { return new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } }) }
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); localStorage.clear() })

describe('video model discovery', () => {
  it('uses the selected gateway key, filters advertised concrete video models and keeps their IDs', async () => {
    localStorage.setItem('auth_token', 'panel-secret')
    const fetcher = vi.fn().mockResolvedValue(json({ data: [
      { id: 'muse-video' }, { id: 'grok-imagine-video-1.5' }, { id: 'muse-spark' },
      { id: 'muse-video' }, { id: 'grok-imagine-video-*' }, { id: 'sora' }, null, {}, { id: 42 },
    ] }))
    vi.stubGlobal('fetch', fetcher)
    expect(await listVideoModels('user-key')).toEqual(['muse-video', 'grok-imagine-video-1.5'])
    expect(fetcher).toHaveBeenCalledWith(buildGatewayUrl('/v1/models?purpose=video'), expect.objectContaining({
      headers: { Authorization: 'Bearer user-key' }, credentials: 'omit', redirect: 'error',
    }))
    expect(JSON.stringify(fetcher.mock.calls)).not.toContain('panel-secret')
  })
  it.each([null, {}, { data: 'all' }])('rejects malformed model responses %j', async value => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(value)))
    await expect(listVideoModels('key')).rejects.toThrow('Invalid video model list')
  })
})

describe('video task protocol', () => {
  it('creates exactly once with the advertised model, duration and reference image', async () => {
    const fetcher = vi.fn().mockResolvedValue(json({ request_id: 'task_one' }))
    vi.stubGlobal('fetch', fetcher)
    const request = { model: 'muse-video', prompt: 'A red bird', duration: 8, aspect_ratio: '9:16', image: { url: 'data:image/png;base64,AA==' } }
    expect(await createVideo('user-key', request)).toEqual({ id: 'task_one', status: 'pending', progress: 0 })
    expect(fetcher).toHaveBeenCalledOnce()
    expect(fetcher.mock.calls[0][0]).toBe(buildGatewayUrl('/v1/videos'))
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual(request)
  })
  it.each(['pending', 'queued', 'processing', 'in_progress'])('normalizes %s to pending', status => {
    expect(normalizeVideoTask({ id: 'task_one', status, progress: 45 })).toEqual({ id: 'task_one', status: 'pending', progress: 45 })
  })
  it.each(['done', 'completed', 'succeeded'])('normalizes %s to completed', status => {
    expect(normalizeVideoTask({ id: 'task_one', status })).toEqual({ id: 'task_one', status: 'done', progress: 100 })
  })
  it('preserves failed upstream errors', () => {
    expect(normalizeVideoTask({ id: 'task_one', status: 'failed', error: { message: 'No Muse account available' } }).error).toBe('No Muse account available')
  })
  it.each([null, {}, { id: '../../accounts', status: 'done' }, { id: 'a', status: 'unrecognized' }])('rejects malformed task %j', value => {
    expect(() => normalizeVideoTask(value)).toThrow()
  })
  it('binds a polling response to the expected task and accepts a status without an echoed ID', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(json({ status: 'pending' })).mockResolvedValueOnce(json({ id: 'other', status: 'done' }))
    vi.stubGlobal('fetch', fetcher)
    expect((await getVideoTask('key', 'one')).id).toBe('one')
    await expect(getVideoTask('key', 'one')).rejects.toThrow('mismatch')
  })
  it('surfaces API errors and never automatically retries a generation', async () => {
    const fetcher = vi.fn().mockResolvedValue(json({ error: { message: 'Muse account is not configured' } }, 503))
    vi.stubGlobal('fetch', fetcher)
    await expect(createVideo('key', { model: 'muse-video', prompt: 'Bird', duration: 6, aspect_ratio: '16:9' })).rejects.toThrow('Muse account is not configured')
    expect(fetcher).toHaveBeenCalledOnce()
  })
  it('cancels the polling delay promptly', async () => {
    vi.useFakeTimers()
    const controller = new AbortController()
    const waiting = videoPollDelay(controller.signal)
    const rejected = expect(waiting).rejects.toMatchObject({ name: 'AbortError' })
    controller.abort()
    await rejected
    expect(vi.getTimerCount()).toBe(0)
  })
})

describe('protected video download', () => {
  it('reports received bytes while buffering a complete authenticated video', async () => {
    const body = new ReadableStream({ start(controller) {
      controller.enqueue(new Uint8Array([1, 2]))
      controller.enqueue(new Uint8Array([3]))
      controller.close()
    } })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body, { headers: { 'Content-Type': 'video/mp4', 'Content-Length': '3' } })))
    const progress = vi.fn()
    expect((await fetchVideoContent('key', 'one', undefined, progress)).size).toBe(3)
    expect(progress.mock.calls.map(([value]) => value)).toEqual([
      { received: 0, total: 3 }, { received: 2, total: 3 }, { received: 3, total: 3 },
    ])
  })
  it('times out even when the request never returns headers', async () => {
    vi.useFakeTimers()
    const fetcher = vi.fn().mockReturnValue(new Promise(() => {}))
    vi.stubGlobal('fetch', fetcher)
    const pending = fetchVideoContent('key', 'one')
    const rejected = expect(pending).rejects.toThrow('videoGen.downloadTimeout')
    await vi.advanceTimersByTimeAsync(VIDEO_CONTENT_TIMEOUT_MS)
    await rejected
    expect(fetcher.mock.calls[0][1].signal.aborted).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
  })
  it('times out a stalled body without waiting for its underlying cancellation', async () => {
    vi.useFakeTimers()
    const cancel = vi.fn().mockReturnValue(new Promise(() => {}))
    const body = new ReadableStream({ start(controller) { controller.enqueue(new Uint8Array([1])) }, cancel })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body, { headers: { 'Content-Type': 'video/mp4' } })))
    const pending = fetchVideoContent('key', 'one')
    const rejected = expect(pending).rejects.toThrow('videoGen.downloadTimeout')
    await vi.advanceTimersByTimeAsync(VIDEO_CONTENT_TIMEOUT_MS)
    await rejected
    expect(cancel).toHaveBeenCalledOnce()
    expect(vi.getTimerCount()).toBe(0)
  })
  it('cancels a stalled body promptly and removes its timeout', async () => {
    vi.useFakeTimers()
    const cancel = vi.fn().mockReturnValue(new Promise(() => {}))
    const body = new ReadableStream({ cancel })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body, { headers: { 'Content-Type': 'video/mp4' } })))
    const controller = new AbortController()
    const pending = fetchVideoContent('key', 'one', controller.signal)
    const rejected = expect(pending).rejects.toMatchObject({ name: 'AbortError' })
    await vi.advanceTimersByTimeAsync(0)
    controller.abort()
    await rejected
    expect(cancel).toHaveBeenCalledOnce()
    expect(vi.getTimerCount()).toBe(0)
  })
  it('rejects a truncated video instead of creating an unusable preview', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('short', { headers: { 'Content-Type': 'video/mp4', 'Content-Length': '10' } })))
    await expect(fetchVideoContent('key', 'one')).rejects.toThrow('videoGen.downloadIncomplete')
  })
  it('downloads bytes through the authenticated gateway path, ignoring upstream media links', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(new Uint8Array([1, 2, 3]), { headers: { 'Content-Type': 'video/mp4' } }))
    vi.stubGlobal('fetch', fetcher)
    const content = await fetchVideoContent('user-key', 'task_one')
    expect(content.size).toBe(3)
    expect(content.type).toBe('video/mp4')
    expect(fetcher).toHaveBeenCalledWith(buildGatewayUrl('/v1/videos/task_one/content'), expect.objectContaining({
      headers: { Authorization: 'Bearer user-key' }, redirect: 'error', credentials: 'omit',
    }))
  })
  it('does not send credentials for an invalid task ID', async () => {
    const fetcher = vi.fn()
    vi.stubGlobal('fetch', fetcher)
    await expect(fetchVideoContent('secret', 'https://foreign.example/video')).rejects.toThrow('Invalid video task ID')
    expect(fetcher).not.toHaveBeenCalled()
  })
  it.each(['application/json', 'text/html'])('rejects %s masquerading as video', async mime => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('failure', { headers: { 'Content-Type': mime } })))
    await expect(fetchVideoContent('key', 'one')).rejects.toThrow('Invalid video content type')
  })
  it('rejects oversized content before buffering it', async () => {
    const response = new Response('bytes', { headers: { 'Content-Type': 'video/mp4', 'Content-Length': String(201 * 1024 * 1024) } })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response))
    await expect(fetchVideoContent('key', 'one')).rejects.toThrow('too large')
  })
  it('rejects empty video content', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { headers: { 'Content-Type': 'video/mp4' } })))
    await expect(fetchVideoContent('key', 'one')).rejects.toThrow('Empty video content')
  })
})

describe('video image reference validation', () => {
  it('reads a valid PNG as a data URL', async () => {
    const file = new File([new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10])], 'ref.png', { type: 'image/png' })
    expect(await readVideoReference(file)).toMatch(/^data:image\/png;base64,/)
  })
  it('rejects MIME spoofing and SVG', async () => {
    await expect(readVideoReference(new File(['html'], 'image.png', { type: 'image/png' }))).rejects.toThrow('videoGen.referenceError')
    await expect(readVideoReference(new File(['<svg/>'], 'image.svg', { type: 'image/svg+xml' }))).rejects.toThrow('videoGen.referenceError')
  })
  it('rejects oversized references before reading bytes', async () => {
    const file = new File(['x'], 'large.png', { type: 'image/png' })
    Object.defineProperty(file, 'size', { value: 10 * 1024 * 1024 + 1 })
    await expect(readVideoReference(file)).rejects.toThrow('videoGen.referenceError')
  })
})
