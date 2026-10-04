package service

import (
	"context"
	"log/slog"
	"time"
)

// PricingSource 定价来源标识
const (
	PricingSourceChannel  = "channel"
	PricingSourceLiteLLM  = "litellm"
	PricingSourceFallback = "fallback"
)

// ResolvedPricing 统一定价解析结果
type ResolvedPricing struct {
	// Mode 计费模式
	Mode BillingMode

	// Token 模式：基础定价（来自 LiteLLM 或 fallback）
	BasePricing *ModelPricing

	// Token 模式：区间定价列表（如有，覆盖 BasePricing 中的对应字段）
	Intervals []PricingInterval

	// 按次/图片模式：分层定价
	RequestTiers []PricingInterval

	// 按次/图片模式：默认价格（未命中层级时使用）
	DefaultPerRequestPrice float64

	// 来源标识
	Source string // "channel", "litellm", "fallback"

	// 是否支持缓存细分
	SupportsCacheBreakdown bool

	// 渠道定价原始配置（用于区间模式下获取 ImageOutputPrice）
	channelPricing *ChannelModelPricing
}

// ModelPricingResolver 统一模型定价解析器。
// 解析链：Channel → LiteLLM → Fallback。
type ModelPricingResolver struct {
	channelService *ChannelService
	// 显式注入的分组策略；nil 时由 channelService 构造 legacyPolicy（见 group_policy.go）。
	policyOverride GroupPolicy
	billingService *BillingService
}

// NewModelPricingResolver 创建定价解析器实例
func NewModelPricingResolver(channelService *ChannelService, billingService *BillingService) *ModelPricingResolver {
	return &ModelPricingResolver{
		channelService: channelService,
		billingService: billingService,
	}
}

// PricingInput 定价解析输入
type PricingInput struct {
	Model   string
	GroupID *int64 // nil 表示不检查渠道
}

// Resolve 解析模型定价。
// 1. 获取基础定价（LiteLLM → Fallback）
// 2. 如果指定了 GroupID，查找渠道定价并覆盖
func (r *ModelPricingResolver) Resolve(ctx context.Context, input PricingInput) *ResolvedPricing {
	var chPricing *ChannelModelPricing
	if input.GroupID != nil {
		chPricing = r.priceOverride(ctx, *input.GroupID, input.Model)
		if chPricing != nil {
			mode := chPricing.BillingMode
			if mode == "" {
				mode = BillingModeToken
			}
			if mode == BillingModePerRequest || mode == BillingModeImage {
				resolved := &ResolvedPricing{
					Mode:           mode,
					Source:         PricingSourceChannel,
					channelPricing: chPricing,
				}
				r.applyRequestTierOverrides(chPricing, resolved)
				return resolved
			}
		}
	}

	// 1. 获取基础定价
	basePricing, source := r.resolveBasePricing(input.Model)

	resolved := &ResolvedPricing{
		Mode:                   BillingModeToken,
		BasePricing:            basePricing,
		Source:                 source,
		SupportsCacheBreakdown: basePricing != nil && basePricing.SupportsCacheBreakdown,
	}

	// 2. 如果有 GroupID，尝试渠道覆盖
	if chPricing != nil {
		resolved.Source = PricingSourceChannel
		resolved.channelPricing = chPricing
		r.applyTokenOverrides(chPricing, resolved)
	} else if input.GroupID != nil {
		r.applyChannelOverrides(ctx, *input.GroupID, input.Model, resolved)
	}

	return resolved
}

// resolveBasePricing 从 LiteLLM 或 Fallback 获取基础定价
func (r *ModelPricingResolver) resolveBasePricing(model string) (*ModelPricing, string) {
	pricing, err := r.billingService.GetModelPricing(model)
	if err != nil {
		slog.Debug("failed to get model pricing from LiteLLM, using fallback",
			"model", model, "error", err)
		return nil, PricingSourceFallback
	}
	return pricing, PricingSourceLiteLLM
}

// priceOverride 经 GroupPolicy 取分组对该模型的价格覆盖（字面名、codex 归一化名两步查找在策略内部，
// 见 legacyPolicy.PriceOverride）；没有策略（未配置渠道服务）时返回 nil。
func (r *ModelPricingResolver) priceOverride(ctx context.Context, groupID int64, model string) *ChannelModelPricing {
	gp := r.groupPolicy()
	if gp == nil {
		return nil
	}
	return gp.PriceOverride(ctx, groupID, model, time.Time{})
}

// applyChannelOverrides 应用渠道定价覆盖
func (r *ModelPricingResolver) applyChannelOverrides(ctx context.Context, groupID int64, model string, resolved *ResolvedPricing) {
	chPricing := r.priceOverride(ctx, groupID, model)
	if chPricing == nil {
		return
	}

	resolved.Source = PricingSourceChannel
	resolved.channelPricing = chPricing
	resolved.Mode = chPricing.BillingMode
	if resolved.Mode == "" {
		resolved.Mode = BillingModeToken
	}

	switch resolved.Mode {
	case BillingModeToken:
		r.applyTokenOverrides(chPricing, resolved)
	case BillingModePerRequest, BillingModeImage:
		r.applyRequestTierOverrides(chPricing, resolved)
	}
}

// applyTokenOverrides 应用 token 模式的渠道覆盖
func (r *ModelPricingResolver) applyTokenOverrides(chPricing *ChannelModelPricing, resolved *ResolvedPricing) {
	// The base can point at a shared dynamic-catalog or offline fallback entry.
	// Channel pricing is local to this resolution and must never leak to another
	// group or request.
	if resolved.BasePricing != nil {
		cloned := *resolved.BasePricing
		resolved.BasePricing = &cloned
	}

	// 过滤掉所有价格字段都为空的无效 interval
	validIntervals := filterValidIntervals(chPricing.Intervals)
	if resolved.BasePricing != nil {
		if len(validIntervals) > 0 {
			resolved.BasePricing.solFastLongContextFields = 0
		}
		if chPricing.InputPrice != nil {
			resolved.BasePricing.solFastLongContextFields &^= solFastLongInput
		}
		if chPricing.OutputPrice != nil {
			resolved.BasePricing.solFastLongContextFields &^= solFastLongOutput
		}
		if chPricing.CacheReadPrice != nil {
			resolved.BasePricing.solFastLongContextFields &^= solFastLongCacheRead
		}
		if chPricing.CacheWritePrice != nil {
			resolved.BasePricing.solFastLongContextFields &^= solFastLongCacheWrite
		}
	}

	// 如果有有效的区间定价，使用区间
	if len(validIntervals) > 0 {
		resolved.Intervals = validIntervals
		// 区间不匹配时回退到 BasePricing，也需要覆盖图片价格
		if resolved.BasePricing == nil {
			resolved.BasePricing = &ModelPricing{}
		} else {
			// 防止修改 fallbackPrices 中的共享指针
			cloned := *resolved.BasePricing
			resolved.BasePricing = &cloned
		}
		applyChannelImageOutputPrice(chPricing, resolved.BasePricing)
		return
	}

	// 否则用 flat 字段覆盖 BasePricing
	if resolved.BasePricing == nil {
		resolved.BasePricing = &ModelPricing{}
	} else {
		// 防止修改 fallbackPrices 中的共享指针
		cloned := *resolved.BasePricing
		resolved.BasePricing = &cloned
	}

	if chPricing.InputPrice != nil {
		resolved.BasePricing.InputPricePerToken = *chPricing.InputPrice
		resolved.BasePricing.InputPricePerTokenPriority = *chPricing.InputPrice
	}
	if chPricing.OutputPrice != nil {
		resolved.BasePricing.OutputPricePerToken = *chPricing.OutputPrice
		resolved.BasePricing.OutputPricePerTokenPriority = *chPricing.OutputPrice
	}
	if chPricing.CacheWritePrice != nil {
		resolved.BasePricing.CacheCreationPricePerToken = *chPricing.CacheWritePrice
		resolved.BasePricing.CacheCreationPricePerTokenPriority = *chPricing.CacheWritePrice
		resolved.BasePricing.CacheCreationPriceExplicit = true
		resolved.BasePricing.CacheCreation5mPrice = *chPricing.CacheWritePrice
		resolved.BasePricing.CacheCreation1hPrice = *chPricing.CacheWritePrice
	}
	if chPricing.CacheReadPrice != nil {
		resolved.BasePricing.CacheReadPricePerToken = *chPricing.CacheReadPrice
		resolved.BasePricing.CacheReadPricePerTokenPriority = *chPricing.CacheReadPrice
	}
	applyChannelImageOutputPrice(chPricing, resolved.BasePricing)
}

// applyChannelImageOutputPrice 只覆盖渠道显式填写的图片输出价。
// nil 保留目录价；显式 0 需要标记为免费，避免计费时回退文本输出价。
func applyChannelImageOutputPrice(chPricing *ChannelModelPricing, pricing *ModelPricing) {
	if chPricing == nil || chPricing.ImageOutputPrice == nil || pricing == nil {
		return
	}
	pricing.ImageOutputPricePerToken = *chPricing.ImageOutputPrice
	pricing.ImageOutputPriceExplicit = true
}

// applyRequestTierOverrides 应用按次/图片模式的渠道覆盖
func (r *ModelPricingResolver) applyRequestTierOverrides(chPricing *ChannelModelPricing, resolved *ResolvedPricing) {
	resolved.RequestTiers = filterValidIntervals(chPricing.Intervals)
	if chPricing.PerRequestPrice != nil {
		resolved.DefaultPerRequestPrice = *chPricing.PerRequestPrice
	}
}

// filterValidIntervals 过滤掉所有价格字段都为空的无效 interval。
// 前端可能创建了只有 min/max 但无价格的空 interval。
func filterValidIntervals(intervals []PricingInterval) []PricingInterval {
	var valid []PricingInterval
	for _, iv := range intervals {
		if iv.InputPrice != nil || iv.OutputPrice != nil ||
			iv.CacheWritePrice != nil || iv.CacheReadPrice != nil ||
			iv.PerRequestPrice != nil {
			valid = append(valid, iv)
		}
	}
	return valid
}

// GetIntervalPricing 根据 context token 数获取区间定价。
// 如果有区间列表，找到匹配区间并构造 ModelPricing；否则直接返回 BasePricing。
func (r *ModelPricingResolver) GetIntervalPricing(resolved *ResolvedPricing, totalContextTokens int) *ModelPricing {
	if len(resolved.Intervals) == 0 {
		return resolved.BasePricing
	}

	iv := FindMatchingInterval(resolved.Intervals, totalContextTokens)
	if iv == nil {
		return resolved.BasePricing
	}

	return intervalToModelPricing(iv, resolved.SupportsCacheBreakdown, resolved.BasePricing, resolved.channelPricing)
}

// intervalToModelPricing 将区间定价转换为 ModelPricing
func intervalToModelPricing(iv *PricingInterval, supportsCacheBreakdown bool, base *ModelPricing, chPricing *ChannelModelPricing) *ModelPricing {
	pricing := &ModelPricing{
		SupportsCacheBreakdown: supportsCacheBreakdown,
	}
	// 区间只配置文本与缓存价，图片价仍来自目录或渠道级配置。
	if base != nil {
		pricing.ImageInputPricePerToken = base.ImageInputPricePerToken
		pricing.ImageOutputPricePerToken = base.ImageOutputPricePerToken
		pricing.ImageOutputPriceExplicit = base.ImageOutputPriceExplicit
	}
	if iv.InputPrice != nil {
		pricing.InputPricePerToken = *iv.InputPrice
		pricing.InputPricePerTokenPriority = *iv.InputPrice
	}
	if iv.OutputPrice != nil {
		pricing.OutputPricePerToken = *iv.OutputPrice
		pricing.OutputPricePerTokenPriority = *iv.OutputPrice
	}
	if iv.CacheWritePrice != nil {
		pricing.CacheCreationPricePerToken = *iv.CacheWritePrice
		pricing.CacheCreationPricePerTokenPriority = *iv.CacheWritePrice
		pricing.CacheCreationPriceExplicit = true
		pricing.CacheCreation5mPrice = *iv.CacheWritePrice
		pricing.CacheCreation1hPrice = *iv.CacheWritePrice
	}
	if iv.CacheReadPrice != nil {
		pricing.CacheReadPricePerToken = *iv.CacheReadPrice
		pricing.CacheReadPricePerTokenPriority = *iv.CacheReadPrice
	}
	applyChannelImageOutputPrice(chPricing, pricing)
	return pricing
}

// GetRequestTierPrice 根据层级标签获取按次价格
func (r *ModelPricingResolver) GetRequestTierPrice(resolved *ResolvedPricing, tierLabel string) float64 {
	for _, tier := range resolved.RequestTiers {
		if tier.TierLabel == tierLabel && tier.PerRequestPrice != nil {
			return *tier.PerRequestPrice
		}
	}
	return 0
}

// GetRequestTierPriceByContext 根据 context token 数获取按次价格
func (r *ModelPricingResolver) GetRequestTierPriceByContext(resolved *ResolvedPricing, totalContextTokens int) float64 {
	iv := FindMatchingInterval(resolved.RequestTiers, totalContextTokens)
	if iv != nil && iv.PerRequestPrice != nil {
		return *iv.PerRequestPrice
	}
	return 0
}
