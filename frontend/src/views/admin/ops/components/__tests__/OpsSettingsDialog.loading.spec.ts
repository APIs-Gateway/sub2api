import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import OpsSettingsDialog from '../OpsSettingsDialog.vue'

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
enableAutoUnmount(afterEach)
const { opsAPI, showError, showSuccess } = vi.hoisted(() => ({
  opsAPI: {
    getAlertRuntimeSettings: vi.fn(), getEmailNotificationConfig: vi.fn(),
    getAdvancedSettings: vi.fn(), getMetricThresholds: vi.fn(),
    updateAlertRuntimeSettings: vi.fn(), updateEmailNotificationConfig: vi.fn(),
    updateAdvancedSettings: vi.fn(), updateMetricThresholds: vi.fn(),
  }, showError: vi.fn(), showSuccess: vi.fn(),
}))
vi.mock('@/api/admin/ops', () => ({ opsAPI }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
const runtime = () => ({ evaluation_interval_seconds: 30 })
const email = () => ({ alert: { enabled: false, recipients: [] }, report: { enabled: false, recipients: [] } })
const advanced = () => ({
  data_retention: { error_log_retention_days: 7, minute_metrics_retention_days: 7, hourly_metrics_retention_days: 7 },
  openai_account_quota_auto_pause: { default_threshold_5h: 0.85, default_threshold_7d: 0.95 },
})
const thresholds = () => ({ sla_percent_min: 98, ttft_p99_ms_max: 400, request_error_rate_percent_max: 4, upstream_error_rate_percent_max: 3 })
const queries = [
  ['getAlertRuntimeSettings', runtime], ['getEmailNotificationConfig', email],
  ['getAdvancedSettings', advanced], ['getMetricThresholds', thresholds],
] as const
const updates = [opsAPI.updateAlertRuntimeSettings, opsAPI.updateEmailNotificationConfig, opsAPI.updateAdvancedSettings, opsAPI.updateMetricThresholds]
type View = { saveAllSettings: () => Promise<void>; metricThresholds: ReturnType<typeof thresholds> }
function mountDialog(show = false) {
  return shallowMount(OpsSettingsDialog, {
    props: { show },
    global: { stubs: { BaseDialog: { template: '<div><slot name="footer" /></div>' } } },
  })
}
async function openDialog() {
  const wrapper = mountDialog()
  await wrapper.setProps({ show: true })
  return wrapper
}
function noUpdates() { for (const update of updates) expect(update).not.toHaveBeenCalled() }
function disabled(wrapper: ReturnType<typeof mountDialog>) { return wrapper.get('button.btn-primary').attributes('disabled') }

beforeEach(() => {
  vi.resetAllMocks()
  vi.spyOn(console, 'error').mockImplementation(() => {})
  for (const [name, value] of queries) opsAPI[name].mockImplementation(value)
})
afterEach(() => { vi.restoreAllMocks() })

describe('ops settings loaded save guard', () => {
  it.each(queries)('waits for %s before any update and preserves all settings', async (name, value) => {
    const pending = deferred<object>()
    opsAPI[name].mockReturnValueOnce(pending.promise)
    const wrapper = await openDialog()
    expect(disabled(wrapper)).toBeDefined()
    await (wrapper.vm as unknown as View).saveAllSettings()
    noUpdates()
    pending.resolve(value())
    await flushPromises()
    expect(disabled(wrapper)).toBeUndefined()
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(opsAPI.updateAlertRuntimeSettings).toHaveBeenCalledWith(runtime())
    expect(opsAPI.updateEmailNotificationConfig).toHaveBeenCalledWith(email())
    expect(opsAPI.updateAdvancedSettings).toHaveBeenCalledWith(advanced())
    expect(opsAPI.updateMetricThresholds).toHaveBeenCalledWith(thresholds())
    expect(wrapper.emitted('saved')).toHaveLength(1)
  })

  it.each(queries)('blocks %s load failure and permits a fresh successful reopen', async (name) => {
    opsAPI[name].mockRejectedValueOnce(new Error('load failed'))
    const wrapper = await openDialog()
    await flushPromises()
    expect(disabled(wrapper)).toBeDefined()
    await (wrapper.vm as unknown as View).saveAllSettings()
    noUpdates()
    expect(wrapper.emitted('saved')).toBeUndefined()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(disabled(wrapper)).toBeUndefined()
    await (wrapper.vm as unknown as View).saveAllSettings()
    expect(opsAPI.updateMetricThresholds).toHaveBeenCalledWith(thresholds())
  })

  it.each(['resolve', 'reject'] as const)('ignores obsolete %s while the current opening still loads', async outcome => {
    const old = deferred<object>()
    const current = deferred<object>()
    opsAPI.getMetricThresholds.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const wrapper = await openDialog()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    if (outcome === 'resolve') old.resolve({ sla_percent_min: 1 })
    else old.reject(new Error('obsolete load'))
    await flushPromises()
    expect(disabled(wrapper)).toBeDefined()
    await (wrapper.vm as unknown as View).saveAllSettings()
    noUpdates()
    expect(showError).not.toHaveBeenCalled()
    current.resolve(thresholds())
    await flushPromises()
    await (wrapper.vm as unknown as View).saveAllSettings()
    expect(opsAPI.updateMetricThresholds).toHaveBeenCalledWith(thresholds())
  })

  it.each(['resolve', 'reject'] as const)('ignores obsolete %s after a newer load fails', async outcome => {
    const old = deferred<object>()
    opsAPI.getMetricThresholds.mockReturnValueOnce(old.promise).mockRejectedValueOnce(new Error('current load'))
    const wrapper = await openDialog()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    if (outcome === 'resolve') old.resolve({ sla_percent_min: 1 })
    else old.reject(new Error('obsolete load'))
    await flushPromises()
    expect(disabled(wrapper)).toBeDefined()
    await (wrapper.vm as unknown as View).saveAllSettings()
    noUpdates()
    expect(showError).toHaveBeenCalledTimes(1)
  })

  it('does not overwrite a newer successful opening with older settings', async () => {
    const old = deferred<object>()
    opsAPI.getMetricThresholds.mockReturnValueOnce(old.promise)
    const wrapper = await openDialog()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    old.resolve({ sla_percent_min: 1 })
    await flushPromises()
    await (wrapper.vm as unknown as View).saveAllSettings()
    expect(opsAPI.updateMetricThresholds).toHaveBeenCalledWith(thresholds())
  })

  it.each(queries)('blocks missing %s payload', async name => {
    opsAPI[name].mockResolvedValueOnce(null)
    const wrapper = await openDialog()
    await flushPromises()
    expect(disabled(wrapper)).toBeDefined()
    await (wrapper.vm as unknown as View).saveAllSettings()
    noUpdates()
  })

  it.each(['close', 'unmount'] as const)('ignores late load rejection after %s', async action => {
    const pending = deferred<object>()
    opsAPI.getMetricThresholds.mockReturnValueOnce(pending.promise)
    const wrapper = await openDialog()
    if (action === 'close') await wrapper.setProps({ show: false })
    else wrapper.unmount()
    pending.reject(new Error('closed load'))
    await flushPromises()
    expect(showError).not.toHaveBeenCalled()
    noUpdates()
  })

  it('loads when mounted open and blocks duplicate saves until the previous save finishes', async () => {
    const wrapper = mountDialog(true)
    await flushPromises()
    expect(disabled(wrapper)).toBeUndefined()
    const pending = deferred<unknown>()
    opsAPI.updateMetricThresholds.mockReturnValueOnce(pending.promise)
    const save = (wrapper.vm as unknown as View).saveAllSettings()
    await (wrapper.vm as unknown as View).saveAllSettings()
    expect(opsAPI.updateMetricThresholds).toHaveBeenCalledTimes(1)
    pending.resolve(undefined)
    await save
    expect(wrapper.emitted('saved')).toHaveLength(1)
  })

  it.each(['resolve', 'reject'] as const)('does not close a reopened dialog after an older save %ss', async outcome => {
    const wrapper = await openDialog()
    await flushPromises()
    const pending = deferred<unknown>()
    opsAPI.updateMetricThresholds.mockReturnValueOnce(pending.promise)
    const save = (wrapper.vm as unknown as View).saveAllSettings()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    if (outcome === 'resolve') pending.resolve(undefined)
    else pending.reject(new Error('old save'))
    await save
    expect(wrapper.emitted('saved')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
    expect(disabled(wrapper)).toBeUndefined()
  })
})
