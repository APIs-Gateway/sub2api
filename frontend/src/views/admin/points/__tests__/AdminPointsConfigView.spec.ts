import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AdminPointsConfigView from '../AdminPointsConfigView.vue'
import { pointsViewMountOptions } from '@/views/__tests__/pointsTestStubs'

// 邀请返利积分制（issue #11）—— 后台积分配置视图测试:加载/保存/peg 变更二次确认/错误。

const { getPointsSettings, updatePointsSettings } = vi.hoisted(() => ({
  getPointsSettings: vi.fn(),
  updatePointsSettings: vi.fn(),
}))
vi.mock('@/api/admin/points', () => ({ getPointsSettings, updatePointsSettings }))

const showError = vi.fn()
const showSuccess = vi.fn()
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const baseSettings = {
  enabled: true,
  peg: 0.01,
  cashback_rate_percent: 20,
  freeze_hours: 0,
  withdraw_enabled: true,
  withdraw_min_points: 0,
  withdraw_fee_percent: 10,
  redeem_balance_on: true,
  redeem_plan_on: true,
}

describe('admin AdminPointsConfigView', () => {
  beforeEach(() => {
    getPointsSettings.mockReset()
    updatePointsSettings.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    getPointsSettings.mockResolvedValue({ ...baseSettings })
    updatePointsSettings.mockImplementation(async (s) => ({ ...s }))
  })

  it('loads settings on mount', async () => {
    const wrapper = mount(AdminPointsConfigView, pointsViewMountOptions())
    await flushPromises()
    expect(getPointsSettings).toHaveBeenCalled()
    expect(wrapper.find('input[step="0.0001"]').exists()).toBe(true)
  })

  it('saves directly when peg unchanged', async () => {
    const wrapper = mount(AdminPointsConfigView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()
    expect(updatePointsSettings).toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalled()
    expect(wrapper.find('[data-test="dialog"]').exists()).toBe(false)
  })

  it('opens peg-change confirm dialog and confirms', async () => {
    const wrapper = mount(AdminPointsConfigView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.find('input[step="0.0001"]').setValue('0.02')
    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()
    // 弹确认框,尚未保存
    expect(wrapper.find('[data-test="dialog"]').exists()).toBe(true)
    expect(updatePointsSettings).not.toHaveBeenCalled()
    // 确认 → persist
    await wrapper.find('button.btn-danger').trigger('click')
    await flushPromises()
    expect(updatePointsSettings).toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalled()
  })

  it('peg-change dialog can be cancelled (no save)', async () => {
    const wrapper = mount(AdminPointsConfigView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.find('input[step="0.0001"]').setValue('0.05')
    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()
    await wrapper.find('button.btn-secondary').trigger('click')
    await flushPromises()
    expect(updatePointsSettings).not.toHaveBeenCalled()
    expect(wrapper.find('[data-test="dialog"]').exists()).toBe(false)
  })

  it('shows error when load fails', async () => {
    getPointsSettings.mockRejectedValueOnce(new Error('boom'))
    mount(AdminPointsConfigView, pointsViewMountOptions())
    await flushPromises()
    expect(showError).toHaveBeenCalled()
  })

  it('shows error when persist fails', async () => {
    updatePointsSettings.mockRejectedValueOnce(new Error('boom'))
    const wrapper = mount(AdminPointsConfigView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()
    expect(showError).toHaveBeenCalled()
  })
})
