//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type preflightBillingCapture struct {
	compatPartialBillingRepo
	service.BillingInflightRepository
	reserves int
}

func (r *preflightBillingCapture) ReserveBillingInflight(context.Context, int64, string, float64, bool, time.Duration) (bool, error) {
	r.reserves++
	return true, nil
}

func TestResponsesInputTokensNormalModeNeverBillsOrReserves(t *testing.T) {
	for _, tc := range []struct {
		name, base string
		balance    float64
		want       int
		calls      int
	}{
		{"native", "", 100, 200, 1},
		{"estimated", "https://relay.invalid", 100, 200, 0},
		{"insufficient wallet", "", 0, 403, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{RunMode: config.RunModeStandard}
			userRepo := &openAIRecordUsageUserRepoStub795{user: service.User{ID: 100, Balance: tc.balance, Status: service.StatusActive}}
			billing := &preflightBillingCapture{}
			usage := &partialUsageBillingUsageLogRepo{created: make(chan *service.UsageLog, 4)}
			cache := service.NewBillingCacheService(nil, userRepo, nil, nil, nil, nil, cfg, nil, nil)
			t.Cleanup(cache.Stop)
			upstream := &alphaSearchHTTPUpstream{responses: []*http.Response{{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"object":"response.input_tokens","input_tokens":42}`))}}}
			account := service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "key", "base_url": tc.base}}
			concurrency := service.NewConcurrencyService(nil)
			gateway := service.NewOpenAIGatewayService(openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}, usage, billing, userRepo, nil, nil, nil, cfg, nil, concurrency, service.NewBillingService(cfg, nil), nil, cache, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			h := NewOpenAIGatewayHandler(gateway, concurrency, cache, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
			h.usageRecordWorkerPool = newUsageRecordTestPool(t)
			key := alphaSearchAPIKey(service.PlatformOpenAI, 5102)
			c, rec := newAlphaSearchContext(`{"model":"gpt-4o","input":"Hello world"}`, key, &middleware2.AuthSubject{UserID: 100})
			c.Request.URL.Path = "/v1/responses/input_tokens"
			h.ResponsesInputTokens(c)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
			require.Len(t, upstream.calls(), tc.calls)
			require.Empty(t, billing.commands)
			require.Zero(t, billing.reserves)
			require.Empty(t, usage.created)
		})
	}
}

func TestResponsesInputTokensChainAndAudit(t *testing.T) {
	for _, tc := range []struct {
		name               string
		unavailable, audit bool
		want               int
		calls              []int64
	}{
		{"primary", false, false, 200, []int64{11}},
		{"fallback", true, false, 200, []int64{21}},
		{"fallback audit union", true, true, 403, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := chainRespBase()
			for group, accounts := range o.schedulable {
				for i := range accounts {
					accounts[i].Credentials = map[string]any{"api_key": "key"}
				}
				o.schedulable[group] = accounts
			}
			if tc.unavailable {
				delete(o.schedulable, 1)
			}
			if tc.audit {
				o.audit = []int64{2}
			}
			o.replies = map[int64]chainRespReply{11: {body: `{"object":"response.input_tokens","input_tokens":42}`, contentType: "application/json"}, 21: {body: `{"object":"response.input_tokens","input_tokens":42}`, contentType: "application/json"}}
			hs := newChainRespHarness(t, o)
			hs.router.POST("/v1/responses/input_tokens", hs.handler.ResponsesInputTokens)
			rec := httptest.NewRecorder()
			hs.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(`{"model":"gpt-5.4","input":"hello"}`)))
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
			require.Equal(t, tc.calls, hs.upstream.accountCalls())
			require.Equal(t, int32(1), hs.routes.calls.Load())
			require.Empty(t, hs.usageLogs)
		})
	}
}
