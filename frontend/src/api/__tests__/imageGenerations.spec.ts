import { AxiosError, type InternalAxiosRequestConfig } from 'axios'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { apiClient, buildGatewayUrl } from '../client'
import { archiveHistory, fetchHistoryContent, listModels } from '../imageGenerations'

vi.mock('@/i18n', () => ({ getLocale: () => 'zh' }))

describe('image generation model API', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.removeItem('auth_token')
  })

  it('uses the selected API Key and preserves valid gateway IDs while filtering duplicates and invalid models', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: [
      { id: 'gpt-image-2' }, { id: 'claude-sonnet-4-5' }, { id: 'gpt-image-2' },
      { id: 'gemini-3-pro-image-preview' }, { id: '' }, { id: '   ' },
      { id: 123 }, {}, null, 'seedream-5.0',
    ] }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    localStorage.setItem('auth_token', 'panel-session-token')

    expect(await listModels('selected-group-key')).toEqual([
      'gpt-image-2', 'claude-sonnet-4-5', 'gemini-3-pro-image-preview',
    ])
    expect(fetchMock).toHaveBeenCalledOnce()
    expect(fetchMock).toHaveBeenCalledWith(buildGatewayUrl('/v1/models?purpose=image-generation'), {
      headers: { Authorization: 'Bearer selected-group-key' }, signal: undefined,
    })
  })

  it('passes the cancellation signal to the gateway request', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('{"data":[]}', { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    const controller = new AbortController()

    await listModels('selected-group-key', controller.signal)

    expect(fetchMock.mock.calls[0][1].signal).toBe(controller.signal)
  })

  it('preserves API error messages, codes and status', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { message: 'The API key is disabled', code: 'invalid_api_key' },
    }), { status: 401, statusText: 'Unauthorized' })))

    await expect(listModels('disabled-key')).rejects.toMatchObject({
      message: 'The API key is disabled', code: 'invalid_api_key', status: 401,
    })
  })

  it.each([{}, { data: null }, { data: {} }, { data: 'gpt-image-2' }])(
    'rejects a malformed model list instead of falling back to a static catalog: %j', async body => {
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status: 200 })))

      await expect(listModels('selected-group-key')).rejects.toThrow('Invalid model list response')
    }
  )

  it('returns an empty list when the gateway supports no models', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{"data":[]}', { status: 200 })))

    expect(await listModels('selected-group-key')).toEqual([])
  })
})

describe('image generation history API', () => {
  const originalAdapter = apiClient.defaults.adapter

  afterEach(() => {
    apiClient.defaults.adapter = originalAdapter
    localStorage.removeItem('auth_token')
  })

  it('loads protected image bytes through the panel API with the session authorization header', async () => {
    const blob = new Blob(['png'], { type: 'image/png' })
    const adapter = vi.fn(async (config: InternalAxiosRequestConfig) => ({
      data: blob, status: 200, statusText: 'OK', headers: {}, config,
    }))
    apiClient.defaults.adapter = adapter
    localStorage.setItem('auth_token', 'history-test-session')

    expect(await fetchHistoryContent(11)).toBe(blob)
    const request = adapter.mock.calls[0][0]
    expect(apiClient.getUri(request)).toContain('/api/v1/image-generations/history/11/content')
    expect(request.headers.get('Authorization')).toBe('Bearer history-test-session')
    expect(request.responseType).toBe('blob')
    expect(request.timeout).toBe(30000)
  })

  it('archives actual image bytes together with metadata so a remounted page can retrieve the image', async () => {
    const adapter = vi.fn(async (config: InternalAxiosRequestConfig) => ({
      data: { code: 0, data: { id: 11, content_available: true } },
      status: 201, statusText: 'Created', headers: {}, config,
    }))
    apiClient.defaults.adapter = adapter
    const file = new File(['png'], 'generated.png', { type: 'image/png' })

    const saved = await archiveHistory({
      model: 'gpt-image-2', provider: 'openai', prompt: 'a cat', content_type: file.type,
      normalized_params: { size: '1024x1024' }, file,
    })

    const request = adapter.mock.calls[0][0]
    expect(request.data).toBeInstanceOf(FormData)
    expect(request.timeout).toBe(5 * 60 * 1000)
    expect(apiClient.defaults.timeout).toBe(30000)
    expect(request.headers.get('Content-Type')).toBe('multipart/form-data')
    const form = request.data as FormData
    expect((form.get('file') as File).size).toBe(file.size)
    expect((form.get('file') as File).type).toBe('image/png')
    expect(form.get('normalized_params')).toBe('{"size":"1024x1024"}')
    expect(saved).toEqual({ id: 11, content_available: true })
  })

  it.each(['ECONNABORTED', 'ETIMEDOUT', 'ERR_NETWORK'])(
    'preserves structured transport error %s for archive error handling', async code => {
      apiClient.defaults.adapter = async (config: InternalAxiosRequestConfig) => {
        expect(config.timeout).toBe(5 * 60 * 1000)
        throw new AxiosError('transport detail', code, config)
      }

      await expect(archiveHistory({
        model: 'gpt-image-2', provider: 'openai-responses', prompt: 'a cat',
        file: new File(['png'], 'generated.png', { type: 'image/png' }),
      })).rejects.toMatchObject({ status: 0, code })
      expect(apiClient.defaults.timeout).toBe(30000)
    },
  )
})
