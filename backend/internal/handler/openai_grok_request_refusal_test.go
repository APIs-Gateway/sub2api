//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type grokRefusalHandlerRepo struct {
	openAIWSFailoverHandlerAccountRepoStub
	errors, cooldowns, updates atomic.Int32
}

func (r *grokRefusalHandlerRepo) SetError(context.Context, int64, string) error {
	r.errors.Add(1)
	return nil
}
func (r *grokRefusalHandlerRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.cooldowns.Add(1)
	return nil
}
func (r *grokRefusalHandlerRepo) Update(context.Context, *service.Account) error {
	r.updates.Add(1)
	return nil
}
func (r *grokRefusalHandlerRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	r.updates.Add(1)
	return nil
}

func (r *grokRefusalHandlerRepo) ExtendModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	r.cooldowns.Add(1)
	return nil
}

func TestGrokRequestRefusalHandlerPoolAndQueuedFraming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// The scheduler settings cache is process-wide; let this test's enabled
	// fixture expire before unrelated handler fixtures run with their defaults.
	t.Cleanup(func() { time.Sleep(5 * time.Second) })
	for _, route := range []string{"raw", "bridge", "native"} {
		for _, stream := range []bool{false, true} {
			for _, queue := range []string{"none", "user", "account", "account_fault", "account_escaped"} {
				if queue != "none" && queue != "account_fault" && queue != "account_escaped" && !stream {
					continue
				}
				t.Run(fmt.Sprintf("%s_stream_%t_queue_%s", route, stream, queue), func(t *testing.T) {
					cfg := &config.Config{RunMode: config.RunModeStandard}
					cfg.Default.RateMultiplier = 1
					cfg.Security.URLAllowlist.Enabled = false
					cfg.Gateway.Scheduling.FallbackWaitTimeout = 3 * time.Second
					cfg.Gateway.Scheduling.FallbackMaxWaiting = 10
					cfg.Gateway.Scheduling.StickySessionWaitTimeout = 3 * time.Second
					cfg.Gateway.Scheduling.LoadBatchEnabled = false
					accounts := []service.Account{keepaliveMismatchAccount(151611, 1, nil), keepaliveMismatchAccount(151612, 2, nil)}
					for i := range accounts {
						accounts[i].Platform = service.PlatformGrok
						accounts[i].Credentials["temp_unschedulable_enabled"] = true
						accounts[i].Credentials["temp_unschedulable_rules"] = []any{map[string]any{"error_code": 403, "keywords": []any{"permission-denied", "entitlement_required"}, "duration_minutes": 5}}
						accounts[i].Credentials["base_url"] = "https://grok.test/v1"
						if route == "native" {
							accounts[i].Type = service.AccountTypeOAuth
							accounts[i].Credentials["access_token"] = "token"
						} else if route == "raw" {
							accounts[i].Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: false}
						}
					}
					repo := &grokRefusalHandlerRepo{openAIWSFailoverHandlerAccountRepoStub: openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}}
					usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 8)}
					userRepo := &openAIRecordUsageUserRepoStub795{user: service.User{ID: 15161, Status: service.StatusActive, Balance: 100}}
					billingRepo := &chatPartialBillingRepo{applied: make(chan *service.UsageBillingCommand, 8)}
					billing := service.NewBillingCacheService(nil, userRepo, nil, nil, nil, nil, cfg, nil, nil)
					t.Cleanup(billing.Stop)
					flushed := make(chan struct{})
					available := func() bool {
						select {
						case <-flushed:
							return true
						default:
							return false
						}
					}
					cache := &concurrencyCacheMock{acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return queue != "user" || available(), nil }, acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return queue != "account" || available(), nil }}
					concurrency := service.NewConcurrencyService(cache)
					var calls atomic.Int32
					var usedAccount int64
					var usedAccounts []int64
					upstream := openAIHandlerHTTPUpstreamStub{do: func(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
						calls.Add(1)
						usedAccount = id
						usedAccounts = append(usedAccounts, id)
						if queue == "user" || queue == "account" {
							require.True(t, available(), "queue keepalive must precede upstream")
						}
						if route == "raw" {
							require.True(t, strings.HasSuffix(req.URL.Path, "/chat/completions"))
						} else {
							require.True(t, strings.HasSuffix(req.URL.Path, "/responses"))
						}
						body := `{"code":"permission-denied","error":"I'm sorry, I can't help with that request."}`
						if queue == "account_fault" {
							body = `{"code":"permission-denied","error":{"code":"entitlement_required","message":"I'm sorry, I can't help with that request."}}`
						}
						if queue == "account_escaped" {
							body = `{"code":"permission-denied","error":"I'm sorry, I can't help with that request.","detail":"account\u0020has\u0020been\u0020disabled"}`
						}
						return &http.Response{StatusCode: 403, Header: http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"grok-refusal-request"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
					}}
					rateLimit := service.NewRateLimitService(repo, usage, cfg, nil, nil)
					rateLimit.SetSettingService(service.NewSettingService(&wechatOAuthSettingRepoStub{values: map[string]string{"openai_advanced_scheduler_enabled": "true"}}, cfg))
					gateway := service.NewOpenAIGatewayService(repo, usage, billingRepo, userRepo, nil, nil, nil, cfg, nil, concurrency, service.NewBillingService(cfg, nil), rateLimit, billing, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil)
					// Seed a different account to prove the advanced scheduler is
					// enabled even if another test left its five-second cache off.
					require.Eventually(t, func() bool {
						gateway.ReportOpenAIAccountScheduleResult(151699, false, nil)
						return gateway.SnapshotOpenAIAccountSchedulerMetrics().RuntimeStatsAccountCount == 1
					}, 6*time.Second, 10*time.Millisecond)
					metricsBefore := gateway.SnapshotOpenAIAccountSchedulerMetrics()
					h := NewOpenAIGatewayHandler(gateway, concurrency, billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
					h.concurrencyHelper = NewConcurrencyHelper(concurrency, SSEPingFormatComment, 5*time.Millisecond)
					groupID := int64(15161)
					apiKey := &service.APIKey{ID: 15161, GroupID: &groupID, User: &service.User{ID: 15161, Status: service.StatusActive}, Group: &service.Group{ID: groupID, Platform: service.PlatformGrok, Status: service.StatusActive, RateMultiplier: 1}}
					router := gin.New()
					router.Use(func(c *gin.Context) {
						c.Set(string(middleware.ContextKeyAPIKey), apiKey)
						c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
						c.Writer = &keepaliveMismatchFlushSignalWriter{ResponseWriter: c.Writer, flushed: flushed}
						c.Next()
					})
					path := "/v1/chat/completions"
					body := fmt.Sprintf(`{"model":"grok-4.3","messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream)
					if route == "native" {
						path = "/v1/responses"
						body = fmt.Sprintf(`{"model":"grok-4.3","input":"hello","stream":%t}`, stream)
						router.POST(path, h.Responses)
					} else {
						router.POST(path, h.ChatCompletions)
					}
					recorder := httptest.NewRecorder()
					request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
					request.Header.Set("Content-Type", "application/json")
					router.ServeHTTP(recorder, request)
					if queue == "account_fault" || queue == "account_escaped" {
						require.Equal(t, int32(2), calls.Load(), "true entitlement failure must still use account failover")
						require.NotEqual(t, usedAccounts[0], usedAccounts[1])
						require.Positive(t, repo.cooldowns.Load())
						require.Greater(t, gateway.SnapshotOpenAIAccountSchedulerMetrics().AccountSwitchTotal, metricsBefore.AccountSwitchTotal)
						return
					}
					require.Equal(t, int32(1), calls.Load(), "a two-account pool must not retry a request refusal")
					require.Contains(t, []int64{151611, 151612}, usedAccount)
					require.Zero(t, repo.errors.Load())
					require.Zero(t, repo.cooldowns.Load())
					require.Zero(t, repo.updates.Load())
					require.Empty(t, repo.rateLimitedIDs)
					metricsAfter := gateway.SnapshotOpenAIAccountSchedulerMetrics()
					require.Equal(t, metricsBefore.RuntimeStatsAccountCount, metricsAfter.RuntimeStatsAccountCount, "request refusal must not report account failure or success")
					require.Equal(t, metricsBefore.AccountSwitchTotal, metricsAfter.AccountSwitchTotal)
					select {
					case log := <-usage.created:
						t.Fatalf("refusal must not bill: %+v", log)
					case cmd := <-billingRepo.applied:
						t.Fatalf("refusal must not settle or deduct: %+v", cmd)
					case <-time.After(20 * time.Millisecond):
					}
					response := recorder.Body.String()
					require.Contains(t, response, "upstream content safety system")
					require.NotContains(t, response, "I'm sorry")
					require.NotContains(t, response, "permission-denied")
					require.NotContains(t, response, "upstream_error")
					if queue == "none" {
						require.Equal(t, 403, recorder.Code)
						require.True(t, json.Valid(recorder.Body.Bytes()))
						require.Equal(t, "invalid_request_error", gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
					} else {
						require.Equal(t, 200, recorder.Code)
						require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
						require.True(t, strings.HasPrefix(response, ":\n\n"))
						terminal := "error"
						if route == "native" {
							terminal = "response.failed"
						}
						require.Equal(t, 1, strings.Count(response, "event: "+terminal+"\n"))
						for _, frame := range strings.Split(response, "\n\n") {
							if strings.TrimSpace(frame) == "" || strings.HasPrefix(frame, ":") {
								continue
							}
							require.True(t, strings.HasPrefix(frame, "event: "+terminal+"\ndata: "), "no bare JSON/mixed framing: %s", frame)
							payload := strings.SplitN(frame, "\ndata: ", 2)[1]
							require.True(t, json.Valid([]byte(payload)))
							if route == "native" {
								require.Equal(t, "failed", gjson.Get(payload, "response.status").String())
								require.Equal(t, "invalid_request", gjson.Get(payload, "response.error.code").String())
								require.Greater(t, gjson.Get(payload, "response.created_at").Int(), int64(0))
								require.True(t, gjson.Get(payload, "response.output").IsArray())
							} else {
								require.Equal(t, "invalid_request_error", gjson.Get(payload, "error.type").String())
							}
						}
					}
				})
			}
		}
	}
}
