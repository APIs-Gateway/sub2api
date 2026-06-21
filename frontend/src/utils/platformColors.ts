/**
 * Centralized platform color definitions.
 *
 * All components that need platform-specific styling should import from here
 * instead of defining their own color mappings.
 */

export type Platform = 'anthropic' | 'openai' | 'antigravity' | 'gemini'

// Quiet Ledger：去彩虹。四个平台统一中性处理，仅靠 platformLabel() 文字区分；
// 黏土仅作克制点缀（强调条/折扣）。下列每个映射对所有平台返回同一中性值，
// 保留 Record 结构与公共 API 不变，调用方零改动。
const NEUTRAL = {
  // 发丝标签：透明底 + 灰描边 + 墨字
  badge: 'bg-transparent text-gray-700 border-gray-200 dark:text-gray-300 dark:border-dark-700',
  badgeLight: 'text-gray-700 dark:text-gray-300',
  border: 'border-gray-200 dark:border-dark-700',
  // 强调条：单一黏土（图表/强调是黏土的指定岗位），去渐变
  accentBar: 'bg-primary-500',
  // 价格/数据：墨色（机器之声由调用处的 font-mono 承载）
  text: 'text-gray-900 dark:text-white',
  // 勾选等图标：中性墨色
  icon: 'text-gray-600 dark:text-gray-300',
  // 按钮：扁平墨黑主按钮（与 .btn-primary 一致），绝不用平台色填充
  button: 'bg-gray-900 text-white hover:bg-gray-800 dark:bg-gray-100 dark:text-gray-900 dark:hover:bg-white',
  // 折扣：黏土点缀（克制的促销强调）
  discount: 'bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-400',
  // 头部"渐变"：收为扁平墨黑横幅（深底浅字结构不变，去彩虹）
  gradient: 'from-gray-900 to-gray-900 dark:from-dark-800 dark:to-dark-800',
  gradientText: 'text-gray-100',
  gradientSubtext: 'text-gray-400',
}

const BADGE: Record<Platform, string> = {
  anthropic: NEUTRAL.badge, openai: NEUTRAL.badge, antigravity: NEUTRAL.badge, gemini: NEUTRAL.badge,
}
const BADGE_DEFAULT = NEUTRAL.badge

const BADGE_LIGHT: Record<Platform, string> = {
  anthropic: NEUTRAL.badgeLight, openai: NEUTRAL.badgeLight, antigravity: NEUTRAL.badgeLight, gemini: NEUTRAL.badgeLight,
}

const BORDER: Record<Platform, string> = {
  anthropic: NEUTRAL.border, openai: NEUTRAL.border, antigravity: NEUTRAL.border, gemini: NEUTRAL.border,
}
const BORDER_DEFAULT = NEUTRAL.border

const ACCENT_BAR: Record<Platform, string> = {
  anthropic: NEUTRAL.accentBar, openai: NEUTRAL.accentBar, antigravity: NEUTRAL.accentBar, gemini: NEUTRAL.accentBar,
}
const ACCENT_BAR_DEFAULT = NEUTRAL.accentBar

const TEXT: Record<Platform, string> = {
  anthropic: NEUTRAL.text, openai: NEUTRAL.text, antigravity: NEUTRAL.text, gemini: NEUTRAL.text,
}
const TEXT_DEFAULT = NEUTRAL.text

const ICON: Record<Platform, string> = {
  anthropic: NEUTRAL.icon, openai: NEUTRAL.icon, antigravity: NEUTRAL.icon, gemini: NEUTRAL.icon,
}
const ICON_DEFAULT = NEUTRAL.icon

const BUTTON: Record<Platform, string> = {
  anthropic: NEUTRAL.button, openai: NEUTRAL.button, antigravity: NEUTRAL.button, gemini: NEUTRAL.button,
}
const BUTTON_DEFAULT = NEUTRAL.button

const DISCOUNT: Record<Platform, string> = {
  anthropic: NEUTRAL.discount, openai: NEUTRAL.discount, antigravity: NEUTRAL.discount, gemini: NEUTRAL.discount,
}
const DISCOUNT_DEFAULT = NEUTRAL.discount

const GRADIENT: Record<Platform, string> = {
  anthropic: NEUTRAL.gradient, openai: NEUTRAL.gradient, antigravity: NEUTRAL.gradient, gemini: NEUTRAL.gradient,
}
const GRADIENT_DEFAULT = NEUTRAL.gradient

const GRADIENT_TEXT: Record<Platform, string> = {
  anthropic: NEUTRAL.gradientText, openai: NEUTRAL.gradientText, antigravity: NEUTRAL.gradientText, gemini: NEUTRAL.gradientText,
}
const GRADIENT_TEXT_DEFAULT = NEUTRAL.gradientText

const GRADIENT_SUBTEXT: Record<Platform, string> = {
  anthropic: NEUTRAL.gradientSubtext, openai: NEUTRAL.gradientSubtext, antigravity: NEUTRAL.gradientSubtext, gemini: NEUTRAL.gradientSubtext,
}
const GRADIENT_SUBTEXT_DEFAULT = NEUTRAL.gradientSubtext

// ── Public API ──────────────────────────────────────────────────────

function isPlatform(p: string): p is Platform {
  return p === 'anthropic' || p === 'openai' || p === 'antigravity' || p === 'gemini'
}

export function platformBadgeClass(p: string): string {
  return isPlatform(p) ? BADGE[p] : BADGE_DEFAULT
}

export function platformBadgeLightClass(p: string): string {
  return isPlatform(p) ? BADGE_LIGHT[p] : BADGE_DEFAULT
}

export function platformBorderClass(p: string): string {
  return isPlatform(p) ? BORDER[p] : BORDER_DEFAULT
}

export function platformAccentBarClass(p: string): string {
  return isPlatform(p) ? ACCENT_BAR[p] : ACCENT_BAR_DEFAULT
}

export function platformTextClass(p: string): string {
  return isPlatform(p) ? TEXT[p] : TEXT_DEFAULT
}

export function platformIconClass(p: string): string {
  return isPlatform(p) ? ICON[p] : ICON_DEFAULT
}

export function platformButtonClass(p: string): string {
  return isPlatform(p) ? BUTTON[p] : BUTTON_DEFAULT
}

export function platformDiscountClass(p: string): string {
  return isPlatform(p) ? DISCOUNT[p] : DISCOUNT_DEFAULT
}

export function platformGradientClass(p: string): string {
  return isPlatform(p) ? GRADIENT[p] : GRADIENT_DEFAULT
}

export function platformGradientTextClass(p: string): string {
  return isPlatform(p) ? GRADIENT_TEXT[p] : GRADIENT_TEXT_DEFAULT
}

export function platformGradientSubtextClass(p: string): string {
  return isPlatform(p) ? GRADIENT_SUBTEXT[p] : GRADIENT_SUBTEXT_DEFAULT
}

export function platformLabel(p: string): string {
  switch (p) {
    case 'anthropic': return 'Anthropic'
    case 'openai': return 'OpenAI'
    case 'antigravity': return 'Antigravity'
    case 'gemini': return 'Gemini'
    default: return p || 'API'
  }
}
