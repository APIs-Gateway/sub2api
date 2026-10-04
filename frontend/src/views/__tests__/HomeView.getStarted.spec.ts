import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import HomeView from '../HomeView.vue'

// 首页的两个主按钮（首屏「立即开始」、页尾「免费注册」）：
// 未登录先注册、注册完回到密钥页；已登录直接进密钥页。

const { authState, publicSettings } = vi.hoisted(() => ({
  authState: { isAuthenticated: false, isAdmin: false },
  publicSettings: { value: null as null | { registration_enabled?: boolean } },
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
      locale: { value: 'en' },
    }),
  }
})

vi.mock('@/stores', () => ({
  useAuthStore: () => ({
    get isAuthenticated() {
      return authState.isAuthenticated
    },
    get isAdmin() {
      return authState.isAdmin
    },
    checkAuth: vi.fn(),
  }),
  useAppStore: () => ({
    get cachedPublicSettings() {
      return publicSettings.value
    },
    siteName: 'Sub2API',
    siteLogo: '',
    docUrl: '',
    homeContent: '',
    publicSettingsLoaded: true,
    fetchPublicSettings: vi.fn(),
  }),
}))

const mountHome = () =>
  mount(HomeView, {
    global: {
      stubs: {
        RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
        LocaleSwitcher: true,
        Icon: true,
        BrandMark: true,
      },
    },
  })

function ctaLinks(wrapper: ReturnType<typeof mountHome>) {
  const hero = wrapper.findAll('a').find((a) => a.text().includes('home.getStarted') || a.text().includes('home.goToDashboard'))
  const closing = wrapper
    .findAll('a')
    .filter((a) => a.text().includes('home.cta.button') || a.text().includes('home.goToDashboard'))
    .at(-1)
  return { hero: hero?.attributes('href'), closing: closing?.attributes('href') }
}

describe('HomeView 主按钮的去向', () => {
  beforeEach(() => {
    authState.isAuthenticated = false
    authState.isAdmin = false
    publicSettings.value = null
    localStorage.clear()
    document.documentElement.classList.remove('dark')
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn().mockReturnValue({ matches: false }),
    })
  })

  it('未登录：两个按钮都先去注册，注册完回到密钥页', () => {
    const wrapper = mountHome()

    expect(ctaLinks(wrapper)).toEqual({
      hero: '/register?redirect=/keys',
      closing: '/register?redirect=/keys',
    })
    wrapper.unmount()
  })

  it('已登录：两个按钮都直接进密钥页', () => {
    authState.isAuthenticated = true
    const wrapper = mountHome()

    expect(ctaLinks(wrapper)).toEqual({ hero: '/keys', closing: '/keys' })
    wrapper.unmount()
  })

  it('已登录的管理员同样进密钥页；右上角的控制台入口仍是管理后台', () => {
    authState.isAuthenticated = true
    authState.isAdmin = true
    const wrapper = mountHome()

    expect(ctaLinks(wrapper)).toEqual({ hero: '/keys', closing: '/keys' })
    expect(wrapper.find('a[href="/admin/dashboard"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('站点关闭注册时，首屏按钮改去登录页，登录后同样回到密钥页', () => {
    publicSettings.value = { registration_enabled: false }
    const wrapper = mountHome()

    expect(ctaLinks(wrapper).hero).toBe('/login?redirect=/keys')
    wrapper.unmount()
  })

  it('公开设置没加载出来时按开放注册处理', () => {
    publicSettings.value = null
    const wrapper = mountHome()

    expect(ctaLinks(wrapper).hero).toBe('/register?redirect=/keys')
    wrapper.unmount()
  })

  it('右上角的「登录」入口不变', () => {
    const wrapper = mountHome()

    expect(wrapper.find('a[href="/login"]').exists()).toBe(true)
    wrapper.unmount()
  })
})
