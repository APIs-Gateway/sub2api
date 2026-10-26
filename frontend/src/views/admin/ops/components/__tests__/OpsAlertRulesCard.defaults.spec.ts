import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import { ref } from 'vue'
import Select from '@/components/common/Select.vue'
import OpsAlertRulesCard from '../OpsAlertRulesCard.vue'

const { createAlertRule, updateAlertRule, listAlertRules, showError } = vi.hoisted(() => ({
  createAlertRule: vi.fn(),
  updateAlertRule: vi.fn(),
  listAlertRules: vi.fn(),
  showError: vi.fn()
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key })
}))
vi.mock('@vueuse/core', () => ({ useMediaQuery: () => ref(true) }))
vi.mock('@/api', () => ({ adminAPI: { groups: { getAll: vi.fn().mockResolvedValue([]) } } }))
vi.mock('@/api/admin/ops', () => ({ opsAPI: { listAlertRules, createAlertRule, updateAlertRule } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn() }) }))
enableAutoUnmount(afterEach)

const NAME_REQUIRED = 'admin.ops.alertRules.validation.nameRequired'
const VALIDATION_TITLE = 'admin.ops.alertRules.validation.title'

type Wrapper = ReturnType<typeof shallowMount>

function button(wrapper: Wrapper, text: string) {
  const match = wrapper.findAll('button').find((item) => item.text() === text)
  expect(match, `button ${text} must be rendered`).toBeDefined()
  return match!
}

async function mountCard() {
  const wrapper = shallowMount(OpsAlertRulesCard, {
    global: {
      stubs: {
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' }
      }
    }
  })
  await flushPromises()
  return wrapper
}

async function openCreate() {
  const wrapper = await mountCard()
  await button(wrapper, 'admin.ops.alertRules.create').trigger('click')
  return wrapper
}

async function openEdit(threshold: number) {
  listAlertRules.mockResolvedValue([{
    id: 17, name: 'Error rate', enabled: true, metric_type: 'error_rate', operator: '>',
    threshold, window_minutes: 1, sustained_minutes: 2, cooldown_minutes: 10,
    severity: 'P1', notify_email: true
  }])
  const wrapper = await mountCard()
  await button(wrapper, 'common.edit').trigger('click')
  return wrapper
}

// 指标下拉框：选项里带有「未定价使用记录数」的那一个。
function metricSelect(wrapper: Wrapper) {
  const match = wrapper.findAllComponents(Select).find((item) =>
    (item.props('options') as Array<{ value: unknown }>).some((option) => option.value === 'unpriced_billing_rows')
  )
  expect(match, 'metric select must be rendered').toBeDefined()
  return match!
}

async function pickMetric(wrapper: Wrapper, metric: string) {
  metricSelect(wrapper).vm.$emit('update:modelValue', metric)
  await flushPromises()
}

function thresholdInput(wrapper: Wrapper) {
  return wrapper.get('input[type="number"]:not([min])')
}

function thresholdValue(wrapper: Wrapper) {
  return (thresholdInput(wrapper).element as HTMLInputElement).value
}

describe('alert rule dialog metric defaults', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listAlertRules.mockResolvedValue([])
    createAlertRule.mockResolvedValue({ id: 18 })
    updateAlertRule.mockResolvedValue({ id: 17 })
  })

  it('fills threshold 0 with the ">" operator when unpriced usage records is picked', async () => {
    const wrapper = await openCreate()
    expect(thresholdValue(wrapper)).toBe('1')

    await pickMetric(wrapper, 'unpriced_billing_rows')
    expect(thresholdValue(wrapper)).toBe('0')

    await wrapper.get('input[type="text"]').setValue('Unpriced usage')
    await button(wrapper, 'common.save').trigger('click')
    await flushPromises()

    expect(createAlertRule).toHaveBeenCalledTimes(1)
    expect(createAlertRule).toHaveBeenCalledWith(
      expect.objectContaining({ metric_type: 'unpriced_billing_rows', operator: '>', threshold: 0 })
    )
    expect(showError).not.toHaveBeenCalled()
  })

  it('goes back to the common default when the metric is switched away before touching the threshold', async () => {
    const wrapper = await openCreate()
    await pickMetric(wrapper, 'unpriced_billing_rows')
    expect(thresholdValue(wrapper)).toBe('0')

    await pickMetric(wrapper, 'error_rate')
    expect(thresholdValue(wrapper)).toBe('1')
  })

  it.each(['5', '1'])('keeps a threshold the user typed by hand (%s)', async (typed) => {
    const wrapper = await openCreate()
    await thresholdInput(wrapper).setValue(typed)

    await pickMetric(wrapper, 'unpriced_billing_rows')
    expect(thresholdValue(wrapper)).toBe(typed)

    await wrapper.get('input[type="text"]').setValue('Unpriced usage')
    await button(wrapper, 'common.save').trigger('click')
    await flushPromises()
    expect(createAlertRule).toHaveBeenCalledWith(
      expect.objectContaining({ metric_type: 'unpriced_billing_rows', threshold: Number(typed) })
    )
  })

  it('does not rewrite the threshold of an existing rule when its metric is changed', async () => {
    const wrapper = await openEdit(3)
    expect(thresholdValue(wrapper)).toBe('3')

    await pickMetric(wrapper, 'unpriced_billing_rows')
    expect(thresholdValue(wrapper)).toBe('3')

    await button(wrapper, 'common.save').trigger('click')
    await flushPromises()
    expect(updateAlertRule).toHaveBeenCalledWith(
      17,
      expect.objectContaining({ metric_type: 'unpriced_billing_rows', threshold: 3 })
    )
  })

  it('applies the default again after the dialog is reopened', async () => {
    const wrapper = await openCreate()
    await thresholdInput(wrapper).setValue('5')
    await button(wrapper, 'common.cancel').trigger('click')
    await button(wrapper, 'admin.ops.alertRules.create').trigger('click')

    expect(thresholdValue(wrapper)).toBe('1')
    await pickMetric(wrapper, 'unpriced_billing_rows')
    expect(thresholdValue(wrapper)).toBe('0')
  })
})

describe('alert rule dialog validation timing', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listAlertRules.mockResolvedValue([])
    createAlertRule.mockResolvedValue({ id: 18 })
    updateAlertRule.mockResolvedValue({ id: 17 })
  })

  it('does not show the name error when the dialog has just been opened', async () => {
    const wrapper = await openCreate()
    expect(wrapper.find('input[type="text"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain(NAME_REQUIRED)
    expect(wrapper.text()).not.toContain(VALIDATION_TITLE)
    expect(showError).not.toHaveBeenCalled()
  })

  it('shows the name error once an empty name is submitted, and hides it again on reopen', async () => {
    const wrapper = await openCreate()
    await button(wrapper, 'common.save').trigger('click')
    await flushPromises()

    expect(createAlertRule).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith(NAME_REQUIRED)
    expect(wrapper.text()).toContain(VALIDATION_TITLE)
    expect(wrapper.text()).toContain(NAME_REQUIRED)

    // 补上名称后提示随之消失。
    await wrapper.get('input[type="text"]').setValue('Unpriced usage')
    expect(wrapper.text()).not.toContain(NAME_REQUIRED)

    // 清空再提交又出现，关掉重新打开则不再提前显示。
    await wrapper.get('input[type="text"]').setValue('')
    expect(wrapper.text()).toContain(NAME_REQUIRED)
    await button(wrapper, 'common.cancel').trigger('click')
    await button(wrapper, 'admin.ops.alertRules.create').trigger('click')
    expect(wrapper.text()).not.toContain(NAME_REQUIRED)
  })
})
