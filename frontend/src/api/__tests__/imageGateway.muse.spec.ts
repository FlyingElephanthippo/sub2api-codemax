import { afterEach, describe, expect, it, vi } from 'vitest'

import { generateImage, normalizedParamsFor, type ImageGenerateRequest, type ImageReference } from '../imageGateway'

vi.mock('../client', () => ({ buildGatewayUrl: (path: string) => path }))

const request: ImageGenerateRequest = {
  apiBase: 'https://gateway.example/', apiKey: 'muse-test-key',
  model: 'muse-image', prompt: 'a teacup', aspectRatio: '16:9',
  size: '3840x2160', imageSize: '4K', quality: 'high', background: 'transparent',
  outputFormat: 'webp', outputCompression: 70,
}
const dataUrl = 'data:image/png;base64,iVBORw0KGgo='
const reference: ImageReference = { id: 'reference', name: 'reference.png', dataUrl }

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

describe('Muse native image gateway', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('sends one synchronous generation with native geometry and gateway Key', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json({ data: [{ b64_json: 'generated', mime_type: 'image/png' }] }))
    vi.stubGlobal('fetch', fetchMock)
    const signal = new AbortController().signal

    expect(await generateImage(request, signal)).toEqual({ data: 'generated', mimeType: 'image/png' })
    expect(fetchMock).toHaveBeenCalledOnce()
    const [url, options] = fetchMock.mock.calls[0]
    expect(url).toBe('https://gateway.example/v1/images/generations')
    expect(JSON.parse(options.body)).toEqual({
      model: 'muse-image', prompt: 'a teacup', size: '16:9', n: 1, response_format: 'b64_json',
    })
    expect(options).toMatchObject({ method: 'POST', signal, credentials: 'omit', redirect: 'error',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer muse-test-key' } })
  })

  it('resolves inline edit references once without sending duplicate representations', async () => {
    const loadDataUrl = vi.fn().mockResolvedValue(dataUrl)
    const fetchMock = vi.fn().mockResolvedValue(json({ data: [{ b64_json: 'edited' }] }))
    vi.stubGlobal('fetch', fetchMock)

    await generateImage({ ...request, references: [reference, {
      id: 'lazy', name: 'second.png', url: 'https://upstream.example/reference', loadDataUrl,
    }] }, new AbortController().signal)

    expect(loadDataUrl).toHaveBeenCalledOnce()
    expect(fetchMock).toHaveBeenCalledOnce()
    expect(fetchMock.mock.calls[0][0]).toBe('https://gateway.example/v1/images/edits')
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({
      model: 'muse-image', prompt: 'a teacup', size: '16:9', n: 1, response_format: 'b64_json',
      images: [{ url: dataUrl }, { url: dataUrl }],
    })
  })

  it.each([400, 404, 409, 415, 422, 500, 503])('does not retry a failed creation (%s)', async status => {
    const fetchMock = vi.fn().mockResolvedValue(json({ error: { message: 'Muse unavailable' } }, status))
    vi.stubGlobal('fetch', fetchMock)
    await expect(generateImage({ ...request, references: [reference] }, new AbortController().signal))
      .rejects.toThrow(`API ${status}: Muse unavailable`)
    expect(fetchMock).toHaveBeenCalledOnce()
  })

  it.each([{}, { data: [{ url: 'https://upstream.example/private.png' }] }, { task_id: 'upstream-task' }])(
    'does not fetch upstream URLs or poll an unexpected task: %j', async body => {
      const fetchMock = vi.fn().mockResolvedValue(json(body))
      vi.stubGlobal('fetch', fetchMock)
      await expect(generateImage(request, new AbortController().signal)).rejects.toThrow('Muse returned no inline image')
      expect(fetchMock).toHaveBeenCalledOnce()
    },
  )

  it('rejects a fifth reference before reading files or calling the gateway', async () => {
    const loadDataUrl = vi.fn().mockResolvedValue(dataUrl)
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await expect(generateImage({ ...request, references: Array.from({ length: 5 }, () => ({
      id: 'lazy', name: 'reference.png', loadDataUrl,
    })) }, new AbortController().signal)).rejects.toThrow('at most 4')
    expect(loadDataUrl).not.toHaveBeenCalled()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each(['data:image/gif;base64,R0lG', 'data:image/png;base64,===', 'https://upstream.example/ref.png'])(
    'rejects an unsupported or invalid inline reference: %s', async invalid => {
      const fetchMock = vi.fn()
      vi.stubGlobal('fetch', fetchMock)
      await expect(generateImage({ ...request, references: [{ ...reference, dataUrl: invalid }] }, new AbortController().signal))
        .rejects.toThrow('failed to read reference image')
      expect(fetchMock).not.toHaveBeenCalled()
    },
  )

  it('enforces the per-reference limit before decoding or sending a large payload', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const huge = `data:image/png;base64,${'A'.repeat(Math.ceil((10 * 1024 * 1024 + 3) / 3) * 4)}`
    await expect(generateImage({ ...request, references: [{ ...reference, dataUrl: huge }] }, new AbortController().signal))
      .rejects.toThrow('exceeds 10 MiB')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('enforces the total inline reference budget before sending the payload', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const large = `data:image/png;base64,${'A'.repeat(9 * 1024 * 1024 * 4 / 3)}`
    await expect(generateImage({ ...request, references: Array.from({ length: 3 }, () => ({
      ...reference, dataUrl: large,
    })) }, new AbortController().signal)).rejects.toThrow('exceed 24 MiB')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('keeps only supported geometry when archiving native Muse parameters', () => {
    expect(normalizedParamsFor('muse-native', request)).toEqual({ aspect_ratio: '16:9' })
  })
})
