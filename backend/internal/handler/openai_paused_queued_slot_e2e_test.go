//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type pausedQueuedSlotAccountRepo struct {
	service.AccountRepository
	mu               sync.Mutex
	accounts         []service.Account
	secondAfterPause bool
	paused           bool
}

func (r *pausedQueuedSlotAccountRepo) pauseFirst() {
	r.mu.Lock()
	r.accounts[0].Schedulable = false
	r.paused = true
	r.mu.Unlock()
}

func (r *pausedQueuedSlotAccountRepo) isPaused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused
}

func (r *pausedQueuedSlotAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, account := range r.accounts {
		if account.ID == id {
			copy := account
			return &copy, nil
		}
	}
	return nil, service.ErrAccountNotFound
}

func (r *pausedQueuedSlotAccountRepo) list(platform string) ([]service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []service.Account
	for i, account := range r.accounts {
		if account.Platform == platform && (i == 0 || !r.secondAfterPause || r.paused) {
			result = append(result, account)
		}
	}
	return result, nil
}

func (r *pausedQueuedSlotAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.list(platform)
}

func (r *pausedQueuedSlotAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, _ int64, platform string) ([]service.Account, error) {
	return r.list(platform)
}

func (r *pausedQueuedSlotAccountRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.list(platform)
}

type pausedQueuedSlotCache struct {
	*concurrencyCacheMock
	repo          *pausedQueuedSlotAccountRepo
	firstAttempts atomic.Int32
	waitCount     atomic.Int32
}

func (c *pausedQueuedSlotCache) AcquireAccountSlot(_ context.Context, accountID int64, _ int, _ string) (bool, error) {
	if accountID == 1396 {
		c.firstAttempts.Add(1)
		return c.repo.isPaused(), nil
	}
	return true, nil
}

func (c *pausedQueuedSlotCache) IncrementAccountWaitCount(_ context.Context, _ int64, _ int) (bool, error) {
	c.waitCount.Add(1)
	c.repo.pauseFirst()
	return true, nil
}

func newPausedQueuedSlotHandler(t *testing.T, repo *pausedQueuedSlotAccountRepo, cache *pausedQueuedSlotCache, upstream service.HTTPUpstream) *OpenAIGatewayHandler {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	concurrency := service.NewConcurrencyService(cache)
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	t.Cleanup(billingCache.Stop)
	gateway := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cfg, nil, concurrency,
		nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := NewOpenAIGatewayHandler(gateway, concurrency, billingCache,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil, cfg)
	h.concurrencyHelper = NewConcurrencyHelper(concurrency, SSEPingFormatNone, time.Second)
	return h
}

func TestOpenAIResponses_PauseWhileQueuedReturnsNoAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &pausedQueuedSlotAccountRepo{accounts: []service.Account{{
		ID: 1396, Name: "queued-first", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Status: service.StatusActive,
		Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-first"},
	}}}
	cache := &pausedQueuedSlotCache{concurrencyCacheMock: &concurrencyCacheMock{}, repo: repo}
	var forwards atomic.Int32
	upstream := openAIHandlerHTTPUpstreamStub{do: func(*http.Request, string, int64, int) (*http.Response, error) {
		forwards.Add(1)
		return nil, nil
	}}
	h := newPausedQueuedSlotHandler(t, repo, cache, upstream)
	c, recorder := newOpenAIResponsesFailoverTestContext(t, context.Background())

	h.Responses(c)

	require.Equal(t, int32(1), cache.waitCount.Load())
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.releaseAccountCalled))
	require.Zero(t, forwards.Load())
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestOpenAIChatCompletions_PauseWhileQueuedForwardsOnlyToBackup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &pausedQueuedSlotAccountRepo{
		secondAfterPause: true,
		accounts: []service.Account{
			{ID: 1396, Name: "queued-first", Platform: service.PlatformOpenAI,
				Type: service.AccountTypeAPIKey, Status: service.StatusActive,
				Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"api_key": "sk-first"}},
			{ID: 1397, Name: "backup", Platform: service.PlatformOpenAI,
				Type: service.AccountTypeAPIKey, Status: service.StatusActive,
				Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"api_key": "sk-backup"}},
		},
	}
	cache := &pausedQueuedSlotCache{concurrencyCacheMock: &concurrencyCacheMock{}, repo: repo}
	var mu sync.Mutex
	var forwarded []int64
	upstream := openAIHandlerHTTPUpstreamStub{do: func(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
		mu.Lock()
		forwarded = append(forwarded, accountID)
		mu.Unlock()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"id":"chatcmpl-backup","object":"chat.completion","model":"gpt-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)),
		}, nil
	}}
	h := newPausedQueuedSlotHandler(t, repo, cache, upstream)
	c, recorder := newOpenAIFailoverTestContext(t, context.Background(), "/v1/chat/completions", `{"model":"gpt-5.1","stream":false,"messages":[{"role":"user","content":"hello"}]}`, false)

	h.ChatCompletions(c)

	require.Equal(t, int32(1), cache.waitCount.Load())
	mu.Lock()
	got := append([]int64(nil), forwarded...)
	mu.Unlock()
	require.Equal(t, []int64{1397}, got)
	require.Equal(t, int32(2), atomic.LoadInt32(&cache.releaseAccountCalled))
	require.Equal(t, http.StatusOK, recorder.Code)
}
