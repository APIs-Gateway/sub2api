import { defineComponent, h } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AdminPaymentPlansView from '../AdminPaymentPlansView.vue'

const mocks = vi.hoisted(() => ({
  getPlans: vi.fn(),
  getAllGroups: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin/payment', () => ({
  adminPaymentAPI: {
    getPlans: mocks.getPlans,
  },
}))
vi.mock('@/api/admin', () => ({
  default: {
    groups: { getAll: mocks.getAllGroups },
  },
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: mocks.showError }),
}))
vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})
vi.mock('@/utils/apiError', () => ({
  extractI18nErrorMessage: (error: unknown) => String(error),
}))

const AppLayoutStub = defineComponent({ template: '<div><slot /></div>' })
const IconStub = defineComponent({ setup: () => () => h('i') })
const GroupBadgeStub = defineComponent({ setup: () => () => h('span') })
const PlanEditDialogStub = defineComponent({ template: '<div />' })
const ConfirmDialogStub = defineComponent({ template: '<div />' })
const DataTableStub = defineComponent({
  props: ['data'],
  setup: (props, { slots }) => () => h(
    'div',
    (props.data as Array<Record<string, unknown>>).map(row => h(
      'div',
      { 'data-test': 'plan-row' },
      slots['cell-price']?.({ value: row.price, row }),
    )),
  ),
})

describe('AdminPaymentPlansView display currency', () => {
  beforeEach(() => {
    mocks.getPlans.mockReset().mockResolvedValue({
      data: [{
        id: 1,
        group_id: 1,
        name: 'Pro',
        description: 'Pro plan',
        daily_amount_usd: 10,
        price: 10,
        original_price: 20,
        currency: 'USD',
        validity_days: 30,
        validity_unit: 'day',
        features: '',
        for_sale: true,
        sort_order: 1,
      }],
    })
    mocks.getAllGroups.mockResolvedValue([])
    mocks.showError.mockReset()
  })

  it('renders the display currency beside current and original prices', async () => {
    const wrapper = mount(AdminPaymentPlansView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          DataTable: DataTableStub,
          Icon: IconStub,
          GroupBadge: GroupBadgeStub,
          PlanEditDialog: PlanEditDialogStub,
          ConfirmDialog: ConfirmDialogStub,
        },
      },
    })
    await flushPromises()

    expect(wrapper.find('[data-test="plan-row"]').text()).toContain('USD')
    expect(wrapper.find('[data-test="plan-row"]').text()).toContain('$20.00')
  })
})
