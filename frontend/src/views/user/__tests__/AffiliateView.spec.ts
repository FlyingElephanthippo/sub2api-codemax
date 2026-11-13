import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import AffiliateView from '../AffiliateView.vue'

const { copyToClipboard, getAffiliateDetail, bindAffiliateCode, showError, showSuccess } = vi.hoisted(() => ({
  copyToClipboard: vi.fn(),
  getAffiliateDetail: vi.fn(),
  bindAffiliateCode: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/user', () => ({
  default: {
    getAffiliateDetail,
    bindAffiliateCode,
    transferAffiliateQuota: vi.fn(),
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    refreshUser: vi.fn(),
  }),
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard }),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

describe('AffiliateView', () => {
  const affiliateCode = 'affiliate-code-that-is-long-enough-to-overflow-a-mobile-viewport'
  const makeDetail = () => ({
    user_id: 1,
    aff_code: affiliateCode,
    inviter_id: null as number | null,
    aff_count: 0,
    aff_quota: 0,
    aff_frozen_quota: 0,
    aff_history_quota: 0,
    effective_rebate_rate_percent: 10,
    invitees: [],
  })
  const mountView = () => mount(AffiliateView, {
    global: {
      stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        Icon: true,
      },
    },
  })

  beforeEach(() => {
    vi.resetAllMocks()
    copyToClipboard.mockResolvedValue(true)
    getAffiliateDetail.mockResolvedValue(makeDetail())
    bindAffiliateCode.mockResolvedValue({ bound: true })
  })

  it('stacks long values and copy controls on mobile while retaining desktop rows', async () => {
    const wrapper = mountView()

    await flushPromises()

    const values = wrapper.findAll('code')
    expect(values).toHaveLength(2)
    for (const value of values) {
      expect(value.classes()).toEqual(expect.arrayContaining([
        'min-w-0',
        'break-all',
        'sm:flex-1',
        'sm:truncate',
      ]))
      expect(Array.from(value.element.parentElement?.classList ?? [])).toEqual(expect.arrayContaining([
        'flex-col',
        'items-stretch',
        'sm:flex-row',
        'sm:items-center',
      ]))
    }

    const copyButtons = wrapper.findAll('button').filter((button) =>
      ['affiliate.copyCode', 'affiliate.copyLink'].includes(button.text()),
    )
    expect(copyButtons).toHaveLength(2)
    for (const button of copyButtons) {
      expect(button.classes()).toEqual(expect.arrayContaining([
        'w-full',
        'sm:w-auto',
        'sm:shrink-0',
      ]))
    }

    await copyButtons[0].trigger('click')
    await copyButtons[1].trigger('click')
    await flushPromises()

    expect(copyToClipboard).toHaveBeenNthCalledWith(1, affiliateCode, 'affiliate.codeCopied')
    expect(copyToClipboard).toHaveBeenNthCalledWith(
      2,
      `${window.location.origin}/register?aff=${encodeURIComponent(affiliateCode)}`,
      'affiliate.linkCopied',
    )
  })

  it('does not submit an empty or whitespace-only inviter code', async () => {
    const wrapper = mountView()
    await flushPromises()

    const input = wrapper.get('#affiliate-inviter-code')
    await input.setValue('   ')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    await wrapper.get('form').trigger('submit')

    expect(bindAffiliateCode).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('affiliate.bind.onceOnly')
    expect(wrapper.text()).toContain('affiliate.bind.futureOnly')
  })

  it('submits a trimmed code once and stays bound even when refreshing the detail fails', async () => {
    let resolveBinding!: (value: { bound: boolean }) => void
    bindAffiliateCode.mockReturnValueOnce(new Promise((resolve) => { resolveBinding = resolve }))
    const wrapper = mountView()
    await flushPromises()
    getAffiliateDetail.mockRejectedValueOnce(new Error('refresh failed'))

    await wrapper.get('#affiliate-inviter-code').setValue('  FRIEND123  ')
    await wrapper.get('form').trigger('submit')
    await wrapper.get('form').trigger('submit')

    expect(bindAffiliateCode).toHaveBeenCalledTimes(1)
    expect(bindAffiliateCode).toHaveBeenCalledWith('FRIEND123')
    expect(wrapper.get('#affiliate-inviter-code').attributes('disabled')).toBeDefined()
    expect(wrapper.get('button[type="submit"]').text()).toContain('affiliate.bind.binding')

    resolveBinding({ bound: true })
    await flushPromises()

    expect(showSuccess).toHaveBeenCalledWith('affiliate.bind.success')
    expect(getAffiliateDetail).toHaveBeenCalledTimes(2)
    expect(wrapper.find('#affiliate-inviter-code').exists()).toBe(false)
    expect(wrapper.get('[role="status"]').text()).toBe('affiliate.bind.bound')
  })

  it('shows a read-only status for users who already have an inviter', async () => {
    getAffiliateDetail.mockResolvedValueOnce({ ...makeDetail(), inviter_id: 42 })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.get('[role="status"]').text()).toBe('affiliate.bind.bound')
    expect(bindAffiliateCode).not.toHaveBeenCalled()
  })

  it.each(['AFFILIATE_CODE_INVALID', 'AFFILIATE_DISABLED'])(
    'shows a localized %s error and preserves the code for correction',
    async (reason) => {
      bindAffiliateCode.mockRejectedValueOnce({ reason, message: 'Raw backend error' })
      const wrapper = mountView()
      await flushPromises()

      const input = wrapper.get<HTMLInputElement>('#affiliate-inviter-code')
      await input.setValue('WRONG123')
      await wrapper.get('form').trigger('submit')
      await flushPromises()

      expect(wrapper.get('[role="alert"]').text()).toBe(`affiliate.bind.errors.${reason}`)
      expect(input.element.value).toBe('WRONG123')
      expect(input.attributes('aria-invalid')).toBe('true')
      expect(showSuccess).not.toHaveBeenCalled()

      await input.setValue('FRIEND123')
      expect(wrapper.find('[role="alert"]').exists()).toBe(false)
      expect(input.attributes('aria-invalid')).toBe('false')
    },
  )

  it('locks the form when another session has already bound an inviter', async () => {
    bindAffiliateCode.mockRejectedValueOnce({ reason: 'AFFILIATE_ALREADY_BOUND' })
    const wrapper = mountView()
    await flushPromises()
    getAffiliateDetail.mockResolvedValueOnce({ ...makeDetail(), inviter_id: 42 })

    await wrapper.get('#affiliate-inviter-code').setValue('FRIEND123')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toBe('affiliate.bind.errors.AFFILIATE_ALREADY_BOUND')
    expect(wrapper.get('[role="status"]').text()).toBe('affiliate.bind.bound')
    expect(getAffiliateDetail).toHaveBeenCalledTimes(2)
  })
})
