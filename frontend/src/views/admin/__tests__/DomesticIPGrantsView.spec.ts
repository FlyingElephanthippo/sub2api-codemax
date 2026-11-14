import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import DomesticIPGrantsView from '../DomesticIPGrantsView.vue'
import type { AdminDomesticIPGrant, AdminDomesticIPGrantList } from '@/api/admin/domesticIp'
import zh from '@/i18n/locales/zh/adminDomesticIp'

const api = vi.hoisted(() => ({ list: vi.fn(), add: vi.fn(), remove: vi.fn() }))
const store = vi.hoisted(() => ({ showSuccess: vi.fn() }))
vi.mock('@/api/admin/domesticIp', () => ({ adminDomesticIPAPI: api }))
vi.mock('@/stores/app', () => ({ useAppStore: () => store }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({
  t: (key: string, params: Record<string, unknown> = {}) => {
    const value = key.startsWith('adminDomesticIp.')
      ? zh.adminDomesticIp[key.slice('adminDomesticIp.'.length) as keyof typeof zh.adminDomesticIp]
      : key
    return Object.entries(params).reduce((text, [name, replacement]) => text.replace(`{${name}}`, String(replacement)), value)
  }
}) }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))

const entry = (id = 1, ip = '1.2.3.4'): AdminDomesticIPGrant => ({ id, ip, note: '办公室', created_at: '2026-11-14T08:00:00Z', updated_at: '2026-11-14T08:00:00Z' })
function state(overrides: Partial<AdminDomesticIPGrantList> = {}): AdminDomesticIPGrantList {
  return { items: [], total: 0, page: 1, page_size: 20, enabled: true, static_ips: ['8.8.8.8'], ...overrides }
}
let wrapper: VueWrapper | undefined
async function render(initial = state()) {
  api.list.mockResolvedValueOnce(initial)
  wrapper = mount(DomesticIPGrantsView, { global: { stubs: {
    BaseDialog: { props: ['show', 'title'], template: '<section v-if="show" data-testid="dialog"><h2>{{ title }}</h2><slot /><slot name="footer" /></section>' },
    Pagination: { props: ['page', 'pageSize', 'total'], emits: ['update:page', 'update:pageSize'], template: '<div data-testid="pagination"><button data-testid="next-page" @click="$emit(\'update:page\', page + 1)">next</button></div>' },
    Icon: true, LoadingSpinner: true
  } } })
  await flushPromises()
  return wrapper
}
beforeEach(() => vi.resetAllMocks())
afterEach(() => { wrapper?.unmount(); wrapper = undefined })

describe('administrator IP access', () => {
  it('adds a manually entered IPv6 without a user or payment field, then refreshes the list', async () => {
    const page = await render()
    const added = entry(3, '2606:4700:4700::1111')
    api.add.mockResolvedValueOnce(added)
    api.list.mockResolvedValueOnce(state({ items: [added], total: 1 }))
    await page.get('[data-testid="ip-input"]').setValue(' 2606:4700:4700::1111 ')
    await page.get('[data-testid="note-input"]').setValue(' 办公室 ')
    await page.get('[data-testid="add-form"]').trigger('submit')
    await flushPromises()
    expect(api.add).toHaveBeenCalledWith('2606:4700:4700::1111', '办公室')
    expect(page.get('[data-testid="grant-3"]').text()).toContain('2606:4700:4700::1111')
    expect((page.get('[data-testid="ip-input"]').element as HTMLInputElement).value).toBe('')
    expect(store.showSuccess).toHaveBeenCalledOnce()
  })

  it('blocks duplicate submissions while saving', async () => {
    const page = await render()
    let resolve: (value: AdminDomesticIPGrant) => void = () => {}
    api.add.mockReturnValueOnce(new Promise<AdminDomesticIPGrant>(done => { resolve = done }))
    api.list.mockResolvedValueOnce(state({ items: [entry()], total: 1 }))
    await page.get('[data-testid="ip-input"]').setValue('1.2.3.4')
    await page.get('[data-testid="add-form"]').trigger('submit')
    await page.get('[data-testid="add-form"]').trigger('submit')
    expect(page.get('[data-testid="add-ip"]').attributes('disabled')).toBeDefined()
    expect(api.add).toHaveBeenCalledOnce()
    resolve(entry())
    await flushPromises()
  })

  it('rejects a subnet before submitting and preserves a server-rejected address for correction', async () => {
    const page = await render()
    await page.get('[data-testid="ip-input"]').setValue('1.2.3.0/24')
    await page.get('[data-testid="add-form"]').trigger('submit')
    expect(api.add).not.toHaveBeenCalled()
    expect(page.get('[data-testid="add-error"]').text()).toContain('单个公网')
    api.add.mockRejectedValueOnce({ message: '不支持私网地址' })
    await page.get('[data-testid="ip-input"]').setValue('192.168.1.1')
    await page.get('[data-testid="add-form"]').trigger('submit')
    await flushPromises()
    expect(page.get('[data-testid="add-error"]').text()).toBe('不支持私网地址')
    expect((page.get('[data-testid="ip-input"]').element as HTMLInputElement).value).toBe('192.168.1.1')
    expect(store.showSuccess).not.toHaveBeenCalled()
  })

  it('only deletes after confirmation and explains why other grants can retain access', async () => {
    const page = await render(state({ items: [entry()], total: 1 }))
    api.remove.mockResolvedValueOnce(undefined)
    api.list.mockResolvedValueOnce(state())
    await page.get('[data-testid="remove-1"]').trigger('click')
    expect(api.remove).not.toHaveBeenCalled()
    expect(page.get('[data-testid="dialog"]').text()).toContain('1.2.3.4')
    expect(page.get('[data-testid="dialog"]').text()).toContain('其他有效自助登记')
    await page.get('[data-testid="confirm-remove"]').trigger('click')
    await flushPromises()
    expect(api.remove).toHaveBeenCalledWith(1)
    expect(page.find('[data-testid="grant-1"]').exists()).toBe(false)
    expect(page.find('[data-testid="dialog"]').exists()).toBe(false)
  })

  it('retains the record and the confirmation after a failed deletion', async () => {
    const page = await render(state({ items: [entry()], total: 1 }))
    api.remove.mockRejectedValueOnce({ message: '服务暂不可用' })
    await page.get('[data-testid="remove-1"]').trigger('click')
    await page.get('[data-testid="confirm-remove"]').trigger('click')
    await flushPromises()
    expect(page.get('[data-testid="remove-error"]').text()).toBe('服务暂不可用')
    expect(page.find('[data-testid="grant-1"]').exists()).toBe(true)
    expect(store.showSuccess).not.toHaveBeenCalled()
  })

  it('keeps fixed grants read-only and permits removal when the feature is disabled', async () => {
    const page = await render(state({ enabled: false, items: [entry()], total: 1 }))
    expect(page.get('[data-testid="fixed-grants"]').text()).toContain('8.8.8.8')
    expect(page.get('[data-testid="fixed-grants"]').find('button').exists()).toBe(false)
    expect(page.get('[data-testid="add-ip"]').attributes('disabled')).toBeDefined()
    expect(page.get('[data-testid="remove-1"]').attributes('disabled')).toBeUndefined()
  })

  it('searches from page one and carries the query into pagination', async () => {
    const page = await render(state({ items: [entry()], total: 41 }))
    api.list.mockResolvedValueOnce(state({ items: [entry()], total: 30 }))
    await page.get('#admin-grant-search').setValue(' 办公室 ')
    await page.get('[data-testid="search-form"]').trigger('submit')
    await flushPromises()
    expect(api.list).toHaveBeenLastCalledWith(1, 20, '办公室')
    api.list.mockResolvedValueOnce(state({ page: 2, items: [entry(2)], total: 30 }))
    await page.get('[data-testid="next-page"]').trigger('click')
    await flushPromises()
    expect(api.list).toHaveBeenLastCalledWith(2, 20, '办公室')
  })

  it('returns to the final valid page after deleting the last item on a page', async () => {
    const page = await render(state({ page: 2, total: 21, items: [entry()] }))
    api.remove.mockResolvedValueOnce(undefined)
    api.list.mockResolvedValueOnce(state({ page: 2, total: 20 }))
    api.list.mockResolvedValueOnce(state({ page: 1, total: 20, items: [entry(2)] }))
    await page.get('[data-testid="remove-1"]').trigger('click')
    await page.get('[data-testid="confirm-remove"]').trigger('click')
    await flushPromises()
    expect(api.list).toHaveBeenLastCalledWith(1, 20, '')
    expect(page.find('[data-testid="grant-2"]').exists()).toBe(true)
  })

  it('does not let an older list response overwrite a newer search', async () => {
    const page = await render()
    let resolve: (value: AdminDomesticIPGrantList) => void = () => {}
    api.list.mockReturnValueOnce(new Promise<AdminDomesticIPGrantList>(done => { resolve = done }))
    api.list.mockResolvedValueOnce(state({ items: [entry(2)], total: 1 }))
    await page.get('#admin-grant-search').setValue('old')
    await page.get('[data-testid="search-form"]').trigger('submit')
    await page.get('#admin-grant-search').setValue('new')
    await page.get('[data-testid="search-form"]').trigger('submit')
    await flushPromises()
    resolve(state({ items: [entry(1)], total: 1 }))
    await flushPromises()
    expect(page.find('[data-testid="grant-2"]').exists()).toBe(true)
    expect(page.find('[data-testid="grant-1"]').exists()).toBe(false)
  })
})
