//go:build unit

package service

// W6 PR5：两个网关的影子比对点。
//
// 零行为变化的证明方式：同一个请求，分别在「只有 legacy 策略」和「stagedPolicy（分组处于 shadow 阶段，
// v2 一侧故意与 legacy 不同）」下走完整的 RecordUsage，写出的用量行逐字段相同；差异只出现在指标与样本里。
// 测试里「legacy 一侧」与「v2 一侧」都用 matrixPolicy 构造（查找规则与 legacyPolicy 一致，等价性已在 PR4-1 证明），
// 这样可以随意制造两侧的差异。测试名以 TestPricingShadow_ 开头，CI 的 -race job 会跑它们。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type sgStaged struct {
	policy *stagedPolicy
	sink   *spSink
}

// sgNewStaged 造一个 stagedPolicy：legacy 一侧用 legacySnap，v2 一侧（分组 1，shadow 阶段）用 v2Snap，v2 一侧已预热。
func sgNewStaged(platform string, legacySnap, v2Snap GroupStateSnapshot) *sgStaged {
	legacy, _ := newMPForTest(newMPSource(platform, map[int64]GroupStateSnapshot{1: legacySnap}), nil)
	matrix, clock := newMPForTest(newMPSource(platform, map[int64]GroupStateSnapshot{1: v2Snap}), nil)
	sink := &spSink{}
	staged := newStagedGroupPolicy(legacy, matrix, sink)
	staged.hub.now = clock.Now
	matrix.Stage(context.Background(), 1)
	return &sgStaged{policy: staged, sink: sink}
}

func sgShadowSnap(mutate func(*MatrixGroupConfig), cells ...StoredMatrixCell) GroupStateSnapshot {
	return GroupStateSnapshot{Config: mpStoredConfig(PricingStageShadow, mutate), Cells: cells}
}

func sgLegacySnap(mutate func(*MatrixGroupConfig), cells ...StoredMatrixCell) GroupStateSnapshot {
	return GroupStateSnapshot{Config: mpStoredConfig(PricingStageLegacy, mutate), Cells: cells}
}

func sgFollowBilling(c *MatrixGroupConfig) { c.CostMode = MatrixCostFollowBilling }

func sgDiffCount(s *stagedPolicy, kind, class string) int64 {
	var n int64
	for _, d := range s.Stats().DiffTotal {
		if d.GroupID == 1 && d.Kind == kind && d.Class == class {
			n += d.Count
		}
	}
	return n
}

func sgCompared(s *stagedPolicy) int64 {
	var n int64
	for _, c := range s.Stats().ComparedTotal {
		if c.GroupID == 1 {
			n += c.Count
		}
	}
	return n
}

func sgRunOpenAI(t *testing.T, policy GroupPolicy, withGroupID bool) *UsageLog {
	t.Helper()
	logStub := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(logStub, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, &openAIUserGroupRateRepoStub{})
	svc.policyOverride = policy
	svc.resolver = &ModelPricingResolver{policyOverride: policy, billingService: svc.billingService}
	gid := int64(1)
	key := &APIKey{ID: 2, Group: &Group{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1}}
	if withGroupID {
		key.GroupID = &gid
	}
	require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result:  &OpenAIForwardResult{RequestID: "req-openai-shadow", Model: "gpt-5.4-high", Duration: time.Second, Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 100}},
		APIKey:  key,
		User:    &User{ID: 1},
		Account: &Account{ID: 3},
	}))
	require.NotNil(t, logStub.lastLog)
	return logStub.lastLog
}

func sgRunGateway(t *testing.T, policy GroupPolicy, withGroupID bool) *UsageLog {
	t.Helper()
	logStub := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(logStub, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	svc.policyOverride = policy
	svc.resolver = &ModelPricingResolver{policyOverride: policy, billingService: svc.billingService}
	gid := int64(1)
	key := &APIKey{ID: 501, Group: &Group{ID: 1, Platform: PlatformAnthropic, RateMultiplier: 1}}
	if withGroupID {
		key.GroupID = &gid
	}
	require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result:  &ForwardResult{RequestID: "req-gateway-shadow", Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 100}, Model: "claude-sonnet-4", Duration: time.Second},
		APIKey:  key,
		User:    &User{ID: 601},
		Account: &Account{ID: 701},
	}))
	require.NotNil(t, logStub.lastLog)
	return logStub.lastLog
}

// requireSameBilling 逐字段比较两行用量记录里和计费有关的全部字段。
func requireSameBilling(t *testing.T, want, got *UsageLog) {
	t.Helper()
	require.Equal(t, want.InputCost, got.InputCost)
	require.Equal(t, want.OutputCost, got.OutputCost)
	require.Equal(t, want.CacheCreationCost, got.CacheCreationCost)
	require.Equal(t, want.CacheReadCost, got.CacheReadCost)
	require.Equal(t, want.ImageOutputCost, got.ImageOutputCost)
	require.Equal(t, want.TotalCost, got.TotalCost)
	require.Equal(t, want.ActualCost, got.ActualCost)
	require.Equal(t, want.RateMultiplier, got.RateMultiplier)
	require.Equal(t, want.BillingMode, got.BillingMode)
	require.Equal(t, want.AccountStatsCost, got.AccountStatsCost)
	require.Equal(t, want.AccountRateMultiplier, got.AccountRateMultiplier)
	require.Equal(t, want.GroupID, got.GroupID)
	require.Equal(t, want.RequestID, got.RequestID)
}

func sgDecodeCostView(t *testing.T, raw json.RawMessage) shadowCostView {
	t.Helper()
	var v shadowCostView
	require.NoError(t, json.Unmarshal(raw, &v))
	return v
}

func TestPricingShadow_OpenAIRecordUsageIsUnchangedAndDifferenceIsReported(t *testing.T) {
	base := sgRunOpenAI(t, newMPPolicyFor(GroupStateSnapshot{}), false)
	require.Greater(t, base.ActualCost, 0.0)

	// v2 一侧对 gpt-5.4 设了 x2 额外倍率，legacy 一侧没有：结算必须仍然是 legacy 的结果。
	st := sgNewStaged(PlatformOpenAI, sgLegacySnap(nil), sgShadowSnap(nil, mpExtra("gpt-5.4", 2)))
	got := sgRunOpenAI(t, st.policy, false)
	requireSameBilling(t, base, got)

	require.EqualValues(t, 1, sgCompared(st.policy))
	require.EqualValues(t, 1, sgDiffCount(st.policy, ShadowKindCost, ShadowClassTranslation))
	samples := st.sink.all()
	require.Len(t, samples, 1)
	require.Equal(t, ShadowKindCost, samples[0].Kind)
	require.Equal(t, "req-openai-shadow", samples[0].UsageRef)
	legacyView := sgDecodeCostView(t, samples[0].LegacyView)
	v2View := sgDecodeCostView(t, samples[0].V2View)
	require.Equal(t, base.ActualCost, legacyView.ActualCost)
	require.InDelta(t, base.ActualCost*2, v2View.ActualCost, 1e-12)
	require.Equal(t, 2.0, v2View.ExtraMultiplier)
	require.Equal(t, base.TotalCost, v2View.TotalCost, "the unit price is not changed by the extra multiplier")
}

func TestPricingShadow_OpenAIIdenticalConfigurationHasNoDiff(t *testing.T) {
	cells := []StoredMatrixCell{mpExtra("gpt-5.4", 2)}
	base := sgRunOpenAI(t, newMPPolicyFor(GroupStateSnapshot{Cells: cells}), true)

	st := sgNewStaged(PlatformOpenAI, sgLegacySnap(nil, cells...), sgShadowSnap(nil, cells...))
	got := sgRunOpenAI(t, st.policy, true)
	requireSameBilling(t, base, got)

	// 成本与账号成本各比一次，一次结算算一次比对。
	require.EqualValues(t, 1, sgCompared(st.policy))
	require.Empty(t, st.policy.Stats().DiffTotal)
	require.Empty(t, st.sink.all())
}

func TestPricingShadow_OpenAIAccountCostDiffIsReportedOnACopy(t *testing.T) {
	// legacy 一侧「跟随计费」，v2 一侧默认的 account_rate：账号成本一个有值、一个是 nil。
	base := sgRunOpenAI(t, newMPPolicyFor(sgLegacySnap(sgFollowBilling)), true)
	require.NotNil(t, base.AccountStatsCost, "the legacy side sets an account stats cost")

	st := sgNewStaged(PlatformOpenAI, sgLegacySnap(sgFollowBilling), sgShadowSnap(nil))
	got := sgRunOpenAI(t, st.policy, true)
	requireSameBilling(t, base, got)
	require.NotNil(t, got.AccountStatsCost, "the settlement usage log keeps the legacy account cost: the shadow ran on a copy")
	require.Equal(t, *base.AccountStatsCost, *got.AccountStatsCost)

	require.EqualValues(t, 1, sgDiffCount(st.policy, ShadowKindAccountCost, ShadowClassTranslation))
	require.Zero(t, sgDiffCount(st.policy, ShadowKindCost, ShadowClassTranslation))
}

// 无价模型：结算那一次计一次 PR1 的无价指标；影子重算带非结算标记，不重复计数。
func TestPricingShadow_RecomputeDoesNotDoubleCountUnpricedBilling(t *testing.T) {
	run := func(policy GroupPolicy) *UsageLog {
		logStub := &openAIRecordUsageLogRepoStub{inserted: true}
		svc := newOpenAIRecordUsageServiceForTest(logStub, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, &openAIUserGroupRateRepoStub{})
		svc.policyOverride = policy
		svc.resolver = &ModelPricingResolver{policyOverride: policy, billingService: svc.billingService}
		require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
			Result:  &OpenAIForwardResult{RequestID: "req-unpriced", Model: unpricedTestModel, Duration: time.Second, Usage: OpenAIUsage{InputTokens: 10, OutputTokens: 5}},
			APIKey:  &APIKey{ID: 2, Group: &Group{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1}},
			User:    &User{ID: 1},
			Account: &Account{ID: 3},
		}))
		return logStub.lastLog
	}

	resetUnpricedBillingCountersForTest()
	base := run(newMPPolicyFor(GroupStateSnapshot{}))
	want := UnpricedBillingCounterSnapshot()
	require.NotEmpty(t, want, "the settlement notes the unpriced request once")

	resetUnpricedBillingCountersForTest()
	st := sgNewStaged(PlatformOpenAI, sgLegacySnap(nil), sgShadowSnap(nil))
	got := run(st.policy)
	require.Equal(t, want, UnpricedBillingCounterSnapshot(), "the shadow recomputation is not counted again")
	requireSameBilling(t, base, got)
	require.EqualValues(t, 1, sgCompared(st.policy))
	require.Empty(t, st.policy.Stats().DiffTotal, "both sides are unpriced: identical error class, no diff")
}

func TestPricingShadow_GatewayRecordUsageIsUnchangedAndDifferenceIsReported(t *testing.T) {
	base := sgRunGateway(t, newMPPolicyFor(GroupStateSnapshot{}), false)
	require.Greater(t, base.ActualCost, 0.0)

	st := sgNewStaged(PlatformAnthropic, sgLegacySnap(nil), sgShadowSnap(nil, mpExtra("claude-sonnet-4", 2.5)))
	got := sgRunGateway(t, st.policy, false)
	requireSameBilling(t, base, got)

	require.EqualValues(t, 1, sgCompared(st.policy))
	require.EqualValues(t, 1, sgDiffCount(st.policy, ShadowKindCost, ShadowClassTranslation))
	samples := st.sink.all()
	require.Len(t, samples, 1)
	require.Equal(t, "req-gateway-shadow", samples[0].UsageRef)
	require.Equal(t, "claude-sonnet-4", samples[0].Model)
	v2View := sgDecodeCostView(t, samples[0].V2View)
	require.Equal(t, 2.5, v2View.ExtraMultiplier)
	require.Equal(t, "claude-sonnet-4", v2View.Model)
}

func TestPricingShadow_GatewayIdenticalConfigurationAndAccountCost(t *testing.T) {
	cells := []StoredMatrixCell{mpExtra("claude-sonnet-4", 2)}
	base := sgRunGateway(t, newMPPolicyFor(GroupStateSnapshot{Cells: cells}), true)

	same := sgNewStaged(PlatformAnthropic, sgLegacySnap(nil, cells...), sgShadowSnap(nil, cells...))
	requireSameBilling(t, base, sgRunGateway(t, same.policy, true))
	require.EqualValues(t, 1, sgCompared(same.policy))
	require.Empty(t, same.policy.Stats().DiffTotal)

	// 账号成本差异：结算的用量行不变，差异记在 account_cost 上。
	followBase := sgRunGateway(t, newMPPolicyFor(sgLegacySnap(sgFollowBilling)), true)
	require.NotNil(t, followBase.AccountStatsCost)
	diff := sgNewStaged(PlatformAnthropic, sgLegacySnap(sgFollowBilling), sgShadowSnap(nil))
	got := sgRunGateway(t, diff.policy, true)
	requireSameBilling(t, followBase, got)
	require.EqualValues(t, 1, sgDiffCount(diff.policy, ShadowKindAccountCost, ShadowClassTranslation))
}

// legacy 阶段的分组：stagedPolicy 不做任何比对，用量行与 legacy 逐字段相同。
func TestPricingShadow_LegacyStageGroupIsNeverCompared(t *testing.T) {
	base := sgRunOpenAI(t, newMPPolicyFor(GroupStateSnapshot{}), true)
	st := sgNewStaged(PlatformOpenAI, sgLegacySnap(nil), GroupStateSnapshot{Config: mpStoredConfig(PricingStageLegacy, nil), Cells: []StoredMatrixCell{mpExtra("gpt-5.4", 9)}})
	got := sgRunOpenAI(t, st.policy, true)
	requireSameBilling(t, base, got)
	require.Empty(t, st.policy.Stats().ComparedTotal)
	require.Empty(t, st.policy.Stats().DiffTotal)
}

// 快照是兜底或沿用的旧数据、刚失效过时，成本比对被跳过，不产生噪声。
func TestPricingShadow_CostComparisonSkipsUnreliableSnapshots(t *testing.T) {
	st := sgNewStaged(PlatformOpenAI, sgLegacySnap(nil), sgShadowSnap(nil, mpExtra("gpt-5.4", 2)))
	st.policy.matrix.InvalidateGroups(1)
	base := sgRunOpenAI(t, newMPPolicyFor(GroupStateSnapshot{}), false)
	got := sgRunOpenAI(t, st.policy, false)
	requireSameBilling(t, base, got)
	require.Empty(t, st.policy.Stats().ComparedTotal)
	require.EqualValues(t, 1, st.policy.Stats().SkippedTotal[ShadowSkipRecentChange])
}

func TestPricingShadow_BeginSessionGuards(t *testing.T) {
	ctx := context.Background()
	st := sgNewStaged(PlatformOpenAI, sgLegacySnap(nil), sgShadowSnap(nil))

	require.Nil(t, beginPricingShadow(ctx, nil, 1), "no policy")
	require.Nil(t, beginPricingShadow(ctx, legacyPolicy{}, 1), "not a staged policy")
	require.Nil(t, beginPricingShadow(ctx, st.policy, 0), "no group")
	require.Nil(t, beginPricingShadow(withShadowRecompute(ctx, PricingStageV2), st.policy, 1), "no nested shadow")
	require.Nil(t, beginPricingShadow(ctx, newStagedGroupPolicy(legacyPolicy{}, nil, nil), 1), "no matrix")

	sess := beginPricingShadow(ctx, st.policy, 1)
	require.NotNil(t, sess)
	stage, forced := forcedStageFromCtx(sess.ctx)
	require.True(t, forced)
	require.Equal(t, PricingStageV2, stage)
	require.True(t, IsBillingNonSettlement(sess.ctx))

	// 会话里的 panic 不外泄。
	require.NotPanics(t, func() { sess.run(func(context.Context) { panic("recompute exploded") }) })
	require.EqualValues(t, 1, st.policy.Stats().PanicsTotal)
	require.Empty(t, st.policy.Stats().ComparedTotal)

	// 冷启动（还没有快照）：不比对。
	cold := newStagedGroupPolicy(legacyPolicy{}, newMPPolicyFor(sgShadowSnap(nil)), nil)
	require.Nil(t, beginPricingShadow(ctx, cold, 1))
}

// 影子重算里 billableModelWithFallback 跳过 falling back 日志，但选出的模型与结算那次相同。
func TestPricingShadow_FallbackSelectionIsUnaffectedByTheShadowMarker(t *testing.T) {
	svc := mcGatewaySvc(newMPPolicyFor(GroupStateSnapshot{}))
	key := mcKey(Group{ID: 1, Platform: PlatformAnthropic})
	plain := svc.billableModelWithFallback(context.Background(), key, unpricedTestModel, unpricedTestModel, "claude-sonnet-4")
	shadow := svc.billableModelWithFallback(withShadowRecompute(context.Background(), PricingStageV2), key, unpricedTestModel, unpricedTestModel, "claude-sonnet-4")
	require.Equal(t, "claude-sonnet-4", plain, "the unpriced alias falls back to the concrete model")
	require.Equal(t, plain, shadow)
}
