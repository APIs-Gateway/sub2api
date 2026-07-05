import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AdminPointsWithdrawalsView from '../AdminPointsWithdrawalsView.vue'
import { pointsViewMountOptions } from '@/views/__tests__/pointsTestStubs'

// 邀请返利积分制（issue #11）—— 后台提现审核视图测试:加载/过滤/审核(approve/reject)/错误。

const { listPointsWithdrawals, approveWithdrawal, rejectWithdrawal } = vi.hoisted(() => ({
  listPointsWithdrawals: vi.fn(),
  approveWithdrawal: vi.fn(),
  rejectWithdrawal: vi.fn(),
}))
vi.mock('@/api/admin/points', () => ({ listPointsWithdrawals, approveWithdrawal, rejectWithdrawal }))

const showError = vi.fn()
const showSuccess = vi.fn()
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const rows = [
  {
    id: 1, user_id: 7, user_email: 'u@e.com', points: 1000, net_amount: 9,
    payout_method: 'alipay', payout_alipay_name: 'N', payout_alipay_account: 'A',
    status: 'pending', created_at: '2026-06-01T00:00:00Z',
  },
  {
    id: 2, user_id: 8, username: 'bob', points: 500, net_amount: 4.5,
    payout_method: 'usdt', payout_usdt_chain: 'TRC20', payout_usdt_address: 'TXxxx',
    status: 'paid', created_at: '2026-06-02T00:00:00Z',
  },
  {
    id: 3, user_id: 9, points: 200, net_amount: 1.8,
    payout_method: 'usdt', payout_usdt_chain: 'ERC20', payout_usdt_address: 'TYyyy',
    status: 'rejected', created_at: '2026-06-03T00:00:00Z',
  },
]

function resolveList() {
  listPointsWithdrawals.mockResolvedValue({ items: rows, total: 3, page: 1, page_size: 20, pages: 1 })
}

describe('admin AdminPointsWithdrawalsView', () => {
  beforeEach(() => {
    listPointsWithdrawals.mockReset()
    approveWithdrawal.mockReset()
    rejectWithdrawal.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    resolveList()
    approveWithdrawal.mockResolvedValue({ id: 1, status: 'paid' })
    rejectWithdrawal.mockResolvedValue({ id: 1, status: 'rejected' })
  })

  it('loads withdrawals and renders alipay/usdt payout + status badges', async () => {
    const wrapper = mount(AdminPointsWithdrawalsView, pointsViewMountOptions())
    await flushPromises()
    expect(listPointsWithdrawals).toHaveBeenCalledWith({ status: '', page: 1, page_size: 20 })
    const text = wrapper.text()
    expect(text).toContain('u@e.com') // alipay row user_email
    expect(text).toContain('bob') // usdt row username
    expect(text).toContain('#9') // rejected row user_id fallback
    expect(text).toContain('A') // alipay account
    expect(text).toContain('TXxxx') // usdt address
  })

  it('status filter reloads', async () => {
    const wrapper = mount(AdminPointsWithdrawalsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.find('select').setValue('pending')
    await flushPromises()
    expect(listPointsWithdrawals).toHaveBeenLastCalledWith({ status: 'pending', page: 1, page_size: 20 })
  })

  it('refresh and pagination', async () => {
    const wrapper = mount(AdminPointsWithdrawalsView, pointsViewMountOptions())
    await flushPromises()
    listPointsWithdrawals.mockClear()
    const refreshBtn = wrapper.findAll('button').find((b) => b.attributes('title') === 'common.refresh')
    await refreshBtn?.trigger('click')
    await flushPromises()
    expect(listPointsWithdrawals).toHaveBeenCalled()
    await wrapper.find('[data-test="next-page"]').trigger('click')
    await flushPromises()
    expect(listPointsWithdrawals).toHaveBeenLastCalledWith({ status: '', page: 2, page_size: 20 })
    await wrapper.find('[data-test="change-size"]').trigger('click')
    await flushPromises()
    expect(listPointsWithdrawals).toHaveBeenLastCalledWith({ status: '', page: 1, page_size: 50 })
  })

  it('approve flow: open dialog, confirm, calls approveWithdrawal', async () => {
    const wrapper = mount(AdminPointsWithdrawalsView, pointsViewMountOptions())
    await flushPromises()
    // 仅 pending 行有操作按钮
    await wrapper.find('button.btn-secondary.btn-sm').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="dialog"]').exists()).toBe(true)
    await wrapper.find('[data-test="dialog-footer"] button.btn-primary').trigger('click')
    await flushPromises()
    expect(approveWithdrawal).toHaveBeenCalledWith(1, {})
    expect(showSuccess).toHaveBeenCalled()
  })

  it('reject flow: open dialog, fill note, confirm calls rejectWithdrawal', async () => {
    const wrapper = mount(AdminPointsWithdrawalsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.find('button.btn-danger.btn-sm').trigger('click')
    await flushPromises()
    const textarea = wrapper.find('textarea')
    expect(textarea.exists()).toBe(true)
    await textarea.setValue('bad account')
    await wrapper.find('[data-test="dialog-footer"] button.btn-danger').trigger('click')
    await flushPromises()
    expect(rejectWithdrawal).toHaveBeenCalledWith(1, { note: 'bad account' })
    expect(showSuccess).toHaveBeenCalled()
  })

  it('reject with empty note sends undefined', async () => {
    const wrapper = mount(AdminPointsWithdrawalsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.find('button.btn-danger.btn-sm').trigger('click')
    await flushPromises()
    await wrapper.find('[data-test="dialog-footer"] button.btn-danger').trigger('click')
    await flushPromises()
    expect(rejectWithdrawal).toHaveBeenCalledWith(1, { note: undefined })
  })

  it('cancel review closes dialog without action', async () => {
    const wrapper = mount(AdminPointsWithdrawalsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.find('button.btn-secondary.btn-sm').trigger('click')
    await flushPromises()
    await wrapper.find('[data-test="dialog-footer"] button.btn-secondary').trigger('click')
    await flushPromises()
    expect(approveWithdrawal).not.toHaveBeenCalled()
    expect(wrapper.find('[data-test="dialog"]').exists()).toBe(false)
  })

  it('shows error when load fails', async () => {
    listPointsWithdrawals.mockRejectedValueOnce(new Error('boom'))
    mount(AdminPointsWithdrawalsView, pointsViewMountOptions())
    await flushPromises()
    expect(showError).toHaveBeenCalled()
  })

  it('shows error when approve fails', async () => {
    approveWithdrawal.mockRejectedValueOnce(new Error('boom'))
    const wrapper = mount(AdminPointsWithdrawalsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.find('button.btn-secondary.btn-sm').trigger('click')
    await flushPromises()
    await wrapper.find('[data-test="dialog-footer"] button.btn-primary').trigger('click')
    await flushPromises()
    expect(showError).toHaveBeenCalled()
  })

  it('renders empty slot when no rows', async () => {
    listPointsWithdrawals.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    const wrapper = mount(AdminPointsWithdrawalsView, pointsViewMountOptions())
    await flushPromises()
    expect(wrapper.find('[data-test="empty"]').exists()).toBe(true)
  })
})
