import { defineComponent, h, nextTick } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import PlanEditDialog from '../PlanEditDialog.vue'
import type { AdminGroup } from '@/types'

const mocks = vi.hoisted(() => ({
  createPlan: vi.fn(),
  updatePlan: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin/payment', () => ({
  adminPaymentAPI: {
    createPlan: mocks.createPlan,
    updatePlan: mocks.updatePlan,
  },
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: mocks.showError, showSuccess: mocks.showSuccess }),
}))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('@/utils/apiError', () => ({
  extractApiErrorMessage: (error: unknown) => String(error),
}))

const BaseDialogStub = defineComponent({
  props: ['show'],
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})
const SelectStub = defineComponent({
  props: ['modelValue', 'options'],
  emits: ['update:modelValue'],
  setup: (props, { emit }) => () => h(
    'select',
    {
      value: props.modelValue,
      onChange: (event: Event) => emit('update:modelValue', (event.target as HTMLSelectElement).value),
    },
    (props.options as Array<{ value: string; label: string }> | undefined)?.map(option =>
      h('option', { value: option.value }, option.label)),
  ),
})
const GroupBadgeStub = defineComponent({ setup: () => () => h('span') })
const IconStub = defineComponent({ setup: () => () => h('i') })

const group = {
  id: 1,
  name: 'OpenAI',
  platform: 'openai',
  rate_multiplier: 1,
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
} as AdminGroup

const plan = {
  id: 9,
  group_id: 1,
  name: 'Pro',
  description: 'Pro plan',
  daily_amount_usd: 10,
  price: 10,
  original_price: 20,
  currency: 'USD',
  validity_days: 30,
  validity_unit: 'day',
  features: ['priority'],
  for_sale: true,
  sort_order: 1,
}

function mountDialog(editingPlan: typeof plan | null) {
  return mount(PlanEditDialog, {
    props: { show: false, plan: editingPlan, groups: [group] },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Select: SelectStub,
        GroupBadge: GroupBadgeStub,
        Icon: IconStub,
      },
    },
  })
}

describe('PlanEditDialog display currency', () => {
  beforeEach(() => {
    mocks.createPlan.mockReset().mockResolvedValue({})
    mocks.updatePlan.mockReset().mockResolvedValue({})
    mocks.showError.mockReset()
    mocks.showSuccess.mockReset()
  })

  it('loads an existing currency and normalizes the update payload', async () => {
    const wrapper = mountDialog(plan)
    await wrapper.setProps({ show: true })
    await nextTick()

    const currencyInput = wrapper.get('input[maxlength="3"]')
    expect((currencyInput.element as HTMLInputElement).value).toBe('USD')
    await currencyInput.setValue(' eur ')
    await wrapper.find('form').trigger('submit.prevent')
    await flushPromises()

    expect(mocks.updatePlan).toHaveBeenCalledWith(9, expect.objectContaining({ currency: 'EUR' }))
  })

  it('sends a normalized currency when creating a new plan', async () => {
    const wrapper = mountDialog(null)
    await wrapper.setProps({ show: true })
    await nextTick()

    const vm = wrapper.vm as unknown as {
      planForm: {
        group_id: number
        name: string
        description: string
        daily_amount_usd: number
        price: number
        currency: string
        validity_days: number
      }
      planFeaturesText: string
      handleSavePlan: () => Promise<void>
    }
    vm.planForm.group_id = 1
    vm.planForm.name = 'New plan'
    vm.planForm.description = 'New plan description'
    vm.planForm.daily_amount_usd = 10
    vm.planForm.price = 10
    vm.planForm.currency = ' usd '
    vm.planForm.validity_days = 30
    vm.planFeaturesText = 'priority\nfast support'

    await vm.handleSavePlan()
    await flushPromises()

    expect(mocks.createPlan).toHaveBeenCalledWith(expect.objectContaining({
      currency: 'USD',
      features: 'priority\nfast support',
    }))
  })
})
