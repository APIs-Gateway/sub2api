import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AdminPointsRecordsView from '../AdminPointsRecordsView.vue'
import { pointsViewMountOptions } from '@/views/__tests__/pointsTestStubs'

// 邀请返利积分制（issue #11）—— 后台积分流水视图测试:加载/过滤/分页/错误 + 单元格渲染。

const { listPointsLedgerAdmin } = vi.hoisted(() => ({ listPointsLedgerAdmin: vi.fn() }))
vi.mock('@/api/admin/points', () => ({ listPointsLedgerAdmin }))

const showError = vi.fn()
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn() }) }))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const ledgerRows = [
  { id: 1, user_id: 7, kind: 'earn', points: 50, available_after: 50, frozen_after: 20, created_at: '2026-06-01T00:00:00Z' },
  { id: 2, user_id: 7, kind: 'to_balance', points: -20, available_after: null, created_at: '2026-06-02T00:00:00Z' },
]

describe('admin AdminPointsRecordsView', () => {
  beforeEach(() => {
    listPointsLedgerAdmin.mockReset()
    showError.mockReset()
    listPointsLedgerAdmin.mockResolvedValue({ items: ledgerRows, total: 2, page: 1, page_size: 20, pages: 1 })
  })

  it('loads ledger on mount and renders cells (positive/negative points, null balance)', async () => {
    const wrapper = mount(AdminPointsRecordsView, pointsViewMountOptions())
    await flushPromises()

    expect(listPointsLedgerAdmin).toHaveBeenCalledWith({ kind: '', search: '', page: 1, page_size: 20 })
    const text = wrapper.text()
    expect(text).toContain('#7')
    expect(text).toContain('+50')
    expect(text).toContain('points.stats.available 50')
    expect(text).toContain('points.stats.frozen 20')
    expect(text).toContain('points.stats.frozen —')
    expect(text).not.toContain('points.account.available')
    expect(text).not.toContain('points.account.frozen')
    expect(text).toContain('—') // available_after null
  })

  it('reloads with kind filter (resets to page 1)', async () => {
    const wrapper = mount(AdminPointsRecordsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.find('select').setValue('earn')
    await flushPromises()
    expect(listPointsLedgerAdmin).toHaveBeenLastCalledWith({ kind: 'earn', search: '', page: 1, page_size: 20 })
  })

  it('search on enter triggers reload', async () => {
    const wrapper = mount(AdminPointsRecordsView, pointsViewMountOptions())
    await flushPromises()
    const input = wrapper.find('input[type="text"]')
    await input.setValue('foo')
    await input.trigger('keyup.enter')
    await flushPromises()
    expect(listPointsLedgerAdmin).toHaveBeenLastCalledWith({ kind: '', search: 'foo', page: 1, page_size: 20 })
  })

  it('refresh button re-loads', async () => {
    const wrapper = mount(AdminPointsRecordsView, pointsViewMountOptions())
    await flushPromises()
    listPointsLedgerAdmin.mockClear()
    await wrapper.find('button').trigger('click')
    await flushPromises()
    expect(listPointsLedgerAdmin).toHaveBeenCalledTimes(1)
  })

  it('pagination change page and page size', async () => {
    const wrapper = mount(AdminPointsRecordsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.find('[data-test="next-page"]').trigger('click')
    await flushPromises()
    expect(listPointsLedgerAdmin).toHaveBeenLastCalledWith({ kind: '', search: '', page: 2, page_size: 20 })
    await wrapper.find('[data-test="change-size"]').trigger('click')
    await flushPromises()
    expect(listPointsLedgerAdmin).toHaveBeenLastCalledWith({ kind: '', search: '', page: 1, page_size: 50 })
  })

  it('shows error when load fails', async () => {
    listPointsLedgerAdmin.mockRejectedValueOnce(new Error('boom'))
    mount(AdminPointsRecordsView, pointsViewMountOptions())
    await flushPromises()
    expect(showError).toHaveBeenCalled()
  })

  it('renders empty slot when no rows', async () => {
    listPointsLedgerAdmin.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    const wrapper = mount(AdminPointsRecordsView, pointsViewMountOptions())
    await flushPromises()
    expect(wrapper.find('[data-test="empty"]').exists()).toBe(true)
  })
})
