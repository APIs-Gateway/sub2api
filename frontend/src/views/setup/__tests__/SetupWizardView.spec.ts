import { nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

vi.mock('@/api/setup', () => ({
  testDatabase: vi.fn(),
  testRedis: vi.fn(),
  install: vi.fn()
}))

import SetupWizardView from '../SetupWizardView.vue'

describe('SetupWizardView Redis username', () => {
  it('renders and updates the Redis username field', async () => {
    const wrapper = mount(SetupWizardView, {
      global: {
        stubs: {
          Icon: { template: '<span />' },
          Select: { template: '<select />' },
          Toggle: { template: '<input type="checkbox" />' }
        }
      }
    })

    const testDatabaseButton = wrapper.find('button.btn-secondary')
    await testDatabaseButton.trigger('click')
    await nextTick()

    const nextButton = wrapper.find('button.btn-primary')
    await nextButton.trigger('click')
    await nextTick()

    const username = wrapper.find('input[placeholder="setup.redis.usernamePlaceholder"]')
    expect(username.exists()).toBe(true)

    await username.setValue('app-user')
    expect((username.element as HTMLInputElement).value).toBe('app-user')

    wrapper.unmount()
  })
})
