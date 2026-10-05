import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { BILLING_TYPE_WALLET, useLowBalanceStore, WALLET_DEBIT_LOOKBACK_DAYS } from '../lowBalance'

const query = vi.hoisted(() => vi.fn())
vi.mock('@/api/usage', () => ({ usageAPI: { query } }))

const page = (count: number) => ({ items: Array.from({ length: count }, (_, i) => ({ id: i + 1 })), total: count, page: 1, page_size: 1, pages: 1 })

describe('lowBalance store', () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    setActivePinia(createPinia())
    query.mockReset()
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 9, 5, 12, 0, 0))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  describe('近 7 天钱包扣费', () => {
    it('只查钱包扣费（billing_type = 0）、近 7 天、只要一条', async () => {
      query.mockResolvedValue(page(1))
      const store = useLowBalanceStore()

      await store.refreshWalletDebit(7)

      expect(query).toHaveBeenCalledTimes(1)
      expect(query).toHaveBeenCalledWith({
        page: 1,
        page_size: 1,
        billing_type: BILLING_TYPE_WALLET,
        start_date: '2026-09-29',
        end_date: '2026-10-05',
      })
      expect(BILLING_TYPE_WALLET).toBe(0)
      expect(WALLET_DEBIT_LOOKBACK_DAYS).toBe(7)
      expect(store.recentWalletDebit(7)).toBe(true)
    })

    it('没有记录就是 false', async () => {
      query.mockResolvedValue(page(0))
      const store = useLowBalanceStore()

      await store.refreshWalletDebit(7)

      expect(store.recentWalletDebit(7)).toBe(false)
    })

    it('还没查到时是 null，也不会串到别的用户', async () => {
      query.mockResolvedValue(page(1))
      const store = useLowBalanceStore()
      expect(store.recentWalletDebit(7)).toBeNull()

      await store.refreshWalletDebit(7)

      expect(store.recentWalletDebit(8)).toBeNull()
    })

    it('10 分钟内复用缓存，过期后重新查', async () => {
      query.mockResolvedValue(page(1))
      const store = useLowBalanceStore()

      await store.refreshWalletDebit(7)
      await store.refreshWalletDebit(7)
      expect(query).toHaveBeenCalledTimes(1)

      vi.advanceTimersByTime(9 * 60 * 1000)
      await store.refreshWalletDebit(7)
      expect(query).toHaveBeenCalledTimes(1)

      vi.advanceTimersByTime(2 * 60 * 1000)
      await store.refreshWalletDebit(7)
      expect(query).toHaveBeenCalledTimes(2)
    })

    it('同一个用户同时发起只查一次', async () => {
      let resolve!: (value: ReturnType<typeof page>) => void
      query.mockReturnValue(new Promise((r) => { resolve = r }))
      const store = useLowBalanceStore()

      const a = store.refreshWalletDebit(7)
      const b = store.refreshWalletDebit(7)
      resolve(page(1))
      await Promise.all([a, b])

      expect(query).toHaveBeenCalledTimes(1)
    })

    it('换了用户就重新查', async () => {
      query.mockResolvedValue(page(1))
      const store = useLowBalanceStore()

      await store.refreshWalletDebit(7)
      await store.refreshWalletDebit(8)

      expect(query).toHaveBeenCalledTimes(2)
      expect(store.recentWalletDebit(8)).toBe(true)
      expect(store.recentWalletDebit(7)).toBeNull()
    })

    it('查询失败按「没有近期扣费」处理（不误报），1 分钟后重试', async () => {
      const error = vi.spyOn(console, 'error').mockImplementation(() => {})
      query.mockRejectedValueOnce(new Error('boom')).mockResolvedValueOnce(page(1))
      const store = useLowBalanceStore()

      await store.refreshWalletDebit(7)
      expect(store.recentWalletDebit(7)).toBeNull()

      await store.refreshWalletDebit(7)
      expect(query).toHaveBeenCalledTimes(1)

      vi.advanceTimersByTime(61 * 1000)
      await store.refreshWalletDebit(7)
      expect(query).toHaveBeenCalledTimes(2)
      expect(store.recentWalletDebit(7)).toBe(true)
      error.mockRestore()
    })
  })

  describe('关闭状态', () => {
    it('按用户记录，写进 sessionStorage，新建 store 后仍在', () => {
      const store = useLowBalanceStore()
      expect(store.isDismissed(7)).toBe(false)

      store.dismiss(7)
      expect(store.isDismissed(7)).toBe(true)
      expect(store.isDismissed(8)).toBe(false)

      setActivePinia(createPinia())
      expect(useLowBalanceStore().isDismissed(7)).toBe(true)
    })

    it('清除只影响这个用户', () => {
      const store = useLowBalanceStore()
      store.dismiss(7)
      store.dismiss(8)

      store.clearDismissed(7)

      expect(store.isDismissed(7)).toBe(false)
      expect(store.isDismissed(8)).toBe(true)
    })

    it('sessionStorage 里的脏数据不会让它崩', () => {
      window.sessionStorage.setItem('low-balance-banner-dismissed', '{not json')
      setActivePinia(createPinia())
      expect(useLowBalanceStore().isDismissed(7)).toBe(false)

      window.sessionStorage.setItem('low-balance-banner-dismissed', JSON.stringify({ a: 1 }))
      setActivePinia(createPinia())
      expect(useLowBalanceStore().isDismissed(7)).toBe(false)
    })
  })
})
