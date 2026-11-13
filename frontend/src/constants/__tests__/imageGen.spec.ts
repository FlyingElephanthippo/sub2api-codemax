import { describe, expect, it } from 'vitest'

import { getAvailableImageModels, resolveImageModel } from '../imageGen'

describe('advertised image model availability', () => {
  it('offers Muse only when advertised, with its own geometry and reference limits', () => {
    expect(getAvailableImageModels(['muse-spark', 'muse-video'])).toEqual([])
    expect(getAvailableImageModels(['muse-image', 'muse-image', 'gpt-image-2'], 'muse-native'))
      .toEqual([expect.objectContaining({
        value: 'muse-image', provider: 'muse-native', maxReferences: 4,
        aspectRatios: ['1:1', '16:9', '9:16', '4:3', '3:4'], imageSizes: [],
        supportsTransparentBackground: false, supportsOutputFormat: false,
      })])
    expect(resolveImageModel('muse-image').value).toBe('muse-image')
  })

  it.each(['gpt-image-2.0', 'gpt-image-2.0-不超分', 'gpt-image-2.5-flare'])(
    'preserves the public group alias %s while using the OpenAI image request shape', model => {
      expect(resolveImageModel(model)).toMatchObject({
        value: model, provider: 'openai-responses', maxReferences: 16,
      })
    },
  )

  it('offers only the production group public IDs without substituting the canonical model or adding other providers', () => {
    const publicIDs = ['gpt-image-2.0', 'gpt-image-2.0-不超分', 'gpt-image-2.5-flare']

    expect(getAvailableImageModels(publicIDs).map(model => model.value)).toEqual(publicIDs)
  })

  it('keeps known catalog ordering and appends advertised dynamic aliases once', () => {
    const publicIDs = [
      'gpt-image-2.0', 'w-nano-banana-2', 'gpt-image-2', 'dreamina-seedream-5.0-pro',
      'gpt-image-2.0-不超分', 'w-nano-banana-2', 'gpt-image-2.0', 'gpt-image-2.5-flare',
    ]

    expect(getAvailableImageModels(publicIDs).map(model => model.value)).toEqual([
      'gpt-image-2', 'w-nano-banana-2', 'dreamina-seedream-5.0-pro',
      'gpt-image-2.0', 'gpt-image-2.0-不超分', 'gpt-image-2.5-flare',
    ])
  })

  it('does not turn text IDs, wildcards, display labels or unrecognized aliases into the fallback image model', () => {
    expect(getAvailableImageModels([
      'claude-sonnet-4-5', 'gpt-image-*', 'GPT Image 2', 'my-image-alias', '', '   ',
    ])).toEqual([])
    expect(getAvailableImageModels([])).toEqual([])
  })

  it('applies the dedicated provider filter to advertised known models and dynamic aliases', () => {
    const publicIDs = ['gpt-image-2.0', 'w-nano-banana-2', 'dreamina-seedream-5.0-pro']

    expect(getAvailableImageModels(publicIDs, 'openai-responses').map(model => model.value))
      .toEqual(['gpt-image-2.0'])
    expect(getAvailableImageModels(publicIDs, 'aggregate-w-images').map(model => model.value))
      .toEqual(['w-nano-banana-2'])
  })
})
