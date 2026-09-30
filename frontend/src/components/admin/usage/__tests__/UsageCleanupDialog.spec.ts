import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import UsageCleanupDialog from '../UsageCleanupDialog.vue'
import Pagination from '@/components/common/Pagination.vue'

const { listCleanupTasks, showError } = vi.hoisted(() => ({
  listCleanupTasks: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/components/admin/usage/UsageFilters.vue', () => ({
  default: { template: '<div />' }
}))
vi.mock('@/api/admin/usage', () => ({ adminUsageAPI: { listCleanupTasks } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

enableAutoUnmount(afterEach)

beforeEach(() => {
  vi.clearAllMocks()
  vi.spyOn(console, 'error').mockImplementation(() => {})
})

afterEach(() => {
  vi.restoreAllMocks()
})

const page = (number: number) => ({
  items: [{ id: number, status: 'succeeded', deleted_rows: number, filters: {} }],
  total: 15,
  page: number,
  page_size: 5
})

function deferredPage() {
  let resolve!: (value: ReturnType<typeof page>) => void
  let reject!: (error: Error) => void
  const promise = new Promise<ReturnType<typeof page>>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

async function openDialog() {
  listCleanupTasks.mockResolvedValueOnce(page(1))
  const wrapper = mount(UsageCleanupDialog, {
    props: { show: false, filters: {}, startDate: '2026-09-01', endDate: '2026-09-02' },
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' },
        UsageFilters: true,
        ConfirmDialog: true,
        Pagination: true
      }
    }
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('cleanup task pagination', () => {
  it.each(['success', 'failure'])('ignores an older page request that completes with %s', async outcome => {
    const wrapper = await openDialog()
    const older = deferredPage()
    const latest = deferredPage()
    listCleanupTasks.mockImplementationOnce(() => older.promise)
    wrapper.findComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()
    listCleanupTasks.mockImplementationOnce(() => latest.promise)
    wrapper.findComponent(Pagination).vm.$emit('update:page', 3)
    await flushPromises()

    latest.resolve(page(3))
    await flushPromises()

    if (outcome === 'success') older.resolve(page(2))
    else older.reject(new Error('obsolete failure'))
    await flushPromises()
    expect(wrapper.findComponent(Pagination).props('page')).toBe(3)
    expect(wrapper.text()).toContain('#3')
    expect(wrapper.text()).not.toContain('#2')
    expect(wrapper.text()).not.toContain('admin.usage.cleanup.loadingTasks')
    expect(showError).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('keeps loading while the newest request is still pending', async () => {
    const wrapper = await openDialog()
    const older = deferredPage()
    const latest = deferredPage()
    listCleanupTasks.mockImplementationOnce(() => older.promise)
    wrapper.findComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()
    listCleanupTasks.mockImplementationOnce(() => latest.promise)
    wrapper.findComponent(Pagination).vm.$emit('update:page', 3)
    await flushPromises()

    older.resolve(page(2))
    await flushPromises()
    expect(wrapper.text()).toContain('admin.usage.cleanup.loadingTasks')

    latest.resolve(page(3))
    await flushPromises()
    expect(wrapper.text()).not.toContain('admin.usage.cleanup.loadingTasks')
    expect(wrapper.text()).toContain('#3')
  })

  it('ignores a pending response after the dialog is closed and reopened', async () => {
    const wrapper = await openDialog()
    const obsolete = deferredPage()
    listCleanupTasks.mockImplementationOnce(() => obsolete.promise)
    wrapper.findComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()

    await wrapper.setProps({ show: false })
    listCleanupTasks.mockResolvedValueOnce(page(1))
    await wrapper.setProps({ show: true })
    await flushPromises()
    obsolete.resolve(page(2))
    await flushPromises()

    expect(wrapper.findComponent(Pagination).props('page')).toBe(1)
    expect(wrapper.text()).toContain('#1')
    expect(wrapper.text()).not.toContain('#2')
  })

  it('does not show an obsolete list error after close is clicked', async () => {
    const wrapper = await openDialog()
    const obsolete = deferredPage()
    listCleanupTasks.mockImplementationOnce(() => obsolete.promise)
    wrapper.findComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()

    await wrapper.get('button.btn-secondary').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
    obsolete.reject(new Error('obsolete failure'))
    await flushPromises()

    expect(showError).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('does not report a pending list failure after unmount', async () => {
    const wrapper = await openDialog()
    const obsolete = deferredPage()
    listCleanupTasks.mockImplementationOnce(() => obsolete.promise)
    wrapper.findComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()

    wrapper.unmount()
    obsolete.reject(new Error('obsolete failure'))
    await flushPromises()

    expect(showError).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })
})
