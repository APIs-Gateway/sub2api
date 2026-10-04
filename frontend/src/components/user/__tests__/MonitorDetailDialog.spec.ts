import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import MonitorDetailDialog from '../MonitorDetailDialog.vue'
const mocks = vi.hoisted(() => ({ status: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/channelMonitor', () => ({ status: mocks.status }))
vi.mock('@/stores/app', () => ({ useAppStore: () => mocks }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => vi.clearAllMocks())
function deferred() {
  let resolve!: (value: unknown) => void
  let reject!: (value: unknown) => void
  const promise = new Promise((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
const detail = (model: string) => ({ models: [{ model, latest_status: 'operational' }] })
function open() {
  return mount(MonitorDetailDialog, { props: { show: true, monitorId: 1, title: 'Monitor' },
    global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' } } } })
}
describe('monitor detail request ownership', () => {
  it('keeps the new monitor response when the old response arrives last', async () => {
    const old = deferred()
    mocks.status.mockReturnValueOnce(old.promise).mockResolvedValueOnce(detail('new-model'))
    const w = open(); await w.setProps({ monitorId: 2 }); await flushPromises()
    old.resolve(detail('old-model')); await flushPromises()
    expect(w.text()).toContain('new-model'); expect(w.text()).not.toContain('old-model')
  })
  it('ignores a previous failure while the current monitor is loading', async () => {
    const old = deferred(); const current = deferred()
    mocks.status.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const w = open(); await w.setProps({ show: false }); await w.setProps({ show: true })
    old.reject(new Error('old failure')); await flushPromises()
    expect(mocks.showError).not.toHaveBeenCalled(); expect(w.text()).toContain('common.loading')
    current.resolve(detail('current')); await flushPromises(); expect(w.text()).toContain('current')
  })
  it('reports a failure for the current monitor', async () => {
    mocks.status.mockRejectedValueOnce(new Error('current failure'))
    const w = open(); await flushPromises()
    expect(mocks.showError).toHaveBeenCalledWith('current failure')
    expect(w.text()).toContain('channelStatus.detailLoadError')
  })
})

describe('monitor detail without model names (normal users)', () => {
  const userDetail = {
    models: [{
      latest_status: 'operational', latest_latency_ms: 820,
      availability_7d: 99.5, availability_15d: 98.1, availability_30d: 97.7, avg_latency_7d_ms: 900,
    }],
  }

  it('shows the primary stats as a grid instead of a one-row table', async () => {
    mocks.status.mockResolvedValueOnce(userDetail)
    const w = open(); await flushPromises()
    expect(w.find('table').exists()).toBe(false)
    expect(w.findAll('dl > div')).toHaveLength(6)
    expect(w.text()).toContain('monitorCommon.status.operational')
    expect(w.text()).toContain('820')
    expect(w.text()).toContain('99.50%')
    expect(w.text()).toContain('98.10%')
    expect(w.text()).toContain('97.70%')
    expect(w.text()).toContain('900')
    expect(w.text()).not.toContain('channelStatus.detailColumns.model')
  })

  it('keeps the per-model table for admins', async () => {
    mocks.status.mockResolvedValueOnce({ models: [
      { model: 'primary-model', latest_status: 'operational' },
      { model: 'extra-model', latest_status: 'degraded' },
    ] })
    const w = open(); await flushPromises()
    expect(w.find('table').exists()).toBe(true)
    expect(w.find('dl').exists()).toBe(false)
    expect(w.text()).toContain('primary-model')
    expect(w.text()).toContain('extra-model')
  })

  it('renders nothing but the shell when the response has no models', async () => {
    mocks.status.mockResolvedValueOnce({ models: [] })
    const w = open(); await flushPromises()
    expect(w.findAll('dl > div')).toHaveLength(0)
    expect(w.find('table').exists()).toBe(false)
  })
})
