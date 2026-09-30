import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import AuthLayout from '../AuthLayout.vue'

const appStore = vi.hoisted(() => ({
  siteName: '',
  siteLogo: '',
  cachedPublicSettings: null,
  publicSettingsLoaded: true,
  fetchPublicSettings: vi.fn()
}))

vi.mock('@/stores', () => ({
  useAppStore: () => appStore
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

describe('AuthLayout branding fallback', () => {
  it('renders the neutral site name until public settings provide one', () => {
    const wrapper = mount(AuthLayout, {
      global: {
        stubs: {
          BrandMark: true
        }
      }
    })

    expect(wrapper.find('h1').text()).toBe('API Gateway')
    expect(wrapper.text()).toContain('API Gateway. All rights reserved.')
  })
})
