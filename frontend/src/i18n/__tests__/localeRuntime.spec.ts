import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

type AppStoreMock = {
  cachedPublicSettings: { default_locale?: string } | null
  siteName: string
}

function mockRuntimeModules(store: AppStoreMock) {
  const resolveDocumentTitle = vi.fn(() => 'Resolved page title')

  vi.doMock('@/stores/app', () => ({
    useAppStore: () => store
  }))
  vi.doMock('@/router/title', () => ({
    resolveDocumentTitle
  }))
  vi.doMock('@/router', () => ({
    default: {
      currentRoute: {
        value: {
          meta: {
            title: 'Fallback title',
            titleKey: 'nav.dashboard'
          }
        }
      }
    }
  }))

  return { resolveDocumentTitle }
}

describe('i18n runtime behavior', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.clearAllMocks()
    localStorage.clear()
    delete (window as any).__APP_CONFIG__
    document.documentElement.removeAttribute('lang')
    document.title = ''
  })

  afterEach(() => {
    vi.doUnmock('@/stores/app')
    vi.doUnmock('@/router/title')
    vi.doUnmock('@/router')
    localStorage.clear()
    delete (window as any).__APP_CONFIG__
  })

  it('bootstraps from injected default locale when no local preference exists', async () => {
    (window as any).__APP_CONFIG__ = { default_locale: 'zh-HK' }
    mockRuntimeModules({ cachedPublicSettings: null, siteName: 'Sub2API' })

    const { i18n } = await import('../index')

    expect(i18n.global.locale.value).toBe('zh-HK')
  })

  it('migrates a legacy stored zh locale during bootstrap', async () => {
    mockRuntimeModules({ cachedPublicSettings: { default_locale: 'zh-HK' }, siteName: 'Sub2API' })
    localStorage.setItem('sub2api_locale', 'zh')

    const { LOCALE_KEY, i18n } = await import('../index')

    expect(i18n.global.locale.value).toBe('zh-CN')
    expect(localStorage.getItem(LOCALE_KEY)).toBe('zh-CN')
  })

  it('loads all locale message variants with the expected fallback completion', async () => {
    mockRuntimeModules({ cachedPublicSettings: null, siteName: 'Sub2API' })
    const { i18n, loadLocaleMessages } = await import('../index')
    const setLocaleMessage = vi.spyOn(i18n.global, 'setLocaleMessage')

    await loadLocaleMessages('zh-HK')
    await loadLocaleMessages('en')
    await loadLocaleMessages('zh-CN')
    await loadLocaleMessages('zh-HK')

    expect(setLocaleMessage).toHaveBeenCalledTimes(3)
    expect(i18n.global.getLocaleMessage('zh-HK')).toHaveProperty('common')
    expect(i18n.global.getLocaleMessage('en')).toHaveProperty('common')
    expect(i18n.global.getLocaleMessage('zh-CN')).toHaveProperty('common')
  })

  it('initializes from public settings and clears invalid stored locales', async () => {
    mockRuntimeModules({ cachedPublicSettings: { default_locale: 'en' }, siteName: 'Sub2API' })
    localStorage.setItem('sub2api_locale', 'fr')

    const { LOCALE_KEY, i18n, initI18n } = await import('../index')
    await initI18n()

    expect(i18n.global.locale.value).toBe('en')
    expect(document.documentElement.getAttribute('lang')).toBe('en')
    expect(localStorage.getItem(LOCALE_KEY)).toBeNull()
  })

  it('falls back to injected config when loading the app store fails', async () => {
    (window as any).__APP_CONFIG__ = { default_locale: 'zh-HK' }
    vi.doMock('@/stores/app', () => {
      throw new Error('store unavailable')
    })

    const { i18n, initI18n } = await import('../index')
    await initI18n()

    expect(i18n.global.locale.value).toBe('zh-HK')
  })

  it('sets explicit locales, persists them, and refreshes the document title', async () => {
    const { resolveDocumentTitle } = mockRuntimeModules({
      cachedPublicSettings: null,
      siteName: 'Demo Site'
    })
    const { LOCALE_KEY, getLocale, i18n, setLocale } = await import('../index')

    await setLocale('not-a-locale')
    expect(localStorage.getItem(LOCALE_KEY)).toBeNull()

    await setLocale('zh-HK')

    expect(i18n.global.locale.value).toBe('zh-HK')
    expect(document.documentElement.getAttribute('lang')).toBe('zh-HK')
    expect(localStorage.getItem(LOCALE_KEY)).toBe('zh-HK')
    expect(document.title).toBe('Resolved page title')
    expect(resolveDocumentTitle).toHaveBeenCalledWith('Fallback title', 'Demo Site', 'nav.dashboard')

    i18n.global.locale.value = 'unsupported-locale'
    expect(getLocale()).toBe('zh-CN')
  })
})
