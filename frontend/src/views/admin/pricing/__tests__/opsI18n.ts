import zhCN from '@/i18n/locales/zh-CN'

const lookup = (key: string) => key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], zhCN)

/** 测试环境的 vue-i18n 是 runtime 构建，不能现场编译消息：按 zh-CN 语言包取文案并替换 {占位符}。 */
export function translate(key: string, params: Record<string, unknown> = {}): string {
  const hit = lookup(key)
  if (typeof hit !== 'string') return key
  return hit.replace(/\{(\w+)\}/g, (_, name) => String(params[name] ?? `{${name}}`))
}

export const hasKey = (key: string) => typeof lookup(key) === 'string'
