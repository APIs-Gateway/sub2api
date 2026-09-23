import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import LinuxDoOAuthSection from '@/components/auth/LinuxDoOAuthSection.vue'
import OidcOAuthSection from '@/components/auth/OidcOAuthSection.vue'

const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>,
}))

const locationState = vi.hoisted(() => ({
  current: { href: 'http://localhost/register' } as { href: string },
}))

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

describe('OAuth login sections promo code', () => {
  beforeEach(() => {
    routeState.query = { redirect: '/billing?plan=pro' }
    locationState.current = { href: 'http://localhost/register' }
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: locationState.current,
    })
    window.localStorage.clear()
    window.sessionStorage.clear()
  })

  it('passes a trimmed promo code to the LinuxDo OAuth start URL', async () => {
    const wrapper = mount(LinuxDoOAuthSection, {
      props: { promoCode: ' LINUX DO ' },
    })

    await wrapper.get('button').trigger('click')

    expect(locationState.current.href).toBe(
      '/api/v1/auth/oauth/linuxdo/start?redirect=%2Fbilling%3Fplan%3Dpro&promo_code=LINUX%20DO'
    )
  })

  it('does not add promo_code to the LinuxDo OAuth start URL when it is blank', async () => {
    const wrapper = mount(LinuxDoOAuthSection, {
      props: { promoCode: '   ' },
    })

    await wrapper.get('button').trigger('click')

    expect(locationState.current.href).toBe(
      '/api/v1/auth/oauth/linuxdo/start?redirect=%2Fbilling%3Fplan%3Dpro'
    )
  })

  it('passes a trimmed promo code to the OIDC OAuth start URL', async () => {
    const wrapper = mount(OidcOAuthSection, {
      props: { promoCode: ' OIDC-PROMO ' },
    })

    await wrapper.get('button').trigger('click')

    expect(locationState.current.href).toBe(
      '/api/v1/auth/oauth/oidc/start?redirect=%2Fbilling%3Fplan%3Dpro&promo_code=OIDC-PROMO'
    )
  })

  it('keeps the OIDC OAuth start URL unchanged without a promo code', async () => {
    const wrapper = mount(OidcOAuthSection)

    await wrapper.get('button').trigger('click')

    expect(locationState.current.href).toBe(
      '/api/v1/auth/oauth/oidc/start?redirect=%2Fbilling%3Fplan%3Dpro'
    )
  })
})
