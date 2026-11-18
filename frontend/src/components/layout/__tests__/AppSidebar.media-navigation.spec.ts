import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent } from 'vue'
import AppSidebar from '../AppSidebar.vue'
import { useBatchImageAccess } from '@/composables/useBatchImageAccess'

const state = vi.hoisted(() => ({
  auth: { isAuthenticated: true, isAdmin: false, isSimpleMode: false },
  app: {
    sidebarCollapsed: false,
    mobileOpen: false,
    sidebarScrollTop: 0,
    backendModeEnabled: false,
    siteName: 'Sub2API',
    siteLogo: '',
    siteVersion: 'test',
    publicSettingsLoaded: true,
    cachedPublicSettings: { custom_menu_items: [] },
    toggleSidebar: vi.fn(),
    setMobileOpen: vi.fn(),
  },
  adminSettings: {
    customMenuItems: [],
    opsMonitoringEnabled: false,
    paymentEnabled: false,
    fetch: vi.fn(),
  },
  listKeys: vi.fn(),
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => state.auth,
  useAppStore: () => state.app,
  useAdminSettingsStore: () => state.adminSettings,
  useOnboardingStore: () => ({ isCurrentStep: vi.fn(() => false), nextStep: vi.fn() }),
}))

vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('@/stores/app', () => ({ useAppStore: () => state.app }))
vi.mock('@/api/keys', () => ({ keysAPI: { list: state.listKeys } }))
vi.mock('@/components/common/VersionBadge.vue', () => ({ default: { template: '<span />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ path: '/dashboard' }),
  useRouter: () => ({ push: vi.fn() }),
}))

const RouterLink = defineComponent({
  props: { to: { type: String, required: true } },
  template: '<a :href="to"><slot /></a>',
})

let wrapper: VueWrapper | undefined

function mountSidebar() {
  wrapper = mount(AppSidebar, {
    global: { stubs: { RouterLink, VersionBadge: true, Icon: true } },
  })
  return wrapper
}

function expectMediaLinks(visible: boolean) {
  for (const path of ['/image-gen', '/video-gen']) {
    expect(wrapper?.find(`a[href="${path}"]`).exists()).toBe(visible)
  }
}

beforeEach(() => {
  state.auth.isAuthenticated = true
  state.auth.isAdmin = false
  state.auth.isSimpleMode = false
  state.app.backendModeEnabled = false
  state.app.sidebarScrollTop = 0
  state.listKeys.mockReset()
  state.listKeys.mockResolvedValue({ items: [], pages: 1, total: 0 })
  localStorage.clear()
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
})

describe('media navigation without API keys', () => {
  it.each([false, true])('shows both entries without keys (administrator: %s)', async (isAdmin) => {
    state.auth.isAdmin = isAdmin
    await useBatchImageAccess().refreshBatchImageAccess(true)
    mountSidebar()

    expectMediaLinks(true)
    await flushPromises()
    expectMediaLinks(true)
    expect(wrapper?.find('a[href="/batch-image"]').exists()).toBe(false)
  })

  it.each([false, true])('keeps both entries when the key API fails (administrator: %s)', async (isAdmin) => {
    state.auth.isAdmin = isAdmin
    state.listKeys.mockRejectedValue(new Error('Key listing unavailable'))
    await useBatchImageAccess().refreshBatchImageAccess(true)
    mountSidebar()

    expectMediaLinks(true)
    await flushPromises()
    expectMediaLinks(true)
    expect(state.listKeys).toHaveBeenCalled()
  })

  it.each([false, true])('preserves simple-mode navigation (administrator: %s)', async (isAdmin) => {
    state.auth.isAdmin = isAdmin
    state.auth.isSimpleMode = true
    mountSidebar()
    await flushPromises()

    expectMediaLinks(false)
    expect(wrapper?.find('a[href="/keys"]').exists()).toBe(true)
  })

  it('preserves backend-mode navigation for regular users', async () => {
    state.app.backendModeEnabled = true
    mountSidebar()
    await flushPromises()

    expectMediaLinks(false)
    expect(wrapper?.find('a[href="/keys"]').exists()).toBe(false)
  })

  it('preserves administrator personal navigation in backend mode', async () => {
    state.auth.isAdmin = true
    state.app.backendModeEnabled = true
    mountSidebar()
    await flushPromises()

    expectMediaLinks(true)
    expect(wrapper?.find('a[href="/admin/settings"]').exists()).toBe(true)
  })
})
