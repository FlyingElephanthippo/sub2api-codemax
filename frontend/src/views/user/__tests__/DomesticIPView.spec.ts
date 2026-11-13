import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ref } from 'vue'
import DomesticIPView from '../DomesticIPView.vue'
import type { DomesticIPState } from '@/api/domesticIp'
import zh from '@/i18n/locales/zh/domesticIp'

const api = vi.hoisted(() => ({ get: vi.fn(), add: vi.fn(), replace: vi.fn(), remove: vi.fn() }))
const store = vi.hoisted(() => ({
  cachedPublicSettings: { payment_enabled: true, payment_balance_disabled: false },
  showSuccess: vi.fn(), showWarning: vi.fn()
}))
vi.mock('@/api/domesticIp', () => ({ domesticIPAPI: api }))
vi.mock('@/stores/app', () => ({ useAppStore: () => store }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    locale: ref('zh-CN'),
    t: (key: string, params: Record<string, unknown> = {}) => {
      const value = key.startsWith('domesticIp.')
        ? zh.domesticIp[key.slice('domesticIp.'.length) as keyof typeof zh.domesticIp]
        : key
      return Object.entries(params).reduce((text, [name, replacement]) => text.replace(`{${name}}`, String(replacement)), value)
    }
  })
}))

function state(overrides: Partial<DomesticIPState> = {}): DomesticIPState {
  return {
    enabled: true, eligible: true, paid_amount_cny: 200, remaining_amount_cny: 0,
    current_ip: '1.2.3.4', current_ip_valid: true, entries: [],
    max_ips: 3, daily_limit: 5, changes_used: 0, changes_remaining: 5,
    ...overrides
  }
}
const entry = (id: number, ip: string) => ({ id, ip, created_at: '2026-11-13T01:00:00Z', updated_at: '2026-11-13T01:00:00Z' })
let wrapper: VueWrapper | undefined
async function render(initial = state()) {
  api.get.mockResolvedValueOnce(initial)
  wrapper = mount(DomesticIPView, {
    global: { stubs: {
      AppLayout: { template: '<main><slot /></main>' },
      BaseDialog: { props: ['show', 'title'], template: '<section v-if="show" data-testid="dialog"><h2>{{ title }}</h2><slot /><slot name="footer" /></section>' },
      Icon: true, LoadingSpinner: true,
      RouterLink: { template: '<a><slot /></a>' }
    } }
  })
  await flushPromises()
  return wrapper
}
beforeEach(() => vi.resetAllMocks())
afterEach(() => { wrapper?.unmount(); wrapper = undefined })

describe('domestic IP self-service', () => {
  it('shows the exact shortfall at ¥199 and prevents adding an address', async () => {
    const page = await render(state({ eligible: false, paid_amount_cny: 199, remaining_amount_cny: 1 }))
    expect(page.get('[data-testid="shortfall"]').text()).toContain('¥1.00')
    expect(page.get('[data-testid="add-ip"]').attributes('disabled')).toBeDefined()
    expect(page.find('input').exists()).toBe(false)
    expect(api.add).not.toHaveBeenCalled()
  })

  it('requires confirmation at ¥200 and sends only the server-detected IP for comparison', async () => {
    const page = await render()
    api.add.mockResolvedValueOnce(state({ entries: [entry(1, '1.2.3.4')], changes_used: 1, changes_remaining: 4 }))
    await page.get('[data-testid="add-ip"]').trigger('click')
    expect(api.add).not.toHaveBeenCalled()
    expect(page.get('[data-testid="dialog"]').text()).toContain('1.2.3.4')
    await page.get('[data-testid="confirm-action"]').trigger('click')
    await flushPromises()
    expect(api.add).toHaveBeenCalledWith('1.2.3.4')
    expect(page.get('[data-testid="entry-1"]').text()).toContain('当前网络')
    expect(page.find('[data-testid="dialog"]').exists()).toBe(false)
    expect(page.get('[data-testid="add-ip"]').attributes('disabled')).toBeDefined()
  })

  it('allows a confirmed replacement when all three slots are used', async () => {
    const entries = [entry(1, '1.1.1.1'), entry(2, '2.2.2.2'), entry(3, '3.3.3.3')]
    const page = await render(state({ entries }))
    expect(page.get('[data-testid="add-ip"]').attributes('disabled')).toBeDefined()
    expect(page.text()).toContain('已达到 3 个 IP 上限')
    api.replace.mockResolvedValueOnce(state({ entries: [entry(1, '1.2.3.4'), ...entries.slice(1)], changes_used: 1, changes_remaining: 4 }))
    await page.get('[data-testid="replace-1"]').trigger('click')
    expect(page.get('[data-testid="dialog"]').text()).toContain('1.1.1.1')
    await page.get('[data-testid="confirm-action"]').trigger('click')
    await flushPromises()
    expect(api.replace).toHaveBeenCalledWith(1, '1.2.3.4')
  })

  it('disables add, replacement and deletion after the fifth daily change', async () => {
    const page = await render(state({ entries: [entry(1, '1.1.1.1')], changes_used: 5, changes_remaining: 0 }))
    for (const selector of ['add-ip', 'replace-1', 'remove-1']) {
      expect(page.get(`[data-testid="${selector}"]`).attributes('disabled')).toBeDefined()
    }
    expect(page.text()).toContain('北京时间明天 00:00')
  })

  it('allows an ineligible user to delete their old registration after confirmation', async () => {
    const initial = state({ eligible: false, paid_amount_cny: 150, remaining_amount_cny: 50, entries: [entry(7, '1.1.1.1')] })
    const page = await render(initial)
    expect(page.text()).toContain('资格暂停')
    expect(page.get('[data-testid="replace-7"]').attributes('disabled')).toBeDefined()
    expect(page.get('[data-testid="remove-7"]').attributes('disabled')).toBeUndefined()
    api.remove.mockResolvedValueOnce({ ...initial, entries: [], changes_remaining: 4, changes_used: 1 })
    await page.get('[data-testid="remove-7"]').trigger('click')
    expect(api.remove).not.toHaveBeenCalled()
    await page.get('[data-testid="confirm-action"]').trigger('click')
    await flushPromises()
    expect(api.remove).toHaveBeenCalledWith(7)
    expect(page.find('[data-testid="entry-7"]').exists()).toBe(false)
  })

  it('rejects an unavailable public IP without offering a manual input', async () => {
    const page = await render(state({ current_ip: '', current_ip_valid: false }))
    expect(page.text()).toContain('当前网络取不到公网 IP，请切换网络后重试')
    expect(page.get('[data-testid="add-ip"]').attributes('disabled')).toBeDefined()
    expect(page.find('input').exists()).toBe(false)
  })

  it('requires a new confirmation when the request IP changes', async () => {
    const page = await render()
    api.add.mockRejectedValueOnce({ status: 409, reason: 'IP_CHANGED' })
    api.get.mockResolvedValueOnce(state({ current_ip: '5.6.7.8' }))
    await page.get('[data-testid="add-ip"]').trigger('click')
    await page.get('[data-testid="confirm-action"]').trigger('click')
    await flushPromises()
    expect(page.find('[data-testid="dialog"]').exists()).toBe(false)
    expect(page.get('[data-testid="current-ip"]').text()).toBe('5.6.7.8')
    expect(store.showWarning).toHaveBeenCalledWith(zh.domesticIp.networkChanged)
    expect(api.add).toHaveBeenCalledTimes(1)
  })

  it('prevents duplicate mutations while confirmation is being saved', async () => {
    const page = await render()
    let resolve: (value: DomesticIPState) => void = () => {}
    api.add.mockReturnValueOnce(new Promise<DomesticIPState>(done => { resolve = done }))
    await page.get('[data-testid="add-ip"]').trigger('click')
    await page.get('[data-testid="confirm-action"]').trigger('click')
    expect(page.get('[data-testid="confirm-action"]').attributes('disabled')).toBeDefined()
    await page.get('[data-testid="confirm-action"]').trigger('click')
    expect(api.add).toHaveBeenCalledTimes(1)
    resolve(state({ entries: [entry(1, '1.2.3.4')] }))
    await flushPromises()
  })

  it('shows a closed feature without exposing mutation controls', async () => {
    const page = await render(state({ enabled: false }))
    expect(page.text()).toContain(zh.domesticIp.unavailable)
    expect(page.find('[data-testid="add-ip"]').exists()).toBe(false)
  })

  it('keeps a registration visible and reports a failed deletion', async () => {
    const page = await render(state({ entries: [entry(1, '1.1.1.1')] }))
    api.remove.mockRejectedValueOnce({ status: 429, message: '每日变更次数已用完' })
    await page.get('[data-testid="remove-1"]').trigger('click')
    await page.get('[data-testid="confirm-action"]').trigger('click')
    await flushPromises()
    expect(page.get('[data-testid="dialog"]').text()).toContain('每日变更次数已用完')
    expect(page.get('[data-testid="entry-1"]').text()).toContain('1.1.1.1')
    expect(store.showSuccess).not.toHaveBeenCalled()
  })
})
