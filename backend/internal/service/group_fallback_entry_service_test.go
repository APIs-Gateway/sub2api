//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 回退链入口接线（PR2a 第 4a 段）在 service 层的配套：设置缓存与默认值、指标、选号哨兵、forced 路由让位、
// ops 事件的服务分组、按服务分组计费与每跳静态资格。

// ---- 设置：默认值与缓存 ----

func TestGroupFallbackSettings_DefaultStickyQueueShareIsThirtyPercent(t *testing.T) {
	require.Equal(t, 0.3, DefaultGroupFallbackSettings().StickyQueueShare)
	require.Equal(t, 0.3, parseGroupFallbackSettings(nil).StickyQueueShare, "未配置时落到 0.3")
	require.Equal(t, 0.5, parseGroupFallbackSettings(map[string]string{SettingKeyGroupFallbackStickyQueueShare: "0.5"}).StickyQueueShare, "管理员显式配置仍然生效")
}

type groupFallbackCountingRepoStub struct {
	SettingRepository
	vals  map[string]string
	err   error
	calls int32
}

func (r *groupFallbackCountingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	atomic.AddInt32(&r.calls, 1)
	if r.err != nil {
		return nil, r.err
	}
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := r.vals[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func TestSettingServiceGetGroupFallbackSettings_CachedSoHotPathDoesNotHitDB(t *testing.T) {
	repo := &groupFallbackCountingRepoStub{vals: map[string]string{SettingKeyGroupFallbackEnabled: "true"}}
	svc := &SettingService{settingRepo: repo}
	for i := 0; i < 50; i++ {
		require.True(t, svc.GetGroupFallbackSettings(context.Background()).Enabled)
	}
	require.EqualValues(t, 1, atomic.LoadInt32(&repo.calls), "热路径每个带链请求都读设置，必须走进程内缓存")
}

func TestSettingServiceGetGroupFallbackSettings_ReadFailureIsCachedBrieflyAndFailsClosed(t *testing.T) {
	repo := &groupFallbackCountingRepoStub{err: errors.New("boom")}
	svc := &SettingService{settingRepo: repo}
	for i := 0; i < 10; i++ {
		require.False(t, svc.GetGroupFallbackSettings(context.Background()).Enabled)
	}
	require.EqualValues(t, 1, atomic.LoadInt32(&repo.calls), "读取失败也短暂缓存，数据库故障时不能被热路径放大")
}

// ---- 指标 ----

func TestGroupFallbackMetrics_SaturationProbeCounters(t *testing.T) {
	before := SnapshotGroupFallbackMetrics()
	RecordGroupFallbackSaturationProbe(GroupSaturationProbeServed)
	RecordGroupFallbackSaturationProbe(GroupSaturationProbeServed)
	RecordGroupFallbackSaturationProbe(GroupSaturationProbeSaturated)
	RecordGroupFallbackSaturationProbe(GroupSaturationProbeTruncated)
	RecordGroupFallbackSaturationProbe(GroupSaturationProbeError)
	RecordGroupFallbackSaturationProbe("unknown-result")
	after := SnapshotGroupFallbackMetrics()

	require.EqualValues(t, 2, after.SaturationProbeServed-before.SaturationProbeServed)
	require.EqualValues(t, 1, after.SaturationProbeSaturated-before.SaturationProbeSaturated)
	require.EqualValues(t, 1, after.SaturationProbeTruncated-before.SaturationProbeTruncated)
	require.EqualValues(t, 1, after.SaturationProbeError-before.SaturationProbeError)
}

func TestGroupFallbackMetrics_RecordRun(t *testing.T) {
	before := SnapshotGroupFallbackMetrics()
	RecordGroupFallbackRun(ChainRunResult{Status: ChainRunServed, ServedIndex: 0, Trace: []HopTrace{{Index: 0, Outcome: HopOutcomeDone}}})
	RecordGroupFallbackRun(ChainRunResult{
		Status:      ChainRunServed,
		ServedIndex: 2,
		Trace: []HopTrace{
			{Index: 0, Outcome: HopOutcomeFallbackWorthy, Reason: FallbackReasonNoAccount},
			{Index: 1, Outcome: HopOutcomeSkipped, SkippedBy: "attempt_skipped"},
			{Index: 2, Outcome: HopOutcomeDone},
		},
	})
	RecordGroupFallbackRun(ChainRunResult{
		Status:               ChainRunExhausted,
		ServedIndex:          -1,
		BreakerBypassRetried: true,
		Trace: []HopTrace{
			{Index: 0, Outcome: HopOutcomeSkipped, SkippedBy: "breaker_open"},
			{Index: 1, Outcome: HopOutcomeFallbackWorthy, Reason: FallbackReasonBusy},
			{Index: 2, Outcome: HopOutcomeFallbackWorthy, Reason: FallbackReasonFailoverExhausted},
		},
	})
	RecordGroupFallbackRun(ChainRunResult{Status: ChainRunTerminal, ServedIndex: -1})
	RecordGroupFallbackRun(ChainRunResult{Status: ChainRunClientGone, ServedIndex: -1})
	RecordGroupFallbackRun(ChainRunResult{Status: ChainRunUnresolved, ServedIndex: -1})
	after := SnapshotGroupFallbackMetrics()

	require.EqualValues(t, 1, after.RunServedFirstHop-before.RunServedFirstHop)
	require.EqualValues(t, 1, after.RunServedFallback-before.RunServedFallback)
	require.EqualValues(t, 1, after.RunExhausted-before.RunExhausted)
	require.EqualValues(t, 1, after.RunTerminal-before.RunTerminal)
	require.EqualValues(t, 1, after.RunClientGone-before.RunClientGone)
	require.EqualValues(t, 1, after.RunUnresolved-before.RunUnresolved)
	require.EqualValues(t, 1, after.BreakerBypassRetry-before.BreakerBypassRetry)
	require.EqualValues(t, 1, after.HopSkipped-before.HopSkipped)
	require.EqualValues(t, 1, after.HopBreakerSkipped-before.HopBreakerSkipped)
	require.EqualValues(t, 1, after.HopFallbackNoAccount-before.HopFallbackNoAccount)
	require.EqualValues(t, 1, after.HopFallbackBusy-before.HopFallbackBusy)
	require.EqualValues(t, 1, after.HopFallbackFailover-before.HopFallbackFailover)
}

// ---- 选号哨兵（S-3） ----

func TestOpenAISelectionBudgetExhaustedSentinel(t *testing.T) {
	err := NewOpenAISelectionBudgetExhaustedErrorForTest("gpt-5.1")
	require.True(t, IsOpenAISelectionBudgetExhausted(err))
	require.ErrorIs(t, err, ErrNoAvailableAccounts, "仍然是「没有可用账号」，没有识别哨兵的调用方行为不变")
	require.True(t, IsOpenAISelectionBudgetExhausted(fmt.Errorf("wrapped: %w", err)), "被包装之后仍可识别")

	plain := noAvailableOpenAISelectionError("gpt-5.1", false, "selection_order_exhausted")
	require.False(t, IsOpenAISelectionBudgetExhausted(plain), "组内真的没号不是预算用尽")
	require.ErrorIs(t, plain, ErrNoAvailableAccounts)
	require.False(t, IsOpenAISelectionBudgetExhausted(nil))
	require.False(t, IsOpenAISelectionBudgetExhausted(errors.New("other")))
}

// 预算用尽、组内有号没试完：按「繁忙」分类，不计熔断（它不是分组故障）。
func TestOpenAISelectionBudgetExhaustedClassifiesAsBusyWithoutBreaker(t *testing.T) {
	res := ClassifyHopFailure(HopFailure{Kind: HopFailureBusyTimeout})
	require.Equal(t, HopOutcomeFallbackWorthy, res.Outcome)
	require.Equal(t, FallbackReasonBusy, res.Reason)
	require.Equal(t, BreakerSignalNone, res.Breaker)
}

// ---- forced 账号路由让位 ----

func TestClearOpenAIForcedAccountRouting(t *testing.T) {
	ctx := WithOpenAIForcedAccountRouting(context.Background(), 42)
	require.EqualValues(t, 42, openAIForcedAccountRoutingID(ctx))

	cleared := ClearOpenAIForcedAccountRouting(ctx)
	require.Zero(t, openAIForcedAccountRoutingID(cleared), "让位给链之后，每一跳走标准调度")
	require.EqualValues(t, 42, openAIForcedAccountRoutingID(ctx), "不修改原 ctx")

	plain := context.Background()
	require.Equal(t, plain, ClearOpenAIForcedAccountRouting(plain), "没有 forced 路由时原样返回")
	var nilCtx context.Context
	require.Nil(t, ClearOpenAIForcedAccountRouting(nilCtx))
}

// ---- ops 事件里的服务分组 ----

func TestOpsUpstreamErrorRecordsServedGroupOnlyWhenNotPrimary(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "http_error", Message: "primary failed"})
	SetOpsServedGroup(c, 22)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "http_error", Message: "fallback failed"})
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "http_error", Message: "explicit", ServedGroupID: 5})
	SetOpsServedGroup(c, 0)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "http_error", Message: "back to primary"})

	v, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := v.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 4)
	require.Zero(t, events[0].ServedGroupID, "主分组的事件不带服务分组")
	require.EqualValues(t, 22, events[1].ServedGroupID)
	require.EqualValues(t, 5, events[2].ServedGroupID, "事件自带的值不被覆盖")
	require.Zero(t, events[3].ServedGroupID, "回到主分组后清除标记")

	primaryJSON, err := json.Marshal(events[0])
	require.NoError(t, err)
	require.NotContains(t, string(primaryJSON), "served_group_id", "旧事件的 JSON 形状不变（omitempty）")
	servedJSON, err := json.Marshal(events[1])
	require.NoError(t, err)
	require.Contains(t, string(servedJSON), `"served_group_id":22`)
}

// ---- 按服务分组计费 ----

func newServedKeyForTest(home, served int64, source string, rate float64) *APIKey {
	return &APIKey{
		ID:          1001,
		GroupID:     i64p(served),
		HomeGroupID: i64p(home),
		RouteSource: source,
		Group:       &Group{ID: served, RateMultiplier: rate},
	}
}

func TestOpenAIGatewayServiceRecordUsage_ServedGroupSplitsHomeAndServedAndPricesByServed(t *testing.T) {
	const home, served = int64(11), int64(22)
	usage := OpenAIUsage{InputTokens: 15, OutputTokens: 4}

	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result:  &OpenAIForwardResult{RequestID: "resp_served_user_chain", Usage: usage, Model: "gpt-5.1", Duration: time.Second},
		APIKey:  newServedKeyForTest(home, served, RouteSourceUser, 2.0),
		User:    &User{ID: 2001},
		Account: &Account{ID: 3001},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	log := usageRepo.lastLog

	require.NotNil(t, log.GroupID)
	require.Equal(t, home, *log.GroupID, "group_id 仍是主分组")
	require.NotNil(t, log.ServedGroupID)
	require.Equal(t, served, *log.ServedGroupID)
	require.NotNil(t, log.ServedRouteSource)
	require.Equal(t, ServedRouteSourceUserChain, *log.ServedRouteSource)
	require.Equal(t, 2.0, log.RateMultiplier, "按服务分组的倍率计价")

	expected := expectedOpenAICost(t, svc, "gpt-5.1", usage, 2.0)
	require.InDelta(t, expected.ActualCost, log.ActualCost, 1e-12)
	require.InDelta(t, expected.ActualCost, userRepo.lastAmount, 1e-12, "实际扣的就是服务分组价")
}

func TestOpenAIGatewayServiceRecordUsage_ServedGroupAdminChainSource(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result:  &OpenAIForwardResult{RequestID: "resp_served_admin_chain", Usage: OpenAIUsage{InputTokens: 3, OutputTokens: 1}, Model: "gpt-5.1", Duration: time.Second},
		APIKey:  newServedKeyForTest(11, 33, RouteSourceAdmin, 1.0),
		User:    &User{ID: 2001},
		Account: &Account{ID: 3001},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog.ServedRouteSource)
	require.Equal(t, ServedRouteSourceAdminChain, *usageRepo.lastLog.ServedRouteSource, "管理员链：用户端 DTO 据此不透出服务分组")
}

func TestOpenAIGatewayServiceRecordUsage_NoServedColumnsWhenNotShadowOrServedIsHome(t *testing.T) {
	cases := map[string]*APIKey{
		"ordinary key without home group":     {ID: 1, GroupID: i64p(11), Group: &Group{ID: 11, RateMultiplier: 1}},
		"first hop shadow key (served==home)": newServedKeyForTest(11, 11, RouteSourcePrimary, 1),
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result:  &OpenAIForwardResult{RequestID: "resp_no_served", Usage: OpenAIUsage{InputTokens: 3, OutputTokens: 1}, Model: "gpt-5.1", Duration: time.Second},
				APIKey:  key,
				User:    &User{ID: 2001},
				Account: &Account{ID: 3001},
			})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog.GroupID)
			require.Equal(t, int64(11), *usageRepo.lastLog.GroupID)
			require.Nil(t, usageRepo.lastLog.ServedGroupID, "与引入回退链之前的写入完全一致")
			require.Nil(t, usageRepo.lastLog.ServedRouteSource)
		})
	}
}

// ---- 每跳静态资格 ----

func TestOpenAIGatewayServiceIsModelPricedForGroup(t *testing.T) {
	svc := newOpenAIRecordUsageServiceForTest(&openAIRecordUsageLogRepoStub{inserted: true}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	hopKey := newServedKeyForTest(11, 22, RouteSourceUser, 1)

	require.True(t, svc.IsModelPricedForGroup(context.Background(), hopKey, "gpt-5.1", ChannelMappingResult{}), "有价格的模型可以作为兜底")
	require.False(t, svc.IsModelPricedForGroup(context.Background(), hopKey, "pricing-missing-test-model", ChannelMappingResult{}),
		"没配价格的模型会按零成本放行，兜底分组不能白送")

	require.True(t, svc.IsModelPricedForGroup(context.Background(), nil, "pricing-missing-test-model", ChannelMappingResult{}), "缺少输入时放行（不因预检自身的问题挡请求）")
	require.True(t, svc.IsModelPricedForGroup(context.Background(), &APIKey{ID: 1}, "pricing-missing-test-model", ChannelMappingResult{}))
	var nilSvc *OpenAIGatewayService
	require.True(t, nilSvc.IsModelPricedForGroup(context.Background(), hopKey, "pricing-missing-test-model", ChannelMappingResult{}))
}

func TestOpenAIGatewayServiceIsModelOpenForGroup(t *testing.T) {
	var nilSvc *OpenAIGatewayService
	require.True(t, nilSvc.IsModelOpenForGroup(context.Background(), 10, "gpt-5.1"))

	svc := newOpenAIRecordUsageServiceForTest(&openAIRecordUsageLogRepoStub{inserted: true}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	require.True(t, svc.IsModelOpenForGroup(context.Background(), 10, "gpt-5.1"), "没有渠道服务时不限制")
	require.True(t, svc.IsModelOpenForGroup(context.Background(), 0, "gpt-5.1"))

	ch := Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceRequested,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformOpenAI, Models: []string{"gpt-5.1"}},
		},
	}
	svc.channelService = newTestChannelService(makeStandardRepo(ch, map[int64]string{10: PlatformOpenAI}))
	require.True(t, svc.IsModelOpenForGroup(context.Background(), 10, "gpt-5.1"), "在渠道定价列表里：开放")
	require.False(t, svc.IsModelOpenForGroup(context.Background(), 10, "gpt-4o"), "渠道限制了模型列表、不含该模型：这一跳不具备资格")
	require.True(t, svc.IsModelOpenForGroup(context.Background(), 99, "gpt-4o"), "不在这个渠道里的分组不受限制")
}
