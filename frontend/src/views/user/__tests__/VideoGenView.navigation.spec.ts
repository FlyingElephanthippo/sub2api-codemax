import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const app = readFileSync(resolve(root, 'App.vue'), 'utf8')

describe('video generation navigation', () => {
  it('keeps both image and video jobs alive when navigating between pages', () => {
    expect(app).toContain('include="ImageGenView,VideoGenView" :max="2"')
  })
})
