//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// scriptedLockFreeLister 按需返回名单 / 错误 / 阻塞，并记录调用与并发度。
type scriptedLockFreeLister struct {
	mu     sync.Mutex
	rows   []BillingLockFreeAttributeValue
	err    error
	block  chan struct{} // 非 nil 时，调用阻塞到它被关闭或 ctx 结束
	calls  atomic.Int64
	active atomic.Int64
	peak   atomic.Int64
}

func (l *scriptedLockFreeLister) set(rows []BillingLockFreeAttributeValue, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rows, l.err = rows, err
}

func (l *scriptedLockFreeLister) ListBillingLockFreeAttributeValues(ctx context.Context) ([]BillingLockFreeAttributeValue, error) {
	l.calls.Add(1)
	n := l.active.Add(1)
	defer l.active.Add(-1)
	for {
		p := l.peak.Load()
		if n <= p || l.peak.CompareAndSwap(p, n) {
			break
		}
	}
	l.mu.Lock()
	rows, err, block := l.rows, l.err, l.block
	l.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return append([]BillingLockFreeAttributeValue(nil), rows...), err
}

func trueRows(ids ...int64) []BillingLockFreeAttributeValue {
	rows := make([]BillingLockFreeAttributeValue, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, BillingLockFreeAttributeValue{UserID: id, Value: "true"})
	}
	return rows
}

// manualClock 是可手动拨动的单调时钟。
type manualClock struct{ ns atomic.Int64 }

func (c *manualClock) now() time.Duration      { return time.Duration(c.ns.Load()) }
func (c *manualClock) advance(d time.Duration) { c.ns.Add(int64(d)) }

func newScriptedResolver(t *testing.T, lister *scriptedLockFreeLister) (*BillingLockFreeUsers, *manualClock) {
	t.Helper()
	r := NewBillingLockFreeUsers(lister)
	clock := &manualClock{}
	r.elapsed = clock.now
	t.Cleanup(r.Close)
	return r, clock
}

// lockFreeTestResolver 给其它测试用：名单固定为 ids，已同步加载完成。
func lockFreeTestResolver(t testing.TB, ids ...int64) *BillingLockFreeUsers {
	t.Helper()
	l := &scriptedLockFreeLister{rows: trueRows(ids...)}
	r := NewBillingLockFreeUsers(l)
	t.Cleanup(r.Close)
	require.NoError(t, r.Refresh(context.Background()))
	return r
}

func TestBillingLockFreeValueMatching(t *testing.T) {
	for value, want := range map[string]bool{
		"true": true, "TRUE": true, "True": true, " true": true, "true ": true, "\t true\n": true, "　true": true,
		"": false, " ": false, "false": false, "1": false, "on": false, "yes": false, "开启": false,
		"truee": false, "ttrue": false, "tr ue": false, `"true"`: false, "true;": false, "t": false,
	} {
		require.Equal(t, want, isBillingLockFreeValue(value), "value=%q", value)
	}
}

func TestBillingLockFreeUsersNilAndMissingDependencyAreFailClosed(t *testing.T) {
	var nilResolver *BillingLockFreeUsers
	require.False(t, nilResolver.IsBillingLockFreeUser(1))
	require.NotPanics(t, func() { nilResolver.Prime(); nilResolver.Close() })
	require.NoError(t, nilResolver.Refresh(context.Background()))

	noLister := NewBillingLockFreeUsers(nil)
	defer noLister.Close()
	require.False(t, noLister.IsBillingLockFreeUser(1))
	noLister.Prime()

	require.False(t, (&GatewayService{}).billingLockFree.IsBillingLockFreeUser(1))
	require.False(t, (&OpenAIGatewayService{}).billingLockFree.IsBillingLockFreeUser(1))
}

func TestBillingLockFreeUsersEmptyBeforeFirstSuccessfulLoad(t *testing.T) {
	lister := &scriptedLockFreeLister{}
	lister.set(nil, errors.New("db down"))
	r, _ := newScriptedResolver(t, lister)
	require.False(t, r.IsBillingLockFreeUser(1), "从未成功加载过：所有人无特权")
	r.wg.Wait()
	require.EqualValues(t, 1, lister.calls.Load())
	require.False(t, r.IsBillingLockFreeUser(1))
	require.Error(t, r.Refresh(context.Background()))
	require.False(t, r.IsBillingLockFreeUser(1), "刷新一直失败：仍然为空")
}

func TestBillingLockFreeUsersRefreshFilteringAndReplacement(t *testing.T) {
	lister := &scriptedLockFreeLister{}
	lister.set([]BillingLockFreeAttributeValue{
		{1, "true"}, {2, " TRUE "}, {3, "1"}, {4, ""}, {5, "on"}, {6, "false"}, {0, "true"}, {-7, "true"},
	}, nil)
	r, _ := newScriptedResolver(t, lister)
	require.NoError(t, r.Refresh(context.Background()))
	for id, want := range map[int64]bool{1: true, 2: true, 3: false, 4: false, 5: false, 6: false, 0: false, -7: false, 99: false} {
		require.Equal(t, want, r.IsBillingLockFreeUser(id), "user %d", id)
	}
	// 新名单整体替换旧名单（被撤销的用户失去特权）。
	lister.set(trueRows(3), nil)
	require.NoError(t, r.Refresh(context.Background()))
	require.False(t, r.IsBillingLockFreeUser(1))
	require.True(t, r.IsBillingLockFreeUser(3))
}

func TestBillingLockFreeUsersFailedRefreshKeepsPreviousList(t *testing.T) {
	lister := &scriptedLockFreeLister{rows: trueRows(1, 2)}
	r, clock := newScriptedResolver(t, lister)
	require.NoError(t, r.Refresh(context.Background()))
	require.True(t, r.IsBillingLockFreeUser(1))

	lister.set(nil, errors.New("connection refused"))
	require.Error(t, r.Refresh(context.Background()))
	require.True(t, r.IsBillingLockFreeUser(1), "同步刷新失败：保留旧名单")
	require.True(t, r.IsBillingLockFreeUser(2))

	// 后台刷新失败同样保留旧名单。
	clock.advance(31 * time.Second)
	before := lister.calls.Load()
	require.True(t, r.IsBillingLockFreeUser(1))
	r.wg.Wait()
	require.EqualValues(t, before+1, lister.calls.Load())
	require.True(t, r.IsBillingLockFreeUser(1), "后台刷新失败：保留旧名单")

	// 失败也算一次刷新：30 秒内不会重试，不会因为 DB 故障而每个请求都去查库（也就不会刷屏）。
	for i := 0; i < 100; i++ {
		r.IsBillingLockFreeUser(1)
	}
	r.wg.Wait()
	require.EqualValues(t, before+1, lister.calls.Load())

	// 恢复后下一轮刷新生效。
	lister.set(trueRows(2), nil)
	clock.advance(31 * time.Second)
	r.IsBillingLockFreeUser(1)
	r.wg.Wait()
	require.False(t, r.IsBillingLockFreeUser(1))
	require.True(t, r.IsBillingLockFreeUser(2))
}

func TestBillingLockFreeUsersRefreshesOnlyAfterInterval(t *testing.T) {
	lister := &scriptedLockFreeLister{rows: trueRows(1)}
	r, clock := newScriptedResolver(t, lister)

	// 第一次读：从未刷新，触发一次后台刷新；本次请求直接用当前（空）名单。
	require.False(t, r.IsBillingLockFreeUser(1))
	r.wg.Wait()
	require.EqualValues(t, 1, lister.calls.Load())
	require.True(t, r.IsBillingLockFreeUser(1))

	// 30 秒内不再刷新。
	lister.set(trueRows(2), nil)
	clock.advance(29 * time.Second)
	for i := 0; i < 50; i++ {
		require.True(t, r.IsBillingLockFreeUser(1))
		require.False(t, r.IsBillingLockFreeUser(2))
	}
	r.wg.Wait()
	require.EqualValues(t, 1, lister.calls.Load(), "29 秒时不刷新")

	// 到 30 秒：触发刷新；触发的那一次读仍然是旧名单（不等刷新结果）。
	clock.advance(time.Second)
	require.True(t, r.IsBillingLockFreeUser(1))
	r.wg.Wait()
	require.EqualValues(t, 2, lister.calls.Load())
	require.False(t, r.IsBillingLockFreeUser(1))
	require.True(t, r.IsBillingLockFreeUser(2))

	// 刷新后重新计时。
	clock.advance(10 * time.Second)
	r.IsBillingLockFreeUser(2)
	r.wg.Wait()
	require.EqualValues(t, 2, lister.calls.Load())
}

func TestBillingLockFreeUsersSyncRefreshResetsTimer(t *testing.T) {
	lister := &scriptedLockFreeLister{rows: trueRows(1)}
	r, clock := newScriptedResolver(t, lister)
	require.NoError(t, r.Refresh(context.Background()))
	clock.advance(10 * time.Second)
	require.True(t, r.IsBillingLockFreeUser(1))
	r.wg.Wait()
	require.EqualValues(t, 1, lister.calls.Load(), "预加载之后 30 秒内读路径不再刷新")
}

// 刷新阻塞时，请求路径不等它，也不会再起第二个刷新。
func TestBillingLockFreeUsersReadPathNeverBlocksOnSlowRefresh(t *testing.T) {
	lister := &scriptedLockFreeLister{rows: trueRows(1), block: make(chan struct{})}
	r, clock := newScriptedResolver(t, lister)
	oldList := map[int64]struct{}{1: {}} // 先装载一个旧名单
	r.users.Store(&oldList)
	clock.advance(time.Minute)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			if !r.IsBillingLockFreeUser(1) {
				t.Error("旧名单在慢刷新期间必须继续可读")
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("读路径被慢刷新阻塞")
	}
	require.Eventually(t, func() bool { return lister.active.Load() == 1 }, 2*time.Second, time.Millisecond, "后台刷新应已开始并阻塞在数据源上")
	require.EqualValues(t, 1, lister.calls.Load(), "刷新还没结束时只有一个在跑")
	require.EqualValues(t, 1, lister.active.Load())
	close(lister.block)
	r.wg.Wait()
	require.EqualValues(t, 1, lister.calls.Load())
}

// 并发调用：同一时刻只有一个刷新在跑，且只跑一次。配合 -race。
func TestBillingLockFreeUsersConcurrentCallersTriggerSingleRefresh(t *testing.T) {
	lister := &scriptedLockFreeLister{rows: trueRows(1), block: make(chan struct{})}
	r, clock := newScriptedResolver(t, lister)
	clock.advance(time.Minute)

	const goroutines = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 200; j++ {
				r.IsBillingLockFreeUser(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.Eventually(t, func() bool { return lister.active.Load() == 1 }, 2*time.Second, time.Millisecond, "后台刷新应已开始并阻塞在数据源上")
	require.EqualValues(t, 1, lister.calls.Load(), "并发触发只产生一次刷新")
	require.EqualValues(t, 1, lister.peak.Load())
	close(lister.block)
	r.wg.Wait()
	require.EqualValues(t, 1, lister.peak.Load())
	require.True(t, r.IsBillingLockFreeUser(1))
}

// 刷新进行中，名单被整体替换：读路径拿到的永远是完整的旧名单或完整的新名单。
func TestBillingLockFreeUsersConcurrentReadsDuringRefreshAreConsistent(t *testing.T) {
	lister := &scriptedLockFreeLister{rows: trueRows(1, 2, 3)}
	r, clock := newScriptedResolver(t, lister)
	require.NoError(t, r.Refresh(context.Background()))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				a, b, c := r.IsBillingLockFreeUser(1), r.IsBillingLockFreeUser(2), r.IsBillingLockFreeUser(3)
				_ = a || b || c
			}
		}()
	}
	for i := 0; i < 50; i++ {
		if i%2 == 0 {
			lister.set(trueRows(1, 2, 3), nil)
		} else {
			lister.set(trueRows(4), nil)
		}
		before := lister.calls.Load()
		clock.advance(31 * time.Second)
		r.IsBillingLockFreeUser(1)
		// 读路径的 goroutine 仍在并发触发刷新，这里不能对 WaitGroup 调 Wait（会和并发的 Add 竞争），改为等刷新跑完。
		require.Eventually(t, func() bool { return lister.calls.Load() > before && !r.running.Load() }, 2*time.Second, time.Millisecond)
	}
	close(stop)
	wg.Wait()
}

func TestBillingLockFreeUsersRefreshHasTimeoutAndKeepsOldList(t *testing.T) {
	lister := &scriptedLockFreeLister{rows: trueRows(1)}
	r, _ := newScriptedResolver(t, lister)
	require.NoError(t, r.Refresh(context.Background()))

	lister.mu.Lock()
	lister.block = make(chan struct{}) // 永不关闭：只能靠超时返回
	lister.mu.Unlock()
	r.timeout = 50 * time.Millisecond
	start := time.Now()
	err := r.Refresh(context.Background())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 2*time.Second)
	require.True(t, r.IsBillingLockFreeUser(1), "超时后保留旧名单")
	require.Equal(t, 5*time.Second, NewBillingLockFreeUsers(nil).timeout, "默认超时 5 秒")
	require.Equal(t, 30*time.Second, NewBillingLockFreeUsers(nil).interval, "默认刷新间隔 30 秒")
}

func TestBillingLockFreeUsersCloseCancelsInflightRefreshAndStopsFurtherOnes(t *testing.T) {
	lister := &scriptedLockFreeLister{rows: trueRows(1), block: make(chan struct{})}
	r, clock := newScriptedResolver(t, lister)
	clock.advance(time.Minute)
	r.IsBillingLockFreeUser(1) // 触发一个会一直阻塞的后台刷新
	require.Eventually(t, func() bool { return lister.active.Load() == 1 }, 2*time.Second, time.Millisecond)

	closed := make(chan struct{})
	go func() { r.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 没有取消并等待进行中的刷新")
	}
	require.EqualValues(t, 0, lister.active.Load(), "后台 goroutine 已退出")

	before := lister.calls.Load()
	clock.advance(time.Minute)
	require.False(t, r.IsBillingLockFreeUser(1))
	r.wg.Wait()
	require.Equal(t, before, lister.calls.Load(), "Close 之后不再触发刷新")
	r.Close() // 可重复调用
}

func TestProvideBillingLockFreeUsersInjectsBothGatewaysAndPrimes(t *testing.T) {
	lister := &scriptedLockFreeLister{rows: trueRows(42)}
	gw, oai := &GatewayService{}, &OpenAIGatewayService{}
	r := ProvideBillingLockFreeUsers(lister, gw, oai)
	defer r.Close()
	require.Same(t, r, gw.billingLockFree)
	require.Same(t, r, oai.billingLockFree)
	r.wg.Wait()
	require.EqualValues(t, 1, lister.calls.Load(), "启动时预加载一次")
	require.True(t, r.IsBillingLockFreeUser(42))
	require.Same(t, r, gw.billingDeps().billingLockFree)
	require.Same(t, r, oai.billingDeps().billingLockFree)
}
