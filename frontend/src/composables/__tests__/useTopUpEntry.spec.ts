import { beforeEach, describe, expect, it, vi } from 'vitest'

import { TOP_UP_LOCATION, useTopUpEntry } from '../useTopUpEntry'

const state = vi.hoisted(() => ({
  app: { cachedPublicSettings: null as Record<string, unknown> | null },
  auth: { isSimpleMode: false },
}))

vi.mock('@/stores/app', () => ({ useAppStore: () => state.app }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))

describe('useTopUpEntry', () => {
  beforeEach(() => {
    state.app.cachedPublicSettings = { payment_enabled: true }
    state.auth.isSimpleMode = false
  })

  it('支付开启时可以充值，入口指向购买页的充值标签', () => {
    const { canTopUp, topUpLocation } = useTopUpEntry()

    expect(canTopUp.value).toBe(true)
    expect(topUpLocation).toBe(TOP_UP_LOCATION)
    expect(topUpLocation).toEqual({ path: '/purchase', query: { tab: 'recharge' } })
  })

  it('后台关掉支付（含 free 站）时不出现', () => {
    state.app.cachedPublicSettings = { payment_enabled: false }

    expect(useTopUpEntry().canTopUp.value).toBe(false)
  })

  it('设置还没加载时按开启处理，免得入口一闪而过', () => {
    state.app.cachedPublicSettings = null
    expect(useTopUpEntry().canTopUp.value).toBe(true)

    state.app.cachedPublicSettings = {}
    expect(useTopUpEntry().canTopUp.value).toBe(true)
  })

  it('简易模式下不出现', () => {
    state.auth.isSimpleMode = true

    expect(useTopUpEntry().canTopUp.value).toBe(false)
  })
})
