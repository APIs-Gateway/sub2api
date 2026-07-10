package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// AvailableGroupRef 渠道视图中关联分组的简要信息。
//
// 用户侧「可用渠道」页面据此展示：专属分组 vs 公开分组（IsExclusive）、
// 订阅 vs 标准（SubscriptionType）、默认倍率（RateMultiplier）。用户专属倍率
// 不在这里暴露，前端自己通过 /groups/rates 拉取，和 API 密钥页面保持一致。
type AvailableGroupRef struct {
	ID               int64
	Name             string
	Platform         string
	SubscriptionType string
	RateMultiplier   float64
	IsExclusive      bool
	Description      string
}

// AvailableChannel 可用渠道视图：用于「可用渠道」页面展示渠道基础信息 +
// 关联的分组 + 推导出的支持模型列表（无通配符）。
type AvailableChannel struct {
	ID                 int64
	Name               string
	Description        string
	Status             string
	BillingModelSource string
	RestrictModels     bool
	Groups             []AvailableGroupRef
	SupportedModels    []SupportedModel
}

// ListAvailable 返回所有渠道的可用视图：每个渠道附带关联分组信息与支持模型列表。
//
// 支持模型通过 (*Channel).SupportedModels() 计算（mapping ∪ pricing 并联）。
// 对于渠道未配置定价的模型，进一步用 PricingService 的全局 LiteLLM 数据合成
// 一份展示用定价，让用户看到默认价格而非"未配置"。
//
// 关联分组信息通过 groupRepo.ListActive 查询后按 ID 映射；渠道 GroupIDs 中未在活跃列表中
// 的分组（已停用或删除）会被忽略。
//
// 前置条件：s.groupRepo 必须非 nil（由 wire DI 保证）。直接 nil-deref 用于 fail-fast，
// 避免静默掩盖注入缺失。
func (s *ChannelService) ListAvailable(ctx context.Context) ([]AvailableChannel, error) {
	channels, err := s.repo.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}

	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active groups: %w", err)
	}
	groupByID := make(map[int64]AvailableGroupRef, len(groups))
	for i := range groups {
		g := groups[i]
		groupByID[g.ID] = AvailableGroupRef{
			ID:               g.ID,
			Name:             g.Name,
			Platform:         g.Platform,
			SubscriptionType: g.SubscriptionType,
			RateMultiplier:   g.RateMultiplier,
			IsExclusive:      g.IsExclusive,
			Description:      g.Description,
		}
	}

	out := make([]AvailableChannel, 0, len(channels))
	for i := range channels {
		ch := &channels[i]
		groups := make([]AvailableGroupRef, 0, len(ch.GroupIDs))
		for _, gid := range ch.GroupIDs {
			if ref, ok := groupByID[gid]; ok {
				groups = append(groups, ref)
			}
		}
		sort.SliceStable(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })

		ch.normalizeBillingModelSource()

		supported := ch.SupportedModels()
		s.fillGlobalPricingFallback(supported)

		out = append(out, AvailableChannel{
			ID:                 ch.ID,
			Name:               ch.Name,
			Description:        ch.Description,
			Status:             ch.Status,
			BillingModelSource: ch.BillingModelSource,
			RestrictModels:     ch.RestrictModels,
			Groups:             groups,
			SupportedModels:    supported,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// fillGlobalPricingFallback 对未命中渠道定价的支持模型，从全局 LiteLLM 数据合成一份
// 展示用定价。仅用于「可用渠道」展示，不影响真实计费链路。
//
// 触发条件：
//  1. Pricing == nil（渠道完全没声明该模型的定价条目）
//  2. Pricing 非 nil 但所有价格字段为空（admin UI 建了条目但没填价格）
//
// 当 s.pricingService 为 nil（测试场景），跳过回落。
func (s *ChannelService) fillGlobalPricingFallback(models []SupportedModel) {
	if s.pricingService == nil {
		return
	}
	for i := range models {
		if !pricingNeedsFallback(models[i].Pricing) {
			continue
		}
		lp := s.pricingService.GetModelPricing(models[i].Name)
		if lp == nil {
			continue
		}
		models[i].Pricing = synthesizePricingFromLiteLLM(lp, models[i].Pricing, models[i].Name)
	}
}

// pricingNeedsFallback 判定一个 ChannelModelPricing 是否需要走全局回落。
// 价格全部缺失（无 flat 字段且无任何带价 interval）即视为未配置。
func pricingNeedsFallback(p *ChannelModelPricing) bool {
	if p == nil {
		return true
	}
	if p.InputPrice != nil || p.OutputPrice != nil ||
		p.CacheWritePrice != nil || p.CacheReadPrice != nil ||
		p.ImageOutputPrice != nil || p.PerRequestPrice != nil {
		return false
	}
	for _, iv := range p.Intervals {
		if iv.InputPrice != nil || iv.OutputPrice != nil ||
			iv.CacheWritePrice != nil || iv.CacheReadPrice != nil ||
			iv.PerRequestPrice != nil {
			return false
		}
	}
	return true
}

// synthesizePricingFromLiteLLM 把 LiteLLM 的定价数据转成 ChannelModelPricing 形态，
// 仅用于展示。
//
// 计费模式优先级：
//  1. 渠道已选 BillingMode（admin 在 UI 里选了 image / per_request 但没填价的场景，
//     按选定模式合成对应字段）
//  2. LiteLLM mode="image_generation" → image
//  3. 默认 token
//
// LiteLLM 中字段 0 视为未配置，不带入展示。
func synthesizePricingFromLiteLLM(lp *LiteLLMModelPricing, existing *ChannelModelPricing, modelName string) *ChannelModelPricing {
	if lp == nil {
		return existing
	}

	mode := BillingModeToken
	switch {
	case existing != nil && existing.BillingMode != "":
		mode = existing.BillingMode
	case lp.Mode == "image_generation":
		mode = BillingModeImage
	}

	if mode == BillingModeImage || mode == BillingModePerRequest {
		return &ChannelModelPricing{
			BillingMode:      mode,
			PerRequestPrice:  nonZeroPtr(lp.OutputCostPerImage),
			ImageOutputPrice: nonZeroPtr(lp.OutputCostPerImageToken),
			InputPrice:       nonZeroPtr(lp.InputCostPerToken),
			OutputPrice:      nonZeroPtr(lp.OutputCostPerToken),
		}
	}
	return &ChannelModelPricing{
		BillingMode:      mode,
		InputPrice:       nonZeroPtr(lp.InputCostPerToken),
		OutputPrice:      nonZeroPtr(lp.OutputCostPerToken),
		CacheWritePrice:  nonZeroPtr(lp.CacheCreationInputTokenCost),
		CacheReadPrice:   nonZeroPtr(lp.CacheReadInputTokenCost),
		ImageOutputPrice: nonZeroPtr(lp.OutputCostPerImageToken),
		Intervals:        longContextDisplayIntervals(lp, modelName),
	}
}

const openAI272KLongContextThreshold = 272000

type openAILongContextTier struct {
	threshold            int
	inputMultiplier      float64
	outputMultiplier     float64
	cacheReadMultiplier  float64
	cacheWriteMultiplier float64
}

// openAILongContextTier returns the official long-context rules for the OpenAI
// models whose catalogue entries may omit their 272K+ fields. Cache read/write
// follow the input-side 2x increase for these families.
func openAILongContextTierForModel(model string) (openAILongContextTier, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	// 渠道映射常带 OpenAI 前缀或省略 "gpt-"（例如
	// "openai/gpt5.6-terra"）。先只做拼写规范化，避免把未知 GPT-5
	// 型号归并到其他 SKU 后展示错误的长上下文价格。
	if canonical := canonicalizeOpenAIModelAliasSpelling(m); canonical != "" {
		m = canonical
	}
	if strings.HasPrefix(m, "gpt-5.4-mini") || strings.HasPrefix(m, "gpt-5.4-nano") {
		return openAILongContextTier{}, false
	}

	var pricing *LiteLLMModelPricing
	switch {
	case strings.HasPrefix(m, "gpt-5.6-sol"):
		pricing = openAIGPT56SolFallbackPricing
	case strings.HasPrefix(m, "gpt-5.6-terra"):
		pricing = openAIGPT56TerraFallbackPricing
	case strings.HasPrefix(m, "gpt-5.6-luna"):
		pricing = openAIGPT56LunaFallbackPricing
	case strings.HasPrefix(m, "gpt-5.4") || strings.HasPrefix(m, "gpt-5.5"):
		pricing = openAIGPT54FallbackPricing
	}
	if pricing == nil || pricing.LongContextInputTokenThreshold <= 0 {
		return openAILongContextTier{}, false
	}

	return openAILongContextTier{
		threshold:            pricing.LongContextInputTokenThreshold,
		inputMultiplier:      pricing.LongContextInputCostMultiplier,
		outputMultiplier:     pricing.LongContextOutputCostMultiplier,
		cacheReadMultiplier:  2,
		cacheWriteMultiplier: 2,
	}, true
}

// longContextDisplayIntervals 把官方长上下文分档（阈值 + 倍率）合成成两段展示用 interval：
// (0, 阈值] 用基准价，(阈值, ∞] 用官方长上下文价。优先保留 LiteLLM 的显式
// *_above_272k_tokens 价格；目录缺失时按模型名补齐官方倍率。
//
// 仅用于「价格与计费」页展示官方阶梯定价；实际计费走 billing_service 的独立长上下文
// 逻辑（按整次会话 token 判定），二者口径一致。无阈值 / 无基准价时返回 nil。
func longContextDisplayIntervals(lp *LiteLLMModelPricing, modelName string) []PricingInterval {
	if lp == nil {
		return nil
	}

	threshold := lp.LongContextInputTokenThreshold
	inMult := lp.LongContextInputCostMultiplier
	outMult := lp.LongContextOutputCostMultiplier
	cacheReadMult := 0.0
	cacheWriteMult := 0.0
	hasExplicit272KPrices := lp.InputCostPerTokenAbove272KTokens > 0 ||
		lp.OutputCostPerTokenAbove272KTokens > 0 ||
		lp.CacheCreationInputTokenCostAbove272KTokens > 0 ||
		lp.CacheReadInputTokenCostAbove272KTokens > 0

	if tier, ok := openAILongContextTierForModel(modelName); ok {
		if threshold <= 0 {
			threshold = tier.threshold
		}
		if inMult <= 0 {
			inMult = tier.inputMultiplier
		}
		if outMult <= 0 {
			outMult = tier.outputMultiplier
		}
		cacheReadMult = tier.cacheReadMultiplier
		cacheWriteMult = tier.cacheWriteMultiplier
	}
	if threshold <= 0 && hasExplicit272KPrices {
		threshold = openAI272KLongContextThreshold
	}
	if threshold <= 0 {
		return nil
	}
	if inMult <= 0 {
		inMult = 1
	}
	if outMult <= 0 {
		outMult = 1
	}
	if inMult == 1 && outMult == 1 && !hasExplicit272KPrices {
		return nil
	}
	baseIn := lp.InputCostPerToken
	baseOut := lp.OutputCostPerToken
	baseCacheWrite := lp.CacheCreationInputTokenCost
	baseCacheRead := lp.CacheReadInputTokenCost
	if baseIn == 0 && baseOut == 0 && baseCacheWrite == 0 && baseCacheRead == 0 {
		return nil
	}
	return []PricingInterval{
		{
			MinTokens:       0,
			MaxTokens:       &threshold,
			InputPrice:      nonZeroPtr(baseIn),
			OutputPrice:     nonZeroPtr(baseOut),
			CacheWritePrice: nonZeroPtr(baseCacheWrite),
			CacheReadPrice:  nonZeroPtr(baseCacheRead),
			SortOrder:       0,
		},
		{
			MinTokens:       threshold,
			MaxTokens:       nil,
			InputPrice:      longContextPrice(baseIn, lp.InputCostPerTokenAbove272KTokens, inMult),
			OutputPrice:     longContextPrice(baseOut, lp.OutputCostPerTokenAbove272KTokens, outMult),
			CacheWritePrice: longContextPrice(baseCacheWrite, lp.CacheCreationInputTokenCostAbove272KTokens, cacheWriteMult),
			CacheReadPrice:  longContextPrice(baseCacheRead, lp.CacheReadInputTokenCostAbove272KTokens, cacheReadMult),
			SortOrder:       1,
		},
	}
}

func longContextPrice(base, explicit, multiplier float64) *float64 {
	if explicit > 0 {
		return nonZeroPtr(explicit)
	}
	if base <= 0 || multiplier <= 0 {
		return nil
	}
	return nonZeroPtr(base * multiplier)
}

func nonZeroPtr(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return &v
}
