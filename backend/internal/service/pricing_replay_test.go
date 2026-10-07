//go:build unit

package service

// W6 PR6：回放比较引擎（pricing_replay.go）的测试。
//
// 两侧策略都用 matrixPolicy 构造（查找规则与 legacyPolicy 等价，PR4-1 已证明），这样可以随意制造两侧的差异。
// 关键的一致性保证来自「回放 legacy 一侧的成本 == 同一个输入走 RecordUsage 写出的 actual_cost」那几个测试：
// 回放里复刻了网关里计费模型候选的推导，这些测试守住它不会和网关漂移。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const prOpenAIModel = "gpt-5.4-high"

func prSnap(mutate func(*MatrixGroupConfig), cells ...StoredMatrixCell) GroupStateSnapshot {
	return GroupStateSnapshot{Config: mpStoredConfig(PricingStageShadow, mutate), Cells: cells}
}

// prReplayer 构造回放器：legacy 与 v2 各有自己的分组快照（分组 1、2）。
func prReplayer(platform string, legacy, v2 map[int64]GroupStateSnapshot) *PricingReplayer {
	l, _ := newMPForTest(newMPSource(platform, legacy), nil)
	v, _ := newMPForTest(newMPSource(platform, v2), nil)
	return newPricingReplayer(newTestBillingService(), l, v)
}

func prSame(snap GroupStateSnapshot) map[int64]GroupStateSnapshot {
	return map[int64]GroupStateSnapshot{1: snap, 2: snap}
}

func prRow(id int64, model string) PricingReplayRow {
	return PricingReplayRow{
		ID: id, CreatedAt: mpT0.Add(-time.Hour), UserID: 7, AccountID: 3, GroupID: 1,
		Model: model, RequestedModel: model, InputTokens: 1000, OutputTokens: 100,
		Group:      &Group{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1},
		Multiplier: 1,
	}
}

func prKinds(diffs []PricingReplayDiff) map[string]string {
	out := make(map[string]string, len(diffs))
	for _, d := range diffs {
		out[d.Kind] = d.Class + "/" + d.Reason
	}
	return out
}

func TestPricingReplay_IdenticalConfigurationHasNoDiff(t *testing.T) {
	snap := prSnap(sgFollowBilling, mpExtra("gpt-5.4", 2))
	r := prReplayer(PlatformOpenAI, prSame(snap), prSame(snap))
	row := prRow(1, prOpenAIModel)

	out := r.Replay(context.Background(), &row)
	require.True(t, out.Covered)
	require.Empty(t, out.Err)
	require.Empty(t, out.Diffs)
	require.Greater(t, out.LegacyActualCost, 0.0)
	require.Equal(t, out.LegacyActualCost, out.V2ActualCost)
	require.False(t, out.LegacyCostFailed || out.V2CostFailed)
}

func TestPricingReplay_CostDifferenceIsATranslationDiff(t *testing.T) {
	r := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), prSame(prSnap(nil, mpExtra("gpt-5.4", 2))))
	row := prRow(1, prOpenAIModel)

	out := r.Replay(context.Background(), &row)
	require.Equal(t, map[string]string{ShadowKindCost: ShadowClassTranslation + "/"}, prKinds(out.Diffs))
	require.InDelta(t, out.LegacyActualCost*2, out.V2ActualCost, 1e-12)
	require.Equal(t, prOpenAIModel, out.Diffs[0].Model)
}

func TestPricingReplay_AccountCostDifferenceIsReportedWithoutACostDiff(t *testing.T) {
	r := prReplayer(PlatformOpenAI, prSame(prSnap(sgFollowBilling)), prSame(prSnap(nil)))
	row := prRow(1, prOpenAIModel)

	out := r.Replay(context.Background(), &row)
	require.Equal(t, map[string]string{ShadowKindAccountCost: ShadowClassTranslation + "/"}, prKinds(out.Diffs))
}

func TestPricingReplay_MappingDifferenceIsReported(t *testing.T) {
	mapped := func(c *MatrixGroupConfig) {
		c.ModelMapping = []MatrixMappingEntry{{Src: prOpenAIModel, Dst: "gpt-5.4"}}
		c.BillingModelSource = mpS(BillingModelSourceChannelMapped)
	}
	r := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), prSame(prSnap(mapped)))
	row := prRow(1, prOpenAIModel)

	out := r.Replay(context.Background(), &row)
	kinds := prKinds(out.Diffs)
	require.Equal(t, ShadowClassTranslation+"/", kinds[ShadowKindMapping])
	// 计费模型随映射变了，成本视图的模型标签也不同。
	require.Contains(t, kinds, ShadowKindCost)
}

func TestPricingReplay_AccessDifferences(t *testing.T) {
	row := prRow(1, prOpenAIModel)

	// v2 单元格 open=false：legacy 放行、v2 关闭，是 v2 新增的例外语义，标 expected。
	closed := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), prSame(prSnap(nil, mpClosed(mpInherit(prOpenAIModel)))))
	out := closed.Replay(context.Background(), &row)
	require.Equal(t, map[string]string{ShadowKindAccess: ShadowClassExpected + "/" + PricingReplayReasonClosedInGroup}, prKinds(out.Diffs))

	// v2 是白名单分组而模型不在名单里：翻译差异。
	allowlist := func(c *MatrixGroupConfig) { c.AccessMode = MatrixAccessAllowlist }
	missing := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), prSame(prSnap(allowlist)))
	out = missing.Replay(context.Background(), &row)
	require.Equal(t, map[string]string{ShadowKindAccess: ShadowClassTranslation + "/"}, prKinds(out.Diffs))
}

func TestPricingReplay_UpstreamAccessIsComparedOnlyWhenACheckIsRequired(t *testing.T) {
	row := prRow(1, prOpenAIModel)
	row.UpstreamModel = "gpt-5.4-upstream"

	// 两侧都是白名单分组、计费来源是 upstream（要求逐账号检查），名单里只有请求模型：legacy 同样放行请求模型。
	upstreamCheck := func(c *MatrixGroupConfig) {
		c.AccessMode = MatrixAccessAllowlist
		c.BillingModelSource = mpS(BillingModelSourceUpstream)
	}
	same := prSnap(upstreamCheck, mpInherit(prOpenAIModel), mpInherit("gpt-5.4-upstream"))
	r := prReplayer(PlatformOpenAI, prSame(same), prSame(same))
	require.Empty(t, r.Replay(context.Background(), &row).Diffs)

	// legacy 名单里没有上游模型、v2 有：上游准入不同。
	legacy := prSnap(upstreamCheck, mpInherit(prOpenAIModel))
	r = prReplayer(PlatformOpenAI, prSame(legacy), prSame(same))
	diffs := r.Replay(context.Background(), &row).Diffs
	require.Len(t, diffs, 1)
	require.Equal(t, ShadowKindAccess, diffs[0].Kind)
	require.Equal(t, "gpt-5.4-upstream", diffs[0].Model)
}

func TestPricingReplay_UsesTheServedGroupAndItsMultiplier(t *testing.T) {
	// 分组 2 在 v2 一侧有额外倍率、分组 1 没有；行的主分组是 1、服务分组是 2：按服务分组比较。
	v2 := map[int64]GroupStateSnapshot{1: prSnap(nil), 2: prSnap(nil, mpExtra("gpt-5.4", 3))}
	r := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), v2)

	row := prRow(1, prOpenAIModel)
	row.ServedGroupID = 2
	row.Group = &Group{ID: 2, Platform: PlatformOpenAI, RateMultiplier: 4}
	row.Multiplier = 4
	require.Equal(t, int64(2), row.EffectiveGroupID())

	out := r.Replay(context.Background(), &row)
	require.Equal(t, ShadowKindCost, out.Diffs[0].Kind)
	require.InDelta(t, out.LegacyActualCost*3, out.V2ActualCost, 1e-12)

	home := prRow(2, prOpenAIModel)
	require.Equal(t, int64(1), home.EffectiveGroupID())
	require.Empty(t, r.Replay(context.Background(), &home).Diffs)
}

func TestPricingReplay_RowsWithoutInputsAreNotCovered(t *testing.T) {
	r := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), prSame(prSnap(nil)))

	noModel := prRow(1, "")
	noTime := prRow(2, prOpenAIModel)
	noTime.CreatedAt = time.Time{}
	noGroup := prRow(3, prOpenAIModel)
	noGroup.Group = nil
	for _, row := range []PricingReplayRow{noModel, noTime, noGroup} {
		out := r.Replay(context.Background(), &row)
		require.False(t, out.Covered)
		require.Empty(t, out.Diffs)
		require.Empty(t, out.Err)
	}

	// 请求模型为空时取 model，反之亦然（RecordUsage 里 requestedModel 缺省就是 result.Model）。
	onlyModel := prRow(4, prOpenAIModel)
	onlyModel.RequestedModel = ""
	require.True(t, r.Replay(context.Background(), &onlyModel).Covered)
	onlyRequested := prRow(5, prOpenAIModel)
	onlyRequested.Model = ""
	require.True(t, r.Replay(context.Background(), &onlyRequested).Covered)
}

// panicPolicy 任何方法都会 panic（嵌入的接口为 nil）。
type panicPolicy struct{ GroupPolicy }

func TestPricingReplay_PanicIsContained(t *testing.T) {
	ok, _ := newMPForTest(newMPSource(PlatformOpenAI, prSame(prSnap(nil))), nil)
	r := newPricingReplayer(newTestBillingService(), panicPolicy{}, ok)
	row := prRow(1, prOpenAIModel)

	var out PricingReplayOutcome
	require.NotPanics(t, func() { out = r.Replay(context.Background(), &row) })
	require.False(t, out.Covered)
	require.Contains(t, out.Err, "panic")
}

func TestPricingReplay_CheckGroupFeaturesAndUpstreamCheck(t *testing.T) {
	ctx := context.Background()
	group := &Group{ID: 1, Platform: PlatformAnthropic}

	same := prSnap(func(c *MatrixGroupConfig) { c.Features = map[string]any{featureKeyBedrockCCCompat: true} })
	r := prReplayer(PlatformAnthropic, prSame(same), prSame(same))
	diffs, err := r.CheckGroup(ctx, group)
	require.NoError(t, err)
	require.Empty(t, diffs)
	diffs, err = r.CheckGroup(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, diffs)

	// legacy 开了 bedrock 兼容、v2 没有；legacy 要求逐账号检查上游准入、v2 不要求。
	legacy := prSnap(func(c *MatrixGroupConfig) {
		c.Features = map[string]any{featureKeyBedrockCCCompat: true}
		c.AccessMode = MatrixAccessAllowlist
		c.BillingModelSource = mpS(BillingModelSourceUpstream)
	})
	r = prReplayer(PlatformAnthropic, prSame(legacy), prSame(prSnap(nil)))
	diffs, err = r.CheckGroup(ctx, group)
	require.NoError(t, err)
	models := map[string]bool{}
	for _, d := range diffs {
		require.Equal(t, ShadowKindFeature, d.Kind)
		require.Equal(t, ShadowClassTranslation, d.Class)
		models[d.Model] = true
	}
	require.Equal(t, map[string]bool{"upstream_check": true, string(GroupFeatureBedrockCCCompat): true}, models)
}

// 「未配置」与 false：web_search_emulation 和 bedrock_cc_compat 等价（运行时读取方都把 nil 当 false），
// codex 图片桥不等价（nil 跟随全局开关），仍然严格比较。
func TestPricingReplay_CheckGroupFeatureEquivalence(t *testing.T) {
	ctx := context.Background()
	group := &Group{ID: 1, Platform: PlatformOpenAI}
	features := func(m map[string]any) func(*MatrixGroupConfig) {
		return func(c *MatrixGroupConfig) { c.Features = m }
	}
	models := func(r *PricingReplayer) map[string]bool {
		diffs, err := r.CheckGroup(ctx, group)
		require.NoError(t, err)
		out := map[string]bool{}
		for _, d := range diffs {
			out[d.Model] = true
		}
		return out
	}

	// legacy 显式关闭、v2 没有配置：两个开关都等价，没有差异。
	off := prSnap(features(map[string]any{
		featureKeyWebSearchEmulation: map[string]any{PlatformOpenAI: false}, featureKeyBedrockCCCompat: false,
	}))
	require.Empty(t, models(prReplayer(PlatformOpenAI, prSame(off), prSame(prSnap(nil)))))

	// codex 桥：legacy 显式 false、v2 未配置，要求严格比较，有差异。
	codexOff := prSnap(features(map[string]any{featureKeyCodexImageGenerationBridge: false}))
	require.Equal(t, map[string]bool{string(GroupFeatureCodexImageGenerationBridge): true},
		models(prReplayer(PlatformOpenAI, prSame(codexOff), prSame(prSnap(nil)))))

	// 开启与未配置不等价。
	on := prSnap(features(map[string]any{featureKeyBedrockCCCompat: true}))
	require.Equal(t, map[string]bool{string(GroupFeatureBedrockCCCompat): true},
		models(prReplayer(PlatformOpenAI, prSame(on), prSame(prSnap(nil)))))
}

func TestGroupFeatureEquivalent(t *testing.T) {
	f, tr := false, true
	for _, feat := range []GroupFeature{GroupFeatureWebSearchEmulation, GroupFeatureBedrockCCCompat} {
		require.True(t, groupFeatureEquivalent(feat, nil, &f))
		require.True(t, groupFeatureEquivalent(feat, &f, nil))
		require.True(t, groupFeatureEquivalent(feat, nil, nil))
		require.False(t, groupFeatureEquivalent(feat, nil, &tr))
	}
	require.False(t, groupFeatureEquivalent(GroupFeatureCodexImageGenerationBridge, nil, &f))
	require.True(t, groupFeatureEquivalent(GroupFeatureCodexImageGenerationBridge, nil, nil))
	require.True(t, groupFeatureEquivalent(GroupFeatureCodexImageGenerationBridge, &f, &f))
}

func TestPricingReplay_CheckGroupPanicIsAnError(t *testing.T) {
	ok, _ := newMPForTest(newMPSource(PlatformOpenAI, prSame(prSnap(nil))), nil)
	r := newPricingReplayer(newTestBillingService(), panicPolicy{}, ok)
	diffs, err := r.CheckGroup(context.Background(), &Group{ID: 1, Platform: PlatformOpenAI})
	require.ErrorContains(t, err, "policy panic")
	require.Empty(t, diffs)
}

func TestPricingReplay_ClassifiesExpectedDifferences(t *testing.T) {
	zero := &CostBreakdown{BillingMode: string(BillingModeToken)}
	priced := &CostBreakdown{TotalCost: 1, ActualCost: 1, BillingMode: string(BillingModeToken)}
	side := func(err error, settled *CostBreakdown) replaySide {
		return replaySide{label: "m", view: newShadowCostView("m", settled, err), settled: settled}
	}
	unpriced := ErrModelPricingUnavailable

	// 一侧报无价、结算后与另一侧同样是 0 元：错误形态不同，实付相同，expected。
	require.True(t, replayUnpricedZeroEquivalent(ptrSide(side(nil, zero)), ptrSide(side(unpriced, zero))))
	require.True(t, replayUnpricedZeroEquivalent(ptrSide(side(unpriced, zero)), ptrSide(side(nil, zero))))
	// 实付不同（候选回退让一侧算出了非零价）：仍是翻译差异。
	require.False(t, replayUnpricedZeroEquivalent(ptrSide(side(nil, priced)), ptrSide(side(unpriced, zero))))
	// 两侧都无价或都有价：不是这一类。
	require.False(t, replayUnpricedZeroEquivalent(ptrSide(side(unpriced, zero)), ptrSide(side(unpriced, zero))))
	require.False(t, replayUnpricedZeroEquivalent(ptrSide(side(nil, zero)), ptrSide(side(nil, zero))))
	// 其他错误没有结算成本，不算。
	require.False(t, replayUnpricedZeroEquivalent(ptrSide(side(nil, zero)), ptrSide(replaySide{view: shadowCostView{Error: "calc_error"}})))

	// 完整路径：legacy 的成本函数没有报错、v2 报无价，都按 0 元结算。
	in := &replayInput{requested: "m", model: "m"}
	diffs := (&PricingReplayer{}).compareSides(in, ptrSide(side(nil, zero)), ptrSide(side(unpriced, zero)))
	require.Len(t, diffs, 1)
	require.Equal(t, ShadowClassExpected, diffs[0].Class)
	require.Equal(t, PricingReplayReasonUnpricedZero, diffs[0].Reason)
	diffs = (&PricingReplayer{}).compareSides(in, ptrSide(side(nil, priced)), ptrSide(side(unpriced, zero)))
	require.Len(t, diffs, 1)
	require.Equal(t, ShadowClassTranslation, diffs[0].Class)
}

func ptrSide(s replaySide) *replaySide { return &s }

func TestPricingReplay_UntrimmedModelNamesAreExpected(t *testing.T) {
	legacy := ptrSide(replaySide{mapping: ChannelMappingResult{MappedModel: " m"}, access: QuoteAccess{OK: true}})
	v2 := ptrSide(replaySide{mapping: ChannelMappingResult{MappedModel: "m", Mapped: true}, access: QuoteAccess{OK: false, Reason: QuoteAccessReasonNotInAllowlist}})

	diffs := (&PricingReplayer{}).compareSides(&replayInput{requested: " m", model: " m"}, legacy, v2)
	require.Len(t, diffs, 2)
	for _, d := range diffs {
		require.Equal(t, ShadowClassExpected, d.Class)
		require.Equal(t, PricingReplayReasonUntrimmedModel, d.Reason)
	}

	diffs = (&PricingReplayer{}).compareSides(&replayInput{requested: "m", model: "m"}, legacy, v2)
	require.Len(t, diffs, 2)
	for _, d := range diffs {
		require.Equal(t, ShadowClassTranslation, d.Class)
	}
}

// ---------------------------------------------------------------------------
// 与网关结算逐位一致：同一个输入走 RecordUsage，写出的 actual_cost 必须等于回放 legacy 一侧的结果
// ---------------------------------------------------------------------------

func TestPricingReplay_MatchesOpenAIRecordUsage(t *testing.T) {
	mapped := func(c *MatrixGroupConfig) {
		c.ModelMapping = []MatrixMappingEntry{{Src: prOpenAIModel, Dst: "gpt-5.4"}}
		c.BillingModelSource = mpS(BillingModelSourceChannelMapped)
	}
	requested := func(c *MatrixGroupConfig) { c.BillingModelSource = mpS(BillingModelSourceRequested) }
	for name, snap := range map[string]GroupStateSnapshot{
		"no channel":    prSnap(nil),
		"extra":         prSnap(nil, mpExtra("gpt-5.4", 2)),
		"channel price": prSnap(nil, mpCustom("gpt-5.4", 2e-6)),
		"mapped":        prSnap(mapped, mpExtra("gpt-5.4", 1.5)),
		"requested":     prSnap(requested),
	} {
		t.Run(name, func(t *testing.T) {
			policy, _ := newMPForTest(newMPSource(PlatformOpenAI, map[int64]GroupStateSnapshot{1: snap}), nil)
			ctx := context.Background()

			gid := int64(1)
			logStub := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(logStub, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, &openAIUserGroupRateRepoStub{})
			svc.policyOverride = policy
			svc.resolver = &ModelPricingResolver{policyOverride: policy, billingService: svc.billingService}
			require.NoError(t, svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
				Result:             &OpenAIForwardResult{RequestID: "req", Model: prOpenAIModel, Duration: time.Second, Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 100}},
				APIKey:             &APIKey{ID: 2, GroupID: &gid, Group: &Group{ID: 1, Platform: PlatformOpenAI, RateMultiplier: 1}},
				User:               &User{ID: 1},
				Account:            &Account{ID: 3},
				ChannelUsageFields: policy.Mapping(ctx, 1, prOpenAIModel).ToUsageFields(prOpenAIModel, ""),
			}))
			want := logStub.lastLog
			require.Greater(t, want.ActualCost, 0.0)

			r := newPricingReplayer(newTestBillingService(), policy, policy)
			row := prRow(1, prOpenAIModel)
			got := r.Replay(ctx, &row)
			require.True(t, got.Covered)
			require.Empty(t, got.Diffs)
			require.Equal(t, want.ActualCost, got.LegacyActualCost)
		})
	}
}

func TestPricingReplay_MatchesGatewayRecordUsage(t *testing.T) {
	snap := prSnap(nil, mpExtra("claude-sonnet-4", 1.5))
	policy, _ := newMPForTest(newMPSource(PlatformAnthropic, map[int64]GroupStateSnapshot{1: snap}), nil)
	ctx := context.Background()

	gid := int64(1)
	logStub := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(logStub, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	svc.policyOverride = policy
	svc.resolver = &ModelPricingResolver{policyOverride: policy, billingService: svc.billingService}
	require.NoError(t, svc.RecordUsage(ctx, &RecordUsageInput{
		Result:             &ForwardResult{RequestID: "req", Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 100, CacheReadInputTokens: 50}, Model: "claude-sonnet-4", Duration: time.Second},
		APIKey:             &APIKey{ID: 501, GroupID: &gid, Group: &Group{ID: 1, Platform: PlatformAnthropic, RateMultiplier: 1}},
		User:               &User{ID: 601},
		Account:            &Account{ID: 701},
		ChannelUsageFields: policy.Mapping(ctx, 1, "claude-sonnet-4").ToUsageFields("claude-sonnet-4", ""),
	}))
	want := logStub.lastLog
	require.Greater(t, want.ActualCost, 0.0)

	r := newPricingReplayer(newTestBillingService(), policy, policy)
	row := prRow(1, "claude-sonnet-4")
	row.Group = &Group{ID: 1, Platform: PlatformAnthropic, RateMultiplier: 1}
	row.CacheReadTokens = 50
	got := r.Replay(ctx, &row)
	require.True(t, got.Covered)
	require.Empty(t, got.Diffs)
	require.Equal(t, want.ActualCost, got.LegacyActualCost)
}

// 回放不计 PR1 的无价指标：Anthropic 网关的成本函数在算不出价时会走到 noteUnpricedBilling，
// 非结算标记让它直接返回。去掉标记，这个测试会因为计数不为空而失败。
func TestPricingReplay_DoesNotCountUnpricedBilling(t *testing.T) {
	r := prReplayer(PlatformAnthropic, prSame(prSnap(nil)), prSame(prSnap(nil)))
	row := prRow(1, unpricedTestModel)
	row.Group = &Group{ID: 1, Platform: PlatformAnthropic, RateMultiplier: 1}

	resetUnpricedBillingCountersForTest()
	out := r.Replay(context.Background(), &row)
	require.True(t, out.Covered)
	require.Empty(t, out.Diffs, "both sides are unpriced in the same way")
	require.Empty(t, UnpricedBillingCounterSnapshot())

	openAIRow := prRow(2, unpricedTestModel)
	openAI := prReplayer(PlatformOpenAI, prSame(prSnap(nil)), prSame(prSnap(nil)))
	out = openAI.Replay(context.Background(), &openAIRow)
	require.True(t, out.Covered)
	require.Empty(t, out.Diffs)
	require.Empty(t, UnpricedBillingCounterSnapshot())
}

func TestPricingReplay_WithReplayRecomputeMarksNonSettlement(t *testing.T) {
	ctx := withReplayRecompute(context.Background())
	require.True(t, IsBillingNonSettlement(ctx))
	require.True(t, isShadowRecompute(ctx))
	_, forced := forcedStageFromCtx(ctx)
	require.False(t, forced, "replay does not force a stage: the two sides are separate gateway objects")
}

func TestNewPricingReplayer_RequiresItsDependencies(t *testing.T) {
	_, err := NewPricingReplayer(nil, nil, nil)
	require.Error(t, err)
	r, err := NewPricingReplayer(newTestBillingService(), &ChannelService{}, newMPSource(PlatformOpenAI, nil))
	require.NoError(t, err)
	require.NotNil(t, r)
	require.Equal(t, pricingReplaySnapshotTTL, r.policy[replayV2].(*matrixPolicy).ttl)
}
