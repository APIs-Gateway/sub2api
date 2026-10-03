import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import type { ListResponse } from '@/api/admin/channelMonitorTemplate'
import MonitorTemplateManagerDialog from '../MonitorTemplateManagerDialog.vue'

const mocks = vi.hoisted(() => ({
  list: vi.fn(), create: vi.fn(), update: vi.fn(), del: vi.fn(),
  showError: vi.fn(), showSuccess: vi.fn(),
}))
vi.mock('@/api/admin', () => ({ adminAPI: { channelMonitorTemplate: mocks } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => mocks }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
enableAutoUnmount(afterEach)

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
const response = (name: string): ListResponse => ({
  items: [{
    id: 17, name, provider: 'anthropic', api_mode: 'chat_completions',
    description: 'preserved description', extra_headers: { 'X-Monitor': 'kept' },
    body_override_mode: 'merge', body_override: { temperature: 0.2 },
    response_format: 'sse', created_at: '', updated_at: '', associated_monitors: 2,
  }],
})
function mountDialog(show = true) {
  return shallowMount(MonitorTemplateManagerDialog, {
    props: { show },
    global: {
      stubs: {
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
        ConfirmDialog: {
          props: ['show'], emits: ['confirm', 'cancel'],
          template: '<button v-if="show" data-test="confirm-delete" @click="$emit(\'confirm\')">confirm</button>',
        },
        MonitorTemplateApplyPickerDialog: {
          props: ['show'], emits: ['applied'],
          template: '<button v-if="show" data-test="applied" @click="$emit(\'applied\', 2)">applied</button>',
        },
      },
    },
  })
}
type Dialog = ReturnType<typeof mountDialog>
async function click(wrapper: Dialog, text: string) {
  const target = wrapper.findAll('button').find(button => button.text() === text)
  expect(target, `expected actual button ${text}`).toBeDefined()
  await target!.trigger('click')
}
async function reopen(wrapper: Dialog) {
  await wrapper.setProps({ show: false })
  await wrapper.setProps({ show: true })
  await flushPromises()
}
async function startMutation(wrapper: Dialog, action: 'create' | 'update' | 'delete') {
  if (action === 'delete') {
    await click(wrapper, 'common.delete')
    await wrapper.get('[data-test="confirm-delete"]').trigger('click')
  } else {
    await click(wrapper, action === 'create' ? 'admin.channelMonitor.template.createButton' : 'common.edit')
    await wrapper.get('input').setValue('saved-template')
    await click(wrapper, action === 'create' ? 'common.create' : 'common.update')
  }
  expect(mocks[action === 'delete' ? 'del' : action]).toHaveBeenCalledTimes(1)
}
beforeEach(() => {
  vi.resetAllMocks()
  mocks.list.mockResolvedValue(response('initial-template'))
  mocks.create.mockResolvedValue(response('created').items[0])
  mocks.update.mockResolvedValue(response('updated').items[0])
  mocks.del.mockResolvedValue(undefined)
})

describe('monitor template manager request ownership', () => {
  it.each(['resolve', 'reject'] as const)('ignores a previous dialog list that %ss', async outcome => {
    const old = deferred<ListResponse>()
    mocks.list.mockReturnValueOnce(old.promise).mockResolvedValueOnce(response('current-template'))
    const wrapper = mountDialog()
    expect(mocks.list).toHaveBeenCalledTimes(1)
    await reopen(wrapper)
    expect(mocks.list).toHaveBeenCalledTimes(2)
    if (outcome === 'resolve') old.resolve(response('old-template'))
    else old.reject(new Error('obsolete list error'))
    await flushPromises()
    expect(wrapper.text()).toContain('current-template')
    expect(wrapper.text()).not.toContain('old-template')
    expect(mocks.showError).not.toHaveBeenCalled()
  })
  it.each(['resolve', 'reject'] as const)('keeps current loading after an older list %ss', async outcome => {
    const old = deferred<ListResponse>()
    const current = deferred<ListResponse>()
    mocks.list.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const wrapper = mountDialog()
    await reopen(wrapper)
    if (outcome === 'resolve') old.resolve(response('old-template'))
    else old.reject(new Error('obsolete error'))
    await flushPromises()
    expect(wrapper.text()).toContain('common.loading')
    expect(mocks.showError).not.toHaveBeenCalled()
    current.resolve(response('current-template'))
    await flushPromises()
    expect(wrapper.text()).toContain('current-template')
    expect(wrapper.text()).not.toContain('common.loading')
  })
  it.each(['prop', 'button', 'unmount'] as const)('ignores a pending error after %s close', async close => {
    const request = deferred<ListResponse>()
    mocks.list.mockReturnValueOnce(request.promise)
    const wrapper = mountDialog()
    if (close === 'prop') await wrapper.setProps({ show: false })
    else if (close === 'button') await click(wrapper, 'common.close')
    else wrapper.unmount()
    request.reject(new Error('closed dialog'))
    await flushPromises()
    expect(mocks.showError).not.toHaveBeenCalled()
  })
  it('does not fetch while hidden and can open normally', async () => {
    const wrapper = mountDialog(false)
    expect(mocks.list).not.toHaveBeenCalled()
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(mocks.list).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('initial-template')
  })
  it('reports the current list error and releases loading', async () => {
    mocks.list.mockRejectedValueOnce(new Error('current list error'))
    const wrapper = mountDialog()
    await flushPromises()
    expect(mocks.showError).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).not.toContain('common.loading')
  })
  it.each(['resolve', 'reject'] as const)('keeps the newest apply refresh after a previous refresh %ss', async outcome => {
    const older = deferred<ListResponse>()
    const newer = deferred<ListResponse>()
    const wrapper = mountDialog()
    await flushPromises()
    await click(wrapper, 'admin.channelMonitor.template.applyButton')
    mocks.list.mockReturnValueOnce(older.promise).mockReturnValueOnce(newer.promise)
    await wrapper.get('[data-test="applied"]').trigger('click')
    await wrapper.get('[data-test="applied"]').trigger('click')
    expect(mocks.list).toHaveBeenCalledTimes(3)
    newer.resolve(response('newest-template'))
    await flushPromises()
    if (outcome === 'resolve') older.resolve(response('obsolete-template'))
    else older.reject(new Error('obsolete refresh error'))
    await flushPromises()
    expect(wrapper.emitted('updated')).toEqual([[]])
    expect(wrapper.text()).toContain('newest-template')
    expect(wrapper.text()).not.toContain('obsolete-template')
    expect(mocks.showError).not.toHaveBeenCalled()
  })
  it('does not emit an old apply refresh into a reopened dialog', async () => {
    const refresh = deferred<ListResponse>()
    const wrapper = mountDialog()
    await flushPromises()
    await click(wrapper, 'admin.channelMonitor.template.applyButton')
    mocks.list.mockReturnValueOnce(refresh.promise)
    await wrapper.get('[data-test="applied"]').trigger('click')
    await reopen(wrapper)
    refresh.resolve(response('obsolete-refresh'))
    await flushPromises()
    expect(wrapper.emitted('updated')).toBeUndefined()
    expect(wrapper.text()).toContain('initial-template')
    expect(wrapper.text()).not.toContain('obsolete-refresh')
  })
  it.each(['create', 'update', 'delete'] as const)('preserves normal %s and all monitor fields', async action => {
    const wrapper = mountDialog()
    await flushPromises()
    await startMutation(wrapper, action)
    await flushPromises()
    expect(mocks.list).toHaveBeenCalledTimes(2)
    expect(mocks.showSuccess).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('updated')).toEqual([[]])
    if (action === 'update') {
      expect(mocks.update).toHaveBeenCalledWith(17, expect.objectContaining({
        name: 'saved-template', api_mode: 'chat_completions',
        extra_headers: { 'X-Monitor': 'kept' }, body_override_mode: 'merge',
        body_override: { temperature: 0.2 }, response_format: 'sse',
      }))
    } else if (action === 'delete') expect(mocks.del).toHaveBeenCalledWith(17)
    else expect(mocks.create).toHaveBeenCalledWith(expect.objectContaining({
      name: 'saved-template', provider: 'anthropic', response_format: 'json',
    }))
  })
  it.each(['create', 'update', 'delete'] as const)('reports current %s failure without false updated', async action => {
    mocks[action === 'delete' ? 'del' : action].mockRejectedValueOnce(new Error('current mutation failure'))
    const wrapper = mountDialog()
    await flushPromises()
    await startMutation(wrapper, action)
    await flushPromises()
    expect(mocks.showError).toHaveBeenCalledTimes(1)
    expect(mocks.showSuccess).not.toHaveBeenCalled()
    expect(wrapper.emitted('updated')).toBeUndefined()
    if (action !== 'delete') expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeUndefined()
  })
  for (const action of ['create', 'update', 'delete'] as const) {
    it.each(['resolve', 'reject'] as const)(`ignores late ${action} %s after reopen and preserves a new pending save`, async outcome => {
      const old = deferred<unknown>()
      const current = deferred<unknown>()
      mocks[action === 'delete' ? 'del' : action].mockReturnValueOnce(old.promise)
      const wrapper = mountDialog()
      await flushPromises()
      await startMutation(wrapper, action)
      await reopen(wrapper)
      mocks.create.mockReturnValueOnce(current.promise)
      await click(wrapper, 'admin.channelMonitor.template.createButton')
      await wrapper.get('input').setValue('new-session-draft')
      await click(wrapper, 'common.create')
      expect(mocks.create).toHaveBeenCalledTimes(action === 'create' ? 2 : 1)
      if (outcome === 'resolve') old.resolve(undefined)
      else old.reject(new Error('obsolete mutation failure'))
      await flushPromises()
      expect(mocks.showSuccess).not.toHaveBeenCalled()
      expect(mocks.showError).not.toHaveBeenCalled()
      expect(wrapper.emitted('updated')).toBeUndefined()
      expect(mocks.list).toHaveBeenCalledTimes(2)
      expect(wrapper.get('input').element.value).toBe('new-session-draft')
      expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeDefined()
      current.resolve(response('current-save').items[0])
      await flushPromises()
      expect(mocks.showSuccess).toHaveBeenCalledTimes(1)
      expect(wrapper.emitted('updated')).toEqual([[]])
    })
  }
  it.each(['create', 'update', 'delete'] as const)('does not report late %s failure after unmount', async action => {
    const pending = deferred<unknown>()
    mocks[action === 'delete' ? 'del' : action].mockReturnValueOnce(pending.promise)
    const wrapper = mountDialog()
    await flushPromises()
    await startMutation(wrapper, action)
    wrapper.unmount()
    pending.reject(new Error('unmounted mutation'))
    await flushPromises()
    expect(mocks.showError).not.toHaveBeenCalled()
    expect(mocks.showSuccess).not.toHaveBeenCalled()
    expect(wrapper.emitted('updated')).toBeUndefined()
  })
})
