package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// 本文件覆盖设计 3.5 表中 OpenAI 侧的构造点（#1-#7、#14）与 N1a 的补试逻辑，
// 以及 context 标志 / decision 辅助函数。Anthropic 侧见 gateway_group_saturation_test.go。

func gsatAccount(id int64, priority int, concurrency int) Account {
	return Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: concurrency,
		Priority:    priority,
	}
}

func gsatSchedulerConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 1.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 1.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 0.7
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 0.8
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 0.5
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
	return cfg
}

func gsatCountCalls(ids []int64, id int64) int {
	n := 0
	for _, got := range ids {
		if got == id {
			n++
		}
	}
	return n
}

type gsatScenario struct {
	svc      *OpenAIGatewayService
	acquired *[]int64
}

// newGSATScenario 构造高级调度器场景：accounts 在同一分组，loadMap / acquireResults 由调用方给定，
// errorStats 把指定账号的错误率 EWMA 推到 0.36。
func newGSATScenario(accounts []Account, loadMap map[int64]*AccountLoadInfo, acquireResults map[int64]bool, errorStats map[int64]bool) gsatScenario {
	acquired := make([]int64, 0, 128)
	concurrencyCache := schedulerTestConcurrencyCache{
		loadMap:        loadMap,
		acquireResults: acquireResults,
		acquiredIDs:    &acquired,
	}
	stats := newOpenAIAccountRuntimeStats()
	for id := range errorStats {
		stats.report(id, true, nil)
		stats.report(id, false, nil)
		stats.report(id, false, nil)
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                gsatSchedulerConfig(),
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(concurrencyCache),
		openaiAccountStats: stats,
	}
	return gsatScenario{svc: svc, acquired: &acquired}
}

func (s gsatScenario) selectLB(ctx context.Context) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	groupID := int64(7001)
	return s.svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
}

func TestGroupSaturationProbeContextAndDecisionHelpers(t *testing.T) {
	var nilCtx context.Context
	require.False(t, openAIGroupSaturationProbeEnabled(nilCtx))
	require.False(t, openAIGroupSaturationProbeEnabled(context.Background()))
	require.False(t, openAIGroupSaturationProbeEnabled(context.WithValue(context.Background(), openAIGroupSaturationProbeKey{}, "x")))
	flagged := WithOpenAIGroupSaturationProbe(context.Background())
	require.True(t, openAIGroupSaturationProbeEnabled(flagged))
	require.True(t, openAIGroupSaturationProbeEnabled(WithOpenAIGroupSaturationProbe(nilCtx)))

	require.False(t, OpenAIAccountScheduleDecision{}.HitPreviousResponse())
	require.False(t, OpenAIAccountScheduleDecision{Layer: openAIAccountScheduleLayerSessionSticky, StickySessionHit: true}.HitPreviousResponse())
	require.False(t, OpenAIAccountScheduleDecision{Layer: openAIAccountScheduleLayerLoadBalance}.HitPreviousResponse())
	require.True(t, OpenAIAccountScheduleDecision{StickyPreviousHit: true}.HitPreviousResponse())
	require.True(t, OpenAIAccountScheduleDecision{Layer: openAIAccountScheduleLayerPreviousResponse}.HitPreviousResponse())
}

// N1a：8 个高优先级号满载，另有 1 个低优先级、错误率较高的空闲号。空闲号排在 top-K（默认 7）之外。
// 有链非末跳时必须由组内空闲号服务；无链时行为与改动前一致（返回首个候选的 WaitPlan，不探测空闲号）。
func TestGroupSaturation_N1a_IdleAccountOutsideTopKServesWhenChained(t *testing.T) {
	const idleID = int64(9009)
	accounts := make([]Account, 0, 9)
	loadMap := map[int64]*AccountLoadInfo{}
	acquireResults := map[int64]bool{}
	for id := int64(9001); id <= 9008; id++ {
		accounts = append(accounts, gsatAccount(id, 0, 1))
		loadMap[id] = &AccountLoadInfo{AccountID: id, CurrentConcurrency: 1, LoadRate: 100}
		acquireResults[id] = false
	}
	accounts = append(accounts, gsatAccount(idleID, 9, 1))
	loadMap[idleID] = &AccountLoadInfo{AccountID: idleID, LoadRate: 0}
	acquireResults[idleID] = true

	t.Run("有链非末跳：补试后由空闲号服务", func(t *testing.T) {
		sc := newGSATScenario(accounts, loadMap, acquireResults, map[int64]bool{idleID: true})
		selection, decision, err := sc.selectLB(WithOpenAIGroupSaturationProbe(context.Background()))
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.True(t, selection.Acquired, "组内有空闲号，不应返回 WaitPlan 让上层回退")
		require.Nil(t, selection.WaitPlan)
		require.Equal(t, idleID, selection.Account.ID)
		require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
		require.Equal(t, 1, gsatCountCalls(*sc.acquired, idleID))
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	})

	t.Run("无链：与改动前一致，不探测 top-K 之外的空闲号，WaitPlan 不置 GroupSaturated", func(t *testing.T) {
		sc := newGSATScenario(accounts, loadMap, acquireResults, map[int64]bool{idleID: true})
		selection, _, err := sc.selectLB(context.Background())
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.False(t, selection.Acquired)
		require.NotNil(t, selection.WaitPlan)
		require.False(t, selection.WaitPlan.GroupSaturated)
		require.NotEqual(t, idleID, selection.Account.ID)
		require.Zero(t, gsatCountCalls(*sc.acquired, idleID), "无链请求不得多做一次探测")
	})
}

func TestGroupSaturation_ChainedAllCandidatesFullSetsGroupSaturated(t *testing.T) {
	const idleLookingID = int64(9109)
	accounts := make([]Account, 0, 9)
	loadMap := map[int64]*AccountLoadInfo{}
	acquireResults := map[int64]bool{}
	for id := int64(9101); id <= 9108; id++ {
		accounts = append(accounts, gsatAccount(id, 0, 1))
		loadMap[id] = &AccountLoadInfo{AccountID: id, CurrentConcurrency: 1, LoadRate: 100}
		acquireResults[id] = false
	}
	// 负载快照显示有空位，但真实抢槽失败（快照滞后）。
	accounts = append(accounts, gsatAccount(idleLookingID, 9, 1))
	loadMap[idleLookingID] = &AccountLoadInfo{AccountID: idleLookingID, LoadRate: 50}
	acquireResults[idleLookingID] = false

	sc := newGSATScenario(accounts, loadMap, acquireResults, map[int64]bool{idleLookingID: true})
	selection, _, err := sc.selectLB(WithOpenAIGroupSaturationProbe(context.Background()))
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.False(t, selection.Acquired)
	require.NotNil(t, selection.WaitPlan)
	require.True(t, selection.WaitPlan.GroupSaturated, "补试全部失败才证明整组满")
	require.GreaterOrEqual(t, gsatCountCalls(*sc.acquired, idleLookingID), 1, "top-K 之外的候选必须被补试过")
}

func TestGroupSaturation_ChainedSkipsCandidatesAlreadyFullByFreshLoad(t *testing.T) {
	// 9 个号全部 LoadRate>=100 且抢槽失败：top-K 之外的 2 个号 fresh load 显示已满，不补试（省 Redis 调用），
	// 同时整组满。
	accounts := make([]Account, 0, 9)
	loadMap := map[int64]*AccountLoadInfo{}
	acquireResults := map[int64]bool{}
	for id := int64(9201); id <= 9209; id++ {
		accounts = append(accounts, gsatAccount(id, int(id-9201), 1))
		loadMap[id] = &AccountLoadInfo{AccountID: id, CurrentConcurrency: 1, LoadRate: 100}
		acquireResults[id] = false
	}
	sc := newGSATScenario(accounts, loadMap, acquireResults, nil)
	selection, _, err := sc.selectLB(WithOpenAIGroupSaturationProbe(context.Background()))
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.WaitPlan)
	require.True(t, selection.WaitPlan.GroupSaturated)
	require.Zero(t, gsatCountCalls(*sc.acquired, 9208), "LoadRate>=100 的 top-K 之外候选不补试")
	require.Zero(t, gsatCountCalls(*sc.acquired, 9209), "LoadRate>=100 的 top-K 之外候选不补试")
}

func TestGroupSaturation_ChainedCandidatesWithinTopKSaturatedWithoutExtraProbes(t *testing.T) {
	accounts := []Account{gsatAccount(9301, 0, 1), gsatAccount(9302, 0, 1), gsatAccount(9303, 0, 1)}
	loadMap := map[int64]*AccountLoadInfo{}
	acquireResults := map[int64]bool{}
	for _, acc := range accounts {
		loadMap[acc.ID] = &AccountLoadInfo{AccountID: acc.ID, LoadRate: 50}
		acquireResults[acc.ID] = false
	}

	noChain := newGSATScenario(accounts, loadMap, acquireResults, nil)
	selection, _, err := noChain.selectLB(context.Background())
	require.NoError(t, err)
	require.NotNil(t, selection.WaitPlan)
	require.False(t, selection.WaitPlan.GroupSaturated)

	chained := newGSATScenario(accounts, loadMap, acquireResults, nil)
	selection, _, err = chained.selectLB(WithOpenAIGroupSaturationProbe(context.Background()))
	require.NoError(t, err)
	require.NotNil(t, selection.WaitPlan)
	require.True(t, selection.WaitPlan.GroupSaturated, "候选数 <= top-K：top-K 满即整组满")
	require.Equal(t, len(*noChain.acquired), len(*chained.acquired), "候选数 <= top-K 时补试不增加任何探测")
}

// 探测预算：补试使用独立的有上限预算，最多 64 次抢槽；被截断时不能声称整组满。
func TestGroupSaturation_ChainedProbeBudgetBoundedAndTruncatedIsNotSaturated(t *testing.T) {
	const total = 80
	accounts := make([]Account, 0, total)
	loadMap := map[int64]*AccountLoadInfo{}
	acquireResults := map[int64]bool{}
	for i := 0; i < total; i++ {
		id := int64(9400 + i)
		accounts = append(accounts, gsatAccount(id, 0, 1))
		loadMap[id] = &AccountLoadInfo{AccountID: id, LoadRate: 50}
		acquireResults[id] = false
	}

	noChain := newGSATScenario(accounts, loadMap, acquireResults, nil)
	selection, _, err := noChain.selectLB(context.Background())
	require.NoError(t, err)
	require.NotNil(t, selection.WaitPlan)
	baseline := len(*noChain.acquired)

	chained := newGSATScenario(accounts, loadMap, acquireResults, nil)
	selection, _, err = chained.selectLB(WithOpenAIGroupSaturationProbe(context.Background()))
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.WaitPlan, "补试被截断后仍按单账号等待返回 WaitPlan，而不是报没号")
	require.False(t, selection.WaitPlan.GroupSaturated, "补试被预算截断，没有证明整组满")
	extra := len(*chained.acquired) - baseline
	require.LessOrEqual(t, extra, openAIAccountSelectionProbeLimit)
	require.Greater(t, extra, 0)
}

// 构造点 #5：调度器粘性命中、账号并发已满 -> 单账号等待。
func TestGroupSaturation_SchedulerStickyWaitPlanNotSaturated(t *testing.T) {
	groupID := int64(7005)
	accounts := []Account{gsatAccount(9501, 0, 1), gsatAccount(9502, 9, 1)}
	for i := range accounts {
		accounts[i].GroupIDs = []int64{groupID}
	}
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:gsat_sticky": 9501}}
	cfg := gsatSchedulerConfig()
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 2
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
	cfg.Gateway.OpenAIScheduler.StickyEscapeEnabled = false
	cfg.Gateway.OpenAIScheduler.StickyEscapeTTFTMs = 15000
	cfg.Gateway.OpenAIScheduler.StickyEscapeErrorRate = 0.5
	svc := &OpenAIGatewayService{
		accountRepo:      schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:            cache,
		cfg:              cfg,
		rateLimitService: newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{
			acquireResults: map[int64]bool{9501: false, 9502: true},
			waitCounts:     map[int64]int{9501: 999},
		}),
	}
	// 带上补试标志也不影响粘性单账号等待：补试只在负载选择层。
	selection, decision, err := svc.SelectAccountWithScheduler(
		WithOpenAIGroupSaturationProbe(context.Background()), &groupID, "", "gsat_sticky", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, int64(9501), selection.Account.ID)
	require.False(t, selection.WaitPlan.GroupSaturated)
	require.Equal(t, 45*time.Second, selection.WaitPlan.Timeout)
	require.True(t, decision.StickySessionHit)
	require.False(t, decision.HitPreviousResponse())
}

// 构造点 #14（及规则 3 的判定依据）：previous_response_id 命中、绑定账号满。
func TestGroupSaturation_PreviousResponseWaitPlanNotSaturatedAndFlagsDecision(t *testing.T) {
	ctx := context.Background()
	groupID := int64(7014)
	account := gsatAccount(9601, 0, 1)
	account.Extra = map[string]any{"openai_apikey_responses_websockets_v2_enabled": true}
	other := gsatAccount(9602, 0, 1)
	cfg := newSchedulerTestOpenAIWSV2Config()
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 1800
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 2
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 30 * time.Second
	svc := &OpenAIGatewayService{
		accountRepo:      schedulerTestOpenAIAccountRepo{accounts: []Account{account, other}},
		cache:            &schedulerTestGatewayCache{},
		cfg:              cfg,
		rateLimitService: newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{
			acquireResults: map[int64]bool{9601: false, 9602: true},
		}),
	}
	require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, groupID, "resp_gsat_001", account.ID, time.Hour))

	selection, decision, err := svc.SelectAccountWithScheduler(
		WithOpenAIGroupSaturationProbe(ctx), &groupID, "resp_gsat_001", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, account.ID, selection.Account.ID)
	require.False(t, selection.WaitPlan.GroupSaturated)
	require.True(t, decision.HitPreviousResponse(), "规则 3 以选号结果为准")
}

// 规则 3 看的是选号结果：请求带了 previous_response_id 但绑定没命中时，走普通负载选择，
// decision 不是 previous_response 层，应按普通规则处理。
func TestGroupSaturation_PreviousResponseIDWithoutBindingIsNotRule3(t *testing.T) {
	ctx := context.Background()
	groupID := int64(7015)
	accounts := []Account{gsatAccount(9701, 0, 1)}
	sc := newGSATScenario(accounts, map[int64]*AccountLoadInfo{9701: {AccountID: 9701, LoadRate: 50}}, map[int64]bool{9701: false}, nil)
	selection, decision, err := sc.svc.SelectAccountWithScheduler(
		ctx, &groupID, "resp_never_bound", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.WaitPlan)
	require.False(t, decision.HitPreviousResponse())
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
}

// 构造点 #7：forced 路由单账号等待，不是整组满。
func TestGroupSaturation_ForcedAccountWaitPlanNotSaturated(t *testing.T) {
	ctx := context.Background()
	forced := gsatAccount(9801, 0, 1)
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 3 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 7
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{forced}},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{9801: false}}),
	}
	selection, err := svc.selectForcedOpenAIAccount(ctx, forced.ID, "", nil, OpenAIUpstreamTransportAny, "", "", false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.WaitPlan)
	require.False(t, selection.WaitPlan.GroupSaturated)
	require.Equal(t, 3*time.Second, selection.WaitPlan.Timeout)
}

// 旧选号路径（高级调度器关闭）。构造点 #1、#2、#3、#4。
func TestGroupSaturation_LegacyPathConstructors(t *testing.T) {
	newLegacySvc := func(cfg *config.Config, repoAccounts []Account, cache *stubGatewayCache, cc stubConcurrencyCache) *OpenAIGatewayService {
		return &OpenAIGatewayService{
			accountRepo:        stubOpenAIAccountRepo{accounts: repoAccounts},
			cache:              cache,
			cfg:                cfg,
			concurrencyService: NewConcurrencyService(cc),
		}
	}
	legacyCfg := func(loadBatch bool) *config.Config {
		cfg := &config.Config{}
		cfg.Gateway.Scheduling.LoadBatchEnabled = loadBatch
		cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
		cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
		cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
		cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
		return cfg
	}
	groupID := int64(1)
	oneAccount := []Account{{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}}

	t.Run("#4 Layer 3 负载显示全满：GroupSaturated", func(t *testing.T) {
		svc := newLegacySvc(legacyCfg(true), oneAccount, &stubGatewayCache{}, stubConcurrencyCache{
			loadMap: map[int64]*AccountLoadInfo{1: {AccountID: 1, LoadRate: 100}},
		})
		selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "gpt-4", nil)
		require.NoError(t, err)
		require.NotNil(t, selection.WaitPlan)
		require.True(t, selection.WaitPlan.GroupSaturated)
	})

	t.Run("#4 Layer 3 抢槽全部失败：GroupSaturated", func(t *testing.T) {
		svc := newLegacySvc(legacyCfg(true), oneAccount, &stubGatewayCache{}, stubConcurrencyCache{
			acquireResults: map[int64]bool{1: false},
			loadMap:        map[int64]*AccountLoadInfo{1: {AccountID: 1, LoadRate: 10}},
		})
		selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "gpt-4", nil)
		require.NoError(t, err)
		require.NotNil(t, selection.WaitPlan)
		require.True(t, selection.WaitPlan.GroupSaturated)
		require.Equal(t, 30*time.Second, selection.WaitPlan.Timeout)
	})

	t.Run("#4 取负载出错、逐个尝试全失败：GroupSaturated", func(t *testing.T) {
		svc := newLegacySvc(legacyCfg(true), oneAccount, &stubGatewayCache{}, stubConcurrencyCache{
			loadBatchErr:   errors.New("load batch failed"),
			acquireResults: map[int64]bool{1: false},
		})
		selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "gpt-4", nil)
		require.NoError(t, err)
		require.NotNil(t, selection.WaitPlan)
		require.True(t, selection.WaitPlan.GroupSaturated)
	})

	t.Run("#3 load batch 路径粘性账号满：不是 GroupSaturated", func(t *testing.T) {
		svc := newLegacySvc(legacyCfg(true), oneAccount, &stubGatewayCache{sessionBindings: map[string]int64{"openai:gsat-legacy": 1}}, stubConcurrencyCache{
			acquireResults: map[int64]bool{1: false},
			waitCounts:     map[int64]int{1: 0},
		})
		selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "gsat-legacy", "gpt-4", nil)
		require.NoError(t, err)
		require.NotNil(t, selection.WaitPlan)
		require.False(t, selection.WaitPlan.GroupSaturated)
		require.Equal(t, 45*time.Second, selection.WaitPlan.Timeout)
	})

	t.Run("#1 未开启 load batch，粘性账号满：不是 GroupSaturated", func(t *testing.T) {
		svc := newLegacySvc(legacyCfg(false), oneAccount, &stubGatewayCache{sessionBindings: map[string]int64{"openai:gsat-nobatch": 1}}, stubConcurrencyCache{
			acquireResults: map[int64]bool{1: false},
			waitCounts:     map[int64]int{1: 0},
		})
		selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "gsat-nobatch", "gpt-4", nil)
		require.NoError(t, err)
		require.NotNil(t, selection.WaitPlan)
		require.False(t, selection.WaitPlan.GroupSaturated)
		require.Equal(t, 45*time.Second, selection.WaitPlan.Timeout)
	})

	t.Run("#2 未开启 load batch，选中的单个账号满：不是 GroupSaturated", func(t *testing.T) {
		svc := newLegacySvc(legacyCfg(false), oneAccount, &stubGatewayCache{}, stubConcurrencyCache{
			acquireResults: map[int64]bool{1: false},
		})
		selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "gpt-4", nil)
		require.NoError(t, err)
		require.NotNil(t, selection.WaitPlan)
		require.False(t, selection.WaitPlan.GroupSaturated)
		require.Equal(t, 30*time.Second, selection.WaitPlan.Timeout)
	})
}
