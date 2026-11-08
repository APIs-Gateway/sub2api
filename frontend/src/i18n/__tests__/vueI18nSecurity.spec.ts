import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { LocaleCode } from '@/types'

let application: typeof import('../index') | undefined

async function applicationRuntime() {
  application = await import('../index')
  return application
}

// Keep the actual vue-i18n module, application loaders and title translator.
// Only router/store inputs are isolated; no translator or locale is mocked.
function isolateRuntimeInputs() {
  vi.doMock('@/stores/app', () => ({
    useAppStore: () => ({ cachedPublicSettings: { default_locale: 'en' }, siteName: 'Fixture Site' })
  }))
  vi.doMock('@/stores/auth', () => ({ useAuthStore: () => ({ isAdmin: false }) }))
  vi.doMock('@/stores/adminSettings', () => ({ useAdminSettingsStore: () => ({ customMenuItems: [] }) }))
  vi.doMock('@/router', () => ({
    default: {
      currentRoute: {
        value: { name: 'Dashboard', params: {}, meta: { title: 'Static fallback', titleKey: 'nav.dashboard' } }
      }
    }
  }))
}

describe('Vue I18n security migration contracts', () => {
  beforeEach(() => {
    vi.resetModules()
    localStorage.clear()
    delete window.__APP_CONFIG__
    document.documentElement.removeAttribute('lang')
    document.title = ''
    isolateRuntimeInputs()
  })

  afterEach(() => {
    application?.i18n.dispose()
    application = undefined
    vi.doUnmock('@/stores/app')
    vi.doUnmock('@/stores/auth')
    vi.doUnmock('@/stores/adminSettings')
    vi.doUnmock('@/router')
    localStorage.clear()
    delete window.__APP_CONFIG__
  })

  it.each([
    { locale: 'zh-CN' as LocaleCode, confirmation: '确认', elapsed: '7天前' },
    { locale: 'zh-HK' as LocaleCode, confirmation: '確認', elapsed: '7天前' },
    { locale: 'en' as LocaleCode, confirmation: 'Confirm', elapsed: '7d ago' }
  ])('lazily renders $locale literal and named values', async ({ locale, confirmation, elapsed }) => {
    const { i18n, loadLocaleMessages } = await applicationRuntime()
    const install = vi.spyOn(i18n.global, 'setLocaleMessage')
    await loadLocaleMessages(locale)
    await loadLocaleMessages(locale)
    expect(install).toHaveBeenCalledTimes(1)
    i18n.global.locale.value = locale
    expect(i18n.global.t('common.confirm')).toBe(confirmation)
    expect(i18n.global.t('common.time.daysAgo', { n: 7 })).toBe(elapsed)
  })

  it('renders the actual global and component composer after a locale switch', async () => {
    const { i18n, loadLocaleMessages } = await applicationRuntime()
    const { useI18n } = await import('vue-i18n')
    const { defineComponent, h, nextTick } = await import('vue')
    const { mount } = await import('@vue/test-utils')
    await loadLocaleMessages('en')
    await loadLocaleMessages('zh-HK')
    i18n.global.locale.value = 'en'
    const component = defineComponent({
      setup() {
        const { t } = useI18n({ useScope: 'global' })
        return () => h('p', t('common.time.daysAgo', { n: 3 }))
      }
    })
    const wrapper = mount(component, { global: { plugins: [i18n] } })
    try {
      expect(wrapper.text()).toBe('3d ago')
      expect(i18n.global.t('common.confirm')).toBe('Confirm')
      i18n.global.locale.value = 'zh-HK'
      await nextTick()
      expect(wrapper.text()).toBe('3天前')
      expect(i18n.global.t('common.confirm')).toBe('確認')
    } finally {
      wrapper.unmount()
    }
  })

  it('renders preferred messages and ordered nested fallback completion', async () => {
    const { completeLocaleMessages, i18n } = await applicationRuntime()
    const messages = completeLocaleMessages(
      { fixture: { own: 'preferred {name}' } },
      [
        { fixture: { own: 'wrong first', clash: 'first fallback', firstOnly: 'first {n}' } },
        { fixture: { own: 'wrong second', clash: 'second fallback', secondOnly: 'second {n}' } }
      ]
    )
    i18n.global.setLocaleMessage('en', messages)
    i18n.global.locale.value = 'en'
    expect(i18n.global.t('fixture.own', { name: 'Alice' })).toBe('preferred Alice')
    expect(i18n.global.t('fixture.clash')).toBe('second fallback')
    expect(i18n.global.t('fixture.firstOnly', { n: 2 })).toBe('first 2')
    expect(i18n.global.t('fixture.secondOnly', { n: 4 })).toBe('second 4')
  })

  it('migrates the stored zh preference and renders the selected Chinese locale', async () => {
    localStorage.setItem('sub2api_locale', 'zh')
    const { initI18n, i18n, LOCALE_KEY } = await applicationRuntime()
    await initI18n()
    expect(i18n.global.locale.value).toBe('zh-CN')
    expect(localStorage.getItem(LOCALE_KEY)).toBe('zh-CN')
    expect(document.documentElement.lang).toBe('zh-CN')
    expect(i18n.global.t('common.confirm')).toBe('确认')
  })

  it('persists explicit locales and refreshes the real translated document title', async () => {
    const { i18n, LOCALE_KEY, setLocale } = await applicationRuntime()
    await setLocale('zh-HK')
    expect(localStorage.getItem(LOCALE_KEY)).toBe('zh-HK')
    expect(document.documentElement.lang).toBe('zh-HK')
    expect(document.title).toBe('儀表盤 - Fixture Site')
    expect(i18n.global.t('common.confirm')).toBe('確認')
    await setLocale('en')
    expect(localStorage.getItem(LOCALE_KEY)).toBe('en')
    expect(document.documentElement.lang).toBe('en')
    expect(document.title).toBe('Dashboard - Fixture Site')
  })

  it('compiles fresh runtime interpolation when dynamic Function construction is forbidden', async () => {
    const { i18n } = await applicationRuntime()
    i18n.global.setLocaleMessage('en', { securityCsp: { fresh: 'Hello {name}, count {count}' } })
    i18n.global.locale.value = 'en'
    const targets = [...new Set<object>([globalThis, window])]
    const original = targets.map(target => Object.getOwnPropertyDescriptor(target, 'Function'))
    let blocked = 0
    function denyDynamicFunction() {
      blocked++
      throw new Error('fixture CSP forbids dynamic Function construction')
    }
    let controlRejected = false
    let translated = ''
    try {
      for (const target of targets) {
        Object.defineProperty(target, 'Function', { configurable: true, writable: true, value: denyDynamicFunction })
      }
      try {
        Reflect.construct(globalThis.Function, ['return 1'])
      } catch (error) {
        controlRejected = error instanceof Error && error.message === 'fixture CSP forbids dynamic Function construction'
      }
      translated = i18n.global.t('securityCsp.fresh', { name: 'Alice', count: 7 })
    } finally {
      targets.forEach((target, index) => {
        const descriptor = original[index]
        if (descriptor) Object.defineProperty(target, 'Function', descriptor)
        else Reflect.deleteProperty(target, 'Function')
      })
    }
    expect(controlRejected).toBe(true)
    expect(blocked).toBe(1)
    expect(translated).toBe('Hello Alice, count 7')
  })

  it.each([
    { source: 'literal', message: '<iframe srcdoc="<b>harmless</b>" title="frame"></iframe>' },
    { source: 'named', message: '<iframe srcdoc="{content}" title="frame"></iframe>' }
  ])('neutralizes the active srcdoc attribute in $source message HTML', async ({ message }) => {
    const { createI18n } = await import('vue-i18n')
    // A package-level harmless regression, not a claim that the application
    // enables escapeParameter or accepts untrusted translated HTML.
    const instance = createI18n({
      legacy: false,
      locale: 'en',
      escapeParameter: true,
      warnHtmlMessage: false,
      messages: { en: { fixture: message } }
    })
    try {
      const rendered = instance.global.t('fixture', { content: '<b>harmless</b>' })
      const parsed = new DOMParser().parseFromString(rendered, 'text/html')
      const iframe = parsed.querySelector('iframe')
      expect(iframe).not.toBeNull()
      expect(iframe?.getAttribute('title')).toBe('frame')
      expect(iframe?.hasAttribute('srcdoc')).toBe(false)
    } finally {
      instance.dispose()
    }
  })
})
