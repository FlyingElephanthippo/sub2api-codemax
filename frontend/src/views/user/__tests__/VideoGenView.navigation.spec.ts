import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const route = readFileSync(resolve(root, 'router/index.ts'), 'utf8')
const sidebar = readFileSync(resolve(root, 'components/layout/AppSidebar.vue'), 'utf8')
const app = readFileSync(resolve(root, 'App.vue'), 'utf8')

describe('video generation rollout and navigation', () => {
  it('requires an administrator for the test rollout and hides the sidebar entry from users', () => {
    expect(route.match(/path: '\/video-gen'[\s\S]*?\n {2}},/)?.[0]).toContain('requiresAdmin: true')
    expect(sidebar).toMatch(/path: '\/video-gen'[^\n]*featureFlag: \(\) => isAdmin\.value/)
  })
  it('keeps both image and video jobs alive when navigating between pages', () => {
    expect(app).toContain('include="ImageGenView,VideoGenView" :max="2"')
  })
})
