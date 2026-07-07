import { i18n } from '@/i18n'
import type { RouteLocationNormalizedLoaded } from 'vue-router'

/**
 * 统一生成页面标题，避免多处写入 document.title 产生覆盖冲突。
 * 优先使用 titleKey 通过 i18n 翻译，fallback 到静态 routeTitle。
 */
export function resolveDocumentTitle(routeTitle: unknown, siteName?: string, titleKey?: string): string {
  const normalizedSiteName = typeof siteName === 'string' && siteName.trim() ? siteName.trim() : 'Sub2API'

  if (typeof titleKey === 'string' && titleKey.trim()) {
    const translated = i18n.global.t(titleKey)
    if (translated && translated !== titleKey) {
      return `${translated} - ${normalizedSiteName}`
    }
  }

  if (typeof routeTitle === 'string' && routeTitle.trim()) {
    return `${routeTitle.trim()} - ${normalizedSiteName}`
  }

  return normalizedSiteName
}

type CustomMenuItemLike = {
  id?: string | number
  label?: string | null
}

export function resolveRouteDocumentTitle(
  route: Pick<RouteLocationNormalizedLoaded, 'name' | 'params' | 'meta'>,
  siteName?: string,
  customMenuItems: CustomMenuItemLike[] = []
): string {
  if (route.name === 'CustomPage') {
    const id = String(route.params?.id ?? '')
    const menuItem = customMenuItems.find((item) => String(item.id ?? '') === id)
    if (menuItem?.label?.trim()) {
      return resolveDocumentTitle(menuItem.label, siteName)
    }
  }

  return resolveDocumentTitle(route.meta?.title, siteName, route.meta?.titleKey as string | undefined)
}
