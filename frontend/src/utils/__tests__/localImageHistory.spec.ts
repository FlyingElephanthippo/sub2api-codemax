import { IDBFactory, IDBObjectStore } from 'fake-indexeddb'
import { Blob as NodeBlob } from 'node:buffer'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import {
  deleteLocalImage,
  listLocalImages,
  LOCAL_IMAGE_HISTORY_LIMIT,
  LOCAL_IMAGE_HISTORY_MAX_BYTES,
  saveLocalImage,
  type LocalImageHistoryInput,
} from '../localImageHistory'

function image(id: string, timestamp = 1, bytes = 3): LocalImageHistoryInput {
  return {
    id, timestamp, blob: new Blob(['x'.repeat(bytes)], { type: 'image/png' }),
    prompt: `prompt ${id}`, size: '1024x1024', format: 'png', mimeType: 'image/png',
    extension: 'png', model: 'gpt-image-2', provider: 'openai-responses',
    normalizedParams: { size: '1024x1024' }, referenceSnapshot: [{ name: 'source.png' }],
  }
}

describe('local image history persistence', () => {
  beforeEach(() => {
    vi.stubGlobal('indexedDB', new IDBFactory())
    // Node's structuredClone used by fake-indexeddb cannot clone jsdom Blob bytes.
    vi.stubGlobal('Blob', NodeBlob)
  })
  afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

  it('restores image bytes and display metadata without request secrets or temporary URLs', async () => {
    const input = Object.assign(image('first'), { apiKey: 'test-secret', blobUrl: 'blob:temporary' })
    await saveLocalImage(1, input)
    const restored = await listLocalImages(1)
    expect(restored).toHaveLength(1)
    expect(restored[0]).toMatchObject({ id: 'first', userID: 1, prompt: 'prompt first', model: input.model })
    expect(restored[0]!.blob.size).toBe(input.blob.size)
    expect(restored[0]!.normalizedParams).toEqual(input.normalizedParams)
    expect(restored[0]!.referenceSnapshot).toEqual(input.referenceSnapshot)
    expect(restored[0]).not.toHaveProperty('apiKey')
    expect(restored[0]).not.toHaveProperty('blobUrl')
  })

  it('isolates accounts when listing, replacing and deleting the same image id', async () => {
    await saveLocalImage(1, image('shared', 1))
    await saveLocalImage(2, image('shared', 2))
    await saveLocalImage(1, { ...image('shared', 3), prompt: 'updated' })
    expect((await listLocalImages(1))[0]!.prompt).toBe('updated')
    expect((await listLocalImages(2))[0]!.prompt).toBe('prompt shared')
    await deleteLocalImage(1, 'shared')
    expect(await listLocalImages(1)).toEqual([])
    expect(await listLocalImages(2)).toHaveLength(1)
  })

  it('saves reactive batch metadata as plain data supported by IndexedDB', async () => {
    const input = reactive(image('batch-result'))
    await saveLocalImage(1, input)
    const [restored] = await listLocalImages(1)
    expect(restored!.normalizedParams).toEqual({ size: '1024x1024' })
    expect(restored!.referenceSnapshot).toEqual([{ name: 'source.png' }])
    expect(await restored!.blob.text()).toBe('xxx')
  })

  it('keeps the latest images within the per-account count limit', async () => {
    await saveLocalImage(2, image('other-user'))
    for (let index = 0; index <= LOCAL_IMAGE_HISTORY_LIMIT; index++) {
      await saveLocalImage(1, image(`image-${index}`, index))
    }
    const images = await listLocalImages(1)
    expect(images).toHaveLength(LOCAL_IMAGE_HISTORY_LIMIT)
    expect(images[0]!.id).toBe(`image-${LOCAL_IMAGE_HISTORY_LIMIT}`)
    expect(images.some(item => item.id === 'image-0')).toBe(false)
    expect(await listLocalImages(2)).toHaveLength(1)
  })

  it('does not duplicate a saved image on retry and sorts newest first', async () => {
    await saveLocalImage(1, image('old', 1))
    await saveLocalImage(1, image('new', 2))
    await saveLocalImage(1, image('old', 1))
    expect((await listLocalImages(1)).map(item => item.id)).toEqual(['new', 'old'])
  })

  it('evicts oldest images when their total size exceeds the per-account budget', async () => {
    const size = Object.getOwnPropertyDescriptor(NodeBlob.prototype, 'size')!.get!
    // Scale small real blobs so structured cloning still exercises the storage transaction.
    vi.spyOn(NodeBlob.prototype, 'size', 'get').mockImplementation(function (this: NodeBlob) {
      return size.call(this) * (LOCAL_IMAGE_HISTORY_MAX_BYTES / 4)
    })
    await saveLocalImage(1, image('old-small', 1, 1))
    await saveLocalImage(1, image('middle-large', 2, 2))
    await saveLocalImage(2, image('other-user', 1, 1))
    await saveLocalImage(1, image('newest', 3, 3))
    expect((await listLocalImages(1)).map(item => item.id)).toEqual(['newest'])
    expect(await listLocalImages(2)).toHaveLength(1)
    await expect(saveLocalImage(1, image('old-retry', 0, 2))).rejects.toBeDefined()
    expect((await listLocalImages(1)).map(item => item.id)).toEqual(['newest'])
  })

  it('rolls back evictions when a new image cannot be stored', async () => {
    const size = Object.getOwnPropertyDescriptor(NodeBlob.prototype, 'size')!.get!
    vi.spyOn(NodeBlob.prototype, 'size', 'get').mockImplementation(function (this: NodeBlob) {
      return size.call(this) * (LOCAL_IMAGE_HISTORY_MAX_BYTES / 4)
    })
    await saveLocalImage(1, image('old'))
    vi.spyOn(IDBObjectStore.prototype, 'put').mockImplementationOnce(() => {
      throw new DOMException('Storage is full', 'QuotaExceededError')
    })
    await expect(saveLocalImage(1, image('new', 2))).rejects.toBeDefined()
    expect((await listLocalImages(1)).map(item => item.id)).toEqual(['old'])
    await saveLocalImage(1, image('new', 2))
    expect((await listLocalImages(1)).map(item => item.id)).toEqual(['new'])
  })

  it('rejects missing browser storage and invalid input without losing a generated image', async () => {
    await expect(saveLocalImage(0, image('invalid-user'))).rejects.toThrow('signed-in user')
    await expect(saveLocalImage(1, { ...image('empty'), blob: new Blob([]) })).rejects.toThrow('non-empty')
    vi.stubGlobal('indexedDB', undefined)
    await expect(listLocalImages(1)).rejects.toThrow('unavailable')
  })

  it('rejects an image exceeding the storage budget before changing history', async () => {
    await saveLocalImage(1, image('old'))
    const oversized = image('too-large')
    Object.defineProperty(oversized.blob, 'size', { value: LOCAL_IMAGE_HISTORY_MAX_BYTES + 1 })
    await expect(saveLocalImage(1, oversized)).rejects.toMatchObject({ name: 'QuotaExceededError' })
    expect((await listLocalImages(1)).map(item => item.id)).toEqual(['old'])
  })
})
