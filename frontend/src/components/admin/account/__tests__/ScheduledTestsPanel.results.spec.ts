import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import ScheduledTestsPanel from '../ScheduledTestsPanel.vue'

vi.mock('vue-i18n', async (original) => ({
  ...await original<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
enableAutoUnmount(afterEach)
const { api, showError, showSuccess } = vi.hoisted(() => ({
  api: { listByAccount: vi.fn(), listResults: vi.fn(), create: vi.fn(), update: vi.fn(), delete: vi.fn() },
  showError: vi.fn(), showSuccess: vi.fn(),
}))
vi.mock('@/api/admin', () => ({ adminAPI: { scheduledTests: api } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
const plan = (id: number) => ({ id, account_id: 1, model_id: `model-${id}`, cron_expression: '*/30 * * * *', max_results: 100, enabled: true, auto_recover: true })
const result = (id: number) => ({ id, latency_ms: id, status: 'success', started_at: '2026-10-03T00:00:00Z' })
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
type View = {
  handleCreate: () => Promise<void>; handleEdit: () => Promise<void>; handleDelete: () => Promise<void>
  handleToggleEnabled: (p: ReturnType<typeof plan>, enabled: boolean) => Promise<void>
  startEdit: (p: ReturnType<typeof plan>) => void; confirmDeletePlan: (p: ReturnType<typeof plan>) => void
  newPlan: { model_id: string; cron_expression: string }; plans: ReturnType<typeof plan>[]
  editingPlanId: number | null; showDeleteConfirm: boolean; showAddForm: boolean
  editForm: { model_id: string }
  results: ReturnType<typeof result>[]; loadingResults: boolean; expandedPlanId: number | null
}
function mountPanel(show = false) {
  return shallowMount(ScheduledTestsPanel, {
    props: { show, accountId: 1, modelOptions: [] },
    global: { stubs: { BaseDialog: { template: '<div><slot /></div>' } } },
  })
}
async function openPanel() {
  const w = mountPanel()
  await w.setProps({ show: true })
  await flushPromises()
  return w
}
const view = (w: ReturnType<typeof mountPanel>) => w.vm as unknown as View
const expand = async (w: ReturnType<typeof mountPanel>, index = 0) => { await w.findAll('div.cursor-pointer')[index].trigger('click') }
beforeEach(() => {
  vi.resetAllMocks()
  api.listByAccount.mockResolvedValue([plan(1), plan(2)])
  api.listResults.mockResolvedValue([result(22)])
  api.create.mockResolvedValue(plan(3))
  api.update.mockImplementation((id: number) => Promise.resolve(plan(id)))
  api.delete.mockResolvedValue(undefined)
})

describe('scheduled test reads belong to the current selection', () => {
  it.each(['resolve', 'reject'] as const)('ignores old %s while B still loads', async outcome => {
    const old = deferred<ReturnType<typeof result>[]>()
    const current = deferred<ReturnType<typeof result>[]>()
    api.listResults.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const w = await openPanel()
    await expand(w)
    await expand(w, 1)
    expect(api.listResults.mock.calls).toEqual([[1, 20], [2, 20]])
    if (outcome === 'resolve') old.resolve([result(11)])
    else old.reject(new Error('obsolete'))
    await flushPromises()
    expect(w.text()).toContain('common.loading')
    expect(w.text()).not.toContain('11ms')
    expect(showError).not.toHaveBeenCalled()
    current.resolve([result(22)])
    await flushPromises()
    expect(w.text()).toContain('22ms')
    expect(w.text()).not.toContain('common.loading')
  })

  it.each(['resolve', 'reject'] as const)('ignores old %s after B succeeds', async outcome => {
    const old = deferred<ReturnType<typeof result>[]>()
    api.listResults.mockReturnValueOnce(old.promise)
    const w = await openPanel()
    await expand(w)
    await expand(w, 1)
    await flushPromises()
    expect(w.text()).toContain('22ms')
    if (outcome === 'resolve') old.resolve([result(11)])
    else old.reject(new Error('obsolete'))
    await flushPromises()
    expect(w.text()).toContain('22ms')
    expect(w.text()).not.toContain('11ms')
    expect(showError).not.toHaveBeenCalled()
  })

  it('retains the current error after an old request finishes', async () => {
    const old = deferred<ReturnType<typeof result>[]>()
    api.listResults.mockReturnValueOnce(old.promise).mockRejectedValueOnce(new Error('current failure'))
    const w = await openPanel()
    await expand(w)
    await expand(w, 1)
    await flushPromises()
    old.resolve([result(11)])
    await flushPromises()
    expect(showError).toHaveBeenCalledTimes(1)
    expect(showError).toHaveBeenCalledWith('current failure')
    expect(w.text()).toContain('admin.scheduledTests.noResults')
    expect(w.text()).not.toContain('11ms')
  })

  it.each(['collapse', 'reopen', 'account'] as const)('invalidates same-plan request on %s', async action => {
    const old = deferred<ReturnType<typeof result>[]>()
    api.listResults.mockReturnValueOnce(old.promise)
    const w = await openPanel()
    await expand(w)
    if (action === 'collapse') await expand(w)
    else if (action === 'reopen') { await w.setProps({ show: false }); await w.setProps({ show: true }) }
    else await w.setProps({ accountId: 2 })
    await flushPromises()
    await expand(w)
    await flushPromises()
    old.resolve([result(11)])
    await flushPromises()
    expect(w.text()).toContain('22ms')
    expect(w.text()).not.toContain('11ms')
  })

  it.each(['close', 'null-account', 'unmount'] as const)('suppresses obsolete rejection after %s', async action => {
    const old = deferred<ReturnType<typeof result>[]>()
    api.listResults.mockReturnValueOnce(old.promise)
    const w = await openPanel()
    await expand(w)
    if (action === 'close') await w.setProps({ show: false })
    else if (action === 'null-account') await w.setProps({ accountId: null })
    else w.unmount()
    old.reject(new Error('obsolete'))
    await flushPromises()
    expect(showError).not.toHaveBeenCalled()
  })

  it.each(['resolve', 'reject'] as const)('ignores obsolete account plan-list %s', async outcome => {
    const old = deferred<ReturnType<typeof plan>[]>()
    api.listByAccount.mockReturnValueOnce(old.promise).mockResolvedValueOnce([plan(9)])
    const w = mountPanel()
    await w.setProps({ show: true })
    expect(api.listByAccount).toHaveBeenCalledTimes(1)
    expect(api.listByAccount).toHaveBeenCalledWith(1)
    await w.setProps({ accountId: 2 })
    await flushPromises()
    if (outcome === 'resolve') old.resolve([plan(1)])
    else old.reject(new Error('obsolete account'))
    await flushPromises()
    expect(w.text()).toContain('model-9')
    expect(w.text()).not.toContain('model-1')
    expect(showError).not.toHaveBeenCalled()
    expect(api.listByAccount).toHaveBeenLastCalledWith(2)
  })

  it('loads when initially open and reports current plan-list failure', async () => {
    api.listByAccount.mockRejectedValueOnce(new Error('current list failure'))
    const w = mountPanel(true)
    await flushPromises()
    expect(api.listByAccount).toHaveBeenCalledTimes(1)
    expect(api.listByAccount).toHaveBeenCalledWith(1)
    expect(showError).toHaveBeenCalledTimes(1)
    expect(showError).toHaveBeenCalledWith('current list failure')
    expect(w.text()).not.toContain('common.loading')
  })
})

describe('scheduled test mutations keep their account and plan identity', () => {
  const mutate = (w: ReturnType<typeof mountPanel>, operation: string) => {
    const vm = view(w)
    if (operation === 'create') {
      vm.newPlan.model_id = 'model-new'
      vm.newPlan.cron_expression = '*/30 * * * *'
      return vm.handleCreate()
    }
    if (operation === 'edit') { vm.startEdit(plan(1)); return vm.handleEdit() }
    if (operation === 'delete') { vm.confirmDeletePlan(plan(1)); return vm.handleDelete() }
    return vm.handleToggleEnabled(plan(1), false)
  }
  it.each(['edit', 'delete', 'toggle'])('%s invalidates prior selected results', async operation => {
    const old = deferred<ReturnType<typeof result>[]>()
    api.listResults.mockReturnValueOnce(old.promise)
    const w = await openPanel()
    await expand(w)
    await mutate(w, operation)
    old.resolve([result(11)])
    await flushPromises()
    expect(w.text()).not.toContain('11ms')
    expect(showSuccess).toHaveBeenCalledTimes(1)
    if (operation === 'delete') expect(view(w).plans.map(p => p.id)).toEqual([2])
  })

  it.each(['loaded', 'loading'] as const)('create preserves unrelated current plan B while its results are %s', async state => {
    const createPending = deferred<ReturnType<typeof plan>>()
    const oldResults = deferred<ReturnType<typeof result>[]>()
    const currentResults = deferred<ReturnType<typeof result>[]>()
    api.create.mockReturnValueOnce(createPending.promise)
    api.listResults.mockReturnValueOnce(oldResults.promise).mockReturnValueOnce(currentResults.promise)
    const w = await openPanel()
    await expand(w)
    const creating = mutate(w, 'create')
    expect(api.create).toHaveBeenCalledTimes(1)
    await expand(w, 1)
    if (state === 'loaded') currentResults.resolve([result(22)])
    await flushPromises()
    createPending.resolve(plan(3))
    await creating
    await flushPromises()
    expect(view(w).expandedPlanId).toBe(2)
    expect(view(w).loadingResults).toBe(state === 'loading')
    expect(view(w).results).toEqual(state === 'loaded' ? [result(22)] : [])
    expect(api.listResults.mock.calls).toEqual([[1, 20], [2, 20]])
    oldResults.resolve([result(11)])
    if (state === 'loading') currentResults.resolve([result(22)])
    await flushPromises()
    expect(w.text()).toContain('22ms')
    expect(w.text()).not.toContain('11ms')
    expect(view(w).expandedPlanId).toBe(2)
    expect(showSuccess).toHaveBeenCalledTimes(1)
  })

  for (const operation of ['create', 'edit', 'delete', 'toggle']) {
    it.each(['resolve', 'reject'] as const)(`${operation} ignores stale %s after switching account`, async outcome => {
      const pending = deferred<ReturnType<typeof plan> | undefined>()
      const method = operation === 'create' ? api.create : operation === 'delete' ? api.delete : api.update
      method.mockReturnValueOnce(pending.promise)
      const w = await openPanel()
      const request = mutate(w, operation)
      expect(method).toHaveBeenCalledTimes(1)
      api.listByAccount.mockResolvedValueOnce([plan(9)])
      await w.setProps({ accountId: 2 })
      await flushPromises()
      if (outcome === 'resolve') pending.resolve(plan(1))
      else pending.reject(new Error('old mutation'))
      await request
      await flushPromises()
      expect(view(w).plans.map(p => p.id)).toEqual([9])
      expect(showError).not.toHaveBeenCalled()
      expect(showSuccess).not.toHaveBeenCalled()
      expect(api.listByAccount).toHaveBeenCalledTimes(2)
    })
  }

  it('captures the edited plan without clearing a newer edit selection', async () => {
    const pending = deferred<ReturnType<typeof plan>>()
    api.update.mockReturnValueOnce(pending.promise)
    const w = await openPanel()
    const request = mutate(w, 'edit')
    view(w).startEdit(plan(2))
    pending.resolve({ ...plan(1), model_id: 'updated-one' })
    await request
    expect(view(w).plans.map(p => p.model_id)).toEqual(['updated-one', 'model-2'])
    expect(view(w).editingPlanId).toBe(2)
    expect(api.update).toHaveBeenCalledWith(1, expect.objectContaining({ model_id: 'model-1', auto_recover: true }))
  })

  it('captures the deleted plan without closing a newer confirmation', async () => {
    const pending = deferred<undefined>()
    api.delete.mockReturnValueOnce(pending.promise)
    const w = await openPanel()
    const request = mutate(w, 'delete')
    view(w).confirmDeletePlan(plan(2))
    pending.resolve(undefined)
    await request
    expect(view(w).plans.map(p => p.id)).toEqual([2])
    expect(view(w).showDeleteConfirm).toBe(true)
    expect(api.delete).toHaveBeenCalledTimes(1)
    expect(api.delete).toHaveBeenCalledWith(1)
  })

  it('keeps a new create draft and unrelated selected results after cancelling the pending create', async () => {
    const pending = deferred<ReturnType<typeof plan>>()
    api.create.mockReturnValueOnce(pending.promise)
    const w = await openPanel()
    const click = async (text: string) => { await w.findAll('button').find(b => b.text() === text)!.trigger('click') }
    await click('admin.scheduledTests.addPlan')
    view(w).newPlan.model_id = 'submitted-model'
    view(w).newPlan.cron_expression = '*/30 * * * *'
    await flushPromises()
    await click('common.save')
    expect(api.create).toHaveBeenCalledTimes(1)
    await click('common.cancel')
    await click('admin.scheduledTests.addPlan')
    view(w).newPlan.model_id = 'new-draft-model'
    view(w).newPlan.cron_expression = '0 * * * *'
    await expand(w, 1)
    await flushPromises()
    expect(w.text()).toContain('22ms')
    pending.resolve(plan(3))
    await flushPromises()
    expect(view(w).showAddForm).toBe(true)
    expect(view(w).newPlan.model_id).toBe('new-draft-model')
    expect(view(w).newPlan.cron_expression).toBe('0 * * * *')
    expect(w.text()).toContain('22ms')
    expect(showSuccess).toHaveBeenCalledTimes(1)
  })

  it('keeps a newly reopened edit of the same plan after an older save finishes', async () => {
    const pending = deferred<ReturnType<typeof plan>>()
    api.update.mockReturnValueOnce(pending.promise)
    const w = await openPanel()
    const edit = () => w.findAll('button[title="admin.scheduledTests.editPlan"]')[0].trigger('click')
    await edit()
    await w.findAll('button').find(b => b.text() === 'common.save')!.trigger('click')
    expect(api.update).toHaveBeenCalledTimes(1)
    await w.findAll('button').find(b => b.text() === 'common.cancel')!.trigger('click')
    await edit()
    view(w).editForm.model_id = 'new-edit-model'
    pending.resolve({ ...plan(1), model_id: 'submitted-old-edit' })
    await flushPromises()
    expect(view(w).editingPlanId).toBe(1)
    expect(view(w).editForm.model_id).toBe('new-edit-model')
    expect(w.findAll('button').some(b => b.text() === 'common.save')).toBe(true)
    expect(showSuccess).toHaveBeenCalledTimes(1)
  })
})
