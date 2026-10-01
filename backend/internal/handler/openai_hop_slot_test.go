package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// 回退链的「繁忙短等 + 组内重选一次」：openAIHopSlotPolicy 与 acquireResponsesAccountSlotForHop。
// hop 为 nil（无链、末跳）必须与引入回退链之前的 acquireResponsesAccountSlot 完全一致。

type hopSlotCache struct {
	*concurrencyCacheMock
	queueFull    bool
	waitIncCalls int32
}

func (c *hopSlotCache) IncrementAccountWaitCount(ctx context.Context, accountID int64, maxWait int) (bool, error) {
	atomic.AddInt32(&c.waitIncCalls, 1)
	return !c.queueFull, nil
}

type hopSlotEnv struct {
	h        *OpenAIGatewayHandler
	cache    *hopSlotCache
	recorder *httptest.ResponseRecorder
	c        *gin.Context
}

func newHopSlotEnv(t *testing.T, queueFull bool, acquireFn func(accountID int64) bool) *hopSlotEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	account := service.Account{
		ID:          7001,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
	}
	repo := &openAIWSUsageHandlerAccountRepoStub{account: account}
	mock := &concurrencyCacheMock{
		acquireAccountSlotFn: func(_ context.Context, accountID int64, _ int, _ string) (bool, error) {
			if acquireFn == nil {
				return false, nil
			}
			return acquireFn(accountID), nil
		},
	}
	cache := &hopSlotCache{concurrencyCacheMock: mock, queueFull: queueFull}
	svc := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, &config.Config{}, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := &OpenAIGatewayHandler{
		gatewayService:    svc,
		concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return &hopSlotEnv{h: h, cache: cache, recorder: recorder, c: c}
}

func hopSlotSelection(accountID int64, timeout time.Duration, saturated bool) *service.AccountSelectionResult {
	account := &service.Account{
		ID:          accountID,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
	}
	return &service.AccountSelectionResult{
		Account: account,
		WaitPlan: &service.AccountWaitPlan{
			AccountID:      accountID,
			MaxConcurrency: 1,
			Timeout:        timeout,
			MaxWaiting:     5,
			GroupSaturated: saturated,
		},
	}
}

func hopSlotPolicy(busy, sticky time.Duration) *openAIHopSlotPolicy {
	return &openAIHopSlotPolicy{BusyWait: busy, StickyWait: sticky}
}

func (e *hopSlotEnv) acquire(selection *service.AccountSelectionResult, decision service.OpenAIAccountScheduleDecision, hop *openAIHopSlotPolicy) (func(), accountSlotAcquireStatus) {
	streamStarted := false
	return e.h.acquireResponsesAccountSlotForHop(e.c, nil, "sess", selection, decision, hop, false, &streamStarted, zap.NewNop())
}

// ---------- 纯函数：构造与等待时长 ----------

func TestNewOpenAIHopSlotPolicy_OnlyForChainedNonLastHop(t *testing.T) {
	settings := service.DefaultGroupFallbackSettings()
	now := time.Now()

	require.Nil(t, newOpenAIHopSlotPolicy(service.HopInfo{HasChain: false, IsLast: false}, settings, now), "无链：nil，行为不变")
	require.Nil(t, newOpenAIHopSlotPolicy(service.HopInfo{HasChain: true, IsLast: true}, settings, now), "末跳：nil，保持原等待与原错误")

	p := newOpenAIHopSlotPolicy(service.HopInfo{HasChain: true, IsLast: false, TimeRemaining: 10 * time.Second}, settings, now)
	require.NotNil(t, p)
	require.Equal(t, 2*time.Second, p.BusyWait)
	require.Equal(t, 8*time.Second, p.StickyWait)
	require.Equal(t, now.Add(10*time.Second), p.Deadline)
	require.False(t, p.ReselectUsed())

	noDeadline := newOpenAIHopSlotPolicy(service.HopInfo{HasChain: true}, settings, now)
	require.True(t, noDeadline.Deadline.IsZero())

	var nilPolicy *openAIHopSlotPolicy
	require.False(t, nilPolicy.ReselectUsed())
	ctx := context.Background()
	require.True(t, ctx == nilPolicy.selectionContext(ctx), "无链 / 末跳不给 ctx 打补试标记")
	require.False(t, ctx == p.selectionContext(ctx))
}

func TestOpenAIHopSlotPolicy_WaitFor(t *testing.T) {
	sticky := service.OpenAIAccountScheduleDecision{StickySessionHit: true, Layer: "session_hash"}
	balance := service.OpenAIAccountScheduleDecision{Layer: "load_balance"}
	prev := service.OpenAIAccountScheduleDecision{StickyPreviousHit: true, Layer: "previous_response_id"}
	plan := func(timeout time.Duration, saturated bool) *service.AccountWaitPlan {
		return &service.AccountWaitPlan{Timeout: timeout, GroupSaturated: saturated}
	}

	var nilPolicy *openAIHopSlotPolicy
	require.Equal(t, 120*time.Second, nilPolicy.waitFor(plan(120*time.Second, false), sticky), "无链保持原来的 120 秒")
	require.Zero(t, nilPolicy.waitFor(nil, sticky))

	p := hopSlotPolicy(2*time.Second, 8*time.Second)
	require.Equal(t, 2*time.Second, p.waitFor(plan(30*time.Second, true), balance), "整组满：busy_wait_ms")
	require.Equal(t, 2*time.Second, p.waitFor(plan(30*time.Second, true), sticky), "整组满：即使粘性标记也用 busy")
	require.Equal(t, 8*time.Second, p.waitFor(plan(120*time.Second, false), sticky), "单账号粘性：sticky_wait_ms")
	require.Equal(t, 2*time.Second, p.waitFor(plan(30*time.Second, false), balance), "单账号非粘性：busy_wait_ms")
	require.Equal(t, 3*time.Second, p.waitFor(plan(3*time.Second, false), sticky), "WaitPlan.Timeout 更短时取它")
	require.Equal(t, 120*time.Second, p.waitFor(plan(120*time.Second, false), prev), "规则 3：命中 previous_response_id 层保持原时长")
	require.Equal(t, 120*time.Second, p.waitFor(plan(120*time.Second, true), prev), "规则 3 优先于 GroupSaturated")

	p.reselectUsed = true
	require.Equal(t, 2*time.Second, p.waitFor(plan(120*time.Second, false), sticky), "重选之后任何 WaitPlan 都按 busy_wait_ms")

	capped := &openAIHopSlotPolicy{BusyWait: 2 * time.Second, StickyWait: 8 * time.Second, Deadline: time.Now().Add(500 * time.Millisecond)}
	got := capped.waitFor(plan(120*time.Second, false), sticky)
	require.Greater(t, got, time.Duration(0))
	require.LessOrEqual(t, got, 500*time.Millisecond, "受剩余总预算封顶")

	expired := &openAIHopSlotPolicy{BusyWait: 2 * time.Second, StickyWait: 8 * time.Second, Deadline: time.Now().Add(-time.Second)}
	require.Zero(t, expired.waitFor(plan(120*time.Second, false), sticky))
}

func TestOpenAIHopSlotPolicy_OnCapacityFailure(t *testing.T) {
	balance := service.OpenAIAccountScheduleDecision{Layer: "load_balance"}
	prev := service.OpenAIAccountScheduleDecision{StickyPreviousHit: true}
	single := &service.AccountWaitPlan{}
	saturated := &service.AccountWaitPlan{GroupSaturated: true}

	var nilPolicy *openAIHopSlotPolicy
	_, deferred := nilPolicy.onCapacityFailure(single, balance, service.HopFailureBusyTimeout, nil)
	require.False(t, deferred)

	p := hopSlotPolicy(time.Second, time.Second)
	_, deferred = p.onCapacityFailure(single, prev, service.HopFailureBusyTimeout, nil)
	require.False(t, deferred, "规则 3：不接管，保持原错误")
	require.False(t, p.reselectUsed, "规则 3 不消耗重选机会")

	status, deferred := p.onCapacityFailure(single, balance, service.HopFailureQueueFull, nil)
	require.True(t, deferred)
	require.Equal(t, accountSlotRetrySelection, status, "单账号等待：先在组内重选一次")
	require.True(t, p.reselectUsed)
	require.Equal(t, service.HopFailureQueueFull, p.lastKind)

	status, deferred = p.onCapacityFailure(single, balance, service.HopFailureBusyTimeout, nil)
	require.True(t, deferred)
	require.Equal(t, accountSlotBusyDeferred, status, "已重选过一次：按繁忙回退")

	q := hopSlotPolicy(time.Second, time.Second)
	status, deferred = q.onCapacityFailure(saturated, balance, service.HopFailureBusyTimeout, nil)
	require.True(t, deferred)
	require.Equal(t, accountSlotBusyDeferred, status, "GroupSaturated：直接按繁忙，不重选")
	require.False(t, q.reselectUsed)
}

// ---------- acquireResponsesAccountSlotForHop ----------

// 无链 / 末跳（hop == nil）：原有各分支的响应与状态不变。
func TestAcquireSlotForHop_NilHopKeepsLegacyBehavior(t *testing.T) {
	t.Run("等待超时：写 429 并返回 Failed", func(t *testing.T) {
		e := newHopSlotEnv(t, false, nil)
		release, status := e.acquire(hopSlotSelection(7001, 60*time.Millisecond, false), service.OpenAIAccountScheduleDecision{}, nil)
		require.Nil(t, release)
		require.Equal(t, accountSlotAcquireFailed, status)
		require.Equal(t, http.StatusTooManyRequests, e.recorder.Code)
		require.Contains(t, e.recorder.Body.String(), "Concurrency limit exceeded for account")
	})

	t.Run("排队已满：写 429 并返回 Failed", func(t *testing.T) {
		e := newHopSlotEnv(t, true, nil)
		release, status := e.acquire(hopSlotSelection(7001, time.Second, false), service.OpenAIAccountScheduleDecision{}, nil)
		require.Nil(t, release)
		require.Equal(t, accountSlotAcquireFailed, status)
		require.Equal(t, http.StatusTooManyRequests, e.recorder.Code)
		require.Contains(t, e.recorder.Body.String(), "Too many pending requests")
	})

	t.Run("旧入口 acquireResponsesAccountSlot 与 ForHop(nil) 一致", func(t *testing.T) {
		e := newHopSlotEnv(t, true, nil)
		streamStarted := false
		release, status := e.h.acquireResponsesAccountSlot(e.c, nil, "sess", hopSlotSelection(7001, time.Second, false), false, &streamStarted, zap.NewNop())
		require.Nil(t, release)
		require.Equal(t, accountSlotAcquireFailed, status)
		require.Equal(t, http.StatusTooManyRequests, e.recorder.Code)
	})

	t.Run("无链请求使用完整的 WaitPlan.Timeout（不被任何短等封顶）", func(t *testing.T) {
		e := newHopSlotEnv(t, false, nil)
		start := time.Now()
		_, status := e.acquire(hopSlotSelection(7001, 250*time.Millisecond, false), service.OpenAIAccountScheduleDecision{StickySessionHit: true}, nil)
		require.Equal(t, accountSlotAcquireFailed, status)
		require.GreaterOrEqual(t, time.Since(start), 200*time.Millisecond)
	})

	t.Run("快速抢到槽：直接返回 Acquired", func(t *testing.T) {
		e := newHopSlotEnv(t, false, func(int64) bool { return true })
		release, status := e.acquire(hopSlotSelection(7001, time.Second, false), service.OpenAIAccountScheduleDecision{}, nil)
		require.Equal(t, accountSlotAcquired, status)
		require.NotNil(t, release)
		release()
		require.Empty(t, e.recorder.Body.String())
	})

	t.Run("selection / WaitPlan 为空：写 503", func(t *testing.T) {
		e := newHopSlotEnv(t, false, nil)
		_, status := e.acquire(nil, service.OpenAIAccountScheduleDecision{}, nil)
		require.Equal(t, accountSlotAcquireFailed, status)
		require.Equal(t, http.StatusServiceUnavailable, e.recorder.Code)

		e2 := newHopSlotEnv(t, false, nil)
		noPlan := hopSlotSelection(7001, time.Second, false)
		noPlan.WaitPlan = nil
		_, status = e2.acquire(noPlan, service.OpenAIAccountScheduleDecision{}, nil)
		require.Equal(t, accountSlotAcquireFailed, status)
		require.Equal(t, http.StatusServiceUnavailable, e2.recorder.Code)
	})
}

// 有链非末跳：失败分支不写响应。
func TestAcquireSlotForHop_ChainedNonLastDoesNotWriteResponse(t *testing.T) {
	balance := service.OpenAIAccountScheduleDecision{Layer: "load_balance"}

	t.Run("单账号等待超时：返回 RetrySelection，不写响应，短等生效", func(t *testing.T) {
		e := newHopSlotEnv(t, false, nil)
		hop := hopSlotPolicy(80*time.Millisecond, 8*time.Second)
		start := time.Now()
		release, status := e.acquire(hopSlotSelection(7001, 30*time.Second, false), balance, hop)
		require.Nil(t, release)
		require.Equal(t, accountSlotRetrySelection, status)
		require.Less(t, time.Since(start), 5*time.Second, "只等 busy_wait_ms，而不是 WaitPlan 的 30 秒")
		require.Empty(t, e.recorder.Body.String())
		require.Equal(t, http.StatusOK, e.recorder.Code)
		require.True(t, hop.ReselectUsed())
	})

	t.Run("单账号排队已满：返回 RetrySelection，不写响应", func(t *testing.T) {
		e := newHopSlotEnv(t, true, nil)
		hop := hopSlotPolicy(time.Second, time.Second)
		_, status := e.acquire(hopSlotSelection(7001, time.Second, false), balance, hop)
		require.Equal(t, accountSlotRetrySelection, status)
		require.Empty(t, e.recorder.Body.String())
	})

	t.Run("GroupSaturated 等待超时：直接 BusyDeferred，不重选，不写响应", func(t *testing.T) {
		e := newHopSlotEnv(t, false, nil)
		hop := hopSlotPolicy(60*time.Millisecond, 8*time.Second)
		_, status := e.acquire(hopSlotSelection(7001, 30*time.Second, true), balance, hop)
		require.Equal(t, accountSlotBusyDeferred, status)
		require.Empty(t, e.recorder.Body.String())
		require.False(t, hop.ReselectUsed())
	})

	t.Run("GroupSaturated 排队已满：BusyDeferred", func(t *testing.T) {
		e := newHopSlotEnv(t, true, nil)
		hop := hopSlotPolicy(time.Second, time.Second)
		_, status := e.acquire(hopSlotSelection(7001, time.Second, true), balance, hop)
		require.Equal(t, accountSlotBusyDeferred, status)
		require.Empty(t, e.recorder.Body.String())
	})

	t.Run("总预算已用完：不排队直接按等待超时处理", func(t *testing.T) {
		e := newHopSlotEnv(t, false, nil)
		hop := &openAIHopSlotPolicy{BusyWait: time.Second, StickyWait: time.Second, Deadline: time.Now().Add(-time.Second)}
		_, status := e.acquire(hopSlotSelection(7001, time.Second, false), balance, hop)
		require.Equal(t, accountSlotRetrySelection, status)
		require.Zero(t, atomic.LoadInt32(&e.cache.waitIncCalls), "预算用完不应占用排队计数")
		require.Empty(t, e.recorder.Body.String())
	})

	t.Run("有链也能直接抢到槽：不受影响", func(t *testing.T) {
		e := newHopSlotEnv(t, false, func(int64) bool { return true })
		hop := hopSlotPolicy(time.Second, time.Second)
		release, status := e.acquire(hopSlotSelection(7001, time.Second, false), balance, hop)
		require.Equal(t, accountSlotAcquired, status)
		require.NotNil(t, release)
		release()
		require.False(t, hop.ReselectUsed())
	})
}

// 规则 3：命中 previous_response_id 层，不重选也不回退：保持原等待时长和原错误响应。
func TestAcquireSlotForHop_PreviousResponseLayerKeepsOriginalWaitAndError(t *testing.T) {
	prev := service.OpenAIAccountScheduleDecision{Layer: "previous_response_id", StickyPreviousHit: true}

	t.Run("等待超时：用原 WaitPlan 时长，写原错误，不重选", func(t *testing.T) {
		e := newHopSlotEnv(t, false, nil)
		hop := hopSlotPolicy(time.Millisecond, time.Millisecond)
		start := time.Now()
		_, status := e.acquire(hopSlotSelection(7001, 250*time.Millisecond, false), prev, hop)
		require.Equal(t, accountSlotAcquireFailed, status)
		require.GreaterOrEqual(t, time.Since(start), 200*time.Millisecond, "没有被 busy_wait_ms 封顶")
		require.Equal(t, http.StatusTooManyRequests, e.recorder.Code)
		require.Contains(t, e.recorder.Body.String(), "Concurrency limit exceeded for account")
		require.False(t, hop.ReselectUsed())
	})

	t.Run("排队已满：写原 429，不重选", func(t *testing.T) {
		e := newHopSlotEnv(t, true, nil)
		hop := hopSlotPolicy(time.Second, time.Second)
		_, status := e.acquire(hopSlotSelection(7001, time.Second, true), prev, hop)
		require.Equal(t, accountSlotAcquireFailed, status)
		require.Equal(t, http.StatusTooManyRequests, e.recorder.Code)
		require.Contains(t, e.recorder.Body.String(), "Too many pending requests")
	})
}

// 末跳（构造器返回 nil）：与无链完全一致，先写响应再返回。
func TestAcquireSlotForHop_LastHopWritesResponseLikeNoChain(t *testing.T) {
	hop := newOpenAIHopSlotPolicy(service.HopInfo{HasChain: true, IsLast: true}, service.DefaultGroupFallbackSettings(), time.Now())
	require.Nil(t, hop)
	e := newHopSlotEnv(t, true, nil)
	_, status := e.acquire(hopSlotSelection(7001, time.Second, false), service.OpenAIAccountScheduleDecision{Layer: "load_balance"}, hop)
	require.Equal(t, accountSlotAcquireFailed, status)
	require.Equal(t, http.StatusTooManyRequests, e.recorder.Code)
	require.Contains(t, e.recorder.Body.String(), "Too many pending requests")
}

// 模拟调用方对状态的处理（与 accountSlotRetrySelection 分支一致）：
// 重选只把忙的号加入 failedAccountIDs，不清 sessionHash，不占用 switchCount。
type hopSlotDriveResult struct {
	status          accountSlotAcquireStatus
	failedAccountID map[int64]struct{}
	switchCount     int
	attempts        int
}

func (e *hopSlotEnv) drive(hop *openAIHopSlotPolicy, decision service.OpenAIAccountScheduleDecision, selections ...*service.AccountSelectionResult) hopSlotDriveResult {
	out := hopSlotDriveResult{failedAccountID: map[int64]struct{}{}}
	for _, selection := range selections {
		out.attempts++
		release, status := e.acquire(selection, decision, hop)
		out.status = status
		if status == accountSlotRetrySelection {
			out.failedAccountID[selection.Account.ID] = struct{}{}
			continue
		}
		if release != nil {
			release()
		}
		return out
	}
	return out
}

func TestAcquireSlotForHop_ReselectOutcomes(t *testing.T) {
	balance := service.OpenAIAccountScheduleDecision{Layer: "load_balance"}
	freeB := func(id int64) bool { return id == 7002 }

	t.Run("重选成功：忙号被排除，空闲号服务，不跨组，不占 switchCount", func(t *testing.T) {
		e := newHopSlotEnv(t, false, freeB)
		hop := hopSlotPolicy(60*time.Millisecond, 60*time.Millisecond)
		// 第一次选号得到忙号 A 的 WaitPlan；重选得到可直接抢到的 B。
		a := hopSlotSelection(7001, 30*time.Second, false)
		b := hopSlotSelection(7002, 30*time.Second, false)
		got := e.drive(hop, balance, a, b)
		require.Equal(t, accountSlotAcquired, got.status)
		require.Equal(t, map[int64]struct{}{7001: {}}, got.failedAccountID)
		require.Zero(t, got.switchCount)
		require.True(t, hop.ReselectUsed())
		require.Empty(t, e.recorder.Body.String())
	})

	t.Run("重选仍是 WaitPlan：按繁忙回退（BusyDeferred），不写响应", func(t *testing.T) {
		e := newHopSlotEnv(t, false, nil)
		hop := hopSlotPolicy(60*time.Millisecond, 60*time.Millisecond)
		a := hopSlotSelection(7001, 30*time.Second, false)
		b := hopSlotSelection(7002, 30*time.Second, false)
		got := e.drive(hop, balance, a, b)
		require.Equal(t, accountSlotBusyDeferred, got.status)
		require.Equal(t, 2, got.attempts)
		require.Equal(t, map[int64]struct{}{7001: {}}, got.failedAccountID, "每跳只重选一次")
		require.Empty(t, e.recorder.Body.String())
	})

	t.Run("GroupSaturated：不重选，直接 BusyDeferred", func(t *testing.T) {
		e := newHopSlotEnv(t, false, freeB)
		hop := hopSlotPolicy(60*time.Millisecond, 60*time.Millisecond)
		a := hopSlotSelection(7001, 30*time.Second, true)
		b := hopSlotSelection(7002, 30*time.Second, false)
		got := e.drive(hop, balance, a, b)
		require.Equal(t, accountSlotBusyDeferred, got.status)
		require.Equal(t, 1, got.attempts, "B 不应该被尝试")
		require.Empty(t, got.failedAccountID)
	})

	t.Run("命中 previous_response_id：不重选也不回退，保持原错误", func(t *testing.T) {
		e := newHopSlotEnv(t, false, freeB)
		hop := hopSlotPolicy(60*time.Millisecond, 60*time.Millisecond)
		a := hopSlotSelection(7001, 80*time.Millisecond, false)
		b := hopSlotSelection(7002, 30*time.Second, false)
		prev := service.OpenAIAccountScheduleDecision{Layer: "previous_response_id", StickyPreviousHit: true}
		got := e.drive(hop, prev, a, b)
		require.Equal(t, accountSlotAcquireFailed, got.status)
		require.Equal(t, 1, got.attempts)
		require.Equal(t, http.StatusTooManyRequests, e.recorder.Code)
		require.False(t, hop.ReselectUsed())
	})

	t.Run("请求带 previous_response_id 但选号没命中该层：按普通规则重选", func(t *testing.T) {
		e := newHopSlotEnv(t, false, freeB)
		hop := hopSlotPolicy(60*time.Millisecond, 60*time.Millisecond)
		a := hopSlotSelection(7001, 30*time.Second, false)
		b := hopSlotSelection(7002, 30*time.Second, false)
		got := e.drive(hop, service.OpenAIAccountScheduleDecision{Layer: "load_balance"}, a, b)
		require.Equal(t, accountSlotAcquired, got.status)
	})
}

// 回退链走完仍无人服务时，原始错误由 writeDeferredAccountSlotFailure 写出，
// 内容必须与无链请求在同一情形下写出的完全相同。
func TestWriteDeferredAccountSlotFailure_MatchesLegacyResponse(t *testing.T) {
	balance := service.OpenAIAccountScheduleDecision{Layer: "load_balance"}

	t.Run("等待超时", func(t *testing.T) {
		legacy := newHopSlotEnv(t, false, nil)
		_, _ = legacy.acquire(hopSlotSelection(7001, 60*time.Millisecond, true), balance, nil)

		deferred := newHopSlotEnv(t, false, nil)
		hop := hopSlotPolicy(60*time.Millisecond, 60*time.Millisecond)
		_, status := deferred.acquire(hopSlotSelection(7001, 30*time.Second, true), balance, hop)
		require.Equal(t, accountSlotBusyDeferred, status)
		require.Empty(t, deferred.recorder.Body.String())
		deferred.h.writeDeferredAccountSlotFailure(deferred.c, hop, false)

		require.Equal(t, legacy.recorder.Code, deferred.recorder.Code)
		require.Equal(t, legacy.recorder.Body.String(), deferred.recorder.Body.String())
	})

	t.Run("排队已满", func(t *testing.T) {
		legacy := newHopSlotEnv(t, true, nil)
		_, _ = legacy.acquire(hopSlotSelection(7001, time.Second, true), balance, nil)

		deferred := newHopSlotEnv(t, true, nil)
		hop := hopSlotPolicy(time.Second, time.Second)
		_, status := deferred.acquire(hopSlotSelection(7001, time.Second, true), balance, hop)
		require.Equal(t, accountSlotBusyDeferred, status)
		require.Empty(t, deferred.recorder.Body.String())
		deferred.h.writeDeferredAccountSlotFailure(deferred.c, hop, false)

		require.Equal(t, legacy.recorder.Code, deferred.recorder.Code)
		require.Equal(t, legacy.recorder.Body.String(), deferred.recorder.Body.String())
	})

	t.Run("nil 策略是空操作", func(t *testing.T) {
		e := newHopSlotEnv(t, false, nil)
		e.h.writeDeferredAccountSlotFailure(e.c, nil, false)
		require.Empty(t, e.recorder.Body.String())
	})
}
