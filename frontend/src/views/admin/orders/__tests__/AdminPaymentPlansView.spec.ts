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
  useAppStore: () => ({ showError: mocks.showError, showSuccess: vi.fn() }),
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
      data: [
        {
          id: 1,
          group_id: 1,
          name: 'CNY plan',
          description: 'CNY plan',
          price: 499,
          original_price: 599,
          currency: 'CNY',
          validity_days: 30,
          validity_unit: 'day',
          features: '',
          for_sale: true,
          sort_order: 1,
        },
        {
          id: 2,
          group_id: 1,
          name: 'Legacy plan',
          description: 'Legacy plan',
          price: 10,
          original_price: 0,
          currency: '',
          validity_days: 30,
          validity_unit: 'day',
          features: '',
          for_sale: true,
          sort_order: 2,
        },
      ],
    })
    mocks.getAllGroups.mockResolvedValue([])
    mocks.showError.mockReset()
  })

  it('uses configured symbols and keeps legacy prices in USD', async () => {
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

    const text = wrapper.text()
    expect(text).toContain('¥499.00')
    expect(text).toContain('¥599.00')
    expect(text).toContain('$10.00')
  })
})
