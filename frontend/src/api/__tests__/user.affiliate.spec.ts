import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { post } }))

import { bindAffiliateCode } from '@/api/user'

describe('user affiliate binding API', () => {
  beforeEach(() => {
    vi.resetAllMocks()
  })

  it('posts the inviter code to the authenticated binding endpoint and returns its data', async () => {
    post.mockResolvedValue({ data: { bound: true } })

    await expect(bindAffiliateCode('FRIEND123')).resolves.toEqual({ bound: true })
    expect(post).toHaveBeenCalledTimes(1)
    expect(post).toHaveBeenCalledWith('/user/aff/bind', { aff_code: 'FRIEND123' })
  })
})
