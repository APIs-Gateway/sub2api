//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type forceCacheBillingGatewayCache struct {
	accountID int64
}

type forceCacheBillingAccountRepo struct {
	service.AccountRepository
}

func (f *forceCacheBillingAccountRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}

func (f *forceCacheBillingGatewayCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return f.accountID, nil
}
func (f *forceCacheBillingGatewayCache) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}
func (f *forceCacheBillingGatewayCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}
func (f *forceCacheBillingGatewayCache) DeleteSessionAccountID(context.Context, int64, string) error {
	return nil
}

type forceCacheBillingUpstream struct {
	mu          sync.Mutex
	forced      []bool
	callNum     int
	firstStatus int
	firstBody   string
	repeatFirst int
	successBody string
}

func (f *forceCacheBillingUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return f.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (f *forceCacheBillingUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	forced := service.IsForceCacheBilling(req.Context())
	f.forced = append(f.forced, forced)
	call := f.callNum
	f.callNum++

	repeatFirst := f.repeatFirst
	if repeatFirst == 0 {
		repeatFirst = 1
	}
	if call < repeatFirst {
		status := f.firstStatus
		if status == 0 {
			status = http.StatusInternalServerError
		}
		body := f.firstBody
		if body == "" {
			body = `{"error":{"type":"invalid_request_error","message":"rotate account"}}`
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}

	body := f.successBody
	if body == "" {
		body = `{"id":"msg_ok","type":"message","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"output_tokens":3}}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func newForceCacheBillingGatewayHandler(t *testing.T, group *service.Group, accounts []*service.Account, upstream service.HTTPUpstream, cache service.GatewayCache) (*GatewayHandler, func()) {
	t.Helper()

	schedulerCache := &fakeSchedulerCache{accounts: accounts}
	schedulerSnapshot := service.NewSchedulerSnapshotService(schedulerCache, nil, nil, nil, nil)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	accountRepo := &forceCacheBillingAccountRepo{}

	gwSvc := service.NewGatewayService(
		accountRepo,
		&fakeGroupRepo{group: group},
		nil,
		nil,
		nil,
		nil,
		nil,
		cache,
		cfg,
		schedulerSnapshot,
		nil,
		nil,
		service.NewRateLimitService(accountRepo, nil, cfg, nil, nil),
		nil,
		nil,
		upstream,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil, nil)
	h := &GatewayHandler{
		gatewayService:           gwSvc,
		billingCacheService:      billingCacheSvc,
		concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
		maxAccountSwitches:       1,
		maxAccountSwitchesGemini: 1,
		cfg:                      cfg,
	}

	return h, func() { billingCacheSvc.Stop() }
}

func TestGatewayHandlerMessages_ForceCacheBillingContextOnStickyFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(9101)
	firstAccountID := int64(9201)
	secondAccountID := int64(9202)
	group := &service.Group{
		ID:       groupID,
		Hydrated: true,
		Platform: service.PlatformAnthropic,
		Status:   service.StatusActive,
	}
	account := func(id int64) *service.Account {
		return &service.Account{
			ID:       id,
			Name:     "anthropic-" + string(rune('a'+id-firstAccountID)),
			Platform: service.PlatformAnthropic,
			Type:     service.AccountTypeAPIKey,
			Credentials: map[string]any{
				"api_key":   "test-key",
				"pool_mode": true,
			},
			Extra:       map[string]any{"anthropic_passthrough": true},
			Concurrency: 1,
			Priority:    1,
			Status:      service.StatusActive,
			Schedulable: true,
			AccountGroups: []service.AccountGroup{{
				AccountID: id,
				GroupID:   groupID,
			}},
		}
	}
	upstream := &forceCacheBillingUpstream{}
	h, cleanup := newForceCacheBillingGatewayHandler(t, group, []*service.Account{account(firstAccountID), account(secondAccountID)}, upstream, &forceCacheBillingGatewayCache{accountID: firstAccountID})
	defer cleanup()

	body := []byte(`{"model":"claude-sonnet-4-5","stream":false,"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, group))
	c.Request = req

	apiKey := &service.APIKey{
		ID:      9301,
		UserID:  9401,
		GroupID: &groupID,
		Status:  service.StatusActive,
		User: &service.User{
			ID:          9401,
			Concurrency: 10,
			Balance:     100,
		},
		Group: group,
	}
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})

	h.Messages(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, secondAccountID, mustSelectedAccountID(t, c))
	require.JSONEq(t, `{"id":"msg_ok","type":"message","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":0,"output_tokens":3,"cache_read_input_tokens":5}}`, rec.Body.String())

	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	require.Equal(t, []bool{false, true}, upstream.forced)
	require.Equal(t, 2, upstream.callNum)
}

func TestGatewayHandlerMessages_GeminiForceCacheBillingContextOnStickyFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(9501)
	firstAccountID := int64(9601)
	secondAccountID := int64(9602)
	group := &service.Group{
		ID:       groupID,
		Hydrated: true,
		Platform: service.PlatformGemini,
		Status:   service.StatusActive,
	}
	accounts := []*service.Account{
		{
			ID:       firstAccountID,
			Name:     "gemini-a",
			Platform: service.PlatformGemini,
			Type:     service.AccountTypeAPIKey,
			Credentials: map[string]any{
				"api_key": "test-key",
			},
			Concurrency: 1,
			Priority:    1,
			Status:      service.StatusActive,
			Schedulable: true,
			AccountGroups: []service.AccountGroup{{
				AccountID: firstAccountID,
				GroupID:   groupID,
			}},
		},
		{
			ID:       secondAccountID,
			Name:     "gemini-b",
			Platform: service.PlatformGemini,
			Type:     service.AccountTypeAPIKey,
			Credentials: map[string]any{
				"api_key": "test-key",
			},
			Concurrency: 1,
			Priority:    1,
			Status:      service.StatusActive,
			Schedulable: true,
			AccountGroups: []service.AccountGroup{{
				AccountID: secondAccountID,
				GroupID:   groupID,
			}},
		},
	}
	upstream := &forceCacheBillingUpstream{
		firstStatus: http.StatusBadRequest,
		firstBody:   `{"error":{"message":"invalid project resource name"}}`,
		repeatFirst: 4,
		successBody: `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3}}`,
	}
	cache := &forceCacheBillingGatewayCache{accountID: firstAccountID}
	h, cleanup := newForceCacheBillingGatewayHandler(t, group, accounts, upstream, cache)
	defer cleanup()

	geminiScheduler := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: accounts}, nil, nil, nil, nil)
	h.geminiCompatService = service.NewGeminiMessagesCompatService(
		&forceCacheBillingAccountRepo{},
		&fakeGroupRepo{group: group},
		cache,
		geminiScheduler,
		nil,
		service.NewRateLimitService(&forceCacheBillingAccountRepo{}, nil, &config.Config{}, nil, nil),
		upstream,
		nil,
		&config.Config{RunMode: config.RunModeSimple},
	)

	body := []byte(`{"model":"gemini-2.5-flash","stream":false,"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, group))
	c.Request = req

	apiKey := &service.APIKey{
		ID:      9701,
		UserID:  9801,
		GroupID: &groupID,
		Status:  service.StatusActive,
		User: &service.User{
			ID:          9801,
			Concurrency: 10,
			Balance:     100,
		},
		Group: group,
	}
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})

	h.Messages(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, secondAccountID, mustSelectedAccountID(t, c))

	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	require.Equal(t, []bool{false, false, false, false, true}, upstream.forced)
	require.Equal(t, 5, upstream.callNum)
}

func mustSelectedAccountID(t *testing.T, c *gin.Context) int64 {
	t.Helper()
	value, ok := c.Get(opsAccountIDKey)
	require.True(t, ok)
	accountID, ok := value.(int64)
	require.True(t, ok)
	return accountID
}

var _ service.HTTPUpstream = (*forceCacheBillingUpstream)(nil)
var _ service.GatewayCache = (*forceCacheBillingGatewayCache)(nil)
