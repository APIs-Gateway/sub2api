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

// ---------- 返修（Opus 审查）：BK-2 / S2 / S3 ----------

func TestOpenAISelectionProbeBudgetExhausted(t *testing.T) {
	var nilBudget *openAISelectionProbeBudget
	require.False(t, nilBudget.exhausted())

	unlimited := newOpenAISelectionProbeBudget()
	for i := 0; i < openAIAccountSelectionProbeLimit*2; i++ {
		require.True(t, unlimited.recordAcquire(int64(i+1)))
		require.True(t, unlimited.recordRecheck())
	}
	require.False(t, unlimited.exhausted(), "没有开启上限（无链请求的默认预算）：永远不算用尽")

	byAcquire := newOpenAISelectionProbeBudget()
	byAcquire.enableLimit()
	require.False(t, byAcquire.exhausted())
	for i := 0; i < openAIAccountSelectionProbeLimit-1; i++ {
		require.True(t, byAcquire.recordAcquire(int64(i+1)))
	}
	require.False(t, byAcquire.exhausted())
	require.True(t, byAcquire.recordAcquire(999))
	require.True(t, byAcquire.exhausted(), "acquire 次数达到上限即视为用尽（>=，偏保守）")

	byRecheck := newOpenAISelectionProbeBudget()
	byRecheck.enableLimit()
	for i := 0; i < openAIAccountSelectionProbeLimit-1; i++ {
		require.True(t, byRecheck.recordRecheck())
	}
	require.False(t, byRecheck.exhausted())
	require.True(t, byRecheck.recordRecheck())
	require.True(t, byRecheck.exhausted(), "DB 复核次数达到上限同样视为用尽")
}

// gsatCostAwareAccounts 构造 n 个同组、并发为 1、负载快照显示有空位（LoadRate 50）但抢槽全部失败的账号。
// 第一个账号的倍率是 0.5，其余缺省 1.0：成本因子非中性，开启成本权重后 includeOverflowFallback 为真，
// 选号顺序覆盖全部候选，主流程的探测预算随之开启上限（64 次抢槽）。
func gsatCostAwareAccounts(n int, baseID int64) ([]Account, map[int64]*AccountLoadInfo, map[int64]bool) {
	accounts := make([]Account, 0, n)
	loadMap := make(map[int64]*AccountLoadInfo, n)
	acquireResults := make(map[int64]bool, n)
	for i := 0; i < n; i++ {
		id := baseID + int64(i)
		account := gsatAccount(id, 0, 1)
		if i == 0 {
			account.RateMultiplier = float64Ptr(0.5)
		}
		accounts = append(accounts, account)
		loadMap[id] = &AccountLoadInfo{AccountID: id, LoadRate: 50}
		acquireResults[id] = false
	}
	return accounts, loadMap, acquireResults
}

func newGSATCostAwareScenario(n int, baseID int64) gsatScenario {
	accounts, loadMap, acquireResults := gsatCostAwareAccounts(n, baseID)
	sc := newGSATScenario(accounts, loadMap, acquireResults, nil)
	sc.svc.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.UpstreamCost = 1.0
	return sc
}

// BK-2：主流程的探测预算被截断时，没试过的候选不能被算作「已尝试」，补试不能证明整组满。
// 否则请求会在主分组还有空号时被送去回退到更贵的分组。
func TestGroupSaturation_BK2_MainBudgetTruncatedIsNotSaturated(t *testing.T) {
	const total = 70 // > 64：成本感知开启时，第一轮抢到第 64 次就撞上限，剩下 6 个一次都没试
	noChain := newGSATCostAwareScenario(total, 9600)
	selection, _, err := noChain.selectLB(context.Background())
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.WaitPlan)
	require.False(t, selection.WaitPlan.GroupSaturated)
	require.Equal(t, openAIAccountSelectionProbeLimit, len(*noChain.acquired),
		"前提：成本感知已开启，主流程预算上限为 64 次抢槽，第一轮撞上限后 fresh 轮一次也抢不了")

	chained := newGSATCostAwareScenario(total, 9600)
	selection, _, err = chained.selectLB(WithOpenAIGroupSaturationProbe(context.Background()))
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.False(t, selection.Acquired)
	require.NotNil(t, selection.WaitPlan, "照常返回单账号 WaitPlan，而不是报没号")
	require.False(t, selection.WaitPlan.GroupSaturated, "主流程被预算截断，还有候选没试过：不能证明整组满")
	require.Equal(t, len(*noChain.acquired), len(*chained.acquired), "此场景补试没有可试的候选，不增加任何抢槽")
}

// 没有被截断、且确实都试过：照常标满（成本感知开启与否都一样）。
func TestGroupSaturation_BK2_MainBudgetNotTruncatedStillSaturated(t *testing.T) {
	t.Run("候选数远小于上限：两轮都试完，整组满", func(t *testing.T) {
		const total = 20
		noChain := newGSATCostAwareScenario(total, 9700)
		selection, _, err := noChain.selectLB(context.Background())
		require.NoError(t, err)
		require.NotNil(t, selection.WaitPlan)
		require.False(t, selection.WaitPlan.GroupSaturated, "无链请求不补试，恒为 false")
		require.Equal(t, 2*total, len(*noChain.acquired), "前提：成本感知开启，第一轮和 fresh 轮各把 20 个候选都试了一遍")

		chained := newGSATCostAwareScenario(total, 9700)
		selection, _, err = chained.selectLB(WithOpenAIGroupSaturationProbe(context.Background()))
		require.NoError(t, err)
		require.NotNil(t, selection.WaitPlan)
		require.True(t, selection.WaitPlan.GroupSaturated, "每个候选都真的试过且全部失败：整组满")
		require.Equal(t, len(*noChain.acquired), len(*chained.acquired))
	})

	t.Run("只有 fresh 轮撞上限：第一轮已经试遍全部候选，仍然整组满", func(t *testing.T) {
		const total = 40 // 第一轮 40 次、fresh 轮 24 次后撞上限；第一轮没有撞上限，说明每个候选都试过
		chained := newGSATCostAwareScenario(total, 9800)
		selection, _, err := chained.selectLB(WithOpenAIGroupSaturationProbe(context.Background()))
		require.NoError(t, err)
		require.NotNil(t, selection.WaitPlan)
		require.Equal(t, openAIAccountSelectionProbeLimit, len(*chained.acquired))
		for i := 0; i < total; i++ {
			require.GreaterOrEqual(t, gsatCountCalls(*chained.acquired, int64(9800+i)), 1, "第 %d 个候选必须至少试过一次", i)
		}
		require.True(t, selection.WaitPlan.GroupSaturated)
	})
}

// gsatErrAcquireCache 在 schedulerTestConcurrencyCache 的基础上，让指定账号的抢槽返回错误（模拟 Redis 故障）。
type gsatErrAcquireCache struct {
	schedulerTestConcurrencyCache
	errIDs map[int64]error
}

func (c gsatErrAcquireCache) AcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
	if err, ok := c.errIDs[accountID]; ok {
		if c.acquiredIDs != nil {
			*c.acquiredIDs = append(*c.acquiredIDs, accountID)
		}
		return false, err
	}
	return c.schedulerTestConcurrencyCache.AcquireAccountSlot(ctx, accountID, maxConcurrency, requestID)
}

func newGSATScenarioWithAcquireErrors(accounts []Account, loadMap map[int64]*AccountLoadInfo, acquireResults map[int64]bool, errIDs map[int64]error, errorStats map[int64]bool) gsatScenario {
	sc := newGSATScenario(accounts, loadMap, acquireResults, errorStats)
	sc.svc.concurrencyService = NewConcurrencyService(gsatErrAcquireCache{
		schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{
			loadMap:        loadMap,
			acquireResults: acquireResults,
			acquiredIDs:    sc.acquired,
		},
		errIDs: errIDs,
	})
	return sc
}

// S3：补试遇到 Redis 错误不能让有链请求失败（无链请求在同样场景下拿到的是 WaitPlan）；
// 主流程自己的抢槽错误仍然照原样返回。
func TestGroupSaturation_S3_ProbeRedisErrorFallsBackToWaitPlan(t *testing.T) {
	const idleID = int64(9909)
	redisErr := errors.New("redis: connection refused")
	accounts := make([]Account, 0, 9)
	loadMap := map[int64]*AccountLoadInfo{}
	acquireResults := map[int64]bool{}
	for id := int64(9901); id <= 9908; id++ {
		accounts = append(accounts, gsatAccount(id, 0, 1))
		loadMap[id] = &AccountLoadInfo{AccountID: id, CurrentConcurrency: 1, LoadRate: 100}
		acquireResults[id] = false
	}
	accounts = append(accounts, gsatAccount(idleID, 9, 1))
	loadMap[idleID] = &AccountLoadInfo{AccountID: idleID, LoadRate: 0}
	acquireResults[idleID] = true

	t.Run("补试的抢槽出错：返回 WaitPlan，不置 GroupSaturated，不返回错误", func(t *testing.T) {
		sc := newGSATScenarioWithAcquireErrors(accounts, loadMap, acquireResults, map[int64]error{idleID: redisErr}, map[int64]bool{idleID: true})
		selection, _, err := sc.selectLB(WithOpenAIGroupSaturationProbe(context.Background()))
		require.NoError(t, err, "补试出错不能让有链请求失败")
		require.NotNil(t, selection)
		require.False(t, selection.Acquired)
		require.NotNil(t, selection.WaitPlan)
		require.False(t, selection.WaitPlan.GroupSaturated, "补试没有跑完，不能证明整组满")
		require.Equal(t, 1, gsatCountCalls(*sc.acquired, idleID), "前提：补试确实试了 top-K 之外的空闲号并遇到错误")
	})

	t.Run("主流程的抢槽出错：照原样返回错误", func(t *testing.T) {
		errIDs := map[int64]error{}
		for id := int64(9901); id <= 9908; id++ {
			errIDs[id] = redisErr
		}
		sc := newGSATScenarioWithAcquireErrors(accounts, loadMap, acquireResults, errIDs, map[int64]bool{idleID: true})
		selection, _, err := sc.selectLB(WithOpenAIGroupSaturationProbe(context.Background()))
		require.ErrorIs(t, err, redisErr)
		require.Nil(t, selection)
		require.Zero(t, gsatCountCalls(*sc.acquired, idleID), "主流程出错就终止，不会走到补试")
	})
}

// S2：旧选号路径 #4。缓存负载显示全满（一次抢槽都没试）时，有链请求不能只凭缓存下「整组满」的结论。
type gsatSeqLoadCache struct {
	stubConcurrencyCache
	firstLoad map[int64]*AccountLoadInfo
	laterLoad map[int64]*AccountLoadInfo
	laterErr  error
	loadCalls int
}

func (c *gsatSeqLoadCache) GetAccountsLoadBatch(_ context.Context, _ []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	c.loadCalls++
	if c.loadCalls == 1 {
		return c.firstLoad, nil
	}
	if c.laterErr != nil {
		return nil, c.laterErr
	}
	return c.laterLoad, nil
}

func TestGroupSaturation_S2_LegacyLayer3NeedsFreshLoadWhenChained(t *testing.T) {
	groupID := int64(1)
	oneAccount := []Account{{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}}
	newSvc := func(cache *gsatSeqLoadCache) *OpenAIGatewayService {
		cfg := &config.Config{}
		cfg.Gateway.Scheduling.LoadBatchEnabled = true
		cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
		cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
		cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
		cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
		return &OpenAIGatewayService{
			accountRepo:        stubOpenAIAccountRepo{accounts: oneAccount},
			cache:              &stubGatewayCache{},
			cfg:                cfg,
			concurrencyService: NewConcurrencyService(cache),
		}
	}
	full := map[int64]*AccountLoadInfo{1: {AccountID: 1, LoadRate: 100}}
	idle := map[int64]*AccountLoadInfo{1: {AccountID: 1, LoadRate: 0}}
	chainedCtx := WithOpenAIGroupSaturationProbe(context.Background())

	t.Run("无链：缓存全满时不取 fresh load，行为与改动前一致", func(t *testing.T) {
		cache := &gsatSeqLoadCache{firstLoad: full, laterLoad: idle}
		selection, err := newSvc(cache).SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "gpt-4", nil)
		require.NoError(t, err)
		require.NotNil(t, selection.WaitPlan)
		require.True(t, selection.WaitPlan.GroupSaturated)
		require.Equal(t, 1, cache.loadCalls, "无链请求只取一次负载，不多取 fresh load")
	})

	t.Run("有链：缓存全满时再取一次 fresh load，fresh 显示有空位就直接服务", func(t *testing.T) {
		cache := &gsatSeqLoadCache{firstLoad: full, laterLoad: idle}
		selection, err := newSvc(cache).SelectAccountWithLoadAwareness(chainedCtx, &groupID, "", "gpt-4", nil)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.True(t, selection.Acquired, "缓存滞后：fresh load 有空位，不应回退")
		require.Nil(t, selection.WaitPlan)
		require.Equal(t, 2, cache.loadCalls)
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	})

	t.Run("有链：fresh load 也显示全满：整组满（经 fresh 确认）", func(t *testing.T) {
		cache := &gsatSeqLoadCache{firstLoad: full, laterLoad: full}
		selection, err := newSvc(cache).SelectAccountWithLoadAwareness(chainedCtx, &groupID, "", "gpt-4", nil)
		require.NoError(t, err)
		require.NotNil(t, selection.WaitPlan)
		require.True(t, selection.WaitPlan.GroupSaturated)
		require.Equal(t, 2, cache.loadCalls)
	})

	t.Run("有链：缓存全满、fresh load 取不到：没有证据，不置 GroupSaturated", func(t *testing.T) {
		cache := &gsatSeqLoadCache{firstLoad: full, laterErr: errors.New("load batch failed")}
		selection, err := newSvc(cache).SelectAccountWithLoadAwareness(chainedCtx, &groupID, "", "gpt-4", nil)
		require.NoError(t, err)
		require.NotNil(t, selection.WaitPlan, "仍然返回单账号 WaitPlan")
		require.False(t, selection.WaitPlan.GroupSaturated)
		require.Equal(t, 2, cache.loadCalls)
	})

	t.Run("有链：缓存显示有空位但抢槽全失败（已试过），fresh load 取不到：仍按原逻辑整组满", func(t *testing.T) {
		cache := &gsatSeqLoadCache{
			stubConcurrencyCache: stubConcurrencyCache{acquireResults: map[int64]bool{1: false}},
			firstLoad:            map[int64]*AccountLoadInfo{1: {AccountID: 1, LoadRate: 10}},
			laterErr:             errors.New("load batch failed"),
		}
		selection, err := newSvc(cache).SelectAccountWithLoadAwareness(chainedCtx, &groupID, "", "gpt-4", nil)
		require.NoError(t, err)
		require.NotNil(t, selection.WaitPlan)
		require.True(t, selection.WaitPlan.GroupSaturated, "只改缓存全满、一次都没试的那种情形")
	})
}
