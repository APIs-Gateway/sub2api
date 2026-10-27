import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import MonitorTemplateApplyPickerDialog from '../MonitorTemplateApplyPickerDialog.vue'

const mocks = vi.hoisted(() => ({
  listAssociatedMonitors: vi.fn(), apply: vi.fn(), showError: vi.fn(), showSuccess: vi.fn(),
}))
vi.mock('@/api/admin', () => ({ adminAPI: { channelMonitorTemplate: mocks } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => mocks }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
enableAutoUnmount(afterEach)
function deferred() {
  let resolve!: (value: { affected: number }) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<{ affected: number }>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
function mountPicker() {
  return mount(MonitorTemplateApplyPickerDialog, {
    props: { show: true, templateId: 17, templateName: 'Template' },
    global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' } } },
  })
}
beforeEach(() => {
  vi.resetAllMocks()
  mocks.listAssociatedMonitors.mockResolvedValue({ items: [{ id: 21, name: 'Monitor 21', provider: 'anthropic', api_mode: 'chat_completions', enabled: true }] })
  mocks.apply.mockResolvedValue({ affected: 1 })
})
describe('template apply request owner', () => {
  for (const boundary of ['reopen', 'switch'] as const) {
    it.each(['resolve', 'reject'] as const)(`ignores old apply %s after ${boundary}, preserving new busy state`, async outcome => {
      const old = deferred()
      const current = deferred()
      mocks.apply.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
      const wrapper = mountPicker()
      await flushPromises()
      await wrapper.get('button.btn-primary').trigger('click')
      expect(mocks.apply).toHaveBeenNthCalledWith(1, 17, [21])
      if (boundary === 'reopen') await wrapper.setProps({ show: false })
      await wrapper.setProps({ show: true, templateId: boundary === 'switch' ? 18 : 17 })
      await flushPromises()
      await wrapper.get('button.btn-primary').trigger('click')
      expect(mocks.apply).toHaveBeenNthCalledWith(2, boundary === 'switch' ? 18 : 17, [21])
      if (outcome === 'resolve') old.resolve({ affected: 2 })
      else old.reject(new Error('old apply error'))
      await flushPromises()
      expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeDefined()
      expect(mocks.showSuccess).not.toHaveBeenCalled()
      expect(mocks.showError).not.toHaveBeenCalled()
      expect(wrapper.emitted('close')).toBeUndefined()
      expect(wrapper.emitted('applied')).toBeUndefined()
      current.resolve({ affected: 1 })
      await flushPromises()
      expect(mocks.showSuccess).toHaveBeenCalledTimes(1)
      expect(wrapper.emitted('applied')).toEqual([[1]])
      expect(wrapper.emitted('close')).toEqual([[]])
    })
  }
  for (const boundary of ['button', 'prop', 'unmount'] as const) {
    it.each(['resolve', 'reject'] as const)(`ignores pending apply %s after ${boundary}`, async outcome => {
      const pending = deferred()
      mocks.apply.mockReturnValueOnce(pending.promise)
      const wrapper = mountPicker()
      await flushPromises()
      await wrapper.get('button.btn-primary').trigger('click')
      expect(mocks.apply).toHaveBeenCalledTimes(1)
      if (boundary === 'button') await wrapper.get('button.btn-secondary').trigger('click')
      else if (boundary === 'prop') await wrapper.setProps({ show: false })
      else wrapper.unmount()
      if (outcome === 'resolve') pending.resolve({ affected: 1 })
      else pending.reject(new Error('closed apply error'))
      await flushPromises()
      expect(mocks.showSuccess).not.toHaveBeenCalled()
      expect(mocks.showError).not.toHaveBeenCalled()
      expect(wrapper.emitted('applied')).toBeUndefined()
      expect(wrapper.emitted('close')?.length ?? 0).toBe(boundary === 'button' ? 1 : 0)
    })
  }
  it('keeps a current failed apply retryable', async () => {
    mocks.apply.mockRejectedValueOnce(new Error('current apply failure'))
    const wrapper = mountPicker()
    await flushPromises()
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(mocks.showError).toHaveBeenCalledTimes(1)
    expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeUndefined()
    expect(wrapper.emitted('applied')).toBeUndefined()
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(mocks.apply).toHaveBeenCalledTimes(2)
    expect(wrapper.emitted('applied')).toEqual([[1]])
  })
})
