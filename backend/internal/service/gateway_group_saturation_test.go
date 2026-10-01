//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// 设计 3.5 表 Anthropic 侧构造点（#8-#13）的 GroupSaturated 归类。
// 只置标志，Anthropic 入口的接入属于 PR3。仅 #13（Layer 3 兜底排队）为 true。

func gsatAnthropicRepo(accounts ...Account) *mockAccountRepoForPlatform {
	repo := &mockAccountRepoForPlatform{accounts: accounts, accountsByID: map[int64]*Account{}}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	return repo
}

func gsatAnthropicTwoAccounts() []Account {
	return []Account{
		{ID: 1, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5},
		{ID: 2, Platform: PlatformAnthropic, Priority: 2, Status: StatusActive, Schedulable: true, Concurrency: 5},
	}
}

func TestGroupSaturation_AnthropicConstructors(t *testing.T) {
	ctx := context.Background()
	const model = "claude-3-5-sonnet-20241022"

	t.Run("#13 Layer 3 兜底排队：候选都拿不到槽 -> GroupSaturated", func(t *testing.T) {
		cfg := testConfig()
		cfg.Gateway.Scheduling.LoadBatchEnabled = true
		concurrencyCache := &mockConcurrencyCache{
			acquireResults: map[int64]bool{1: false, 2: false},
			loadMap: map[int64]*AccountLoadInfo{
				1: {AccountID: 1, LoadRate: 10},
				2: {AccountID: 2, LoadRate: 20},
			},
		}
		svc := &GatewayService{
			accountRepo:        gsatAnthropicRepo(gsatAnthropicTwoAccounts()...),
			cache:              &mockGatewayCacheForPlatform{},
			cfg:                cfg,
			concurrencyService: NewConcurrencyService(concurrencyCache),
		}
		result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "wait", model, nil, "", int64(0))
		require.NoError(t, err)
		require.NotNil(t, result.WaitPlan)
		require.True(t, result.WaitPlan.GroupSaturated)
	})

	t.Run("#13 取负载出错、逐个尝试全失败后进入 Layer 3 -> GroupSaturated", func(t *testing.T) {
		cfg := testConfig()
		cfg.Gateway.Scheduling.LoadBatchEnabled = true
		concurrencyCache := &mockConcurrencyCache{
			loadBatchErr:   errors.New("load batch failed"),
			acquireResults: map[int64]bool{1: false, 2: false},
		}
		svc := &GatewayService{
			accountRepo:        gsatAnthropicRepo(gsatAnthropicTwoAccounts()...),
			cache:              &mockGatewayCacheForPlatform{},
			cfg:                cfg,
			concurrencyService: NewConcurrencyService(concurrencyCache),
		}
		result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", model, nil, "", int64(0))
		require.NoError(t, err)
		require.NotNil(t, result.WaitPlan)
		require.True(t, result.WaitPlan.GroupSaturated)
	})

	t.Run("#12 Layer 1.5 无路由配置的粘性账号满 -> 单账号等待", func(t *testing.T) {
		cfg := testConfig()
		cfg.Gateway.Scheduling.LoadBatchEnabled = true
		cfg.Gateway.Scheduling.StickySessionMaxWaiting = 1
		concurrencyCache := &mockConcurrencyCache{
			acquireResults: map[int64]bool{1: false},
			waitCounts:     map[int64]int{1: 0},
		}
		svc := &GatewayService{
			accountRepo:        gsatAnthropicRepo(Account{ID: 1, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5}),
			cache:              &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sticky": 1}},
			cfg:                cfg,
			concurrencyService: NewConcurrencyService(concurrencyCache),
		}
		result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "sticky", model, nil, "", int64(0))
		require.NoError(t, err)
		require.NotNil(t, result.WaitPlan)
		require.False(t, result.WaitPlan.GroupSaturated)
	})

	t.Run("#11 model_routing 路由账号全满 -> 单账号等待（只覆盖路由子集）", func(t *testing.T) {
		groupID := int64(23)
		groupRepo := &mockGroupRepoForGateway{
			groups: map[int64]*Group{
				groupID: {
					ID:                  groupID,
					Platform:            PlatformAnthropic,
					Status:              StatusActive,
					Hydrated:            true,
					ModelRoutingEnabled: true,
					ModelRouting:        map[string][]int64{model: {1, 2}},
				},
			},
		}
		cfg := testConfig()
		cfg.Gateway.Scheduling.LoadBatchEnabled = true
		concurrencyCache := &mockConcurrencyCache{
			acquireResults: map[int64]bool{1: false, 2: false},
			loadMap: map[int64]*AccountLoadInfo{
				1: {AccountID: 1, LoadRate: 10},
				2: {AccountID: 2, LoadRate: 20},
			},
		}
		svc := &GatewayService{
			accountRepo:        gsatAnthropicRepo(gsatAnthropicTwoAccounts()...),
			groupRepo:          groupRepo,
			cache:              &mockGatewayCacheForPlatform{},
			cfg:                cfg,
			concurrencyService: NewConcurrencyService(concurrencyCache),
		}
		result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "route-full", model, nil, "", int64(0))
		require.NoError(t, err)
		require.NotNil(t, result.WaitPlan)
		require.False(t, result.WaitPlan.GroupSaturated)
	})

	t.Run("#10 Layer 1 model_routing 下粘性账号满 -> 单账号等待", func(t *testing.T) {
		groupID := int64(20)
		sessionHash := "route-sticky"
		groupRepo := &mockGroupRepoForGateway{
			groups: map[int64]*Group{
				groupID: {
					ID:                  groupID,
					Platform:            PlatformAnthropic,
					Status:              StatusActive,
					Hydrated:            true,
					ModelRoutingEnabled: true,
					ModelRouting:        map[string][]int64{model: {1, 2}},
				},
			},
		}
		cfg := testConfig()
		cfg.Gateway.Scheduling.LoadBatchEnabled = true
		cfg.Gateway.Scheduling.StickySessionMaxWaiting = 1
		concurrencyCache := &mockConcurrencyCache{
			acquireResults: map[int64]bool{1: false},
			waitCounts:     map[int64]int{1: 0},
		}
		svc := &GatewayService{
			accountRepo:        gsatAnthropicRepo(gsatAnthropicTwoAccounts()...),
			groupRepo:          groupRepo,
			cache:              &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{sessionHash: 1}},
			cfg:                cfg,
			concurrencyService: NewConcurrencyService(concurrencyCache),
		}
		result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, sessionHash, model, nil, "", int64(0))
		require.NoError(t, err)
		require.NotNil(t, result.WaitPlan)
		require.False(t, result.WaitPlan.GroupSaturated)
	})

	t.Run("#9 未开启 load batch，选中的单个账号满 -> 单账号等待", func(t *testing.T) {
		cfg := testConfig()
		cfg.Gateway.Scheduling.LoadBatchEnabled = false
		concurrencyCache := &mockConcurrencyCache{acquireResults: map[int64]bool{1: false, 2: false}}
		svc := &GatewayService{
			accountRepo:        gsatAnthropicRepo(gsatAnthropicTwoAccounts()...),
			cache:              &mockGatewayCacheForPlatform{},
			cfg:                cfg,
			concurrencyService: NewConcurrencyService(concurrencyCache),
		}
		result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", model, nil, "", int64(0))
		require.NoError(t, err)
		require.NotNil(t, result.WaitPlan)
		require.False(t, result.WaitPlan.GroupSaturated)
	})

	t.Run("#8 未开启 load batch，粘性账号满 -> 单账号等待", func(t *testing.T) {
		cfg := testConfig()
		cfg.Gateway.Scheduling.LoadBatchEnabled = false
		cfg.Gateway.Scheduling.StickySessionMaxWaiting = 1
		concurrencyCache := &mockConcurrencyCache{
			acquireResults: map[int64]bool{1: false, 2: false},
			waitCounts:     map[int64]int{1: 0},
		}
		svc := &GatewayService{
			accountRepo:        gsatAnthropicRepo(gsatAnthropicTwoAccounts()...),
			cache:              &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sticky8": 1}},
			cfg:                cfg,
			concurrencyService: NewConcurrencyService(concurrencyCache),
		}
		result, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "sticky8", model, nil, "", int64(0))
		require.NoError(t, err)
		require.NotNil(t, result.WaitPlan)
		require.False(t, result.WaitPlan.GroupSaturated)
	})
}
