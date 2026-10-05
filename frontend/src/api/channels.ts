/**
 * User Channels API endpoints (non-admin)
 * 用户侧「可用渠道」聚合查询：渠道 + 用户可访问的分组 + 支持模型（含定价）。
 */

import { apiClient } from './client'
import type { BillingMode } from '@/constants/channel'

export interface UserAvailableGroup {
  id: number
  name: string
  platform: string
  /** 'standard' | 'subscription' — 订阅分组视觉加深，和 API 密钥页保持一致。 */
  subscription_type: string
  /** 分组默认倍率。用户专属倍率（若有）通过 /groups/rates 获取后在前端 join。 */
  rate_multiplier: number
  /** true = 专属分组（小范围授权）；false = 公开分组。 */
  is_exclusive: boolean
  /** 分组对外描述（如「0.25倍率，适合追求长上下文极致稳定」），价格页档位卡展示用。 */
  description?: string
}

export interface UserPricingInterval {
  min_tokens: number
  max_tokens: number | null
  tier_label?: string
  input_price: number | null
  output_price: number | null
  cache_write_price: number | null
  cache_read_price: number | null
  per_request_price: number | null
}

export interface UserSupportedModelPricing {
  billing_mode: BillingMode
  input_price: number | null
  output_price: number | null
  cache_write_price: number | null
  cache_read_price: number | null
  image_output_price: number | null
  per_request_price: number | null
  intervals: UserPricingInterval[]
}

export interface UserSupportedModel {
  name: string
  platform: string
  pricing: UserSupportedModelPricing | null
}

/**
 * 渠道下单个平台的子视图：用户可访问的分组 + 该平台支持的模型。
 * 后端把一个渠道按平台聚合成 sections，前端可以把渠道名作为 row-group
 * 一次渲染，后面按 sections 顺序用 rowspan 铺开。
 */
export interface UserChannelPlatformSection {
  platform: string
  groups: UserAvailableGroup[]
  supported_models: UserSupportedModel[]
}

export interface UserAvailableChannel {
  name: string
  description: string
  platforms: UserChannelPlatformSection[]
}

/** 列出当前用户可见的「可用渠道」（与 /groups/available 保持一致，返回平数组）。 */
export async function getAvailable(options?: { signal?: AbortSignal }): Promise<UserAvailableChannel[]> {
  const { data } = await apiClient.get<UserAvailableChannel[]>('/channels/available', {
    signal: options?.signal
  })
  return data
}

/** 价格页上一个分组的展示信息。 */
export interface UserPriceGroup {
  id: number
  name: string
  platform: string
  /** 'standard' | 'subscription' */
  subscription_type: string
  is_exclusive: boolean
  description?: string
}

/** 一组单价（USD）：token 计费是每 token，按次 / 按图是每次；null 表示这一项没有价格。 */
export interface UserPriceSet {
  input: number | null
  output: number | null
  cache_read: number | null
  cache_write: number | null
  image_output: number | null
  unit: number | null
}

/** 一档价格：token 计费按上下文长度分档，按次 / 按图按档位或分辨率分档。 */
export interface UserPriceTier {
  min_tokens: number
  max_tokens: number | null
  label?: string
  /** 乘倍率之前的单价。 */
  official: UserPriceSet
  /** 乘完倍率后的单价。 */
  prices: UserPriceSet
}

/** 一个模型在一个分组里的价格，倍率已在后端乘好。 */
export interface UserPriceEntry {
  group_id: number
  /** 实际乘上的倍率：用户专属倍率优先于分组倍率，v2 分组再含额外倍率。 */
  rate: number
  /** 分组默认倍率。 */
  base_rate: number
  /** rate 是否来自用户专属倍率。 */
  has_custom_rate: boolean
  billing_mode: BillingMode
  /** token：每百万 token 展示；request：每次展示。 */
  kind: 'token' | 'request'
  /** 乘倍率之前的单价（官方价对照）。 */
  official: UserPriceSet
  /** 乘完倍率后的单价。 */
  prices: UserPriceSet
  tiers: UserPriceTier[]
}

export interface UserPriceModel {
  name: string
  platform: string
  /** 只含有价格的分组；为空表示这个模型暂无价格。 */
  entries: UserPriceEntry[]
}

export interface UserPriceCatalog {
  groups: UserPriceGroup[]
  models: UserPriceModel[]
}

/**
 * 用户价格页的数据：模型 → 分组 → 价格。价格由后端按各分组的价格阶段取好、乘好倍率，
 * 不含渠道名，前端只做币种换算与排版。
 */
export async function getPrices(options?: { signal?: AbortSignal }): Promise<UserPriceCatalog> {
  const { data } = await apiClient.get<UserPriceCatalog>('/channels/prices', {
    signal: options?.signal
  })
  return data
}

export const userChannelsAPI = { getAvailable, getPrices }

export default userChannelsAPI
