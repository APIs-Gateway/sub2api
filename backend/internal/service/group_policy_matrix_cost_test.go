//go:build unit

package service

// W6 PR4-1：额外倍率乘进成本函数（设计 3.2、R2-BK-2、BK-2 余项）。
//
// 倍率在成本函数内部、按「实际出价的那个模型」取值并乘入：
//   - OpenAI 网关 token 路径：候选循环里每个候选各取一次，第一个成功出价的候选用它自己的倍率；
//   - OpenAI 网关图片路径：calculateOpenAIImageCost 入口，按它实际收到的模型取；
//   - Anthropic 网关：calculateRecordUsageCost 入口，按选定的计费模型取一次。
// extra 为 1（legacy、没有单元格、窗口外）时一律不乘，结果与改动前逐位相同。

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// mcExtraPolicy 只实现 ExtraMultiplier：其余方法（内嵌的 nil 接口）不应该被调用。
type mcExtraPolicy struct {
	GroupPolicy
	value float64

	gotGroup int64
	gotModel string
	gotAt    time.Time
	calls    int
}

func (p *mcExtraPolicy) ExtraMultiplier(_ context.Context, groupID int64, model string, at time.Time) float64 {
	p.calls++
	p.gotGroup, p.gotModel, p.gotAt = groupID, model, at
	return p.value
}

func mcKey(g Group) *APIKey {
	gid := g.ID
	return &APIKey{ID: 9, GroupID: &gid, Group: &g}
}

func mcOpenAISvc(policy GroupPolicy) *OpenAIGatewayService {
	bs := newTestBillingService()
	return &OpenAIGatewayService{
		billingService: bs,
		policyOverride: policy,
		resolver:       &ModelPricingResolver{policyOverride: policy, billingService: bs},
	}
}

func mcGatewaySvc(policy GroupPolicy) *GatewayService {
	bs := newTestBillingService()
	return &GatewayService{
		billingService: bs,
		policyOverride: policy,
		resolver:       &ModelPricingResolver{policyOverride: policy, billingService: bs},
	}
}

func mcImageCell(key string, perRequest float64) StoredMatrixCell {
	c := mpCellBase(key, MatrixPriceCustom)
	c.CustomPrice = &MatrixCustomPrice{BillingMode: BillingModeImage, PerRequestPrice: mpF(perRequest)}
	return c
}

func mcOpenAICost(t *testing.T, policy GroupPolicy, key *APIKey, models []string, result *OpenAIForwardResult, mult, imageMult float64) *CostBreakdown {
	t.Helper()
	cost, err := mcOpenAISvc(policy).calculateOpenAIRecordUsageCost(
		context.Background(), result, key, models, mult, imageMult,
		UsageTokens{InputTokens: 1000, OutputTokens: 100}, "", time.Time{},
	)
	require.NoError(t, err)
	require.NotNil(t, cost)
	return cost
}

func mcRequireScaled(t *testing.T, base, got *CostBreakdown, extra float64) {
	t.Helper()
	require.Greater(t, base.TotalCost, 0.0, "the baseline must be a real, non-zero price")
	require.InDelta(t, base.TotalCost, got.TotalCost, 1e-15, "the extra multiplier never changes the unit price or total cost")
	require.InDelta(t, base.ActualCost*extra, got.ActualCost, 1e-12)
	require.Equal(t, extra, got.extraMultiplier)
}

// ---------------------------------------------------------------------------
// groupExtraMultiplier / withExtraMultiplier / rateWithExtra
// ---------------------------------------------------------------------------

func TestGroupExtraMultiplier(t *testing.T) {
	ctx := context.Background()
	key := mcKey(Group{ID: 7})
	at := mpT0

	p := &mcExtraPolicy{value: 2.5}
	require.Equal(t, 2.5, groupExtraMultiplier(ctx, p, key, "gpt-5.4", at))
	require.Equal(t, int64(7), p.gotGroup, "the group comes from apiKey.Group (the serving group for a stable-fallback shadow key)")
	require.Equal(t, "gpt-5.4", p.gotModel)
	require.Equal(t, at, p.gotAt)

	// 带的是 Group.ID，不是 GroupID：影子 key 的 Group 指向实际服务的分组。
	shadow := mcKey(Group{ID: 7})
	other := int64(99)
	shadow.GroupID = &other
	groupExtraMultiplier(ctx, p, shadow, "gpt-5.4", at)
	require.Equal(t, int64(7), p.gotGroup)

	for _, bad := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		require.Equal(t, 1.0, groupExtraMultiplier(ctx, &mcExtraPolicy{value: bad}, key, "gpt-5.4", at), "value %v", bad)
	}

	// 没有策略、没有 key、没有分组：1，且不读策略。
	require.Equal(t, 1.0, groupExtraMultiplier(ctx, nil, key, "gpt-5.4", at))
	counting := &mcExtraPolicy{value: 3}
	require.Equal(t, 1.0, groupExtraMultiplier(ctx, counting, nil, "gpt-5.4", at))
	require.Equal(t, 1.0, groupExtraMultiplier(ctx, counting, &APIKey{}, "gpt-5.4", at))
	require.Zero(t, counting.calls)
}

func TestWithExtraMultiplierAndRateWithExtra(t *testing.T) {
	base := &CostBreakdown{TotalCost: 1, ActualCost: 1.5, BillingMode: "token"}

	require.Same(t, base, withExtraMultiplier(base, 1), "extra == 1 returns the same object, untouched")
	require.Zero(t, base.extraMultiplier)
	require.Nil(t, withExtraMultiplier(nil, 3))

	tagged := withExtraMultiplier(base, 3)
	require.NotSame(t, base, tagged, "the object returned by a cost function is never mutated")
	require.Zero(t, base.extraMultiplier)
	require.Equal(t, 3.0, tagged.extraMultiplier)
	require.Equal(t, base.TotalCost, tagged.TotalCost)
	require.Equal(t, base.ActualCost, tagged.ActualCost)

	require.Equal(t, 1.5, rateWithExtra(1.5, nil))
	require.Equal(t, 1.5, rateWithExtra(1.5, base))
	require.Equal(t, 1.5, rateWithExtra(1.5, &CostBreakdown{extraMultiplier: 1}))
	require.Equal(t, 4.5, rateWithExtra(1.5, tagged))
}

// ---------------------------------------------------------------------------
// OpenAI 网关：token 路径
// ---------------------------------------------------------------------------

func TestOpenAICost_ExtraMultiplierScalesActualCostOnly(t *testing.T) {
	key := mcKey(Group{ID: 1, Platform: PlatformOpenAI})
	result := &OpenAIForwardResult{Model: "gpt-5.4"}
	base := mcOpenAICost(t, newMPPolicyFor(GroupStateSnapshot{}), key, []string{"gpt-5.4"}, result, 1.5, 1.5)
	require.Zero(t, base.extraMultiplier)

	extra := newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{mpExtra("gpt-5.4", 2)}})
	got := mcOpenAICost(t, extra, key, []string{"gpt-5.4"}, result, 1.5, 1.5)
	mcRequireScaled(t, base, got, 2)
	require.InDelta(t, base.InputCost, got.InputCost, 1e-15, "per-component costs are the unmultiplied prices")
	require.InDelta(t, base.OutputCost, got.OutputCost, 1e-15)
}

// 变体名使用基名上的 extra；字面名上的单元格（哪怕是 inherit）优先。
func TestOpenAICost_VariantNameUsesBaseCellExtra(t *testing.T) {
	key := mcKey(Group{ID: 1, Platform: PlatformOpenAI})
	result := &OpenAIForwardResult{}
	none := newMPPolicyFor(GroupStateSnapshot{})

	policy := newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{
		mpExtra("gpt-5.6-luna", 4),
		mpExtra("gpt-5.4", 2),
		mpExtra("gpt-5.4-mini", 3),
		mpInherit("gpt-5.2-high"), // 变体名上的 inherit：停止查找，不回落到基名
		mpExtra("gpt-5.2", 5),
	}})

	for model, want := range map[string]float64{
		"gpt-5.6-luna-xhigh": 4,
		"gpt-5.4-high":       2,
		"gpt-5.4-mini-high":  3,
	} {
		base := mcOpenAICost(t, none, key, []string{model}, result, 1.5, 1.5)
		got := mcOpenAICost(t, policy, key, []string{model}, result, 1.5, 1.5)
		mcRequireScaled(t, base, got, want)
	}

	base := mcOpenAICost(t, none, key, []string{"gpt-5.2-high"}, result, 1.5, 1.5)
	got := mcOpenAICost(t, policy, key, []string{"gpt-5.2-high"}, result, 1.5, 1.5)
	require.Equal(t, base, got, "a literal inherit cell stops the lookup: no extra, and the cost is exactly the baseline")
	require.Zero(t, got.extraMultiplier)
}

// 候选回退：第一个候选没有价格时，倍率取「实际出价的那个候选」自己的，不是第一个候选的。
func TestOpenAICost_ExtraMultiplierFollowsTheCandidateThatWasPriced(t *testing.T) {
	key := mcKey(Group{ID: 1, Platform: PlatformOpenAI})
	result := &OpenAIForwardResult{}
	none := newMPPolicyFor(GroupStateSnapshot{})
	policy := newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{
		mpExtra("no-such-model-xyz", 5), // 没有官方价，轮不到出价
		mpExtra("gpt-5.4", 2),
		mpExtra("gpt-5.4-mini", 3),
	}})

	// 第一个候选没有价格：回退到 gpt-5.4，乘 2，不是 5。
	base := mcOpenAICost(t, none, key, []string{"no-such-model-xyz", "gpt-5.4"}, result, 1.5, 1.5)
	got := mcOpenAICost(t, policy, key, []string{"no-such-model-xyz", "gpt-5.4"}, result, 1.5, 1.5)
	mcRequireScaled(t, base, got, 2)

	// 第一个候选有价格：第一个成功即返回，用它自己的倍率（3），后面候选的倍率（2）不参与。
	base = mcOpenAICost(t, none, key, []string{"gpt-5.4-mini", "gpt-5.4"}, result, 1.5, 1.5)
	got = mcOpenAICost(t, policy, key, []string{"gpt-5.4-mini", "gpt-5.4"}, result, 1.5, 1.5)
	mcRequireScaled(t, base, got, 3)

	// 所有候选都没有价格：照旧报错（无价不是倍率能解决的）。
	_, err := mcOpenAISvc(policy).calculateOpenAIRecordUsageCost(context.Background(), result, key,
		[]string{"no-such-model-xyz"}, 1.5, 1.5, UsageTokens{InputTokens: 1000}, "", time.Time{})
	require.Error(t, err)
	require.True(t, isUsagePricingUnavailableError(err))
}

func TestOpenAICost_ExtraMultiplierRespectsEffectiveWindow(t *testing.T) {
	key := mcKey(Group{ID: 1, Platform: PlatformOpenAI})
	result := &OpenAIForwardResult{}
	none := newMPPolicyFor(GroupStateSnapshot{})

	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)
	notYet := mpWindow(mpExtra("gpt-5.4", 2), &future, nil)
	expired := mpWindow(mpExtra("gpt-5.4", 2), nil, &past)
	active := mpWindow(mpExtra("gpt-5.4", 2), &past, &future)

	base := mcOpenAICost(t, none, key, []string{"gpt-5.4"}, result, 1.5, 1.5)
	for name, cell := range map[string]StoredMatrixCell{"not yet effective": notYet, "expired": expired} {
		got := mcOpenAICost(t, newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{cell}}), key, []string{"gpt-5.4"}, result, 1.5, 1.5)
		require.Equal(t, base, got, name)
	}
	got := mcOpenAICost(t, newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{active}}), key, []string{"gpt-5.4"}, result, 1.5, 1.5)
	mcRequireScaled(t, base, got, 2)
}

// ---------------------------------------------------------------------------
// OpenAI 网关：图片路径（BK-2 余项）
// ---------------------------------------------------------------------------

// 带额外倍率的图片请求走 calculateOpenAIImageCost(首个候选)：倍率在函数入口按它收到的模型取，
// 乘进图片倍率。单价（分组图片价）不变。
func TestOpenAICost_ImageRequestExtraMultiplierIsAppliedAtImageCostEntry(t *testing.T) {
	price := 0.04
	key := mcKey(Group{ID: 1, Platform: PlatformOpenAI, ImagePrice1K: &price})
	result := &OpenAIForwardResult{Model: "gpt-image-2", ImageCount: 2, ImageSize: "1K"}
	none := newMPPolicyFor(GroupStateSnapshot{})
	policy := newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{mpExtra("gpt-image-2", 3)}})

	base := mcOpenAICost(t, none, key, []string{"gpt-image-2"}, result, 1.0, 1.5)
	require.Equal(t, string(BillingModeImage), base.BillingMode)
	require.InDelta(t, 0.08, base.TotalCost, 1e-12)
	require.InDelta(t, 0.12, base.ActualCost, 1e-12)

	got := mcOpenAICost(t, policy, key, []string{"gpt-image-2"}, result, 1.0, 1.5)
	require.Equal(t, string(BillingModeImage), got.BillingMode)
	mcRequireScaled(t, base, got, 3)
	require.InDelta(t, 0.36, got.ActualCost, 1e-12)
}

// 图片价由另一个候选的渠道价决定时，倍率按「出价的那个模型」取，不是请求里的第一个候选：
// 这里首个候选 gpt-5.4-mini 有额外倍率，但出价的是 gpt-image-2（自定义按次价，没有额外倍率），所以不乘。
func TestOpenAICost_ImageCostUsesTheExtraOfThePricedImageModelNotTheFirstCandidate(t *testing.T) {
	key := mcKey(Group{ID: 1, Platform: PlatformOpenAI})
	result := &OpenAIForwardResult{Model: "gpt-image-2", UpstreamModel: "gpt-5.4-mini", ImageCount: 1, ImageSize: "1K"}
	policy := newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{
		mpExtra("gpt-5.4-mini", 2),
		mcImageCell("gpt-image-2", 0.03),
	}})

	cost := mcOpenAICost(t, policy, key, []string{"gpt-5.4-mini", "gpt-image-2"}, result, 1.0, 1.0)
	require.Equal(t, string(BillingModeImage), cost.BillingMode)
	require.InDelta(t, 0.03, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.03, cost.ActualCost, 1e-12, "the first candidate's multiplier must not leak into the image price")
	require.Zero(t, cost.extraMultiplier)
}

// ---------------------------------------------------------------------------
// legacy 不受影响
// ---------------------------------------------------------------------------

// 证明「legacy 分组计费逐字段不变」：同一份渠道配置（夹具里 gpGroupMain、gpGroupAnthropic 的渠道对下面这些模型
// 有自己的价，比较的是渠道价，不是官方价），两条取策略的路径对照：
//   - 生产默认路径：服务有 channelService、不注入 override，groupPolicy() 现取 legacyPolicy；
//   - 对照路径：同一个 channelService，显式注入 legacyPolicy。
// 三条成本入口（OpenAI 文本、OpenAI 图片、Anthropic）各覆盖一次；每条既比成本函数的整个返回值，
// 也比 RecordUsage 写出的用量行。

func mcLegacyOpenAISvc(cs *ChannelService, explicit bool, logStub *openAIRecordUsageLogRepoStub) *OpenAIGatewayService {
	svc := newOpenAIRecordUsageServiceForTest(logStub, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, &openAIUserGroupRateRepoStub{})
	svc.channelService = cs
	svc.policyOverride = nil
	svc.resolver = &ModelPricingResolver{channelService: cs, billingService: svc.billingService}
	if explicit {
		legacy := newLegacyGroupPolicy(cs)
		svc.policyOverride = legacy
		svc.resolver.policyOverride = legacy
	}
	return svc
}

func mcLegacyGatewaySvc(cs *ChannelService, explicit bool, logStub *openAIRecordUsageLogRepoStub) *GatewayService {
	svc := newGatewayRecordUsageServiceForTest(logStub, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	svc.channelService = cs
	svc.policyOverride = nil
	svc.resolver = &ModelPricingResolver{channelService: cs, billingService: svc.billingService}
	if explicit {
		legacy := newLegacyGroupPolicy(cs)
		svc.policyOverride = legacy
		svc.resolver.policyOverride = legacy
	}
	return svc
}

type mcLegacyCase struct {
	name string
	// wantRate 是 main 上写进 usage_logs.rate_multiplier 的值：main 的写法是
	// usageLog.RateMultiplier = multiplier（文本）/ imageMultiplier（图片），即分组倍率本身，没有任何乘法。
	// 这里把输入的倍率写死，额外倍率为 1 时写回的值必须与它逐位相同（用 1.3、2.7 这类不能精确表示的小数，
	// 任何多余的乘除都会让末位漂移）。
	wantRate float64
	run      func(t *testing.T, cs *ChannelService, explicit bool) (*UsageLog, *CostBreakdown)
}

func mcLegacyCases() []mcLegacyCase {
	ctx := context.Background()
	return []mcLegacyCase{
		{
			name:     "openai token",
			wantRate: 1.3,
			run: func(t *testing.T, cs *ChannelService, explicit bool) (*UsageLog, *CostBreakdown) {
				key := mcKey(Group{ID: gpGroupMain, Platform: PlatformOpenAI, RateMultiplier: 1.3})
				newResult := func() *OpenAIForwardResult {
					return &OpenAIForwardResult{Model: "gpt-5.6-luna", Duration: time.Second, Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 100}}
				}
				logStub := &openAIRecordUsageLogRepoStub{inserted: true}
				svc := mcLegacyOpenAISvc(cs, explicit, logStub)
				cost, err := svc.calculateOpenAIRecordUsageCost(ctx, newResult(), key, []string{"gpt-5.6-luna"}, 1.3, 1.3,
					UsageTokens{InputTokens: 1000, OutputTokens: 100}, "", time.Time{})
				require.NoError(t, err)
				require.NoError(t, svc.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: newResult(), APIKey: key, User: &User{ID: 1}, Account: &Account{ID: 3}}))
				require.NotNil(t, logStub.lastLog)
				return logStub.lastLog, cost
			},
		},
		{
			name:     "openai image",
			wantRate: 2.7,
			run: func(t *testing.T, cs *ChannelService, explicit bool) (*UsageLog, *CostBreakdown) {
				key := mcKey(Group{ID: gpGroupMain, Platform: PlatformOpenAI, RateMultiplier: 1.3, ImageRateIndependent: true, ImageRateMultiplier: 2.7})
				newResult := func() *OpenAIForwardResult {
					return &OpenAIForwardResult{Model: "gpt-image-2", Duration: time.Second, ImageCount: 2, ImageSize: "1K"}
				}
				logStub := &openAIRecordUsageLogRepoStub{inserted: true}
				svc := mcLegacyOpenAISvc(cs, explicit, logStub)
				cost, err := svc.calculateOpenAIRecordUsageCost(ctx, newResult(), key, []string{"gpt-image-2"}, 1.3, 2.7, UsageTokens{}, "", time.Time{})
				require.NoError(t, err)
				require.NoError(t, svc.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: newResult(), APIKey: key, User: &User{ID: 1}, Account: &Account{ID: 3}}))
				require.NotNil(t, logStub.lastLog)
				return logStub.lastLog, cost
			},
		},
		{
			name:     "anthropic token",
			wantRate: 1.3,
			run: func(t *testing.T, cs *ChannelService, explicit bool) (*UsageLog, *CostBreakdown) {
				key := mcKey(Group{ID: gpGroupAnthropic, Platform: PlatformAnthropic, RateMultiplier: 1.3})
				newResult := func() *ForwardResult {
					return &ForwardResult{RequestID: "legacy_ab", Model: "claude-sonnet-4-5", Duration: time.Second, Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 100}}
				}
				logStub := &openAIRecordUsageLogRepoStub{inserted: true}
				svc := mcLegacyGatewaySvc(cs, explicit, logStub)
				cost := svc.calculateRecordUsageCost(ctx, newResult(), key, "claude-sonnet-4-5", 1.3, 1.3, &recordUsageOpts{}, time.Time{})
				require.NoError(t, svc.RecordUsage(ctx, &RecordUsageInput{Result: newResult(), APIKey: key, User: &User{ID: 601}, Account: &Account{ID: 701}}))
				require.NotNil(t, logStub.lastLog)
				return logStub.lastLog, cost
			},
		},
	}
}

func TestCost_LegacyDefaultPathAndExplicitLegacyPolicyAreFieldForFieldIdentical(t *testing.T) {
	for _, tc := range mcLegacyCases() {
		t.Run(tc.name, func(t *testing.T) {
			cs := newGroupPolicyFixture() // 两条路径共用同一个渠道服务
			defLog, defCost := tc.run(t, cs, false)
			expLog, expCost := tc.run(t, cs, true)

			// 比的是真实的渠道价，不是两个零。
			require.Greater(t, defCost.TotalCost, 0.0)
			require.Greater(t, defLog.TotalCost, 0.0)

			// 成本函数：整个返回值逐字段相同，包括未导出的额外倍率标记；legacy 没有额外倍率，标记恒为零。
			require.Equal(t, defCost, expCost)
			require.Zero(t, defCost.extraMultiplier)
			require.Zero(t, expCost.extraMultiplier)

			// 用量行：总价、实付、rate_multiplier 逐位相同。
			require.Equal(t, defLog.TotalCost, expLog.TotalCost)
			require.Equal(t, defLog.ActualCost, expLog.ActualCost)
			require.Equal(t, defLog.RateMultiplier, expLog.RateMultiplier)

			// extra = 1 时写回的 rate_multiplier 与 main 上的写法逐位相同（期望值写死，见 mcLegacyCase.wantRate）。
			require.Equal(t, tc.wantRate, defLog.RateMultiplier)
			require.Equal(t, tc.wantRate, expLog.RateMultiplier)
			require.InDelta(t, defLog.TotalCost*defLog.RateMultiplier, defLog.ActualCost, 1e-12)
		})
	}
}

// 没有 extra 单元格的 v2 分组同样不乘：成本与没有任何策略的服务逐字段相同。
func TestCost_V2GroupWithoutExtraCellsMatchesNoPolicy(t *testing.T) {
	key := mcKey(Group{ID: gpGroupMain, Platform: PlatformOpenAI})
	result := &OpenAIForwardResult{}
	withoutPolicy := mcOpenAICost(t, nil, key, []string{"gpt-5.6-luna"}, result, 1.5, 1.5)
	require.Greater(t, withoutPolicy.TotalCost, 0.0)
	require.Zero(t, withoutPolicy.extraMultiplier)
	require.InDelta(t, withoutPolicy.TotalCost*1.5, withoutPolicy.ActualCost, 1e-12)

	empty := mcOpenAICost(t, newMPPolicyFor(GroupStateSnapshot{}), key, []string{"gpt-5.6-luna"}, result, 1.5, 1.5)
	require.Equal(t, withoutPolicy, empty)
}

// ---------------------------------------------------------------------------
// Anthropic 网关
// ---------------------------------------------------------------------------

func TestGatewayCost_ExtraMultiplierAtCostEntry(t *testing.T) {
	ctx := context.Background()
	key := mcKey(Group{ID: 1, Platform: PlatformAnthropic})
	result := &ForwardResult{Model: "claude-sonnet-4", Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 100}}
	none := newMPPolicyFor(GroupStateSnapshot{})
	policy := newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{mpExtra("claude-sonnet-4", 2.5)}})

	base := mcGatewaySvc(none).calculateRecordUsageCost(ctx, result, key, "claude-sonnet-4", 1.5, 1.5, &recordUsageOpts{}, time.Time{})
	got := mcGatewaySvc(policy).calculateRecordUsageCost(ctx, result, key, "claude-sonnet-4", 1.5, 1.5, &recordUsageOpts{}, time.Time{})
	mcRequireScaled(t, base, got, 2.5)

	// 倍率按选定的计费模型取：换一个没有单元格的模型，不乘。
	other := mcGatewaySvc(policy).calculateRecordUsageCost(ctx, result, key, "claude-opus-4", 1.5, 1.5, &recordUsageOpts{}, time.Time{})
	otherBase := mcGatewaySvc(none).calculateRecordUsageCost(ctx, result, key, "claude-opus-4", 1.5, 1.5, &recordUsageOpts{}, time.Time{})
	require.Equal(t, otherBase, other)
	require.Zero(t, other.extraMultiplier)
}

func TestGatewayCost_ImagePathAlsoGetsTheExtraMultiplier(t *testing.T) {
	ctx := context.Background()
	price := 0.04
	key := mcKey(Group{ID: 1, Platform: PlatformGemini, ImagePrice1K: &price})
	result := &ForwardResult{Model: "gemini-image", ImageCount: 2, ImageSize: "1K"}
	none := newMPPolicyFor(GroupStateSnapshot{})
	policy := newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{mpExtra("gemini-image", 3)}})

	base := mcGatewaySvc(none).calculateRecordUsageCost(ctx, result, key, "gemini-image", 1.0, 1.5, &recordUsageOpts{}, time.Time{})
	require.Equal(t, string(BillingModeImage), base.BillingMode)
	got := mcGatewaySvc(policy).calculateRecordUsageCost(ctx, result, key, "gemini-image", 1.0, 1.5, &recordUsageOpts{}, time.Time{})
	mcRequireScaled(t, base, got, 3)
	require.InDelta(t, 0.08*1.5*3, got.ActualCost, 1e-12)
}

// ---------------------------------------------------------------------------
// 用量行：rate_multiplier 记成「原倍率 x 额外倍率」，与 actual_cost = total_cost x rate_multiplier 保持一致
// ---------------------------------------------------------------------------

func mcRunOpenAIRecordUsage(t *testing.T, policy GroupPolicy) *UsageLog {
	t.Helper()
	logStub := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(logStub, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, &openAIUserGroupRateRepoStub{})
	svc.policyOverride = policy
	svc.resolver = &ModelPricingResolver{policyOverride: policy, billingService: svc.billingService}
	require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result:  &OpenAIForwardResult{Model: "gpt-5.4-high", Duration: time.Second, Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 100}},
		APIKey:  &APIKey{ID: 2, Group: &Group{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1}},
		User:    &User{ID: 1},
		Account: &Account{ID: 3},
	}))
	require.NotNil(t, logStub.lastLog)
	return logStub.lastLog
}

func TestOpenAIRecordUsage_RateMultiplierIncludesExtra(t *testing.T) {
	base := mcRunOpenAIRecordUsage(t, newMPPolicyFor(GroupStateSnapshot{}))
	require.Greater(t, base.ActualCost, 0.0)

	// 请求的是变体名 gpt-5.4-high：额外倍率设在基名 gpt-5.4 上。
	got := mcRunOpenAIRecordUsage(t, newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{mpExtra("gpt-5.4", 2)}}))
	require.InDelta(t, base.TotalCost, got.TotalCost, 1e-15)
	require.InDelta(t, base.ActualCost*2, got.ActualCost, 1e-12)
	require.InDelta(t, base.RateMultiplier*2, got.RateMultiplier, 1e-12)
	require.InDelta(t, got.TotalCost*got.RateMultiplier, got.ActualCost, 1e-12, "actual_cost = total_cost x rate_multiplier still holds")
}

func mcRunGatewayRecordUsage(t *testing.T, policy GroupPolicy) *UsageLog {
	t.Helper()
	logStub := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(logStub, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	svc.policyOverride = policy
	svc.resolver = &ModelPricingResolver{policyOverride: policy, billingService: svc.billingService}
	require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_extra_multiplier",
			Usage:     ClaudeUsage{InputTokens: 1000, OutputTokens: 100},
			Model:     "claude-sonnet-4",
			Duration:  time.Second,
		},
		APIKey:  &APIKey{ID: 501, Group: &Group{ID: 1, Platform: PlatformAnthropic, RateMultiplier: 1}},
		User:    &User{ID: 601},
		Account: &Account{ID: 701},
	}))
	require.NotNil(t, logStub.lastLog)
	return logStub.lastLog
}

func TestGatewayRecordUsage_RateMultiplierIncludesExtra(t *testing.T) {
	base := mcRunGatewayRecordUsage(t, newMPPolicyFor(GroupStateSnapshot{}))
	require.Greater(t, base.ActualCost, 0.0)

	got := mcRunGatewayRecordUsage(t, newMPPolicyFor(GroupStateSnapshot{Cells: []StoredMatrixCell{mpExtra("claude-sonnet-4", 2)}}))
	require.InDelta(t, base.TotalCost, got.TotalCost, 1e-15)
	require.InDelta(t, base.ActualCost*2, got.ActualCost, 1e-12)
	require.InDelta(t, base.RateMultiplier*2, got.RateMultiplier, 1e-12)
	require.InDelta(t, got.TotalCost*got.RateMultiplier, got.ActualCost, 1e-12)
}
