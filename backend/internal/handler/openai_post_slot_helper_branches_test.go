//go:build unit

package handler

import (
	"context"
	"errors"
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

type postSlotFailingAccountRepo struct{ service.AccountRepository }

func (postSlotFailingAccountRepo) GetByID(context.Context, int64) (*service.Account, error) {
	return nil, errors.New("account database unavailable")
}

type postSlotQueueFullCache struct{ *concurrencyCacheMock }

func (postSlotQueueFullCache) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	return false, nil
}

func newPostSlotBranchHandler(repo service.AccountRepository, cache service.ConcurrencyCache) *OpenAIGatewayHandler {
	svc := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, &config.Config{}, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	return &OpenAIGatewayHandler{
		gatewayService:    svc,
		concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
	}
}

func postSlotBranchContext() (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, recorder
}

func postSlotBranchSelection(account *service.Account) *service.AccountSelectionResult {
	return &service.AccountSelectionResult{
		Account: account,
		WaitPlan: &service.AccountWaitPlan{
			AccountID:      account.ID,
			MaxConcurrency: 1,
			Timeout:        100 * time.Millisecond,
			MaxWaiting:     2,
		},
	}
}

func postSlotAcquireAfterFirst(secondAcquired bool, secondErr error) func(context.Context, int64, int, string) (bool, error) {
	calls := 0
	return func(context.Context, int64, int, string) (bool, error) {
		calls++
		if calls == 1 {
			return false, nil
		}
		return secondAcquired, secondErr
	}
}

func TestOpenAIAcquireAccountSlot_PostSlotGuardBranches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	selected := service.Account{
		ID: 1398, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
	}
	activeRepo := &openAIWSUsageHandlerAccountRepoStub{account: selected}
	paused := selected
	paused.Schedulable = false
	pausedRepo := &openAIWSUsageHandlerAccountRepoStub{account: paused}
	for _, tc := range []struct {
		name        string
		repo        service.AccountRepository
		cache       service.ConcurrencyCache
		selection   *service.AccountSelectionResult
		wantStatus  accountSlotAcquireStatus
		wantRelease int32
		wantHTTP    int
	}{
		{name: "missing selection", repo: activeRepo, cache: &concurrencyCacheMock{}, wantStatus: accountSlotAcquireFailed, wantHTTP: http.StatusServiceUnavailable},
		{name: "missing wait plan", repo: activeRepo, cache: &concurrencyCacheMock{}, selection: &service.AccountSelectionResult{Account: &selected}, wantStatus: accountSlotAcquireFailed, wantHTTP: http.StatusServiceUnavailable},
		{name: "quick slot error", repo: activeRepo, cache: &concurrencyCacheMock{acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return false, errors.New("redis unavailable") }}, selection: postSlotBranchSelection(&selected), wantStatus: accountSlotAcquireFailed},
		{name: "quick slot paused", repo: pausedRepo, cache: &concurrencyCacheMock{acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }}, selection: postSlotBranchSelection(&selected), wantStatus: accountSlotRetrySelection, wantRelease: 1},
		{name: "quick slot healthy", repo: activeRepo, cache: &concurrencyCacheMock{acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }}, selection: postSlotBranchSelection(&selected), wantStatus: accountSlotAcquired, wantRelease: 1},
		{name: "queue full", repo: activeRepo, cache: postSlotQueueFullCache{&concurrencyCacheMock{}}, selection: postSlotBranchSelection(&selected), wantStatus: accountSlotAcquireFailed, wantHTTP: http.StatusTooManyRequests},
		{name: "wait slot healthy", repo: activeRepo, cache: &concurrencyCacheMock{acquireAccountSlotFn: postSlotAcquireAfterFirst(true, nil)}, selection: postSlotBranchSelection(&selected), wantStatus: accountSlotAcquired, wantRelease: 1},
		{name: "wait acquire error", repo: activeRepo, cache: &concurrencyCacheMock{acquireAccountSlotFn: postSlotAcquireAfterFirst(false, errors.New("redis unavailable"))}, selection: postSlotBranchSelection(&selected), wantStatus: accountSlotAcquireFailed},
		{name: "database error after slot", repo: postSlotFailingAccountRepo{}, cache: &concurrencyCacheMock{acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }}, selection: postSlotBranchSelection(&selected), wantStatus: accountSlotAcquireFailed, wantRelease: 1, wantHTTP: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPostSlotBranchHandler(tc.repo, tc.cache)
			c, recorder := postSlotBranchContext()
			streamStarted := false
			release, status := h.acquireResponsesAccountSlot(c, nil, "", tc.selection, false, &streamStarted, zap.NewNop())
			require.Equal(t, tc.wantStatus, status)
			if release != nil {
				release()
			}
			if tc.wantHTTP > 0 {
				require.Equal(t, tc.wantHTTP, recorder.Code)
			}
			if cache, ok := tc.cache.(*concurrencyCacheMock); ok {
				require.Equal(t, tc.wantRelease, atomic.LoadInt32(&cache.releaseAccountCalled))
			}
		})
	}
}
