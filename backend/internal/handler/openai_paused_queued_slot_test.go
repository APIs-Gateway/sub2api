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
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The first acquisition probes a full account, the waiter's immediate retry
// still sees it full, and the administrator pauses it before the waiter gets a
// slot. The queued request must release that slot without binding a session or
// forwarding to the now-paused account.
func TestOpenAIAcquireAccountSlot_PauseDuringWaitRetriesSelection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	selected := service.Account{
		ID:          1396,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
	}
	repo := &openAIWSUsageHandlerAccountRepoStub{account: selected}
	var attempts int
	cache := &concurrencyCacheMock{
		acquireAccountSlotFn: func(_ context.Context, _ int64, _ int, _ string) (bool, error) {
			attempts++
			if attempts <= 2 {
				return false, nil
			}
			repo.account.Schedulable = false
			return true, nil
		},
	}
	svc := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, &config.Config{}, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := &OpenAIGatewayHandler{
		gatewayService:    svc,
		concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
	}
	c, recorder := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	streamStarted := false
	selection := &service.AccountSelectionResult{
		Account: &selected,
		WaitPlan: &service.AccountWaitPlan{
			AccountID:      selected.ID,
			MaxConcurrency: 1,
			Timeout:        time.Second,
			MaxWaiting:     2,
		},
	}

	release, status := h.acquireResponsesAccountSlot(c, nil, "", selection, false, &streamStarted, zap.NewNop())
	require.Nil(t, release)
	require.Equal(t, accountSlotRetrySelection, status)
	require.GreaterOrEqual(t, attempts, 3)
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.releaseAccountCalled))
	require.Empty(t, recorder.Body.String(), "the paused attempt must not send a response or reach forwarding")
}

func TestOpenAIAcquireAccountSlot_PauseAfterSchedulerAcquiredReleasesSlot(t *testing.T) {
	selected := service.Account{
		ID:          1397,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
	}
	repo := &openAIWSUsageHandlerAccountRepoStub{account: selected}
	repo.account.Schedulable = false
	svc := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, &config.Config{}, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := &OpenAIGatewayHandler{gatewayService: svc}
	c, recorder := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	streamStarted := false
	released := 0
	selection := &service.AccountSelectionResult{
		Account:     &selected,
		Acquired:    true,
		ReleaseFunc: func() { released++ },
	}

	release, status := h.acquireResponsesAccountSlot(c, nil, "", selection, false, &streamStarted, zap.NewNop())
	require.Nil(t, release)
	require.Equal(t, accountSlotRetrySelection, status)
	require.Equal(t, 1, released)
	require.Empty(t, recorder.Body.String())
}

func TestOpenAIResponsesWebSocket_PauseAfterHandshakeSlotRejectsWithoutForward(t *testing.T) {
	var repo *openAIWSUsageHandlerAccountRepoStub
	var proxied atomic.Bool
	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) {
			repo.account.Schedulable = false
			return true, nil
		},
	}
	reports := make(chan bool, 1)
	h, selectedRepo := newOpenAIResponsesWebSocketAttributionHandlerWithRepo(t, cache,
		func(context.Context, *gin.Context, *coderws.Conn, *service.Account, string, []byte, *service.OpenAIWSIngressHooks) error {
			proxied.Store(true)
			return nil
		}, reports)
	repo = selectedRepo
	server := newOpenAIResponsesWebSocketAttributionServer(t, h)
	defer server.Close()

	client := dialAndSendFirstResponseCreate(t, server.URL)
	defer func() { _ = client.CloseNow() }()
	closeErr := readClientCloseError(t, client)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.Code)
	require.False(t, proxied.Load())
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.releaseAccountCalled))
	select {
	case success := <-reports:
		t.Fatalf("paused account must not report an upstream result: success=%v", success)
	default:
	}
}
