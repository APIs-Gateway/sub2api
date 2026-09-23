import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'

const mocks = vi.hoisted(() => ({
  update: vi.fn(),
  updateUserAttributeValues: vi.fn().mockResolvedValue({}),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: { update: mocks.update },
    userAttributes: { updateUserAttributeValues: mocks.updateUserAttributeValues }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: mocks.showError, showSuccess: mocks.showSuccess })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import UserEditModal from '../UserEditModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: Boolean },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const SelectStub = defineComponent({
  name: 'Select',
  props: ['modelValue', 'options', 'searchable'],
  emits: ['update:modelValue'],
  template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option></select>'
})

const stubs = {
  BaseDialog: BaseDialogStub,
  Select: SelectStub,
  Icon: true,
  UserAttributeForm: defineComponent({ name: 'UserAttributeForm', template: '<div />' })
}

const mountAdmin = () =>
  mount(UserEditModal, {
    props: {
      show: true,
      user: { id: 7, email: 'admin@example.com', username: '', notes: '', role: 'admin', concurrency: 1, rpm_limit: 0 } as any
    },
    global: { stubs }
  })

const demote = async () => {
  const wrapper = mountAdmin()
  await wrapper.find('select').setValue('user')
  await wrapper.find('form').trigger('submit')
  await flushPromises()
  return wrapper
}

describe('UserEditModal update errors', () => {
  beforeEach(() => {
    mocks.update.mockReset()
    mocks.showError.mockReset()
    mocks.showSuccess.mockReset()
  })

  it('shows the localized message when demoting the last admin is rejected', async () => {
    mocks.update.mockRejectedValue({
      status: 409,
      code: 409,
      reason: 'LAST_ADMIN_DEMOTE_FORBIDDEN',
      message: 'cannot demote the last admin user'
    })

    const wrapper = await demote()

    expect(mocks.showError).toHaveBeenCalledWith('admin.users.lastAdminDemoteForbidden')
    expect(mocks.showSuccess).not.toHaveBeenCalled()
    expect(wrapper.emitted('success')).toBeUndefined()
  })

  it('shows the localized message when an admin tries to demote themselves', async () => {
    mocks.update.mockRejectedValue({
      status: 400,
      code: 400,
      reason: 'CANNOT_DEMOTE_SELF',
      message: 'cannot demote yourself from admin'
    })

    await demote()

    expect(mocks.showError).toHaveBeenCalledWith('admin.users.cannotDemoteSelf')
  })

  it('falls back to the server message for unmapped errors', async () => {
    mocks.update.mockRejectedValue({ status: 500, code: 500, message: 'boom' })

    await demote()

    expect(mocks.showError).toHaveBeenCalledWith('boom')
  })

  it('falls back to the generic message when the error carries no message', async () => {
    mocks.update.mockRejectedValue({})

    await demote()

    expect(mocks.showError).toHaveBeenCalledWith('admin.users.failedToUpdate')
  })
})
