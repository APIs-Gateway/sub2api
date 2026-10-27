import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import Select from '@/components/common/Select.vue'
import OpsAlertEventsCard from '../OpsAlertEventsCard.vue'
import type { AlertEvent } from '../types'

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('@vueuse/core', async importOriginal => {
  const { ref } = await import('vue')
  return { ...await importOriginal<typeof import('@vueuse/core')>(), useMediaQuery: () => ref(true) }
})
const { list, showError } = vi.hoisted(() => ({ list: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin/ops', () => ({ opsAPI: { listAlertEvents: list } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError }) }))
enableAutoUnmount(afterEach)
function deferred() {
  let resolve!: (value: AlertEvent[]) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<AlertEvent[]>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
function rows(start: number, count = 10): AlertEvent[] {
  return Array.from({ length: count }, (_, i) => ({
    id: start + i, rule_id: 1, severity: 'P1', status: 'firing', title: `event-${start + i}`,
    fired_at: '2026-10-03T00:00:00Z', created_at: '2026-10-02T00:00:00Z', email_sent: false,
  }))
}
type View = { events: AlertEvent[]; loading: boolean; loadingMore: boolean; hasMore: boolean; loadMore: () => Promise<void>; loadFirstPage: () => Promise<void> }
function mountCard() { return shallowMount(OpsAlertEventsCard) }
function view(wrapper: ReturnType<typeof mountCard>) { return wrapper.vm as unknown as View }
function filter(wrapper: ReturnType<typeof mountCard>, index: number, value: string) {
  wrapper.findAllComponents(Select)[index].vm.$emit('change', value)
}
function settle(pending: ReturnType<typeof deferred>, outcome: string, value = rows(900)) {
  if (outcome === 'success') pending.resolve(value)
  else if (outcome === 'empty') pending.resolve([])
  else pending.reject(new Error('obsolete request'))
}
beforeEach(() => {
  vi.resetAllMocks()
  vi.spyOn(console, 'error').mockImplementation(() => {})
  list.mockResolvedValue([])
})
afterEach(() => { vi.restoreAllMocks() })

describe('alert event pagination generation', () => {
  it.each(['success', 'empty', 'error'])('ignores obsolete first-page %s while current filter loads', async outcome => {
    const old = deferred(), current = deferred()
    list.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const wrapper = mountCard()
    filter(wrapper, 1, 'P2')
    settle(old, outcome)
    await flushPromises()
    expect(view(wrapper).loading).toBe(true)
    expect(view(wrapper).events).toEqual([])
    expect(view(wrapper).hasMore).toBe(true)
    expect(showError).not.toHaveBeenCalled()
    current.resolve(rows(100))
    await flushPromises()
    expect(view(wrapper).events).toEqual(rows(100))
    expect(view(wrapper).loading).toBe(false)
    expect(list).toHaveBeenLastCalledWith({ limit: 10, time_range: '24h', severity: 'P2' })
  })

  it.each(['success', 'empty', 'error'])('ignores obsolete first-page %s after newer page completed', async outcome => {
    const old = deferred()
    list.mockReturnValueOnce(old.promise).mockResolvedValueOnce(rows(100))
    const wrapper = mountCard()
    filter(wrapper, 2, 'resolved')
    await flushPromises()
    settle(old, outcome)
    await flushPromises()
    expect(view(wrapper).events).toEqual(rows(100))
    expect(view(wrapper).hasMore).toBe(true)
    expect(showError).not.toHaveBeenCalled()
  })

  it.each(['success', 'empty', 'error'])('ignores old pagination %s without clearing current pagination busy state', async outcome => {
    const old = deferred(), current = deferred()
    list.mockResolvedValueOnce(rows(10)).mockReturnValueOnce(old.promise).mockResolvedValueOnce(rows(100)).mockReturnValueOnce(current.promise)
    const wrapper = mountCard()
    await flushPromises()
    const oldLoad = view(wrapper).loadMore()
    expect(list).toHaveBeenLastCalledWith({ limit: 10, time_range: '24h', before_id: 19, before_fired_at: '2026-10-03T00:00:00Z' })
    filter(wrapper, 1, 'P0')
    expect(view(wrapper).loadingMore).toBe(false)
    await flushPromises()
    const currentLoad = view(wrapper).loadMore()
    expect(view(wrapper).loadingMore).toBe(true)
    settle(old, outcome)
    await oldLoad
    expect(view(wrapper).events).toEqual(rows(100))
    expect(view(wrapper).hasMore).toBe(true)
    expect(view(wrapper).loadingMore).toBe(true)
    expect(showError).not.toHaveBeenCalled()
    await view(wrapper).loadMore()
    expect(list).toHaveBeenCalledTimes(4)
    current.resolve(rows(200, 2))
    await currentLoad
    expect(view(wrapper).events).toEqual([...rows(100), ...rows(200, 2)])
    expect(view(wrapper).hasMore).toBe(false)
    expect(view(wrapper).loadingMore).toBe(false)
    expect(list).toHaveBeenLastCalledWith({ limit: 10, time_range: '24h', severity: 'P0', before_id: 109, before_fired_at: '2026-10-03T00:00:00Z' })
  })

  it.each([[0, '7d', { time_range: '7d' }], [1, 'P3', { severity: 'P3' }], [2, 'manual_resolved', { status: 'manual_resolved' }], [3, 'false', { email_sent: false }]] as const)('starts filter %i without stale rows or cursor', async (index, value, query) => {
    list.mockResolvedValueOnce(rows(10)).mockResolvedValueOnce(rows(100, 1))
    const wrapper = mountCard()
    await flushPromises()
    filter(wrapper, index, value)
    expect(view(wrapper).events).toEqual([])
    expect(view(wrapper).loading).toBe(true)
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith({ limit: 10, time_range: '24h', ...query })
    expect(view(wrapper).events).toEqual(rows(100, 1))
    expect(view(wrapper).hasMore).toBe(false)
  })

  it('makes refresh invalidate an active next page and uses the new first-page cursor', async () => {
    const old = deferred()
    const first = rows(100)
    first[9].fired_at = ''
    list.mockResolvedValueOnce(rows(10)).mockReturnValueOnce(old.promise).mockResolvedValueOnce(first).mockResolvedValueOnce([])
    const wrapper = mountCard()
    await flushPromises()
    const obsolete = view(wrapper).loadMore()
    await wrapper.get('button').trigger('click')
    await flushPromises()
    old.resolve(rows(900))
    await obsolete
    expect(view(wrapper).events).toEqual(first)
    await view(wrapper).loadMore()
    expect(list).toHaveBeenLastCalledWith({ limit: 10, time_range: '24h', before_id: 109, before_fired_at: '2026-10-02T00:00:00Z' })
    expect(view(wrapper).hasMore).toBe(false)
  })

  it('retains current first-page errors and allows refresh recovery', async () => {
    list.mockRejectedValueOnce({ response: { data: { detail: 'current failure' } } }).mockResolvedValueOnce(rows(100))
    const wrapper = mountCard()
    await flushPromises()
    expect(showError).toHaveBeenCalledWith('current failure')
    expect(view(wrapper).loading).toBe(false)
    expect(view(wrapper).hasMore).toBe(false)
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(view(wrapper).events).toEqual(rows(100))
    expect(view(wrapper).hasMore).toBe(true)
  })

  it.each(['empty', 'error'])('ends current pagination on %s and preserves loaded rows', async outcome => {
    const next = deferred()
    list.mockResolvedValueOnce(rows(10)).mockReturnValueOnce(next.promise)
    const wrapper = mountCard()
    await flushPromises()
    const loading = view(wrapper).loadMore()
    settle(next, outcome)
    await loading
    expect(view(wrapper).events).toEqual(rows(10))
    expect(view(wrapper).hasMore).toBe(false)
    expect(view(wrapper).loadingMore).toBe(false)
    await view(wrapper).loadMore()
    expect(list).toHaveBeenCalledTimes(2)
    expect(showError).not.toHaveBeenCalled()
  })

  it.each(['success', 'error'])('ignores pending first-page %s after unmount', async outcome => {
    const pending = deferred()
    list.mockReturnValueOnce(pending.promise)
    const wrapper = mountCard()
    const vm = view(wrapper)
    wrapper.unmount()
    settle(pending, outcome)
    await flushPromises()
    expect(vm.events).toEqual([])
    expect(showError).not.toHaveBeenCalled()
    await vm.loadFirstPage()
    await vm.loadMore()
    expect(list).toHaveBeenCalledTimes(1)
  })
})
