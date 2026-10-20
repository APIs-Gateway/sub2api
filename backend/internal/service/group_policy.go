package service

import (
	"context"
	"strings"
	"time"
)

// GroupPolicy 是分组在「准入、映射、功能开关、价格覆盖、账号成本、灰度阶段」上的统一读口（W6 设计 3.3）。
//
// 今天这些信息都由 ChannelService 提供；网关、价格解析器和账号成本只通过本接口读取，
// 这样后续切换到「模型×分组矩阵」时，换一个实现即可，调用点不用再动。
//
// 本 PR 只有 legacyPolicy 一个实现：每个方法都原样转发给 ChannelService，行为与改造前逐位相同。
// 方法名与旧读口的对应关系见各方法的注释。
type GroupPolicy interface {
	// Mapping 解析分组级模型映射，对应 ChannelService.ResolveChannelMapping。
	// 分组没有（启用的）渠道时返回 {MappedModel: model}，计费来源为空串。
	Mapping(ctx context.Context, groupID int64, model string) ChannelMappingResult

	// ModelAccess 判断分组是否放行该模型，对应 ChannelService.IsModelRestricted（受限即 OK=false）。
	// 查找方式与 checkRestricted 完全一致：缓存查找自带去首尾空白、转小写、claude 名点号写成连字符，
	// 但不做 codex 归一化（那是 PriceOverride 的第二步）。
	ModelAccess(ctx context.Context, groupID int64, model string) QuoteAccess

	// UpstreamAccess 判断账号映射之后的上游模型是否放行。
	// 与设计草图的差异：参数是调用方已经算好的上游模型名，而不是账号对象。
	// 原因是两个网关从账号求上游模型的逻辑不同（OpenAI 网关还看 compact 标志与 ctx 里的转发模型），
	// 这部分是网关逻辑、不是分组策略，留在网关里。
	UpstreamAccess(ctx context.Context, groupID int64, upstreamModel string) QuoteAccess

	// UpstreamCheck 判断调度循环是否需要逐账号检查上游模型的准入：
	// 渠道启用模型限制，并且计费来源是 upstream。分组没有渠道返回 false。
	// 返回 error 是因为调用方对缓存加载失败的处理不同（调度器当作「不需要检查」，
	// 降级守卫当作「候选不可用」），由调用方自己决定。
	UpstreamCheck(ctx context.Context, groupID int64) (bool, error)

	// Feature 读取分组上的功能开关。
	// 返回 nil 表示分组没有显式设置（调用方回落到账号或全局配置）。
	// 与设计草图的差异：返回 *bool 而不是 bool，因为 codex 图片桥有「未设置」这一档。
	// 返回 error 的原因同 UpstreamCheck：codex 图片桥在失败时要打日志，其余开关当作未设置。
	Feature(ctx context.Context, groupID int64, platform string, f GroupFeature) (*bool, error)

	// PriceOverride 返回分组对该模型的价格覆盖，没有则返回 nil。
	// 先查字面名，未命中再查 codex 归一化后的名字（gpt-5.6-luna-high 回落到 gpt-5.6-luna）；
	// 两步查找整体属于策略内部，调用方不再自己归一化。at 是计费时点，legacy 不使用。
	PriceOverride(ctx context.Context, groupID int64, model string, at time.Time) *ChannelModelPricing

	// ExtraMultiplier 返回分组对该模型的额外倍率。legacy 没有这个概念，恒为 1。
	ExtraMultiplier(ctx context.Context, groupID int64, model string, at time.Time) float64

	// CostMode 返回账号成本核算模式。分组没有（启用的）渠道是 account_rate，
	// 渠道勾选「应用模型定价到账号统计」是 follow_billing，否则是 catalog_upstream。
	CostMode(ctx context.Context, groupID int64) MatrixCostMode

	// CostRules 返回账号成本的自定义规则与分组平台（resolveAccountStatsCost 的优先级 1）。
	// 设计草图里没有这个方法：规则的读取和模式分开，v2 的规则来自成本核算表。
	CostRules(ctx context.Context, groupID int64) ([]AccountStatsPricingRule, string)

	// Stage 返回分组的价格体系阶段。legacy 恒为 legacy。
	Stage(ctx context.Context, groupID int64) PricingStage
}

// GroupFeature 分组上的功能开关。取值与渠道 features_config 里的键一致。
type GroupFeature string

const (
	GroupFeatureWebSearchEmulation         GroupFeature = featureKeyWebSearchEmulation
	GroupFeatureBedrockCCCompat            GroupFeature = featureKeyBedrockCCCompat
	GroupFeatureCodexImageGenerationBridge GroupFeature = featureKeyCodexImageGenerationBridge
)

// QuoteAccessReasonNotInAllowlist 分组启用了模型限制，且模型不在定价条目里。
const QuoteAccessReasonNotInAllowlist = "not_in_allowlist"

// legacyPolicy 用现有的渠道配置实现 GroupPolicy，只做转发，不加任何逻辑。
// 唯一搬进来的是 PriceOverride 里字面名、归一化名两步查找（原 ModelPricingResolver.lookupChannelPricingNormalized）。
type legacyPolicy struct {
	cs *ChannelService
}

var _ GroupPolicy = legacyPolicy{}

// newLegacyGroupPolicy 由渠道服务构造 legacyPolicy。cs 为 nil 时返回 nil 接口，
// 与调用方原来「channelService == nil 就跳过」的判断保持一致。
func newLegacyGroupPolicy(cs *ChannelService) GroupPolicy {
	if cs == nil {
		return nil
	}
	return legacyPolicy{cs: cs}
}

// resolveGroupPolicy 选出生效的策略：显式注入的优先，否则用渠道服务构造 legacyPolicy。
func resolveGroupPolicy(override GroupPolicy, cs *ChannelService) GroupPolicy {
	if override != nil {
		return override
	}
	return newLegacyGroupPolicy(cs)
}

// groupPolicy 返回 Anthropic 网关当前使用的 GroupPolicy；没有渠道服务也没有注入时返回 nil。
// 每次调用都由 channelService 现取，不缓存，所以测试里改 channelService 字段仍然生效。
func (s *GatewayService) groupPolicy() GroupPolicy {
	if s == nil {
		return nil
	}
	return resolveGroupPolicy(s.policyOverride, s.channelService)
}

// groupPolicy 返回 OpenAI 网关当前使用的 GroupPolicy，规则同 GatewayService.groupPolicy。
func (s *OpenAIGatewayService) groupPolicy() GroupPolicy {
	if s == nil {
		return nil
	}
	return resolveGroupPolicy(s.policyOverride, s.channelService)
}

// groupPolicy 返回价格解析器当前使用的 GroupPolicy，规则同 GatewayService.groupPolicy。
func (r *ModelPricingResolver) groupPolicy() GroupPolicy {
	if r == nil {
		return nil
	}
	return resolveGroupPolicy(r.policyOverride, r.channelService)
}

func (p legacyPolicy) Mapping(ctx context.Context, groupID int64, model string) ChannelMappingResult {
	return p.cs.ResolveChannelMapping(ctx, groupID, model)
}

func (p legacyPolicy) ModelAccess(ctx context.Context, groupID int64, model string) QuoteAccess {
	return legacyAccess(p.cs.IsModelRestricted(ctx, groupID, model))
}

func (p legacyPolicy) UpstreamAccess(ctx context.Context, groupID int64, upstreamModel string) QuoteAccess {
	return legacyAccess(p.cs.IsModelRestricted(ctx, groupID, upstreamModel))
}

// legacyAccess 把 IsModelRestricted 的结果转成 QuoteAccess。
func legacyAccess(restricted bool) QuoteAccess {
	if restricted {
		return QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist}
	}
	return QuoteAccess{OK: true}
}

func (p legacyPolicy) UpstreamCheck(ctx context.Context, groupID int64) (bool, error) {
	ch, err := p.cs.GetChannelForGroup(ctx, groupID)
	if err != nil {
		return false, err
	}
	if ch == nil || !ch.RestrictModels {
		return false, nil
	}
	return ch.BillingModelSource == BillingModelSourceUpstream, nil
}

func (p legacyPolicy) Feature(ctx context.Context, groupID int64, platform string, f GroupFeature) (*bool, error) {
	ch, err := p.cs.GetChannelForGroup(ctx, groupID)
	if err != nil || ch == nil {
		return nil, err
	}
	switch f {
	case GroupFeatureWebSearchEmulation:
		enabled := ch.IsWebSearchEmulationEnabled(platform)
		return &enabled, nil
	case GroupFeatureBedrockCCCompat:
		enabled := ch.IsBedrockCCCompatEnabled(platform)
		return &enabled, nil
	case GroupFeatureCodexImageGenerationBridge:
		return ch.CodexImageGenerationBridgeOverride(platform), nil
	default:
		return nil, nil
	}
}

// PriceOverride 查找渠道定价：先用字面模型名做精确/通配匹配，
// 未命中时用与官方兜底价一致的归一化模型名再查一次。
//
// 官方兜底价对 OpenAI/Codex 族会把 gpt-5.6-luna-high 这类变体名归一化到基名
// （billing_service.go 的 normalizeKnownOpenAICodexModel 分支），而渠道定价此前
// 只认字面名。两者不对称导致：管理员只配基名、请求模型带 effort 后缀时，渠道定价
// 未命中而官方兜底命中，计费候选循环首个成功即返回，渠道定价永远轮不到（issue #5256）。
//
// 字面名优先，保证管理员对具体变体的显式配价不被基名覆盖；非 OpenAI 模型
// normalizeKnownOpenAICodexModel 返回空串，此处天然 no-op。
//
// 这两步原来在 ModelPricingResolver.lookupChannelPricingNormalized 里，原样搬到这里；
// 这样将来的矩阵实现可以一次查到整个单元格，价格覆盖与额外倍率共用同一次查找。
func (p legacyPolicy) PriceOverride(ctx context.Context, groupID int64, model string, _ time.Time) *ChannelModelPricing {
	if pricing := p.cs.GetChannelModelPricing(ctx, groupID, model); pricing != nil {
		return pricing
	}
	normalized := normalizeKnownOpenAICodexModel(model)
	if normalized == "" || strings.EqualFold(normalized, strings.TrimSpace(model)) {
		return nil
	}
	return p.cs.GetChannelModelPricing(ctx, groupID, normalized)
}

func (legacyPolicy) ExtraMultiplier(context.Context, int64, string, time.Time) float64 {
	return 1
}

func (p legacyPolicy) CostMode(ctx context.Context, groupID int64) MatrixCostMode {
	lk, err := p.cs.lookupGroupChannel(ctx, groupID)
	if err != nil || lk == nil {
		return MatrixCostAccountRate
	}
	if lk.channel.ApplyPricingToAccountStats {
		return MatrixCostFollowBilling
	}
	return MatrixCostCatalogUpstream
}

func (p legacyPolicy) CostRules(ctx context.Context, groupID int64) ([]AccountStatsPricingRule, string) {
	ch, err := p.cs.GetChannelForGroup(ctx, groupID)
	if err != nil || ch == nil {
		return nil, ""
	}
	return ch.AccountStatsPricingRules, p.cs.GetGroupPlatform(ctx, groupID)
}

func (legacyPolicy) Stage(context.Context, int64) PricingStage {
	return PricingStageLegacy
}
