import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import { ref } from 'vue'
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

function button(wrapper: ReturnType<typeof shallowMount>, text: string) {
  const match = wrapper.findAll('button').find((item) => item.text() === text)
  expect(match, `button ${text} must be rendered`).toBeDefined()
  return match!
}

async function openRule(mode: 'create' | 'edit') {
  if (mode === 'edit') {
    listAlertRules.mockResolvedValue([{
      id: 17, name: 'Error rate', enabled: true, metric_type: 'error_rate', operator: '>',
      threshold: 1, window_minutes: 1, sustained_minutes: 2, cooldown_minutes: 10,
      severity: 'P1', notify_email: true
    }])
  }
  const wrapper = shallowMount(OpsAlertRulesCard, {
    global: {
      stubs: {
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' }
      }
    }
  })
  await flushPromises()
  await button(wrapper, mode === 'create' ? 'admin.ops.alertRules.create' : 'common.edit').trigger('click')
  await wrapper.get('input[type="text"]').setValue('Error rate')
  return wrapper
}

describe('alert rule whole-minute durations', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listAlertRules.mockResolvedValue([])
    createAlertRule.mockResolvedValue({ id: 18 })
    updateAlertRule.mockResolvedValue({ id: 17 })
  })

  for (const mode of ['create', 'edit'] as const) {
    it.each([
      { field: 'sustained', selector: 'input[min="1"][max="1440"]', value: '1.5', error: 'sustainedRange' },
      { field: 'cooldown', selector: 'input[min="0"][max="1440"]', value: '0.5', error: 'cooldownRange' }
    ])(`${mode} rejects fractional $field minutes before API submission`, async ({ selector, value, error }) => {
      const wrapper = await openRule(mode)
      await wrapper.get(selector).setValue(value)
      const message = `admin.ops.alertRules.validation.${error}`
      await button(wrapper, 'common.save').trigger('click')
      await flushPromises()
      expect(createAlertRule).not.toHaveBeenCalled()
      expect(updateAlertRule).not.toHaveBeenCalled()
      expect(showError).toHaveBeenCalledWith(message)
      expect(wrapper.text()).toContain(message)
    })
  }

  it.each([
    { mode: 'create' as const, sustained: 1, cooldown: 0 },
    { mode: 'create' as const, sustained: 1440, cooldown: 1440 },
    { mode: 'edit' as const, sustained: 1440, cooldown: 0 }
  ])('saves $mode integer boundaries $sustained/$cooldown and a fractional threshold', async ({ mode, sustained, cooldown }) => {
    const wrapper = await openRule(mode)
    await wrapper.get('input[min="1"][max="1440"]').setValue(String(sustained))
    await wrapper.get('input[min="0"][max="1440"]').setValue(String(cooldown))
    await wrapper.get('input[type="number"]:not([min])').setValue('1.25')
    await button(wrapper, 'common.save').trigger('click')
    await flushPromises()
    const payload = expect.objectContaining({ sustained_minutes: sustained, cooldown_minutes: cooldown, threshold: 1.25 })
    if (mode === 'create') {
      expect(createAlertRule).toHaveBeenCalledTimes(1)
      expect(createAlertRule).toHaveBeenCalledWith(payload)
      expect(updateAlertRule).not.toHaveBeenCalled()
    } else {
      expect(updateAlertRule).toHaveBeenCalledTimes(1)
      expect(updateAlertRule).toHaveBeenCalledWith(17, payload)
      expect(createAlertRule).not.toHaveBeenCalled()
    }
    expect(showError).not.toHaveBeenCalled()
  })
})
