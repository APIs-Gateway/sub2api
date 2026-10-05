package service

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// W6 PR6：30 天价格回放（设计 4.5）的比较引擎。
//
// 回放与影子比对（PR5）是同一件事换了输入来源：影子比对拿「正在结算的这一个请求」，回放拿 usage_logs 里的历史行。
// 两者调用同一个网关成本函数（OpenAI 侧 calculateOpenAIRecordUsageCost，Anthropic 侧 resolveBillingModelAndCost），
// 区别只是换一套 GroupPolicy 再算一遍：
//
//   - legacy 一侧：legacyPolicy（渠道配置，与线上今天的行为逐位相同）；
//   - v2 一侧：matrixPolicy（模型 x 分组矩阵）。
//
// 所以网关分派（候选循环、图片选路、成本函数的选择）两边完全相同，差异只可能来自「渠道到矩阵的翻译」。
// 这里没有任何写操作：不写 usage log、不扣费、不计无价指标；ctx 带非结算标记与影子重算标记
// （后者让网关里那几条旧的错误日志与 falling back 日志在回放里不出声，否则 170 万行会刷出海量日志）。

// 回放里附带的差异原因（reason）。class=expected 的差异必须带原因，方便切换预览按原因列清单。
const (
	// PricingReplayReasonUnpricedZero legacy 的空 custom 按 0 元出价，v2 的 inherit 报无价、结算时同样记 0 元：
	// 错误形态不同，最终 TotalCost 与 ActualCost 逐位相同（附录 A 第 23 条）。
	PricingReplayReasonUnpricedZero = "unpriced_zero_equivalent"
	// PricingReplayReasonUntrimmedModel 请求模型名带首尾空白：legacy 把它当成另一个名字（零费用或不命中），
	// v2 在入口统一去空白（设计 3.2）。
	PricingReplayReasonUntrimmedModel = "untrimmed_model_name"
	// PricingReplayReasonClosedInGroup legacy 放行、v2 因单元格 open=false 关闭：v2 新增的例外语义。
	PricingReplayReasonClosedInGroup = "closed_in_group"
)

// 回放的两侧。
const (
	replayLegacy = 0
	replayV2     = 1
)

// PricingReplayRow 一行历史用量，加上回放需要的分组与倍率（由调用方在读取之后补上 Group 与 Multiplier）。
// 不含任何用户标识以外的个人信息；UserID 只用于取专属倍率，不会写进输出。
type PricingReplayRow struct {
	ID        int64
	CreatedAt time.Time
	UserID    int64
	AccountID int64

	// GroupID 是主分组；ServedGroupID 非零表示这次请求由回退链里的另一个分组实际服务（按它计费）。
	GroupID       int64
	ServedGroupID int64

	Model          string
	RequestedModel string
	UpstreamModel  string
	ServiceTier    string
	BillingMode    string

	// 各类 token 与 usage_logs 里存的一致：InputTokens 已经扣掉了缓存读写（OpenAI 网关结算时就是这样写的）。
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	CacheCreation5m     int
	CacheCreation1h     int
	ImageInputTokens    int
	ImageOutputTokens   int

	ImageCount      int
	ImageSize       string
	ImageInputSize  string
	ImageOutputSize string

	// StoredActualCost 是历史上实际记的 actual_cost，只用于校准（信息性），不参与通过条件。
	StoredActualCost float64

	// Group 是实际计费的分组（服务分组优先），Multiplier 是分组倍率或用户专属倍率（额外倍率在成本函数里另乘）。
	Group      *Group
	Multiplier float64
}

// EffectiveGroupID 返回实际计费的分组 id。
func (r *PricingReplayRow) EffectiveGroupID() int64 {
	if r.ServedGroupID > 0 {
		return r.ServedGroupID
	}
	return r.GroupID
}

// PricingReplayDiff 一条差异。Legacy 与 V2 是两侧的视图（可序列化成 JSON，只含金额、模式、模型名，不含用户信息）。
type PricingReplayDiff struct {
	Kind   string
	Class  string
	Reason string
	// Model 是差异所属的模型标签：成本差异取计费模型，其余取请求模型。
	Model  string
	Legacy any
	V2     any
}

// PricingReplayOutcome 一行回放的结果。
type PricingReplayOutcome struct {
	// Covered 为 false 表示重建不出计费输入（模型名与时间缺失、分组缺失），这一行没有参与比较。
	Covered bool
	// Err 非空表示回放这一行时 panic 了（被吞掉）；这一行没有参与比较，并且会让通过判定失败。
	Err   string
	Diffs []PricingReplayDiff

	// 两侧结算后的 ActualCost（无价错误按 0 元结算，与 RecordUsage 一致）；CostFailed 表示成本函数返回了
	// 「无价」以外的错误（结算会直接失败），此时 ActualCost 记 0。
	LegacyActualCost float64
	V2ActualCost     float64
	LegacyCostFailed bool
	V2CostFailed     bool
}

// PricingReplayer 逐行比较 legacy 与 v2 两套 GroupPolicy。方法并发安全（两侧的网关对象在构造之后只读）。
type PricingReplayer struct {
	billing *BillingService
	policy  [2]GroupPolicy
	openai  [2]*OpenAIGatewayService
	gateway [2]*GatewayService
}

// pricingReplaySnapshotTTL 回放期间矩阵快照的存活时间：回放要跑几分钟，期间不能因为 TTL 过期而重载，
// 否则同一次回放里前后读到的可能不是同一份状态。回放结束时再比对配置指纹，发现配置在回放期间变过就判作废。
const pricingReplaySnapshotTTL = 24 * time.Hour

// NewPricingReplayer 构造回放器。channels 是线上同一份渠道服务（legacy 一侧用它构造 legacyPolicy），
// src 是 v2 一侧的矩阵快照来源（读库里已派生的行，或按渠道当前配置实时派生，见 NewDerivedMatrixSource）。
func NewPricingReplayer(billing *BillingService, channels *ChannelService, src MatrixSnapshotSource) (*PricingReplayer, error) {
	if billing == nil || channels == nil || src == nil {
		return nil, fmt.Errorf("pricing replay needs a billing service, a channel service and a matrix source")
	}
	matrix := NewMatrixGroupPolicy(src, nil)
	matrix.ttl = pricingReplaySnapshotTTL
	return newPricingReplayer(billing, newLegacyGroupPolicy(channels), matrix), nil
}

// newPricingReplayer 由两套策略构造回放器（测试直接注入两个 matrixPolicy）。
// 每一侧各有一对网关服务与一个价格解析器，全部只读，策略通过 policyOverride 注入，
// 与线上 stagedPolicy 把策略注入网关的方式相同。
func newPricingReplayer(billing *BillingService, legacy, v2 GroupPolicy) *PricingReplayer {
	r := &PricingReplayer{billing: billing}
	r.policy = [2]GroupPolicy{legacy, v2}
	for i, p := range r.policy {
		resolver := &ModelPricingResolver{policyOverride: p, billingService: billing}
		r.openai[i] = &OpenAIGatewayService{billingService: billing, policyOverride: p, resolver: resolver}
		r.gateway[i] = &GatewayService{billingService: billing, policyOverride: p, resolver: resolver}
	}
	return r
}

// withReplayRecompute 返回回放用的 ctx：非结算标记（PR1 的无价计数与日志跳过）加影子重算标记
// （网关里「重复会让日志与用量行对不上」的那几条日志用 isShadowRecompute 加门）。
// 不带 forceStage：回放的两侧是两套独立的网关对象，不靠 ctx 切换阶段。
func withReplayRecompute(ctx context.Context) context.Context {
	ctx = WithBillingNonSettlement(ctx)
	return context.WithValue(ctx, shadowRecomputeCtxKey{}, true)
}

// replayInput 一行回放里两侧共用的输入。
type replayInput struct {
	row             *PricingReplayRow
	group           *Group
	apiKey          *APIKey
	platform        string
	model           string
	requested       string
	upstream        string
	serviceTier     string
	serviceTierPtr  *string
	tokens          UsageTokens
	multiplier      float64
	imageMultiplier float64
}

// replaySide 一侧的结果。
type replaySide struct {
	mapping        ChannelMappingResult
	access         QuoteAccess
	upstreamAccess QuoteAccess
	upstreamCheck  bool

	label string
	view  shadowCostView
	// settled 是按 RecordUsage 的方式结算后的成本：无价错误记 0 元，其他错误为 nil。
	settled *CostBreakdown

	acctTokens UsageTokens
	acct       shadowAccountCostView
	acctTotal  *float64
}

// Replay 回放一行。panic 被吞掉并记在 Err 里，永不向调用方传播。
func (r *PricingReplayer) Replay(parent context.Context, row *PricingReplayRow) (out PricingReplayOutcome) {
	defer func() {
		if rec := recover(); rec != nil {
			out = PricingReplayOutcome{Err: fmt.Sprintf("panic: %v", rec)}
		}
	}()

	model := row.Model
	requested := row.RequestedModel
	if strings.TrimSpace(requested) == "" {
		requested = model
	}
	if strings.TrimSpace(model) == "" {
		model = requested
	}
	if strings.TrimSpace(requested) == "" || row.CreatedAt.IsZero() || row.Group == nil {
		return PricingReplayOutcome{}
	}
	upstream := row.UpstreamModel
	if strings.TrimSpace(upstream) == "" {
		upstream = model
	}

	group := row.Group
	gid := group.ID
	in := &replayInput{
		row:         row,
		group:       group,
		apiKey:      &APIKey{GroupID: &gid, Group: group},
		platform:    group.Platform,
		model:       model,
		requested:   requested,
		upstream:    upstream,
		serviceTier: strings.TrimSpace(row.ServiceTier),
		tokens: UsageTokens{
			InputTokens:         row.InputTokens,
			ImageInputTokens:    row.ImageInputTokens,
			OutputTokens:        row.OutputTokens,
			CacheCreationTokens: row.CacheCreationTokens,
			CacheReadTokens:     row.CacheReadTokens,
			ImageOutputTokens:   row.ImageOutputTokens,
		},
		multiplier: row.Multiplier,
	}
	if in.serviceTier != "" {
		tier := in.serviceTier
		in.serviceTierPtr = &tier
	}
	in.imageMultiplier = resolveImageRateMultiplier(in.apiKey, in.multiplier)

	ctx := withReplayRecompute(parent)
	legacy := r.runSide(ctx, replayLegacy, in, nil)
	v2 := r.runSide(ctx, replayV2, in, legacy.acctTotal)

	out.Covered = true
	if legacy.settled != nil {
		out.LegacyActualCost = legacy.settled.ActualCost
	} else {
		out.LegacyCostFailed = true
	}
	if v2.settled != nil {
		out.V2ActualCost = v2.settled.ActualCost
	} else {
		out.V2CostFailed = true
	}
	out.Diffs = r.compareSides(in, &legacy, &v2)
	return out
}

// runSide 在一套策略上把一行从「请求模型」起走一遍：映射、准入、成本、账号成本。
// acctTotal 非空时（v2 一侧）用它作为账号成本的 TotalCost 输入，与影子比对一致：账号成本的差异只来自成本核算规则。
func (r *PricingReplayer) runSide(ctx context.Context, idx int, in *replayInput, acctTotal *float64) replaySide {
	policy := r.policy[idx]
	ctx = pinGroupPolicySnapshots(ctx, policy)
	gid := in.group.ID

	var s replaySide
	s.mapping = policy.Mapping(ctx, gid, in.requested)
	s.access = policy.ModelAccess(ctx, gid, in.requested)
	s.upstreamAccess = policy.UpstreamAccess(ctx, gid, in.upstream)
	s.upstreamCheck, _ = policy.UpstreamCheck(ctx, gid)
	fields := s.mapping.ToUsageFields(in.requested, in.upstream)

	switch in.platform {
	case PlatformAnthropic, PlatformGemini, PlatformAntigravity:
		r.anthropicCost(ctx, idx, in, fields, &s)
	default:
		r.openAICost(ctx, idx, in, fields, &s)
	}

	// 账号成本：legacy 一侧用自己结算后的 TotalCost，v2 一侧用 legacy 的，所以两侧的差异只来自规则与模式。
	total := acctTotal
	if idx == replayLegacy && s.settled != nil {
		t := s.settled.TotalCost
		total = &t
	}
	if total != nil {
		usageLog := &UsageLog{ImageCount: in.row.ImageCount, ServiceTier: in.serviceTierPtr}
		applyAccountStatsCost(ctx, usageLog, policy, r.billing, in.row.AccountID, gid, in.upstream, in.model,
			s.acctTokens, *total, in.row.CreatedAt)
		s.acct = newShadowAccountCostView(usageLog.AccountStatsCost)
		s.acctTotal = total
	}
	return s
}

// openAICost 复刻 OpenAIGatewayService.RecordUsage 里「计费模型候选」的推导，再调同一个成本函数。
// 推导逻辑有对应的一致性测试（pricing_replay_test.go 对照 RecordUsage 写出的用量行）。
func (r *PricingReplayer) openAICost(ctx context.Context, idx int, in *replayInput, f ChannelUsageFields, s *replaySide) {
	result := &OpenAIForwardResult{
		Model:         in.model,
		UpstreamModel: in.upstream,
		ServiceTier:   in.serviceTierPtr,
		Usage: OpenAIUsage{
			InputTokens:              in.tokens.InputTokens + in.tokens.CacheReadTokens + in.tokens.CacheCreationTokens,
			ImageInputTokens:         in.tokens.ImageInputTokens,
			OutputTokens:             in.tokens.OutputTokens,
			CacheCreationInputTokens: in.tokens.CacheCreationTokens,
			CacheReadInputTokens:     in.tokens.CacheReadTokens,
			ImageOutputTokens:        in.tokens.ImageOutputTokens,
		},
		ImageCount:      in.row.ImageCount,
		ImageSize:       in.row.ImageSize,
		ImageInputSize:  in.row.ImageInputSize,
		ImageOutputSize: in.row.ImageOutputSize,
	}
	billingModel := forwardResultBillingModel(result.Model, result.UpstreamModel)
	if f.BillingModelSource == BillingModelSourceChannelMapped && f.ChannelMappedModel != "" && f.ChannelMappedModel != f.OriginalModel {
		billingModel = f.ChannelMappedModel
	}
	if f.BillingModelSource == BillingModelSourceRequested && f.OriginalModel != "" {
		billingModel = f.OriginalModel
	}
	billingModels := usageBillingModelCandidates(
		billingModel,
		result.BillingModel,
		f.ChannelMappedModel,
		f.OriginalModel,
		result.UpstreamModel,
		result.Model,
	)
	cost, err := r.openai[idx].calculateOpenAIRecordUsageCost(ctx, result, in.apiKey, billingModels,
		in.multiplier, in.imageMultiplier, in.tokens, in.serviceTier, in.row.CreatedAt)

	s.label = firstUsageBillingModel(billingModels)
	s.view = newShadowCostView(s.label, cost, err)
	switch {
	case err == nil:
		s.settled = cost
	case isUsagePricingUnavailableError(err):
		// RecordUsage 对「无价」错误的处理：记 0 元、照常落库。
		s.settled = &CostBreakdown{BillingMode: string(BillingModeToken)}
	}
	s.acctTokens = in.tokens
}

// anthropicCost 复刻 GatewayService.recordUsageCore 里计费模型的推导，再调 resolveBillingModelAndCost。
// Gemini 与 Antigravity 共用这条路径；Gemini 原生入口的长上下文加价（recordUsageOpts）回放里不重建，
// 两侧一致，不影响比较（线上近 30 天没有这两个平台的用量）。
func (r *PricingReplayer) anthropicCost(ctx context.Context, idx int, in *replayInput, f ChannelUsageFields, s *replaySide) {
	result := &ForwardResult{
		Model:         in.model,
		UpstreamModel: in.upstream,
		Usage: ClaudeUsage{
			InputTokens:              in.tokens.InputTokens,
			OutputTokens:             in.tokens.OutputTokens,
			CacheCreationInputTokens: in.tokens.CacheCreationTokens,
			CacheReadInputTokens:     in.tokens.CacheReadTokens,
			CacheCreation5mTokens:    in.row.CacheCreation5m,
			CacheCreation1hTokens:    in.row.CacheCreation1h,
			ImageOutputTokens:        in.tokens.ImageOutputTokens,
		},
		ImageCount:      in.row.ImageCount,
		ImageSize:       in.row.ImageSize,
		ImageInputSize:  in.row.ImageInputSize,
		ImageOutputSize: in.row.ImageOutputSize,
	}
	concrete := forwardResultBillingModel(result.Model, result.UpstreamModel)
	billingModel := concrete
	if f.BillingModelSource == BillingModelSourceUpstream {
		if upstreamModel := strings.TrimSpace(result.UpstreamModel); upstreamModel != "" {
			billingModel = upstreamModel
		}
	}
	if f.BillingModelSource == BillingModelSourceChannelMapped && f.ChannelMappedModel != "" {
		billingModel = f.ChannelMappedModel
	}
	if f.BillingModelSource == BillingModelSourceRequested && f.OriginalModel != "" {
		billingModel = f.OriginalModel
	}
	input := &recordUsageCoreInput{ChannelUsageFields: f}
	label, cost := r.gateway[idx].resolveBillingModelAndCost(ctx, input, result, in.apiKey,
		billingModel, concrete, in.multiplier, in.imageMultiplier, &recordUsageOpts{}, in.row.CreatedAt)

	s.label = label
	s.view = newShadowCostView(label, cost, nil)
	s.settled = cost
	// 与 recordUsageCore 里账号成本那次调用的 UsageTokens 一致（没有图片输入、不分 5m/1h）。
	s.acctTokens = UsageTokens{
		InputTokens:         in.tokens.InputTokens,
		OutputTokens:        in.tokens.OutputTokens,
		CacheCreationTokens: in.tokens.CacheCreationTokens,
		CacheReadTokens:     in.tokens.CacheReadTokens,
		ImageOutputTokens:   in.tokens.ImageOutputTokens,
	}
}

// compareSides 比较两侧的映射、准入、成本与账号成本，给每个差异分类。
func (r *PricingReplayer) compareSides(in *replayInput, legacy, v2 *replaySide) []PricingReplayDiff {
	var diffs []PricingReplayDiff
	untrimmed := in.requested != strings.TrimSpace(in.requested) || in.model != strings.TrimSpace(in.model)
	add := func(kind, class, reason, model string, lv, vv any) {
		if untrimmed {
			class, reason = ShadowClassExpected, PricingReplayReasonUntrimmedModel
		}
		diffs = append(diffs, PricingReplayDiff{Kind: kind, Class: class, Reason: reason, Model: model, Legacy: lv, V2: vv})
	}

	// 映射：ChannelID 不比（v2 分组不再写 channel_id，设计 2.3、附录 A 第 11 条）。
	lm, vm := legacy.mapping, v2.mapping
	if lm.MappedModel != vm.MappedModel || lm.Mapped != vm.Mapped || lm.BillingModelSource != vm.BillingModelSource {
		add(ShadowKindMapping, ShadowClassTranslation, "", in.requested, shadowMappingView(lm), shadowMappingView(vm))
	}

	// 准入：只比 OK，不比原因（原因只用于展示）。
	if legacy.access.OK != v2.access.OK {
		class, reason := ShadowClassTranslation, ""
		if legacy.access.OK && !v2.access.OK && v2.access.Reason == QuoteAccessReasonClosedInGroup {
			class, reason = ShadowClassExpected, PricingReplayReasonClosedInGroup
		}
		add(ShadowKindAccess, class, reason, in.requested, legacy.access, v2.access)
	}
	// 上游模型的准入只在调度循环需要逐账号检查时才有意义（任一侧要求检查就比）。
	if (legacy.upstreamCheck || v2.upstreamCheck) && legacy.upstreamAccess.OK != v2.upstreamAccess.OK {
		add(ShadowKindAccess, ShadowClassTranslation, "", in.upstream, legacy.upstreamAccess, v2.upstreamAccess)
	}

	// 成本：视图逐位比较；错误形态不同但结算后实付相同的，单独标 expected。
	if !legacy.view.equal(v2.view) {
		class, reason := ShadowClassTranslation, ""
		if replayUnpricedZeroEquivalent(legacy, v2) {
			class, reason = ShadowClassExpected, PricingReplayReasonUnpricedZero
		}
		add(ShadowKindCost, class, reason, legacy.label, legacy.view, v2.view)
	}

	// 账号成本。
	if legacy.acctTotal != nil && v2.acctTotal != nil && !legacy.acct.equal(v2.acct) {
		add(ShadowKindAccountCost, ShadowClassTranslation, "", legacy.label, legacy.acct, v2.acct)
	}
	return diffs
}

// replayUnpricedZeroEquivalent 判断成本差异是不是「只有错误形态不同」：恰好一侧报无价、另一侧没报，
// 并且按 RecordUsage 结算后两侧的 TotalCost 与 ActualCost 逐位相同。候选回退等原因造成实付不同的不算。
func replayUnpricedZeroEquivalent(legacy, v2 *replaySide) bool {
	lu := legacy.view.Error == "pricing_unavailable"
	vu := v2.view.Error == "pricing_unavailable"
	if lu == vu || legacy.settled == nil || v2.settled == nil {
		return false
	}
	return sameBits(legacy.settled.TotalCost, v2.settled.TotalCost) && sameBits(legacy.settled.ActualCost, v2.settled.ActualCost)
}

// CheckGroup 做每个分组只需要做一次的功能类比较：调度循环的上游准入检查与三个功能开关。
// 这些不随请求变化，逐行比没有意义；差异按分组计一次（kind=feature，行号为 0）。
func (r *PricingReplayer) CheckGroup(parent context.Context, group *Group) (diffs []PricingReplayDiff) {
	if group == nil {
		return nil
	}
	gid := group.ID
	lp, vp := r.policy[replayLegacy], r.policy[replayV2]
	ctx := withReplayRecompute(parent)
	vctx := pinGroupPolicySnapshots(ctx, vp)

	// 策略实现出 panic 不能带倒整个回放：记成一条翻译差异，让判定不通过。
	defer func() {
		if rec := recover(); rec != nil {
			diffs = append(diffs, PricingReplayDiff{
				Kind: ShadowKindFeature, Class: ShadowClassTranslation, Model: "panic",
				Legacy: nil, V2: map[string]any{"panic": fmt.Sprint(rec)},
			})
		}
	}()
	lc, lerr := lp.UpstreamCheck(ctx, gid)
	vc, verr := vp.UpstreamCheck(vctx, gid)
	if (lerr == nil) != (verr == nil) || lc != vc {
		diffs = append(diffs, PricingReplayDiff{
			Kind: ShadowKindFeature, Class: ShadowClassTranslation, Model: "upstream_check",
			Legacy: map[string]any{"required": lc, "failed": lerr != nil},
			V2:     map[string]any{"required": vc, "failed": verr != nil},
		})
	}
	for _, f := range []GroupFeature{GroupFeatureWebSearchEmulation, GroupFeatureBedrockCCCompat, GroupFeatureCodexImageGenerationBridge} {
		lv, lerr := lp.Feature(ctx, gid, group.Platform, f)
		vv, verr := vp.Feature(vctx, gid, group.Platform, f)
		if (lerr == nil) != (verr == nil) || !shadowBoolPtrEqual(lv, vv) {
			diffs = append(diffs, PricingReplayDiff{
				Kind: ShadowKindFeature, Class: ShadowClassTranslation, Model: string(f),
				Legacy: shadowBoolPtrView(lv), V2: shadowBoolPtrView(vv),
			})
		}
	}
	return diffs
}
