import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import type { AdminGroup } from '@/types'
import GroupRPMOverridesModal from '../GroupRPMOverridesModal.vue'

const mocks = vi.hoisted(() => ({ getGroupRPMOverrides: vi.fn(), batchSetGroupRPMOverrides: vi.fn(), clearGroupRPMOverrides: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: mocks } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)

beforeEach(() => {
  vi.resetAllMocks()
  mocks.getGroupRPMOverrides.mockResolvedValue([
    { user_id: 7, user_email: 'user@example.com', user_name: '', user_status: 'active', rpm_override: 100 }
  ])
})

async function openEditor() {
  const wrapper = mount(GroupRPMOverridesModal, {
    props: { show: false, group: { id: 1, name: 'Group', platform: 'openai' } as AdminGroup },
    global: { stubs: {
      BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
      Icon: true, PlatformIcon: true, Pagination: true
    } }
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('existing RPM override edits', () => {
  it.each(['', '1.5', '-1'])('does not save invalid RPM %j', async (value) => {
    const wrapper = await openEditor()
    await wrapper.get('tbody input[type="number"]').setValue(value)
    expect(wrapper.findAll('button').some(button => button.text() === 'common.save')).toBe(false)
    expect(mocks.batchSetGroupRPMOverrides).not.toHaveBeenCalled()
  })

  it.each([['0', 0], ['200', 200], ['1e3', 1000]])('saves %s as %i', async (input, expected) => {
    const wrapper = await openEditor()
    await wrapper.get('tbody input[type="number"]').setValue(input)
    await wrapper.findAll('button').find(button => button.text() === 'common.save')!.trigger('click')
    await flushPromises()
    expect(mocks.batchSetGroupRPMOverrides).toHaveBeenCalledWith(1, [{ user_id: 7, rpm_override: expected }])
  })

  it.each(['', '1.5', '-1', 'Infinity'])('blocks a dirty earlier value while the visible input is invalid %j', async (value) => {
    const wrapper = await openEditor()
    const input = wrapper.get('tbody input[type="number"]')
    await input.setValue('200')
    await input.setValue(value)
    const save = wrapper.findAll('button').find(button => button.text() === 'common.save')!
    expect(input.attributes('aria-invalid')).toBe('true')
    expect(save.attributes('disabled')).toBeDefined()
    await save.trigger('click')
    await flushPromises()
    expect(mocks.batchSetGroupRPMOverrides).not.toHaveBeenCalled()
    await input.setValue('0')
    expect(input.attributes('aria-invalid')).toBe('false')
    expect(save.attributes('disabled')).toBeUndefined()
    await save.trigger('click')
    await flushPromises()
    expect(mocks.batchSetGroupRPMOverrides).toHaveBeenCalledWith(1, [{ user_id: 7, rpm_override: 0 }])
  })

  it('releases invalid input state when the edited row is removed', async () => {
    const wrapper = await openEditor()
    await wrapper.get('tbody input[type="number"]').setValue('200')
    await wrapper.get('tbody input[type="number"]').setValue('1.5')
    await wrapper.get('tbody button').trigger('click')
    const save = wrapper.findAll('button').find(button => button.text() === 'common.save')!
    expect(save.attributes('disabled')).toBeUndefined()
    await save.trigger('click')
    await flushPromises()
    expect(mocks.batchSetGroupRPMOverrides).toHaveBeenCalledWith(1, [])
  })

  it('reverts invalid drafts and restores the server value', async () => {
    const wrapper = await openEditor()
    const input = wrapper.get('tbody input[type="number"]')
    await input.setValue('200')
    await input.setValue('1.5')
    await wrapper.findAll('button').find(button => button.text() === 'admin.groups.revertChanges')!.trigger('click')
    expect((wrapper.get('tbody input').element as HTMLInputElement).value).toBe('100')
    expect(wrapper.get('tbody input').attributes('aria-invalid')).toBe('false')
    expect(wrapper.findAll('button').some(button => button.text() === 'common.save')).toBe(false)
  })

  it('does not carry invalid input state into a reopened editor', async () => {
    const wrapper = await openEditor()
    await wrapper.get('tbody input[type="number"]').setValue('200')
    await wrapper.get('tbody input[type="number"]').setValue('1.5')
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    const input = wrapper.get('tbody input[type="number"]')
    expect(input.attributes('aria-invalid')).toBe('false')
    expect((input.element as HTMLInputElement).value).toBe('100')
  })

  it('clears invalid drafts with all overrides and allows new dirty edits', async () => {
    const wrapper = await openEditor()
    await wrapper.get('tbody input[type="number"]').setValue('200')
    await wrapper.get('tbody input[type="number"]').setValue('1.5')
    await wrapper.findAll('button').find(button => button.text() === 'admin.groups.clearAll')!.trigger('click')
    await flushPromises()
    expect(mocks.clearGroupRPMOverrides).toHaveBeenCalledWith(1)
    expect(wrapper.find('tbody').exists()).toBe(false)
    expect(wrapper.findAll('button').some(button => button.text() === 'common.save')).toBe(false)
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.get('tbody input[type="number"]').setValue('200')
    const save = wrapper.findAll('button').find(button => button.text() === 'common.save')!
    expect(save.attributes('disabled')).toBeUndefined()
    await save.trigger('click')
    await flushPromises()
    expect(mocks.batchSetGroupRPMOverrides).toHaveBeenCalledWith(1, [{ user_id: 7, rpm_override: 200 }])
  })

})
