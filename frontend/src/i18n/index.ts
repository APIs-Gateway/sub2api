import { createI18n } from 'vue-i18n'
import type { LocaleCode } from '@/types'
import { normalizeLocaleCode } from './localeUtils'

export { isChineseLocale, normalizeLocaleCode } from './localeUtils'

type LocaleMessages = Record<string, unknown>

export const LOCALE_KEY = 'sub2api_locale'
export const DEFAULT_LOCALE: LocaleCode = 'zh-CN'

const localeLoaders: Record<LocaleCode, () => Promise<{ default: LocaleMessages }>> = {
  'zh-CN': () => import('./locales/zh-CN'),
  'zh-HK': () => import('./locales/zh-HK'),
  en: () => import('./locales/en')
}

export interface InitialLocaleResolutionInput {
  storedLocale: string | null | undefined
  publicDefaultLocale: string | null | undefined
}

export interface InitialLocaleResolution {
  locale: LocaleCode
  persistLocale?: LocaleCode
  clearStoredLocale?: boolean
}

function isLocaleCode(value: string): value is LocaleCode {
  return value === 'zh-CN' || value === 'zh-HK' || value === 'en'
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

function mergeLocaleMessages(base: LocaleMessages, override: LocaleMessages): LocaleMessages {
  const merged: LocaleMessages = { ...base }

  for (const [key, value] of Object.entries(override)) {
    const baseValue = merged[key]
    merged[key] = isRecord(baseValue) && isRecord(value)
      ? mergeLocaleMessages(baseValue, value)
      : value
  }

  return merged
}

export function completeLocaleMessages(messages: LocaleMessages, fallbacks: LocaleMessages[]): LocaleMessages {
  const fallbackMessages = fallbacks.reduce<LocaleMessages>(
    (merged, fallback) => mergeLocaleMessages(merged, fallback),
    {}
  )

  return mergeLocaleMessages(fallbackMessages, messages)
}

export function resolveInitialLocale(input: InitialLocaleResolutionInput): InitialLocaleResolution {
  const storedRaw = input.storedLocale?.trim()
  const storedLocale = normalizeLocaleCode(storedRaw)
  if (storedLocale) {
    return {
      locale: storedLocale,
      persistLocale: storedRaw !== storedLocale ? storedLocale : undefined
    }
  }

  const publicLocale = normalizeLocaleCode(input.publicDefaultLocale)
  return {
    locale: publicLocale ?? DEFAULT_LOCALE,
    clearStoredLocale: !!storedRaw
  }
}

function readStoredLocale(): string | null {
  try {
    return localStorage.getItem(LOCALE_KEY)
  } catch {
    return null
  }
}

function applyStoredLocaleResolution(resolution: InitialLocaleResolution): void {
  try {
    if (resolution.persistLocale) {
      localStorage.setItem(LOCALE_KEY, resolution.persistLocale)
    } else if (resolution.clearStoredLocale) {
      localStorage.removeItem(LOCALE_KEY)
    }
  } catch {
    // Ignore storage failures in private browsing or locked-down environments.
  }
}

function getInjectedDefaultLocale(): string | null {
  if (typeof window === 'undefined') {
    return null
  }
  return window.__APP_CONFIG__?.default_locale ?? null
}

async function getPublicDefaultLocale(): Promise<string | null> {
  try {
    const { useAppStore } = await import('@/stores/app')
    return useAppStore().cachedPublicSettings?.default_locale ?? getInjectedDefaultLocale()
  } catch {
    return getInjectedDefaultLocale()
  }
}

function getBootstrapLocale(): LocaleCode {
  const resolution = resolveInitialLocale({
    storedLocale: readStoredLocale(),
    publicDefaultLocale: getInjectedDefaultLocale()
  })
  applyStoredLocaleResolution(resolution)
  return resolution.locale
}

export const i18n = createI18n({
  legacy: false,
  locale: getBootstrapLocale(),
  fallbackLocale: DEFAULT_LOCALE,
  messages: {},
  // Disable HTML message warnings. These strings are maintained by the app and
  // some onboarding copy intentionally contains trusted inline HTML.
  warnHtmlMessage: false
})

const loadedLocales = new Set<LocaleCode>()

export async function loadLocaleMessages(locale: LocaleCode): Promise<void> {
  if (loadedLocales.has(locale)) {
    return
  }

  const module = await localeLoaders[locale]()
  const fallbacks: LocaleMessages[] = []
  if (locale !== 'en') {
    fallbacks.push((await localeLoaders.en()).default)
  }
  if (locale !== DEFAULT_LOCALE) {
    fallbacks.push((await localeLoaders[DEFAULT_LOCALE]()).default)
  }

  i18n.global.setLocaleMessage(locale, completeLocaleMessages(module.default, fallbacks))
  loadedLocales.add(locale)
}

async function applyLocale(locale: LocaleCode): Promise<void> {
  await loadLocaleMessages(locale)
  i18n.global.locale.value = locale
  document.documentElement.setAttribute('lang', locale)
}

export async function initI18n(): Promise<void> {
  const resolution = resolveInitialLocale({
    storedLocale: readStoredLocale(),
    publicDefaultLocale: await getPublicDefaultLocale()
  })
  applyStoredLocaleResolution(resolution)
  await applyLocale(resolution.locale)
}

export async function setLocale(locale: string): Promise<void> {
  const normalizedLocale = normalizeLocaleCode(locale)
  if (!normalizedLocale) {
    return
  }

  await applyLocale(normalizedLocale)
  localStorage.setItem(LOCALE_KEY, normalizedLocale)

  // 同步更新浏览器页签标题，使其跟随语言切换
  const { resolveRouteDocumentTitle } = await import('@/router/title')
  const { default: router } = await import('@/router')
  const { useAppStore } = await import('@/stores/app')
  const { useAuthStore } = await import('@/stores/auth')
  const { useAdminSettingsStore } = await import('@/stores/adminSettings')
  const route = router.currentRoute.value
  const appStore = useAppStore()
  const authStore = useAuthStore()
  const adminSettingsStore = useAdminSettingsStore()
  const customMenuItems = [
    ...(appStore.cachedPublicSettings?.custom_menu_items ?? []),
    ...(authStore.isAdmin ? adminSettingsStore.customMenuItems : []),
  ]
  document.title = resolveRouteDocumentTitle(route, appStore.siteName, customMenuItems)
}

export function getLocale(): LocaleCode {
  const current = i18n.global.locale.value
  return typeof current === 'string' && isLocaleCode(current) ? current : DEFAULT_LOCALE
}

export const availableLocales = [
  { code: 'zh-CN', name: '简体中文', flag: '简' },
  { code: 'zh-HK', name: '繁體中文（香港）', flag: '繁' },
  { code: 'en', name: 'English', flag: 'EN' }
] as const

export default i18n
