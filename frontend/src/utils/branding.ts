import { sanitizeUrl } from '@/utils/url'

// Shown only while public settings have not loaded. Deployments should set site_name.
export const DEFAULT_SITE_NAME = 'API Gateway'

export function updateFavicon(logoUrl: string): void {
  const sanitizedLogoUrl = sanitizeUrl(logoUrl, {
    allowRelative: true,
    allowDataUrl: true,
  })
  if (!sanitizedLogoUrl) {
    return
  }

  let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!link) {
    link = document.createElement('link')
    link.rel = 'icon'
    document.head.appendChild(link)
  }

  link.type = sanitizedLogoUrl.endsWith('.svg') ? 'image/svg+xml' : 'image/x-icon'
  link.href = sanitizedLogoUrl
}
