/**
 * API 线路（默认地址 + 管理员配置的自定义端点）的选项、归一化与「已选线路」持久化。
 *
 * 「接入密钥」弹窗和使用文档页共用同一个已选线路：localStorage 的 key 是 `docs_api_endpoint`，两处不能各用各的。
 * 注意：文档页（feat/user-docs-anthropic-style，views/docs/docsRender.ts）目前还有一份副本；
 * 等文档分支合并到本分支之上时，改为从这里引用，并删掉那份副本。语义必须保持一致。
 */
import type { CustomEndpoint } from '@/types'
import { sanitizeUrl } from '@/utils/url'

export interface ApiBases {
  /** API 根地址，不带 /v1 */
  base: string
  /** OpenAI 兼容地址，带 /v1 */
  v1: string
}

/** 去掉首尾空白、结尾的 / 和 /v1，得到 API 根地址。 */
export function normalizeApiBase(raw: string | undefined | null): string {
  return (raw || '').trim().replace(/\/+$/, '').replace(/\/v1$/, '')
}

/** 从公开设置的 api_base_url 推出两个常用地址。留空时退回当前站点的来源。 */
export function resolveApiBases(apiBaseUrl: string | undefined | null, fallbackOrigin: string): ApiBases {
  const base = normalizeApiBase((apiBaseUrl || '').trim() || fallbackOrigin)
  return { base, v1: `${base}/v1` }
}

/** 默认地址在 localStorage 和选项列表里的 id。自定义端点的 id 就是它归一化后的 API 根地址。 */
export const DEFAULT_ENDPOINT_ID = 'default'

/** 读者选中的地址存在这里；选回默认地址时直接删掉这一项。 */
export const ENDPOINT_STORAGE_KEY = 'docs_api_endpoint'

export interface EndpointOption extends ApiBases {
  id: string
  /** 管理员填的名称，原样显示。默认地址没有名称，由界面补「默认」。 */
  name: string
  /** 管理员填的说明，原样显示。 */
  description: string
  isDefault: boolean
}

/**
 * 默认地址加上管理员配置的自定义端点，每个都按 resolveApiBases 的规则去掉结尾的 / 和 /v1。
 * 不是 http(s) 绝对地址的端点、和前面某个地址重复的端点会被丢掉，免得出现两个选不出区别的选项。
 */
export function resolveEndpointOptions(
  apiBaseUrl: string | undefined | null,
  customEndpoints: ReadonlyArray<Partial<CustomEndpoint>> | undefined | null,
  fallbackOrigin: string
): EndpointOption[] {
  const options: EndpointOption[] = [
    { id: DEFAULT_ENDPOINT_ID, name: '', description: '', isDefault: true, ...resolveApiBases(apiBaseUrl, fallbackOrigin) },
  ]
  const seen = new Set([options[0].base])
  for (const item of customEndpoints ?? []) {
    const url = sanitizeUrl(item.endpoint ?? '')
    if (!url) continue
    const bases = resolveApiBases(url, fallbackOrigin)
    if (seen.has(bases.base)) continue
    seen.add(bases.base)
    options.push({
      id: bases.base,
      name: (item.name ?? '').trim() || new URL(url).host,
      description: (item.description ?? '').trim(),
      isDefault: false,
      ...bases,
    })
  }
  return options
}

/** 按保存的 id 选地址。保存的那个已经不在选项里（站点删掉了），就回到默认地址。 */
export function pickEndpoint(options: EndpointOption[], savedId: string): EndpointOption {
  return options.find((o) => o.id === savedId) ?? options[0]
}

export function loadSavedEndpointId(): string {
  try {
    return localStorage.getItem(ENDPOINT_STORAGE_KEY) || DEFAULT_ENDPOINT_ID
  } catch {
    return DEFAULT_ENDPOINT_ID
  }
}

export function saveEndpointId(id: string): void {
  try {
    if (id === DEFAULT_ENDPOINT_ID) localStorage.removeItem(ENDPOINT_STORAGE_KEY)
    else localStorage.setItem(ENDPOINT_STORAGE_KEY, id)
  } catch {
    // 隐私模式等写不进去的情况：这次访问内照常切换，只是下次不会记住
  }
}
