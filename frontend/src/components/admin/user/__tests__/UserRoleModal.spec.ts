import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'

const apiMocks = vi.hoisted(() => ({
  create: vi.fn().mockResolvedValue({}),
  update: vi.fn().mockResolvedValue({}),
  updateUserAttributeValues: vi.fn().mockResolvedValue({})
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: {
      create: apiMocks.create,
      update: apiMocks.update
    },
    userAttributes: {
      updateUserAttributeValues: apiMocks.updateUserAttributeValues
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn()
  })
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

import UserCreateModal from '../UserCreateModal.vue'
import UserEditModal from '../UserEditModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: Boolean },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const UserAttributeFormStub = defineComponent({
  name: 'UserAttributeForm',
  props: ['modelValue', 'userId'],
  emits: ['update:modelValue'],
  template: '<div />'
})

const commonStubs = {
  BaseDialog: BaseDialogStub,
  Icon: true,
  UserAttributeForm: UserAttributeFormStub
}

describe('user role modal payloads', () => {
  it('creates an admin when the role control is selected', async () => {
    const wrapper = mount(UserCreateModal, {
      props: { show: true },
      global: { stubs: commonStubs }
    })

    await wrapper.find('input[type="email"]').setValue('admin@example.com')
    await wrapper.find('input[type="text"]').setValue('password')
    await wrapper.find('select').setValue('admin')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(apiMocks.create).toHaveBeenCalledWith(expect.objectContaining({
      email: 'admin@example.com',
      password: 'password',
      role: 'admin'
    }))
  })

  it('includes the edited user role in the update payload', async () => {
    const wrapper = mount(UserEditModal, {
      props: {
        show: true,
        user: {
          id: 7,
          email: 'user@example.com',
          username: '',
          notes: '',
          role: 'admin',
          concurrency: 1,
          rpm_limit: 0
        } as any
      },
      global: { stubs: commonStubs }
    })

    await wrapper.find('select').setValue('user')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(apiMocks.update).toHaveBeenCalledWith(7, expect.objectContaining({ role: 'user' }))
  })
})
