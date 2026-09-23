package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// openAIWSScriptedCancelCtx 的 Done 永不关闭，Err 在前 okCalls 次调用返回 nil、之后返回
// context.Canceled，用来确定性地构造「拿到令牌之后才发现已取消」的时序。
type openAIWSScriptedCancelCtx struct {
	context.Context
	okCalls int32
	calls   atomic.Int32
}

func newOpenAIWSScriptedCancelCtx(okCalls int32) *openAIWSScriptedCancelCtx {
	return &openAIWSScriptedCancelCtx{Context: context.Background(), okCalls: okCalls}
}

func (c *openAIWSScriptedCancelCtx) Done() <-chan struct{} { return nil }

func (c *openAIWSScriptedCancelCtx) Err() error {
	if c.calls.Add(1) <= c.okCalls {
		return nil
	}
	return context.Canceled
}

// 排队者被账号池变更唤醒、重选时拿到了另一条连接的令牌，但请求在此之后才被取消：
// Acquire 必须归还令牌并按取消返回，不能带着租约出去。
func TestOpenAIWSConnPool_AcquireRewokenThenCanceledReturnsToken(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 4

	accountID := int64(996)
	account := &Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	req := openAIWSAcquireRequest{Account: account, WSURL: "wss://example.com/v1/responses"}
	pool := newOpenAIWSConnPool(cfg)
	target := newOpenAIWSConn("target", accountID, &openAIWSFakeConn{}, nil)
	other := newOpenAIWSConn("other", accountID, &openAIWSFakeConn{}, nil)
	require.True(t, target.tryAcquire())
	require.True(t, other.tryAcquire())
	other.waiters.Add(1)

	ap := pool.ensureAccountPoolLocked(accountID)
	ap.mu.Lock()
	ap.conns[target.id] = target
	ap.conns[other.id] = other
	ap.lastAcquire = &req
	ap.mu.Unlock()

	// 第 1 次 Err（唤醒后的检查）放行进入重选，第 2 次（Acquire 出口复查）报取消。
	ctx := newOpenAIWSScriptedCancelCtx(1)
	type result struct {
		lease *openAIWSConnLease
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		lease, err := pool.Acquire(ctx, req)
		resultCh <- result{lease: lease, err: err}
	}()
	require.Eventually(t, func() bool { return target.waiters.Load() == 1 }, time.Second, 5*time.Millisecond)

	ap.mu.Lock()
	other.release()
	ap.signalChangedLocked()
	ap.mu.Unlock()

	select {
	case got := <-resultCh:
		require.ErrorIs(t, got.err, context.Canceled)
		require.Nil(t, got.lease)
	case <-time.After(time.Second):
		t.Fatal("rewoken waiter canceled after re-acquiring must return promptly")
	}
	require.GreaterOrEqual(t, ctx.calls.Load(), int32(2))
	require.True(t, other.tryAcquire(), "the token taken during re-selection must be returned")
	other.release()
	require.Equal(t, int32(0), target.waiters.Load())
}

// acquireOrPoolChanged 拿到令牌后发现上下文已取消：归还令牌并返回取消错误。
func TestOpenAIWSConn_AcquireOrPoolChangedCanceledAfterLeaseReleases(t *testing.T) {
	conn := newOpenAIWSConn("c1", 1, &openAIWSFakeConn{}, nil)
	ctx := newOpenAIWSScriptedCancelCtx(0)

	err := conn.acquireOrPoolChanged(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, conn.tryAcquire(), "token must be released back after a canceled acquire")
	conn.release()
}

func TestOpenAIWSConn_AcquireOrPoolChangedEdgeCases(t *testing.T) {
	var nilConn *openAIWSConn
	require.ErrorIs(t, nilConn.acquireOrPoolChanged(context.Background(), nil), errOpenAIWSConnClosed)

	conn := newOpenAIWSConn("c2", 1, &openAIWSFakeConn{}, nil)
	require.True(t, conn.tryAcquire())
	changed := make(chan struct{})
	close(changed)
	require.ErrorIs(t, conn.acquireOrPoolChanged(context.Background(), changed), errOpenAIWSPoolChanged)
	conn.release()
}

func TestOpenAIWSAccountPool_ChangeChannelHelpers(t *testing.T) {
	var nilPool *openAIWSAccountPool
	require.NotPanics(t, func() { nilPool.signalChangedLocked() })

	ap := &openAIWSAccountPool{}
	ch := ap.changeChannelLocked()
	require.NotNil(t, ch, "change channel must be lazily created")
	require.Equal(t, ch, ap.changeChannelLocked(), "lazily created channel must be reused until signaled")

	ap.signalChangedLocked()
	select {
	case <-ch:
	default:
		t.Fatal("signalChangedLocked must close the previous channel")
	}
	next := ap.changeChannelLocked()
	require.NotEqual(t, ch, next)

	fresh := &openAIWSAccountPool{}
	fresh.signalChangedLocked()
	require.NotNil(t, fresh.changedCh, "signaling without a prior channel must still install a new one")
}

func TestOpenAIWSConnPool_NotifyAccountPoolChangedWithoutPool(t *testing.T) {
	pool := newOpenAIWSConnPool(&config.Config{})
	require.NotPanics(t, func() { pool.notifyAccountPoolChanged(424242) })
	_, ok := pool.getAccountPool(424242)
	require.False(t, ok, "notifying an unknown account must not create a pool")
}

func TestOpenAIWSConnPool_UnpinConnSignalsPoolChange(t *testing.T) {
	pool := newOpenAIWSConnPool(&config.Config{})
	accountID := int64(997)
	conn := newOpenAIWSConn("pinned", accountID, &openAIWSFakeConn{}, nil)
	ap := pool.ensureAccountPoolLocked(accountID)
	ap.mu.Lock()
	ap.conns[conn.id] = conn
	ap.mu.Unlock()

	require.True(t, pool.PinConn(accountID, conn.id))
	require.True(t, pool.PinConn(accountID, conn.id))

	for i := 0; i < 2; i++ {
		ap.mu.Lock()
		ch := ap.changeChannelLocked()
		ap.mu.Unlock()
		pool.UnpinConn(accountID, conn.id)
		select {
		case <-ch:
		default:
			t.Fatalf("UnpinConn #%d must broadcast a pool change", i+1)
		}
	}
	ap.mu.Lock()
	require.Empty(t, ap.pinnedConns)
	ap.mu.Unlock()
}

// acquire 在 queueWait 为 nil 时自行补一个累计器，不得 panic。
func TestOpenAIWSConnPool_AcquireInternalNilQueueWait(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSCountingDialer{})
	account := &Account{ID: 998, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	lease, err := pool.acquire(context.Background(), openAIWSAcquireRequest{Account: account, WSURL: "wss://example.com/v1/responses"}, 0, nil)
	require.NoError(t, err)
	require.NotNil(t, lease)
	lease.Release()
}
