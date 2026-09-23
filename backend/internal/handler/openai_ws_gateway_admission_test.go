package handler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// dialAndSendFirstResponseCreate 建立 WS 连接并发送首个 response.create，返回客户端连接。
func dialAndSendFirstResponseCreate(t *testing.T, url string) *coderws.Conn {
	t.Helper()
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	client, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(url, "http")+"/openai/v1/responses", nil)
	cancelDial()
	require.NoError(t, err)

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = client.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","stream":false}`))
	cancelWrite()
	require.NoError(t, err)
	return client
}

func readClientCloseError(t *testing.T, client *coderws.Conn) coderws.CloseError {
	t.Helper()
	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err := client.Read(readCtx)
	cancelRead()
	require.Error(t, err)
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	return closeErr
}

// 端到端：后续 turn 的 BeforeTurn 因用户/账号并发满被拒（1013）时，客户端收到 1013，
// 但不得把该账号记为调度失败。
func TestOpenAIResponsesWebSocket_BeforeTurnAdmissionRejectionIsNotAccountFailure(t *testing.T) {
	tests := []struct {
		name       string
		rejectUser bool
		wantReason string
		wantStatus coderws.StatusCode
	}{
		{name: "user_slot_full", rejectUser: true, wantReason: "too many concurrent requests, please retry later", wantStatus: coderws.StatusTryAgainLater},
		{name: "account_busy", rejectUser: false, wantReason: "account is busy, please retry later", wantStatus: coderws.StatusTryAgainLater},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rejecting atomic.Bool
			cache := &concurrencyCacheMock{
				acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
					return !(tt.rejectUser && rejecting.Load()), nil
				},
				acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) {
					return tt.rejectUser || !rejecting.Load(), nil
				},
			}
			reports := make(chan bool, 4)
			proxy := func(_ context.Context, _ *gin.Context, _ *coderws.Conn, _ *service.Account, _ string, _ []byte, hooks *service.OpenAIWSIngressHooks) error {
				if hooks == nil || hooks.BeforeTurn == nil || hooks.AfterTurn == nil {
					return errors.New("hooks not wired")
				}
				if err := hooks.BeforeTurn(1); err != nil {
					return err
				}
				// 首轮完成后释放槽位，随后客户端发起第 2 轮时并发已满。
				hooks.AfterTurn(1, nil, nil)
				rejecting.Store(true)
				return hooks.BeforeTurn(2)
			}
			h := newOpenAIResponsesWebSocketAttributionHandlerWithProxy(t, cache, proxy, reports)
			server := newOpenAIResponsesWebSocketAttributionServer(t, h)
			defer server.Close()

			client := dialAndSendFirstResponseCreate(t, server.URL)
			defer func() { _ = client.CloseNow() }()

			closeErr := readClientCloseError(t, client)
			require.Equal(t, tt.wantStatus, closeErr.Code)
			require.Equal(t, tt.wantReason, closeErr.Reason)

			select {
			case success := <-reports:
				t.Fatalf("gateway admission rejection must not report account schedule result, got success=%v", success)
			default:
			}
		})
	}
}

// relay 首次退出后客户端 goroutine 不被 join：handler 返回后迟到的 BeforeTurn 不能再把
// 新抢到的槽位挂到已结束的连接上，必须立即归还并拒绝本 turn。
func TestOpenAIResponsesWebSocket_LateBeforeTurnAfterHandlerExitReleasesSlotsImmediately(t *testing.T) {
	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
			return true, nil
		},
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) {
			return true, nil
		},
	}
	hooksCh := make(chan *service.OpenAIWSIngressHooks, 1)
	proxy := func(_ context.Context, _ *gin.Context, _ *coderws.Conn, _ *service.Account, _ string, _ []byte, hooks *service.OpenAIWSIngressHooks) error {
		hooksCh <- hooks
		return service.NewOpenAIWSClientCloseError(coderws.StatusNormalClosure, "client done", nil)
	}
	reports := make(chan bool, 4)
	h := newOpenAIResponsesWebSocketAttributionHandlerWithProxy(t, cache, proxy, reports)

	handlerDone := make(chan struct{})
	server := newOpenAIResponsesWebSocketAttributionServerWithDone(t, h, handlerDone)
	defer server.Close()

	client := dialAndSendFirstResponseCreate(t, server.URL)
	defer func() { _ = client.CloseNow() }()
	_ = readClientCloseError(t, client)

	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not return")
	}
	hooks := <-hooksCh
	require.NotNil(t, hooks)

	userBefore := atomic.LoadInt32(&cache.releaseUserCalled)
	accountBefore := atomic.LoadInt32(&cache.releaseAccountCalled)

	err := hooks.BeforeTurn(2)
	require.Error(t, err, "late BeforeTurn after handler exit must be rejected")
	require.True(t, errors.Is(err, errOpenAIWSGatewayAdmissionRejected))
	var closeErr *service.OpenAIWSClientCloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusGoingAway, closeErr.StatusCode())

	// 新抢到的槽位已同步归还（不依赖请求 ctx 取消后的异步兜底）。
	require.Equal(t, accountBefore+1, atomic.LoadInt32(&cache.releaseAccountCalled))
	require.Equal(t, userBefore+1, atomic.LoadInt32(&cache.releaseUserCalled))

	// 之后的退出路径 AfterTurn 不会重复释放。
	hooks.AfterTurn(3, nil, errors.New("relay exit"))
	require.Equal(t, accountBefore+1, atomic.LoadInt32(&cache.releaseAccountCalled))
	require.Equal(t, userBefore+1, atomic.LoadInt32(&cache.releaseUserCalled))
}

// 退出路径：主 goroutine 的最终 AfterTurn 与仍在运行的客户端 goroutine 的 BeforeTurn 并发。
// 在 -race 下，未加锁的 currentUserRelease/currentAccountRelease/cyberBlockedThisConn 会被检测到。
func TestOpenAIResponsesWebSocket_ConcurrentBeforeTurnAndAfterTurnIsRaceFree(t *testing.T) {
	var userAcquired, accountAcquired atomic.Int32
	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
			userAcquired.Add(1)
			return true, nil
		},
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) {
			accountAcquired.Add(1)
			return true, nil
		},
	}
	proxy := func(_ context.Context, _ *gin.Context, _ *coderws.Conn, _ *service.Account, _ string, _ []byte, hooks *service.OpenAIWSIngressHooks) error {
		for i := 0; i < 20; i++ {
			var wg sync.WaitGroup
			wg.Add(1)
			go func(turn int) {
				defer wg.Done()
				_ = hooks.BeforeTurn(turn)
			}(i + 2)
			hooks.AfterTurn(i+2, nil, errors.New("upstream closed"))
			wg.Wait()
		}
		hooks.AfterTurn(99, nil, errors.New("final"))
		return service.NewOpenAIWSClientCloseError(coderws.StatusNormalClosure, "client done", nil)
	}
	reports := make(chan bool, 4)
	h := newOpenAIResponsesWebSocketAttributionHandlerWithProxy(t, cache, proxy, reports)
	handlerDone := make(chan struct{})
	server := newOpenAIResponsesWebSocketAttributionServerWithDone(t, h, handlerDone)
	defer server.Close()

	client := dialAndSendFirstResponseCreate(t, server.URL)
	defer func() { _ = client.CloseNow() }()
	closeErr := readClientCloseError(t, client)
	require.Equal(t, coderws.StatusNormalClosure, closeErr.Code)
	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not return")
	}

	// 每个抢到的槽位最终都恰好释放一次：不重复释放，也不泄漏。
	require.GreaterOrEqual(t, accountAcquired.Load(), int32(20))
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&cache.releaseAccountCalled) == accountAcquired.Load() &&
			atomic.LoadInt32(&cache.releaseUserCalled) == userAcquired.Load()
	}, 3*time.Second, 10*time.Millisecond)
}
