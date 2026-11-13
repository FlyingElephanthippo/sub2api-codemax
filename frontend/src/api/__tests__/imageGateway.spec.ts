import { afterEach, describe, expect, it, vi } from 'vitest'

import { generateImage, type ImageGenerateRequest, type ImageReference } from '../imageGateway'

vi.mock('../client', () => ({ buildGatewayUrl: (path: string) => path }))

const apiBase = 'https://gateway.example'
const taskID = 'imgtask_0123456789abcdef0123456789abcdef'
const request: ImageGenerateRequest = {
  apiBase,
  apiKey: 'image-test-key',
  model: 'gpt-image-2',
  prompt: 'a cat',
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function asyncUnavailable(): Response {
  return json({ error: { message: 'async image tasks are not enabled' } }, 404)
}

function acceptedTask() {
  return { id: taskID, task_id: taskID, object: 'image.generation.task', status: 'processing' }
}

function completedTask() {
  return { ...acceptedTask(), status: 'completed', result: { data: [{ b64_json: 'generated-image' }] } }
}

const sourceBytes = [0, 255, 137, 80, 78, 71, 13, 10]
const sourceDataUrl = `data:image/png;base64,${btoa(String.fromCharCode(...sourceBytes))}`
const reference: ImageReference = { id: 'source', name: '原始参考图.png', dataUrl: sourceDataUrl, mimeType: 'image/png' }

function readBytes(blob: Blob): Promise<number[]> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(Array.from(new Uint8Array(reader.result as ArrayBuffer)))
    reader.onerror = () => reject(reader.error)
    reader.readAsArrayBuffer(blob)
  })
}

describe('image gateway response handling', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it.each(['task_id', 'id'])('uses completed image data before interpreting upstream %s as a task', async identifier => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(asyncUnavailable())
      .mockResolvedValueOnce(json({ [identifier]: 'req_upstream', data: [{ b64_json: 'generated-image' }] }))
    vi.stubGlobal('fetch', fetchMock)

    expect(await generateImage(request, new AbortController().signal)).toEqual({ data: 'generated-image' })
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      `${apiBase}/v1/images/generations/async`,
      `${apiBase}/v1/images/generations`,
    ])
  })

  it('downloads a completed image URL without looking up its upstream task_id', async () => {
    const imageResponse = new Response(null, { headers: { 'Content-Type': 'image/png' } })
    // jsdom 的 FileReader 需要同环境 Blob，不能使用 Node Response 生成的 Blob。
    imageResponse.blob = async () => new Blob(['image-bytes'], { type: 'image/png' })
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(asyncUnavailable())
      .mockResolvedValueOnce(json({ task_id: 'req_upstream', data: [{ url: '/generated.png' }] }))
      .mockResolvedValueOnce(imageResponse)
    vi.stubGlobal('fetch', fetchMock)

    expect(await generateImage(request, new AbortController().signal)).toEqual({
      data: btoa('image-bytes'), mimeType: 'image/png',
    })
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      `${apiBase}/v1/images/generations/async`,
      `${apiBase}/v1/images/generations`,
      `${window.location.origin}/generated.png`,
    ])
  })

  it.each(['gpt-image-2', 'dreamina-seedream-5.0-pro'])('polls an accepted local async task for %s', async model => {
    const onAccepted = vi.fn()
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(json({ ...acceptedTask(), poll_url: `/v1/images/tasks/${taskID}` }, 202))
      .mockResolvedValueOnce(json(completedTask()))
    vi.stubGlobal('fetch', fetchMock)

    expect(await generateImage({ ...request, model, onAccepted }, new AbortController().signal)).toEqual({
      data: 'generated-image',
    })
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      `${apiBase}/v1/images/generations/async`,
      `${apiBase}/v1/images/tasks/${taskID}`,
    ])
    expect(fetchMock.mock.calls[1][1].headers).toEqual({ Authorization: 'Bearer image-test-key' })
    if (model === 'gpt-image-2') expect(onAccepted).toHaveBeenCalledOnce()
  })

  it('polls a real local task returned by the synchronous endpoint', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(asyncUnavailable())
      .mockResolvedValueOnce(json(acceptedTask(), 202))
      .mockResolvedValueOnce(json(completedTask()))
    vi.stubGlobal('fetch', fetchMock)

    expect(await generateImage(request, new AbortController().signal)).toEqual({ data: 'generated-image' })
    expect(fetchMock.mock.calls[2][0]).toBe(`${apiBase}/v1/images/tasks/${taskID}`)
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('uses an explicit poll_url for a task returned by the synchronous endpoint', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(asyncUnavailable())
      .mockResolvedValueOnce(json({ task_id: 'provider-task', poll_url: '/provider/tasks/provider-task' }, 202))
      .mockResolvedValueOnce(json(completedTask()))
    vi.stubGlobal('fetch', fetchMock)

    expect(await generateImage(request, new AbortController().signal)).toEqual({ data: 'generated-image' })
    expect(fetchMock.mock.calls[2][0]).toBe(`${apiBase}/provider/tasks/provider-task`)
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it.each([{}, { task_id: 'req_upstream' }, { id: 'req_upstream' }, null])(
    'rejects a response with no image or valid task without polling: %j', async body => {
      const fetchMock = vi.fn()
        .mockResolvedValueOnce(asyncUnavailable())
        .mockResolvedValueOnce(json(body))
      vi.stubGlobal('fetch', fetchMock)

      await expect(generateImage(request, new AbortController().signal)).rejects.toThrow('channel 2 returned no image')
      expect(fetchMock).toHaveBeenCalledTimes(2)
    },
  )

  it('continues parsing Gemini inline image results', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(json({
      candidates: [{ content: { parts: [{ inlineData: { data: 'gemini-image', mimeType: 'image/png' } }] } }],
    }))
    vi.stubGlobal('fetch', fetchMock)

    expect(await generateImage({ ...request, model: 'gemini-3-pro-image' }, new AbortController().signal)).toEqual({
      data: 'gemini-image', mimeType: 'image/png',
    })
    expect(fetchMock).toHaveBeenCalledOnce()
    expect(fetchMock.mock.calls[0][0]).toBe(`${apiBase}/v1beta/models/gemini-3-pro-image:generateContent`)
  })
})

describe('OpenAI image edit request encoding', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it.each([404, 405])('keeps the original image and form fields through async %i fallback', async status => {
    const snapshot = [{ index: 1, name: reference.name, content_type: 'image/png', size_bytes: sourceBytes.length }]
    const prompt = '  保留猫与四个面包。\nOnly change the weather!  '
    const controller = new AbortController()
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(json({ error: { message: 'async unavailable' } }, status))
      .mockResolvedValueOnce(json({ task_id: 'req_upstream', data: [{ b64_json: 'edited-image' }] }))
    vi.stubGlobal('fetch', fetchMock)

    expect(await generateImage({
      ...request, prompt, size: '1536x1024', imageSize: '4K', quality: 'high', background: 'transparent',
      outputFormat: 'webp', outputCompression: 37, referenceSnapshot: snapshot, references: [reference],
      requestID: 'image-edit-request',
    }, controller.signal)).toEqual({ data: 'edited-image' })

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      `${apiBase}/v1/images/edits/async`, `${apiBase}/v1/images/edits`,
    ])
    for (const [, init] of fetchMock.mock.calls) {
      expect(init.body).toBeInstanceOf(FormData)
      expect(init.signal).toBe(controller.signal)
      const headers = new Headers(init.headers)
      expect(headers.get('Content-Type')).toBeNull()
      expect(headers.get('Authorization')).toBe('Bearer image-test-key')
      const form = init.body as FormData
      expect(Object.fromEntries(Array.from(form.entries()).filter(([, value]) => typeof value === 'string'))).toEqual({
        model: 'gpt-image-2', prompt, size: '1536x1024', n: '1', response_format: 'b64_json',
        quality: 'high', background: 'transparent', output_format: 'webp', output_compression: '37',
        reference_snapshot: JSON.stringify(snapshot),
      })
      const image = form.get('image') as File
      expect(image.name).toBe(reference.name)
      expect(image.type).toBe('image/png')
      expect(await readBytes(image)).toEqual(sourceBytes)
      expect(form.has('image[]')).toBe(false)
      expect(form.has('images')).toBe(false)
    }
    expect(new Headers(fetchMock.mock.calls[0][1].headers).get('Idempotency-Key')).toBe('image-edit-request')
  })

  it('uses repeated image[] fields for multiple references in their original order', async () => {
    const secondBytes = [255, 216, 255, 0, 128]
    const second = {
      id: 'second', name: 'second.jpg',
      loadDataUrl: vi.fn(async () => `data:image/jpeg;base64,${btoa(String.fromCharCode(...secondBytes))}`),
    }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(json(acceptedTask(), 202))
      .mockResolvedValueOnce(json(completedTask()))
    vi.stubGlobal('fetch', fetchMock)

    expect(await generateImage({ ...request, references: [reference, second] }, new AbortController().signal)).toEqual({
      data: 'generated-image',
    })
    const form = fetchMock.mock.calls[0][1].body as FormData
    expect(form.get('size')).toBe('1024x1024')
    expect(form.has('image')).toBe(false)
    const files = form.getAll('image[]') as File[]
    expect(files.map(file => [file.name, file.type])).toEqual([[reference.name, 'image/png'], ['second.jpg', 'image/jpeg']])
    expect(await Promise.all(files.map(readBytes))).toEqual([sourceBytes, secondBytes])
    expect(second.loadDataUrl).toHaveBeenCalledOnce()
    expect(fetchMock.mock.calls[1][0]).toBe(`${apiBase}/v1/images/tasks/${taskID}`)
  })

  it.each([undefined, '', 'https://example.com/source.png', 'data:text/plain;base64,AA==', 'data:image/png;base64,%%%', 'data:image/png;base64,'])(
    'rejects an unreadable reference before submitting any request: %s', async dataUrl => {
      const fetchMock = vi.fn()
      vi.stubGlobal('fetch', fetchMock)

      await expect(generateImage({
        ...request, references: [reference, { id: 'invalid', name: 'bad.png', dataUrl }],
      }, new AbortController().signal)).rejects.toThrow('failed to read reference image')
      expect(fetchMock).not.toHaveBeenCalled()
    },
  )

  it('does not repeat the same multipart edit after a validation error', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(asyncUnavailable())
      .mockResolvedValueOnce(json({ error: { message: 'edit validation failed' } }, 422))
    vi.stubGlobal('fetch', fetchMock)

    await expect(generateImage({ ...request, references: [reference] }, new AbortController().signal)).rejects.toThrow(
      'API 422: edit validation failed',
    )
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('stops before submission when cancelled while loading a reference', async () => {
    const controller = new AbortController()
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    await expect(generateImage({ ...request, references: [{
      id: 'loading', name: 'loading.png', loadDataUrl: async () => {
        controller.abort()
        return sourceDataUrl
      },
    }] }, controller.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('propagates cancellation during task polling without another submission or lookup', async () => {
    const controller = new AbortController()
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(json(acceptedTask(), 202))
      .mockImplementationOnce((_url, init) => {
        expect(init.signal).toBe(controller.signal)
        controller.abort()
        return Promise.reject(new DOMException('cancelled', 'AbortError'))
      })
    vi.stubGlobal('fetch', fetchMock)

    await expect(generateImage({ ...request, references: [reference] }, controller.signal)).rejects.toMatchObject({
      name: 'AbortError',
    })
    expect(fetchMock.mock.calls[0][1].body).toBeInstanceOf(FormData)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it.each(['w-gpt-image-2', 'dreamina-seedream-5.0-pro'])('preserves the JSON edit protocol for %s', async model => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(json(acceptedTask(), 202))
      .mockResolvedValueOnce(json(completedTask()))
    vi.stubGlobal('fetch', fetchMock)

    await generateImage({ ...request, model, references: [reference] }, new AbortController().signal)

    const init = fetchMock.mock.calls[0][1]
    expect(new Headers(init.headers).get('Content-Type')).toBe('application/json')
    expect(JSON.parse(init.body)).toMatchObject({ model, prompt: request.prompt, images: [{ image_url: sourceDataUrl }] })
    expect(fetchMock.mock.calls[0][0]).toBe(`${apiBase}/v1/images/edits/async`)
  })

  it('preserves JSON when a W edit falls back to the synchronous endpoint', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(asyncUnavailable())
      .mockResolvedValueOnce(json({ data: [{ b64_json: 'edited-image' }] }))
    vi.stubGlobal('fetch', fetchMock)

    await generateImage({ ...request, model: 'w-gpt-image-2', references: [reference] }, new AbortController().signal)

    expect(fetchMock.mock.calls[1][0]).toBe(`${apiBase}/v1/images/edits`)
    expect(JSON.parse(fetchMock.mock.calls[1][1].body).images).toEqual([{ image_url: sourceDataUrl }])
  })

  it('preserves Gemini inlineData and its JSON content type', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(json({
      candidates: [{ content: { parts: [{ inlineData: { data: 'gemini-image', mimeType: 'image/png' } }] } }],
    }))
    vi.stubGlobal('fetch', fetchMock)

    await generateImage({ ...request, model: 'gemini-3-pro-image', references: [reference] }, new AbortController().signal)

    const init = fetchMock.mock.calls[0][1]
    expect(new Headers(init.headers).get('Content-Type')).toBe('application/json')
    expect(JSON.parse(init.body).contents[0].parts[0]).toEqual({
      inlineData: { mimeType: 'image/png', data: sourceDataUrl.split(',')[1] },
    })
    expect(fetchMock).toHaveBeenCalledOnce()
  })

  it('keeps text-to-image requests as JSON without multipart image fields', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(asyncUnavailable())
      .mockResolvedValueOnce(json({ data: [{ b64_json: 'generated-image' }] }))
    vi.stubGlobal('fetch', fetchMock)

    await generateImage(request, new AbortController().signal)

    expect(fetchMock).toHaveBeenCalledTimes(2)
    for (const [, init] of fetchMock.mock.calls) {
      expect(new Headers(init.headers).get('Content-Type')).toBe('application/json')
      expect(JSON.parse(init.body)).toMatchObject({ model: request.model, prompt: request.prompt })
      expect(JSON.parse(init.body)).not.toHaveProperty('images')
    }
  })
})
