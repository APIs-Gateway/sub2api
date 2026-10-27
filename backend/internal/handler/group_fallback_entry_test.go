//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// Key 级分组回退链入口骨架（group_fallback_entry.go）与 Responses 单跳辅助（openai_responses_attempt.go）的单元测试。
// 端到端场景（真实 OpenAIGatewayService + handler）见 openai_responses_chain_test.go。

// ---- 共用替身 ----

// chainRespSettings 是回退链设置的假实现，记录读取次数。
type chainRespSettings struct {
	settings service.GroupFallbackSettings
	calls    atomic.Int32
}

func (p *chainRespSettings) GetGroupFallbackSettings(context.Context) service.GroupFallbackSettings {
	p.calls.Add(1)
	return p.settings
}

// chainRespRoutes 是分组链解析服务的假实现：只实现 ResolveEffectiveChain，并记录调用次数（B1：每请求只解析一次）。
type chainRespRoutes struct {
	service.GroupRouteService
	hops  []service.ChainHop
	err   error
	calls atomic.Int32
}

func (r *chainRespRoutes) ResolveEffectiveChain(_ context.Context, _ *service.APIKey, _ *service.User, _ service.ResolveOptions) (service.Chain, error) {
	r.calls.Add(1)
	return service.Chain{Hops: r.hops}, r.err
}

// chainRespBreaker 是熔断器的假实现：记录 Admit / RecordFailure 涉及的分组，可指定某些分组拒绝放行。
type chainRespBreaker struct {
	mu       sync.Mutex
	admits   []int64
	failures []int64
	deny     map[int64]bool
}

func (b *chainRespBreaker) Admit(_ context.Context, key service.BreakerKey, _ service.BreakerConfig) service.BreakerAdmission {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.admits = append(b.admits, key.GroupID)
	if b.deny[key.GroupID] {
		return service.BreakerAdmission{Allowed: false, State: service.BreakerStateOpen}
	}
	return service.BreakerAdmission{Allowed: true, State: service.BreakerStateClosed}
}

func (b *chainRespBreaker) RecordFailure(_ context.Context, key service.BreakerKey, _ service.BreakerConfig, _ int64, _ string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = append(b.failures, key.GroupID)
}

func (b *chainRespBreaker) RecordProbeSuccess(context.Context, service.BreakerKey, service.BreakerConfig, string) {
}

func (b *chainRespBreaker) ReleaseProbe(context.Context, service.BreakerKey, string) {}

func (b *chainRespBreaker) admitted() []int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int64(nil), b.admits...)
}

func (b *chainRespBreaker) failedGroups() []int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int64(nil), b.failures...)
}

func chainRespGroup(id int64, rate float64, rpmLimit int) *service.Group {
	return &service.Group{
		ID:             id,
		Name:           "chain-resp-group",
		Platform:       service.PlatformOpenAI,
		Status:         service.StatusActive,
		Hydrated:       true,
		RateMultiplier: rate,
		RPMLimit:       rpmLimit,
	}
}

// chainRespHops 按顺序生成链：第 0 项是主分组，其余是用户链。
func chainRespHops(groups ...*service.Group) []service.ChainHop {
	hops := make([]service.ChainHop, 0, len(groups))
	for i, g := range groups {
		source := service.RouteSourceUser
		if i == 0 {
			source = service.RouteSourcePrimary
		}
		hops = append(hops, service.ChainHop{GroupID: g.ID, Group: g, RouteSource: source})
	}
	return hops
}

func newChainRespEntryContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, rec
}

// newChainRespEntry 用假的单跳逻辑搭一个 groupChainEntry（不经过 OpenAIGatewayService）。
func newChainRespEntry(hops []service.ChainHop, breaker service.GroupChainBreakerGate, billing *service.BillingCacheService, ticket *service.GroupRPMTicket) *groupChainEntry {
	primary := hops[0].GroupID
	apiKey := &service.APIKey{ID: 9, UserID: 7, GroupID: &primary, Group: hops[0].Group, User: &service.User{ID: 7}}
	plan := &groupChainPlan{
		rt:       &groupFallbackRuntime{breaker: breaker},
		hops:     hops,
		settings: service.DefaultGroupFallbackSettings(),
		output:   &service.OutputTracker{},
	}
	return newGroupChainEntry(plan, billing, apiKey, ticket, zap.NewNop(), "gpt-5.4", true, nil)
}

func chainRespDone() service.HopResult {
	return service.HopResult{Outcome: service.HopOutcomeDone, Attempts: 1, UpstreamAttempted: true}
}

func chainRespNoAccount(write func()) service.HopResult {
	return service.HopResult{
		Outcome:         service.HopOutcomeFallbackWorthy,
		Reason:          service.FallbackReasonNoAccount,
		WriteFinalError: write,
	}
}

// ---- 构造与 resolvePlan 判定顺序 ----

func TestNewGroupFallbackRuntime_NilWhenDependenciesMissing(t *testing.T) {
	routes := &chainRespRoutes{}
	settings := &chainRespSettings{}
	require.Nil(t, newGroupFallbackRuntime(nil, settings, nil), "没有链解析服务：不启用")
	require.Nil(t, newGroupFallbackRuntime(routes, nil, nil), "没有设置读取：不启用")
	rt := newGroupFallbackRuntime(routes, settings, nil)
	require.NotNil(t, rt)
	require.Nil(t, rt.breaker, "breaker 可以为空（不做熔断）")
}

func TestResolvePlan_GatingOrderAndSingleResolution(t *testing.T) {
	primary := int64(1)
	newKey := func() *service.APIKey {
		return &service.APIKey{ID: 9, UserID: 7, GroupID: &primary, HasGroupRoutes: true, User: &service.User{ID: 7}}
	}
	enabled := service.DefaultGroupFallbackSettings()
	enabled.Enabled = true

	t.Run("nil runtime / nil key / no routes / no primary group never read settings or resolve", func(t *testing.T) {
		routes := &chainRespRoutes{hops: chainRespHops(chainRespGroup(1, 1, 0), chainRespGroup(2, 1, 0))}
		settings := &chainRespSettings{settings: enabled}
		rt := newGroupFallbackRuntime(routes, settings, nil)

		var nilRT *groupFallbackRuntime
		require.Nil(t, nilRT.resolvePlan(context.Background(), newKey(), nil))
		require.Nil(t, rt.resolvePlan(context.Background(), nil, nil))

		noRoutes := newKey()
		noRoutes.HasGroupRoutes = false
		require.Nil(t, rt.resolvePlan(context.Background(), noRoutes, nil))

		noGroup := newKey()
		noGroup.GroupID = nil
		require.Nil(t, rt.resolvePlan(context.Background(), noGroup, nil))

		require.EqualValues(t, 0, settings.calls.Load(), "热路径零额外读取")
		require.EqualValues(t, 0, routes.calls.Load())
	})

	t.Run("switch off reads cached settings only", func(t *testing.T) {
		routes := &chainRespRoutes{hops: chainRespHops(chainRespGroup(1, 1, 0), chainRespGroup(2, 1, 0))}
		settings := &chainRespSettings{settings: service.DefaultGroupFallbackSettings()}
		rt := newGroupFallbackRuntime(routes, settings, nil)
		require.Nil(t, rt.resolvePlan(context.Background(), newKey(), nil))
		require.EqualValues(t, 1, settings.calls.Load())
		require.EqualValues(t, 0, routes.calls.Load(), "开关关闭：不解析链")
	})

	t.Run("resolve error falls back to the legacy path", func(t *testing.T) {
		routes := &chainRespRoutes{err: errors.New("boom")}
		rt := newGroupFallbackRuntime(routes, &chainRespSettings{settings: enabled}, nil)
		require.Nil(t, rt.resolvePlan(context.Background(), newKey(), zap.NewNop()))
		require.Nil(t, rt.resolvePlan(context.Background(), newKey(), nil), "reqLog 为空也不 panic")
		require.EqualValues(t, 2, routes.calls.Load())
	})

	t.Run("chain shorter than two hops is the legacy path", func(t *testing.T) {
		for _, hops := range [][]service.ChainHop{nil, chainRespHops(chainRespGroup(1, 1, 0))} {
			routes := &chainRespRoutes{hops: hops}
			rt := newGroupFallbackRuntime(routes, &chainRespSettings{settings: enabled}, nil)
			require.Nil(t, rt.resolvePlan(context.Background(), newKey(), nil))
			require.EqualValues(t, 1, routes.calls.Load())
		}
	})

	t.Run("two hops produce a plan that carries the resolved slice and settings", func(t *testing.T) {
		hops := chainRespHops(chainRespGroup(1, 1, 0), chainRespGroup(2, 1, 0))
		routes := &chainRespRoutes{hops: hops}
		rt := newGroupFallbackRuntime(routes, &chainRespSettings{settings: enabled}, nil)
		plan := rt.resolvePlan(context.Background(), newKey(), nil)
		require.NotNil(t, plan)
		require.EqualValues(t, 1, routes.calls.Load(), "每个请求只解析一次")
		require.Equal(t, hops, plan.Hops())
		require.Equal(t, hops[0], plan.FirstHop())
		require.True(t, plan.settings.Enabled)
		require.NotNil(t, plan.output)
		require.False(t, plan.HasAdminHop())
	})
}

func TestGroupChainPlan_AccessorsHandleNilAndAdminHops(t *testing.T) {
	var nilPlan *groupChainPlan
	require.Nil(t, nilPlan.Hops())
	require.False(t, nilPlan.HasAdminHop())

	hops := chainRespHops(chainRespGroup(1, 1, 0), chainRespGroup(2, 1, 0))
	hops[1].RouteSource = service.RouteSourceAdmin
	require.True(t, (&groupChainPlan{hops: hops}).HasAdminHop())
}

// ---- forced 账号路由与链的关系（设计 6.3） ----

func TestResolveOpenAIChainPlan_ForcedRouting(t *testing.T) {
	primary := int64(1)
	enabled := service.DefaultGroupFallbackSettings()
	enabled.Enabled = true
	newKey := func() *service.APIKey {
		return &service.APIKey{ID: 9, UserID: 7001, GroupID: &primary, HasGroupRoutes: true, User: &service.User{ID: 7001}}
	}
	newHandler := func(hops []service.ChainHop, forcedUser int64) (*OpenAIGatewayHandler, *chainRespRoutes) {
		routes := &chainRespRoutes{hops: hops}
		cfg := &config.Config{}
		if forcedUser > 0 {
			cfg.Gateway.OpenAIForcedAccountRoutes = []config.OpenAIForcedAccountRoute{{UserID: forcedUser, AccountID: 11}}
		}
		return &OpenAIGatewayHandler{
			cfg:           cfg,
			groupFallback: newGroupFallbackRuntime(routes, &chainRespSettings{settings: enabled}, nil),
		}, routes
	}
	plainHops := chainRespHops(chainRespGroup(1, 1, 0), chainRespGroup(2, 1, 0))
	adminHops := chainRespHops(chainRespGroup(1, 1, 0), chainRespGroup(2, 1, 0))
	adminHops[0].RouteSource = service.RouteSourceAdmin

	t.Run("not forced user gets the plan", func(t *testing.T) {
		h, _ := newHandler(plainHops, 0)
		c, _ := newChainRespEntryContext(t)
		require.NotNil(t, h.resolveOpenAIChainPlan(c, zap.NewNop(), newKey()))
	})
	t.Run("forced user without hidden chain stays on the legacy path", func(t *testing.T) {
		h, routes := newHandler(plainHops, 7001)
		c, _ := newChainRespEntryContext(t)
		require.Nil(t, h.resolveOpenAIChainPlan(c, zap.NewNop(), newKey()))
		require.EqualValues(t, 1, routes.calls.Load())
	})
	t.Run("forced user with hidden chain yields to the chain", func(t *testing.T) {
		h, _ := newHandler(adminHops, 7001)
		c, _ := newChainRespEntryContext(t)
		require.NotNil(t, h.resolveOpenAIChainPlan(c, zap.NewNop(), newKey()))
	})
	t.Run("no runtime", func(t *testing.T) {
		h := &OpenAIGatewayHandler{cfg: &config.Config{}}
		c, _ := newChainRespEntryContext(t)
		require.Nil(t, h.resolveOpenAIChainPlan(c, zap.NewNop(), newKey()))
	})
}

func TestOpenAIForcedAccountID(t *testing.T) {
	var nilHandler *OpenAIGatewayHandler
	require.Zero(t, nilHandler.openAIForcedAccountID(&service.APIKey{UserID: 1}))

	cfg := &config.Config{}
	cfg.Gateway.OpenAIForcedAccountRoutes = []config.OpenAIForcedAccountRoute{{UserID: 5, AccountID: 0}, {UserID: 7, AccountID: 21}}
	h := &OpenAIGatewayHandler{cfg: cfg}
	require.Zero(t, h.openAIForcedAccountID(nil))
	require.Zero(t, h.openAIForcedAccountID(&service.APIKey{UserID: 0}))
	require.Zero(t, h.openAIForcedAccountID(&service.APIKey{UserID: 5}), "AccountID 为 0 的条目无效")
	require.EqualValues(t, 21, h.openAIForcedAccountID(&service.APIKey{UserID: 7}))
	require.Zero(t, h.openAIForcedAccountID(&service.APIKey{UserID: 8}))
	require.Zero(t, (&OpenAIGatewayHandler{}).openAIForcedAccountID(&service.APIKey{UserID: 7}), "没有配置")
}

// ---- groupChainEntry.run：影子 Key、分组层 RPM、静态资格、Unresolved ----

func TestGroupChainEntry_RunServesFallbackHopWithShadowKeys(t *testing.T) {
	c, rec := newChainRespEntryContext(t)
	hops := chainRespHops(chainRespGroup(1, 1, 0), chainRespGroup(2, 1, 0))
	entry := newChainRespEntry(hops, nil, nil, nil)

	var keys []*service.APIKey
	deferredErrorWritten := false
	res := entry.run(c, chainHopFuncs{
		Attempt: func(_ context.Context, hopKey *service.APIKey, info service.HopInfo) service.HopResult {
			keys = append(keys, hopKey)
			if info.Index == 0 {
				return chainRespNoAccount(func() { deferredErrorWritten = true })
			}
			return chainRespDone()
		},
	})

	require.Equal(t, service.ChainRunServed, res.Status)
	require.Equal(t, 1, res.ServedIndex)
	require.False(t, deferredErrorWritten, "兜底成功后不写暂存的主分组错误")
	require.Zero(t, rec.Body.Len())
	require.Len(t, keys, 2)
	for i, key := range keys {
		require.EqualValues(t, hops[i].GroupID, *key.GroupID, "每一跳的影子 Key 是该跳的分组")
		require.NotNil(t, key.HomeGroupID)
		require.EqualValues(t, 1, *key.HomeGroupID, "主分组始终是用户 Key 的主分组")
		require.Equal(t, hops[i].RouteSource, key.RouteSource)
	}
	require.EqualValues(t, int64(2), c.GetInt64(service.OpsServedGroupIDKey), "ops 事件记录最后一跳的服务分组")
	hop, ok := entry.servedHop(res)
	require.True(t, ok)
	require.EqualValues(t, 2, hop.GroupID)
}

func TestGroupChainEntry_RunFlushesDeferredErrorWhenNoHopServes(t *testing.T) {
	c, rec := newChainRespEntryContext(t)
	entry := newChainRespEntry(chainRespHops(chainRespGroup(1, 1, 0), chainRespGroup(2, 1, 0)), nil, nil, nil)

	res := entry.run(c, chainHopFuncs{
		Attempt: func(_ context.Context, _ *service.APIKey, info service.HopInfo) service.HopResult {
			if info.Index == 0 {
				return chainRespNoAccount(func() { c.String(http.StatusServiceUnavailable, "primary error") })
			}
			// 末跳由 attempt 自己写出原始错误。
			c.String(http.StatusServiceUnavailable, "last hop error")
			return service.HopResult{Outcome: service.HopOutcomeFallbackWorthy, Reason: service.FallbackReasonNoAccount, ErrorWritten: true}
		},
	})

	require.Equal(t, service.ChainRunExhausted, res.Status)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "last hop error", rec.Body.String(), "最后一跳的原始错误，不重复写前面跳暂存的错误")
	_, ok := entry.servedHop(res)
	require.False(t, ok)
}

func TestGroupChainEntry_FirstHopTicketReleasedOnlyWhenFirstHopNeverRan(t *testing.T) {
	user := &service.User{ID: 7}
	newTicket := func(t *testing.T) (*hopRPMCache, *service.BillingCacheService, *service.GroupRPMTicket, []service.ChainHop) {
		t.Helper()
		cache := &hopRPMCache{counts: []int{1}}
		billing := newHopRPMBilling(t, cache)
		hops := chainRespHops(chainRespGroup(1, 1, 5), chainRespGroup(2, 1, 0))
		verdict, ticket, err := CheckHopGroupRPM(context.Background(), billing, user, hops[0], 0)
		require.Equal(t, GroupRPMProceed, verdict)
		require.NoError(t, err)
		require.NotNil(t, ticket)
		return cache, billing, ticket, hops
	}

	t.Run("breaker skips the first hop: its RPM count is returned", func(t *testing.T) {
		cache, billing, ticket, hops := newTicket(t)
		c, _ := newChainRespEntryContext(t)
		entry := newChainRespEntry(hops, &chainRespBreaker{deny: map[int64]bool{1: true}}, billing, ticket)
		entry.apiKey.User = user

		res := entry.run(c, chainHopFuncs{
			Attempt: func(_ context.Context, _ *service.APIKey, info service.HopInfo) service.HopResult {
				require.Equal(t, 1, info.Index, "首跳被熔断跳过，只会进入第二跳")
				return chainRespDone()
			},
		})
		require.Equal(t, service.ChainRunServed, res.Status)
		require.EqualValues(t, 1, cache.decr, "整个请求没有进入过第 0 跳：入口为它计的分组层 RPM 要退回")
	})

	t.Run("first hop served: count kept", func(t *testing.T) {
		cache, billing, ticket, hops := newTicket(t)
		c, _ := newChainRespEntryContext(t)
		entry := newChainRespEntry(hops, nil, billing, ticket)
		entry.apiKey.User = user

		res := entry.run(c, chainHopFuncs{
			Attempt: func(context.Context, *service.APIKey, service.HopInfo) service.HopResult { return chainRespDone() },
		})
		require.Equal(t, service.ChainRunServed, res.Status)
		require.EqualValues(t, 0, cache.decr)
	})

	t.Run("first hop never reaches upstream and falls back: count returned once", func(t *testing.T) {
		cache, billing, ticket, hops := newTicket(t)
		c, _ := newChainRespEntryContext(t)
		entry := newChainRespEntry(hops, nil, billing, ticket)
		entry.apiKey.User = user

		res := entry.run(c, chainHopFuncs{
			Attempt: func(_ context.Context, _ *service.APIKey, info service.HopInfo) service.HopResult {
				if info.Index == 0 {
					return chainRespNoAccount(func() {})
				}
				return chainRespDone()
			},
		})
		require.Equal(t, service.ChainRunServed, res.Status)
		require.EqualValues(t, 1, cache.decr, "第 0 跳没有向上游发出过请求：退回，且只退一次")
	})
}

func TestGroupChainEntry_IneligibleHopsAreSkippedWithoutRPMAndUnresolvedIsWritten(t *testing.T) {
	user := &service.User{ID: 7}
	cache := &hopRPMCache{counts: []int{1, 1}}
	billing := newHopRPMBilling(t, cache)
	hops := chainRespHops(chainRespGroup(1, 1, 5), chainRespGroup(2, 1, 5))
	verdict, ticket, err := CheckHopGroupRPM(context.Background(), billing, user, hops[0], 0)
	require.Equal(t, GroupRPMProceed, verdict)
	require.NoError(t, err)
	require.EqualValues(t, 1, cache.incr)

	c, rec := newChainRespEntryContext(t)
	entry := newChainRespEntry(hops, nil, billing, ticket)
	entry.apiKey.User = user

	attempts := 0
	unresolved := 0
	res := entry.run(c, chainHopFuncs{
		Eligible: func(*service.APIKey, service.HopInfo) bool { return false },
		Attempt: func(context.Context, *service.APIKey, service.HopInfo) service.HopResult {
			attempts++
			return chainRespDone()
		},
		WriteUnresolved: func() {
			unresolved++
			c.String(http.StatusServiceUnavailable, "unresolved")
		},
	})

	require.Equal(t, service.ChainRunUnresolved, res.Status)
	require.Zero(t, attempts, "没有资格的跳不进入单跳逻辑")
	require.Equal(t, 1, unresolved, "没有任何一跳运行也没有暂存错误：入口自己写一个响应")
	require.Equal(t, "unresolved", rec.Body.String())
	require.EqualValues(t, 1, cache.incr, "被静态资格跳过的非首跳不计 RPM")
	require.EqualValues(t, 1, cache.decr, "被跳过的首跳退回入口计的 RPM")
}

func TestGroupChainEntry_NonFirstHopRPMExceeded(t *testing.T) {
	user := &service.User{ID: 7}

	t.Run("middle hop over limit is skipped and its count returned", func(t *testing.T) {
		cache := &hopRPMCache{counts: []int{2}}
		billing := newHopRPMBilling(t, cache)
		hops := chainRespHops(chainRespGroup(1, 1, 0), chainRespGroup(2, 1, 1), chainRespGroup(3, 1, 0))
		c, _ := newChainRespEntryContext(t)
		entry := newChainRespEntry(hops, nil, billing, nil)
		entry.apiKey.User = user

		var tried []int64
		res := entry.run(c, chainHopFuncs{
			Attempt: func(_ context.Context, hopKey *service.APIKey, info service.HopInfo) service.HopResult {
				tried = append(tried, *hopKey.GroupID)
				if info.Index == 0 {
					return chainRespNoAccount(func() {})
				}
				return chainRespDone()
			},
			WriteRPMExceeded: func(error) { t.Fatal("非末跳超限只跳过，不写 429") },
		})
		require.Equal(t, service.ChainRunServed, res.Status)
		require.Equal(t, []int64{1, 3}, tried)
		require.EqualValues(t, 1, cache.decr)
	})

	t.Run("last hop over limit overrides the deferred error with the rate limit error", func(t *testing.T) {
		cache := &hopRPMCache{counts: []int{2}}
		billing := newHopRPMBilling(t, cache)
		hops := chainRespHops(chainRespGroup(1, 1, 0), chainRespGroup(2, 1, 1))
		c, _ := newChainRespEntryContext(t)
		entry := newChainRespEntry(hops, nil, billing, nil)
		entry.apiKey.User = user

		primaryErrorWritten := false
		var rpmErr error
		res := entry.run(c, chainHopFuncs{
			Attempt: func(_ context.Context, _ *service.APIKey, info service.HopInfo) service.HopResult {
				require.Equal(t, 0, info.Index, "末跳超限被跳过，不进入单跳逻辑")
				return chainRespNoAccount(func() { primaryErrorWritten = true })
			},
			WriteRPMExceeded: func(err error) { rpmErr = err },
		})
		require.Equal(t, service.ChainRunExhausted, res.Status)
		require.ErrorIs(t, rpmErr, service.ErrGroupRPMExceeded)
		require.False(t, primaryErrorWritten, "末跳 429 覆盖前面跳暂存的错误")
		require.EqualValues(t, 1, cache.decr)
	})
}

func TestGroupChainEntry_ServedGroupIDForOps(t *testing.T) {
	hops := chainRespHops(chainRespGroup(1, 1, 0), chainRespGroup(2, 1, 0))
	entry := newChainRespEntry(hops, nil, nil, nil)
	require.EqualValues(t, 0, entry.servedGroupIDForOps(hops[0]), "主分组不标记")
	require.EqualValues(t, 2, entry.servedGroupIDForOps(hops[1]))

	entry.apiKey.GroupID = nil
	require.EqualValues(t, 1, entry.servedGroupIDForOps(hops[0]))
}

func TestGroupChainEntry_ServedHopOnlyWhenServed(t *testing.T) {
	hops := chainRespHops(chainRespGroup(1, 1, 0), chainRespGroup(2, 1, 0))
	entry := newChainRespEntry(hops, nil, nil, nil)

	hop, ok := entry.servedHop(service.ChainRunResult{Status: service.ChainRunServed, ServedIndex: 1})
	require.True(t, ok)
	require.EqualValues(t, 2, hop.GroupID)
	for _, res := range []service.ChainRunResult{
		{Status: service.ChainRunTerminal, ServedIndex: 1},
		{Status: service.ChainRunExhausted, ServedIndex: -1},
		{Status: service.ChainRunServed, ServedIndex: -1},
		{Status: service.ChainRunServed, ServedIndex: 2},
	} {
		_, ok := entry.servedHop(res)
		require.False(t, ok)
	}
}

func TestFormatChainTrace(t *testing.T) {
	require.Equal(t, "", formatChainTrace(nil))
	got := formatChainTrace([]service.HopTrace{
		{Index: 0, GroupID: 1, Outcome: service.HopOutcomeFallbackWorthy, Reason: service.FallbackReasonBusy},
		{Index: 1, GroupID: 2, Outcome: service.HopOutcomeSkipped, SkippedBy: "breaker_open"},
		{Index: 2, GroupID: 3, Outcome: service.HopOutcomeDone, BreakerBypassed: true},
	})
	require.Equal(t, "0@1:3/busy,1@2:4/skip=breaker_open,2@3:1/bypass", got)
}

func TestGroupChainEntry_LogRunLevels(t *testing.T) {
	hops := chainRespHops(chainRespGroup(1, 1, 0), chainRespGroup(2, 1, 0))
	entry := newChainRespEntry(hops, nil, nil, nil)
	core, logs := observer.New(zapcore.DebugLevel)
	entry.reqLog = zap.New(core)

	entry.logRun(service.ChainRunResult{
		Status: service.ChainRunServed, ServedIndex: 0,
		Trace: []service.HopTrace{{Index: 0, GroupID: 1, Outcome: service.HopOutcomeDone}},
	})
	entry.logRun(service.ChainRunResult{
		Status: service.ChainRunServed, ServedIndex: 1,
		Trace: []service.HopTrace{{Index: 0, GroupID: 1, Outcome: service.HopOutcomeFallbackWorthy}, {Index: 1, GroupID: 2, Outcome: service.HopOutcomeDone}},
	})
	entry.logRun(service.ChainRunResult{Status: service.ChainRunExhausted, ServedIndex: -1})

	entries := logs.FilterMessage("group_fallback.run_finished").All()
	require.Len(t, entries, 3)
	require.Equal(t, zapcore.DebugLevel, entries[0].Level, "一帆风顺只打 debug")
	require.Equal(t, zapcore.InfoLevel, entries[1].Level, "发生了回退要打 info")
	require.Equal(t, int64(2), entries[1].ContextMap()["served_group_id"])
	require.Equal(t, zapcore.InfoLevel, entries[2].Level)
	require.Equal(t, int64(0), entries[2].ContextMap()["served_group_id"])

	entry.reqLog = nil
	require.NotPanics(t, func() { entry.logRun(service.ChainRunResult{Status: service.ChainRunServed}) })
}

// ---- Responses 单跳：没号的分类（规则 1 / S-3） ----

func TestOpenAIResponsesRun_NoAccountFacts(t *testing.T) {
	budgetErr := service.NewOpenAISelectionBudgetExhaustedErrorForTest("gpt-5.4")

	t.Run("budget exhausted is busy and never counted toward the breaker (S-3)", func(t *testing.T) {
		f := (&openAIResponsesRun{}).noAccountFacts(nil, nil, budgetErr)
		require.Equal(t, service.HopFailureBusyTimeout, f.Kind)
		res := service.ClassifyHopFailure(f)
		require.Equal(t, service.HopOutcomeFallbackWorthy, res.Outcome)
		require.Equal(t, service.FallbackReasonBusy, res.Reason)
		require.Equal(t, service.BreakerSignalNone, res.Breaker)
	})

	t.Run("no account after the one reselect is busy and not counted", func(t *testing.T) {
		policy := &openAIHopSlotPolicy{reselectUsed: true}
		f := (&openAIResponsesRun{}).noAccountFacts(nil, policy, service.ErrNoAvailableAccounts)
		require.Equal(t, service.HopFailureBusyTimeout, f.Kind)
		res := service.ClassifyHopFailure(f)
		require.Equal(t, service.FallbackReasonBusy, res.Reason)
		require.Equal(t, service.BreakerSignalNone, res.Breaker)

		// 同样的没号，没有重选过：才是「没号」，计入熔断。
		f = (&openAIResponsesRun{}).noAccountFacts(nil, &openAIHopSlotPolicy{}, service.ErrNoAvailableAccounts)
		require.Equal(t, service.HopFailureNoAccount, f.Kind)
		res = service.ClassifyHopFailure(f)
		require.Equal(t, service.FallbackReasonNoAccount, res.Reason)
		require.Equal(t, service.BreakerSignalCount, res.Breaker)
	})

	t.Run("a nil selection counts as no account", func(t *testing.T) {
		f := (&openAIResponsesRun{}).noAccountFacts(nil, nil, nil)
		require.Equal(t, service.HopFailureNoAccount, f.Kind)
		require.True(t, f.PoolHasAccounts)
		require.True(t, f.ModelSupported)
		require.False(t, f.CapabilityBlocked)
	})

	t.Run("capability-blocked requests are not a pool fault", func(t *testing.T) {
		for name, run := range map[string]*openAIResponsesRun{
			"image":          {imageIntent: true},
			"legacy compact": {legacyCompact: true},
			"native v2":      {nativeV2: true},
		} {
			f := run.noAccountFacts(nil, nil, service.ErrNoAvailableAccounts)
			require.True(t, f.CapabilityBlocked, name)
			require.Equal(t, service.BreakerSignalNone, service.ClassifyHopFailure(f).Breaker, name)
		}
		f := (&openAIResponsesRun{}).noAccountFacts(nil, nil, service.ErrNoAvailableCompactAccounts)
		require.True(t, f.CapabilityBlocked)
	})

	t.Run("selection errors that are not no-account errors stay terminal", func(t *testing.T) {
		f := (&openAIResponsesRun{}).noAccountFacts(nil, nil, errors.New("snapshot unavailable"))
		require.Equal(t, service.HopFailureOther, f.Kind)
		require.Equal(t, service.HopOutcomeTerminal, service.ClassifyHopFailure(f).Outcome)
	})
}

func TestOpenAIResponsesRun_StreamNowFollowsStreamStartedAndHeartbeat(t *testing.T) {
	started := false
	run := &openAIResponsesRun{streamStarted: &started}
	output := &service.OutputTracker{}
	require.False(t, run.streamNow(output))
	require.False(t, run.streamNow(nil), "无链请求没有 tracker")

	output.MarkStreamCommitted()
	require.True(t, run.streamNow(output), "只写过心跳也要按流式收尾")

	started = true
	require.True(t, run.streamNow(nil), "读取的是调用时刻的状态")
}

// ---- S8：cyber 事件里的分组是用户自己的主分组 ----

func TestCyberPolicyUserVisibleGroup(t *testing.T) {
	groupID, name := cyberPolicyUserVisibleGroup(nil)
	require.Nil(t, groupID)
	require.Empty(t, name)

	primary := int64(1)
	ordinary := &service.APIKey{GroupID: &primary, Group: &service.Group{ID: 1, Name: "primary-group"}}
	groupID, name = cyberPolicyUserVisibleGroup(ordinary)
	require.EqualValues(t, 1, *groupID)
	require.Equal(t, "primary-group", name, "普通 Key 与引入回退链之前一致")

	// 第 0 跳的影子 Key：服务分组就是主分组，原样。
	first := NewServedAPIKey(ordinary, service.ChainHop{GroupID: 1, Group: &service.Group{ID: 1, Name: "primary-group"}, RouteSource: service.RouteSourcePrimary})
	groupID, name = cyberPolicyUserVisibleGroup(first)
	require.EqualValues(t, 1, *groupID)
	require.Equal(t, "primary-group", name)

	// 兜底分组的影子 Key：写主分组 ID，兜底分组的名字不能出现。
	fallback := NewServedAPIKey(ordinary, service.ChainHop{GroupID: 2, Group: &service.Group{ID: 2, Name: "hidden-fallback-group"}, RouteSource: service.RouteSourceAdmin})
	groupID, name = cyberPolicyUserVisibleGroup(fallback)
	require.EqualValues(t, 1, *groupID)
	require.Empty(t, name, "兜底分组的名字不能发给用户")
}
