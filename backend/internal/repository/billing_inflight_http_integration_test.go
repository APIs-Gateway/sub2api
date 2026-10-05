//go:build integration

package repository

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type inflightHTTPUpstream struct {
	calls       atomic.Int32
	started     chan struct{}
	release     chan struct{}
	response    string
	contentType string
	status      int
	readErr     error
	observe     func(*http.Request)
}

type inflightHTTPReadError struct{ err error }

func (r inflightHTTPReadError) Read([]byte) (int, error) { return 0, r.err }

func (u *inflightHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if u.observe != nil {
		u.observe(req)
	}
	if u.calls.Add(1) == 1 {
		close(u.started)
		select {
		case <-u.release:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	status := u.status
	if status == 0 {
		status = http.StatusOK
	}
	var body io.Reader = strings.NewReader(u.response)
	if u.readErr != nil {
		body = io.MultiReader(body, inflightHTTPReadError{u.readErr})
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {u.contentType}}, Body: io.NopCloser(body), Request: req}, nil
}
func (u *inflightHTTPUpstream) DoWithTLS(req *http.Request, proxy string, id int64, slots int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, slots)
}

type inflightHTTPFixture struct {
	user          *service.User
	key           *service.APIKey
	gateway       *userhandler.GatewayHandler
	openAI        *userhandler.OpenAIGatewayHandler
	openAIService *service.OpenAIGatewayService
	pool          *service.UsageRecordWorkerPool
	upstream      *inflightHTTPUpstream
	requests      sync.WaitGroup
}

func newInflightHTTPFixture(t *testing.T, platform, response, contentType string, poolOptions ...service.UsageRecordWorkerPoolOptions) *inflightHTTPFixture {
	t.Helper()
	logger.InitBootstrap()
	client := inflightTestEntClient(t)
	rdb := testRedis(t)
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	cfg.Billing.InflightReservation.Enabled = true
	cfg.Billing.InflightReservation.TTLSeconds = 900
	cfg.Billing.InflightReservation.DefaultMaxOutputTokens = 8
	cfg.Gateway.Scheduling.DbFallbackEnabled = true
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@inflight-http.test", PasswordHash: "hash", Balance: 0.00000001, Concurrency: 100})
	group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: platform, RateMultiplier: 1})
	group.Hydrated = true
	group.AllowImageGeneration = true
	group.AllowMessagesDispatch = true
	_, err := inflightTestDB(t).Exec(`UPDATE groups SET allow_image_generation=true, allow_messages_dispatch=true WHERE id=$1`, group.ID)
	require.NoError(t, err)
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-" + uuid.NewString(), Name: "inflight-http"})
	key.User = user
	key.Group = group
	schedulerCache := NewSchedulerCache(rdb)
	accounts := NewAccountRepository(client, inflightTestDB(t), schedulerCache)
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Platform: platform, Type: service.AccountTypeAPIKey, Concurrency: 100, Credentials: map[string]any{"api_key": "local-fixture", "base_url": "https://upstream.test", "pool_mode": true, "pool_mode_retry_count": 0}, Extra: map[string]any{"privacy_mode": service.PrivacyModeTrainingOff, "openai_responses_supported": true}})
	require.NoError(t, accounts.BindGroups(context.Background(), account.ID, []int64{group.ID}))
	groups := NewGroupRepository(client, inflightTestDB(t))
	users := NewUserRepository(client, inflightTestDB(t))
	subs := NewUserSubscriptionRepository(client)
	rates := NewUserGroupRateRepository(inflightTestDB(t))
	cache := NewGatewayCache(rdb)
	billingCacheRepo := NewBillingCache(rdb)
	require.NoError(t, billingCacheRepo.SetUserBalance(context.Background(), user.ID, 100))
	settings := service.NewSettingService(NewSettingRepository(client), cfg)
	billingCache := service.NewBillingCacheService(billingCacheRepo, users, subs, nil, nil, rates, cfg, nil, settings)
	t.Cleanup(billingCache.Stop)
	concurrency := service.NewConcurrencyService(NewConcurrencyCache(rdb, 15, 30))
	snapshot := service.NewSchedulerSnapshotService(schedulerCache, nil, accounts, groups, cfg)
	t.Cleanup(snapshot.Stop)
	billing := service.NewBillingService(cfg, nil)
	rateLimit := service.NewRateLimitService(accounts, nil, cfg, nil, nil)
	usage := NewUsageLogRepository(client, inflightTestDB(t))
	atomicBilling := NewUsageBillingRepository(client, inflightTestDB(t))
	upstream := &inflightHTTPUpstream{started: make(chan struct{}), release: make(chan struct{}), response: response, contentType: contentType}
	wheel, err := service.NewTimingWheelService()
	require.NoError(t, err)
	t.Cleanup(wheel.Stop)
	deferred := service.NewDeferredService(accounts, wheel, time.Minute)
	t.Cleanup(deferred.Stop)
	gatewaySvc := service.NewGatewayService(accounts, groups, usage, atomicBilling, users, subs, rates, cache, cfg, snapshot, concurrency, billing, rateLimit, billingCache, nil, upstream, deferred, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	openAISvc := service.NewOpenAIGatewayService(accounts, usage, atomicBilling, users, subs, rates, cache, cfg, snapshot, concurrency, billing, rateLimit, billingCache, upstream, deferred, nil, nil, nil, nil, nil, nil, nil, nil, groups)
	t.Cleanup(openAISvc.CloseOpenAIWSPool)
	keyService := service.NewAPIKeyService(NewAPIKeyRepository(client, inflightTestDB(t)), users, groups, subs, rates, nil, cfg)
	gemini := service.NewGeminiMessagesCompatService(accounts, groups, cache, snapshot, nil, rateLimit, upstream, nil, cfg)
	options := service.UsageRecordWorkerPoolOptions{WorkerCount: 1, QueueSize: 16, TaskTimeout: 5 * time.Second}
	if len(poolOptions) > 0 {
		options = poolOptions[0]
	}
	pool := service.NewUsageRecordWorkerPoolWithOptions(options)
	t.Cleanup(pool.Stop)
	fixture := &inflightHTTPFixture{user: user, key: key, pool: pool, upstream: upstream, openAIService: openAISvc,
		gateway: userhandler.NewGatewayHandler(gatewaySvc, gemini, nil, nil, nil, concurrency, billingCache, nil, nil, pool, nil, nil, nil, cfg, nil, openAISvc),
		openAI:  userhandler.NewOpenAIGatewayHandler(openAISvc, concurrency, billingCache, keyService, pool, nil, nil, nil, cfg)}
	t.Cleanup(func() {
		select {
		case <-upstream.release:
		default:
			close(upstream.release)
		}
		done := make(chan struct{})
		go func() { fixture.requests.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("HTTP handlers did not stop before isolated database cleanup")
		}
	})
	return fixture
}
func (f *inflightHTTPFixture) request(body, path, modelAction string, serve func(*gin.Context)) *httptest.ResponseRecorder {
	f.requests.Add(1)
	defer f.requests.Done()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, f.key.Group))
	c.Set(string(middleware.ContextKeyAPIKey), f.key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: f.user.ID, Concurrency: 100})
	if modelAction != "" {
		c.Params = gin.Params{{Key: "modelAction", Value: modelAction}}
	}
	serve(c)
	return rec
}

const inflightAnthropicJSON = `{"id":"msg_inflight","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`
const inflightGeminiJSON = `{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-3.6-flash"}`
const inflightResponsesJSON = `{"id":"resp_inflight","object":"response","status":"completed","model":"gpt-5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`
const inflightResponsesSSE = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + inflightResponsesJSON + "}\n\n"
const inflightChatJSON = `{"id":"chat_inflight","model":"gpt-5","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
const inflightAnthropicSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_inflight\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestBillingInflightHTTP_ConcurrentAdmissionAndBillOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name, platform, body, path, modelAction, response, contentType string
		handler                                                        func(*inflightHTTPFixture) func(*gin.Context)
	}{
		{"gateway_messages_anthropic", service.PlatformAnthropic, `{"model":"claude-sonnet-4-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages", "", inflightAnthropicJSON, "application/json", func(f *inflightHTTPFixture) func(*gin.Context) { return f.gateway.Messages }},
		{"gateway_messages_gemini", service.PlatformGemini, `{"model":"gemini-3.6-flash","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages", "", inflightGeminiJSON, "application/json", func(f *inflightHTTPFixture) func(*gin.Context) { return f.gateway.Messages }},
		{"gateway_chat_completions", service.PlatformAnthropic, `{"model":"claude-sonnet-4-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions", "", inflightAnthropicSSE, "text/event-stream", func(f *inflightHTTPFixture) func(*gin.Context) { return f.gateway.ChatCompletions }},
		{"gateway_responses", service.PlatformAnthropic, `{"model":"claude-sonnet-4-5","max_output_tokens":8,"input":"hi"}`, "/v1/responses", "", inflightAnthropicSSE, "text/event-stream", func(f *inflightHTTPFixture) func(*gin.Context) { return f.gateway.Responses }},
		{"gemini_native", service.PlatformGemini, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":8}}`, "/v1beta/models/gemini-3.6-flash:generateContent", "/gemini-3.6-flash:generateContent", inflightGeminiJSON, "application/json", func(f *inflightHTTPFixture) func(*gin.Context) { return f.gateway.GeminiV1BetaModels }},
		{"openai_responses", service.PlatformOpenAI, `{"model":"gpt-5","max_output_tokens":8,"input":"hi"}`, "/v1/responses", "", inflightResponsesJSON, "application/json", func(f *inflightHTTPFixture) func(*gin.Context) { return f.openAI.Responses }},
		{"openai_messages", service.PlatformOpenAI, `{"model":"gpt-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages", "", inflightResponsesSSE, "text/event-stream", func(f *inflightHTTPFixture) func(*gin.Context) { return f.openAI.Messages }},
		{"openai_chat_completions", service.PlatformOpenAI, `{"model":"gpt-5","max_completion_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions", "", inflightResponsesSSE, "text/event-stream", func(f *inflightHTTPFixture) func(*gin.Context) { return f.openAI.ChatCompletions }},
		{"openai_images", service.PlatformOpenAI, `{"model":"gpt-image-2","prompt":"hi","n":1,"size":"1024x1024","stream":false,"response_format":"b64_json"}`, "/v1/images/generations", "", `{"created":1780000000,"data":[{"b64_json":"aGVsbG8="}]}`, "application/json", func(f *inflightHTTPFixture) func(*gin.Context) { return f.openAI.Images }},
		{"openai_embeddings", service.PlatformOpenAI, `{"model":"qwen3-embedding-8b","input":"hi"}`, "/v1/embeddings", "", `{"object":"list","model":"qwen3-embedding-8b","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"usage":{"prompt_tokens":10,"total_tokens":10}}`, "application/json", func(f *inflightHTTPFixture) func(*gin.Context) { return f.openAI.Embeddings }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newInflightHTTPFixture(t, tc.platform, tc.response, tc.contentType)
			if tc.name == "openai_images" {
				// This fixture returns native Images JSON. Responses-capable accounts
				// deliberately route image requests through the Responses bridge.
				_, err := inflightTestDB(t).Exec(`UPDATE accounts SET extra = extra || '{"openai_responses_supported":false}'::jsonb WHERE id IN (SELECT account_id FROM account_groups WHERE group_id=$1)`, *f.key.GroupID)
				require.NoError(t, err)
			}
			firstDone := make(chan *httptest.ResponseRecorder, 1)
			go func() { firstDone <- f.request(tc.body, tc.path, tc.modelAction, tc.handler(f)) }()
			select {
			case <-f.upstream.started:
			case rec := <-firstDone:
				t.Fatalf("first rejected before upstream: %d %s", rec.Code, rec.Body.String())
			case <-time.After(10 * time.Second):
				t.Fatal("first did not dispatch")
			}
			require.Positive(t, inflightHeld(t, f.user.ID), "first request reserves despite estimate exceeding tiny historical wallet")
			second := f.request(tc.body, tc.path, tc.modelAction, tc.handler(f))
			require.Equal(t, http.StatusForbidden, second.Code, second.Body.String())
			require.EqualValues(t, 1, f.upstream.calls.Load(), "stale Redis balance must not admit another funded request")
			close(f.upstream.release)
			var first *httptest.ResponseRecorder
			select {
			case first = <-firstDone:
			case <-time.After(10 * time.Second):
				t.Fatal("first handler stuck")
			}
			require.Equal(t, http.StatusOK, first.Code, first.Body.String())
			f.pool.Stop()
			t.Logf("worker stats=%+v", f.pool.Stats())
			var logs, dedup int
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
			require.Equal(t, 1, logs, "one usage record for the dispatched request")
			require.Equal(t, 1, dedup, "one authoritative settlement for the dispatched request")
			require.InDelta(t, 0, inflightHeld(t, f.user.ID), 1e-8, "settlement and final handler release consume all holds")
		})
	}
}

func TestBillingInflightHTTP_StaleWalletPreservesPreciseCardDenials(t *testing.T) {
	for _, tc := range []struct {
		name, code             string
		daily, weekly, monthly float64
	}{
		{"wallet", "insufficient balance", 0, 0, 0},
		{"daily", "daily usage limit exceeded", 1, 0, 0},
		{"weekly", "weekly usage limit exceeded", 0, 10, 0},
		{"monthly", "monthly usage limit exceeded", 0, 0, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInflightHTTPFixture(t, service.PlatformAnthropic, inflightAnthropicJSON, "application/json")
			_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=0 WHERE id=$1`, f.user.ID)
			require.NoError(t, err)
			if tc.name != "wallet" {
				admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 1, 10, 20, tc.daily, tc.weekly, tc.monthly)
			}
			rec := f.request(`{"model":"claude-sonnet-4-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages", "", f.gateway.Messages)
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), `"type":"billing_error"`)
			require.Contains(t, rec.Body.String(), tc.code)
			require.Zero(t, f.upstream.calls.Load(), "fresh PG rejection must propagate before sending upstream despite cached wallet 100")
			require.Zero(t, inflightHeld(t, f.user.ID))
		})
	}
}

func TestBillingInflightHTTP_ProviderRefusalAndUnknownReadFunding(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		readErr    error
		noCharge   bool
	}{
		{"complete_auth", `{"error":{"type":"authentication_error","message":"bad credential"}}`, 401, nil, true},
		{"incomplete_auth_prefix", `{"error":{"type":"authentication_error","message":"bad credential"}}`, 401, errors.New("upstream reader reset"), false},
		{"escaped_usage", `{"error":{"type":"authentication_error"},"response":{"us\u0061ge":{"input_tokens":10}}}`, 401, nil, false},
		{"duplicate_error", `{"error":{"type":"authentication_error"},"error":{"type":"server_error"}}`, 401, nil, false},
		{"image_partial", `{"type":"response.failed","error":{"type":"permission_error"},"output":[{"type":"image_generation_call","result":"partial"}]}`, 403, nil, false},
		{"unknown_502", `{"error":{"type":"server_error"}}`, 502, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInflightHTTPFixture(t, service.PlatformAnthropic, tc.body, "application/json")
			f.upstream.status, f.upstream.readErr = tc.status, tc.readErr
			close(f.upstream.release)
			rec := f.request(`{"model":"claude-sonnet-4-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages", "", f.gateway.Messages)
			require.GreaterOrEqual(t, rec.Code, 400, rec.Body.String())
			require.EqualValues(t, 1, f.upstream.calls.Load())
			f.pool.Stop()
			if tc.noCharge {
				require.Zero(t, inflightHeld(t, f.user.ID), "complete canonical rejection proves no charge")
			} else {
				require.Positive(t, inflightHeld(t, f.user.ID), "unknown execution must retain bounded estimate")
			}
			var logs int
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
			require.Zero(t, logs)
			var wallet float64
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&wallet))
			require.InDelta(t, f.user.Balance, wallet, 1e-10)
			ok, err := NewUsageBillingRepository(inflightTestEntClient(t), inflightTestDB(t)).(service.BillingInflightRepository).ReserveBillingInflight(context.Background(), f.user.ID, uuid.NewString(), 1, false, time.Minute)
			require.NoError(t, err)
			require.Equal(t, tc.noCharge, ok, "a separate paid owner must see authoritative retained funding")
		})
	}
}

func TestBillingInflightHTTP_QueuedAndDroppedUsageRetainFunding(t *testing.T) {
	for _, drop := range []bool{false, true} {
		name := "queue_delay"
		if drop {
			name = "queue_drop"
		}
		t.Run(name, func(t *testing.T) {
			f := newInflightHTTPFixture(t, service.PlatformAnthropic, inflightAnthropicJSON, "application/json", service.UsageRecordWorkerPoolOptions{WorkerCount: 1, QueueSize: 1, TaskTimeout: time.Minute, OverflowPolicy: config.UsageRecordOverflowPolicyDrop})
			workerStarted, workerRelease := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseWorker := func() { releaseOnce.Do(func() { close(workerRelease) }) }
			t.Cleanup(releaseWorker)
			require.Equal(t, service.UsageRecordSubmitModeEnqueued, f.pool.Submit(func(context.Context) { close(workerStarted); <-workerRelease }))
			<-workerStarted
			if drop {
				require.Equal(t, service.UsageRecordSubmitModeEnqueued, f.pool.Submit(func(context.Context) {}))
			}
			close(f.upstream.release)
			body := `{"model":"claude-sonnet-4-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`
			first := f.request(body, "/v1/messages", "", f.gateway.Messages)
			require.Equal(t, http.StatusOK, first.Code, first.Body.String())
			require.Positive(t, inflightHeld(t, f.user.ID), "completion cannot free unexecuted billing task")
			second := f.request(body, "/v1/messages", "", f.gateway.Messages)
			require.Equal(t, http.StatusForbidden, second.Code, second.Body.String())
			require.EqualValues(t, 1, f.upstream.calls.Load())
			releaseWorker()
			f.pool.Stop()
			var logs int
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
			if drop {
				require.EqualValues(t, 1, f.pool.Stats().DroppedQueueFull)
				require.Zero(t, logs)
				require.Positive(t, inflightHeld(t, f.user.ID), "dropped accounting keeps a bounded, non-durable mitigation hold")
				_, err := inflightTestDB(t).Exec(`UPDATE billing_inflight_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE user_id=$1`, f.user.ID)
				require.NoError(t, err)
				ok, err := NewUsageBillingRepository(inflightTestEntClient(t), inflightTestDB(t)).(service.BillingInflightRepository).ReserveBillingInflight(context.Background(), f.user.ID, uuid.NewString(), 1, false, time.Minute)
				require.NoError(t, err)
				require.True(t, ok, "expired dropped work must not become a permanent funds freeze")
			} else {
				require.Equal(t, 1, logs)
				require.Zero(t, inflightHeld(t, f.user.ID))
			}
		})
	}
}

func TestBillingInflightHTTP_ChatErrorReplayPreservesOriginalReadFailure(t *testing.T) {
	for _, responsesCompat := range []bool{false, true} {
		name := "raw_chat"
		if responsesCompat {
			name = "responses_compat_chat"
		}
		for _, incomplete := range []bool{false, true} {
			caseName := name + "/complete"
			if incomplete {
				caseName = name + "/reader_reset"
			}
			t.Run(caseName, func(t *testing.T) {
				f := newInflightHTTPFixture(t, service.PlatformOpenAI, `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"bad key"}}`, "application/json")
				_, err := inflightTestDB(t).Exec(`UPDATE accounts SET extra=jsonb_set(extra,'{openai_responses_supported}',to_jsonb($2::boolean)) WHERE id IN(SELECT account_id FROM account_groups WHERE group_id=$1)`, *f.key.GroupID, responsesCompat)
				require.NoError(t, err)
				f.upstream.status = http.StatusUnauthorized
				if incomplete {
					f.upstream.readErr = errors.New("non-EOF upstream read failure after valid JSON prefix")
				}
				close(f.upstream.release)
				rec := f.request(`{"model":"gpt-5","max_completion_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions", "", f.openAI.ChatCompletions)
				require.GreaterOrEqual(t, rec.Code, 400, rec.Body.String())
				require.EqualValues(t, 1, f.upstream.calls.Load())
				f.pool.Stop()
				if incomplete {
					require.Positive(t, inflightHeld(t, f.user.ID), "captured auth prefix replay cannot fabricate complete refusal proof")
				} else {
					require.Zero(t, inflightHeld(t, f.user.ID))
				}
				var n int
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&n))
				require.Zero(t, n)
			})
		}
	}
}

func TestBillingInflightHTTP_RealWorkerPanicKeepsBoundedPGFunding(t *testing.T) {
	f := newInflightHTTPFixture(t, service.PlatformOpenAI, inflightResponsesJSON, "application/json")
	lease, err := f.openAIService.ReserveBillingInflight(context.Background(), service.BillingInflightRequest{APIKey: f.key, Model: "gpt-5", Body: []byte(`{"model":"gpt-5","input":"hello"}`)})
	require.NoError(t, err)
	require.NotNil(t, lease)
	lease.MarkDispatched()
	wrap, finish := service.AcquireBillingInflightTask(service.WithBillingInflightLease(context.Background(), lease))
	panicReached := make(chan struct{})
	require.Equal(t, service.UsageRecordSubmitModeEnqueued, f.pool.Submit(func(ctx context.Context) {
		_ = wrap(ctx)
		defer finish(true)
		defer close(panicReached)
		panic("simulated billing worker failure before actual settlement")
	}))
	lease.HandlerDone()
	f.pool.Stop()
	<-panicReached
	require.EqualValues(t, 1, f.pool.Stats().CompletedTasks, "existing pool recovers the panic; lease requires explicit cost completion")
	require.Positive(t, inflightHeld(t, f.user.ID), "panic before completion cannot free the PG attempt")
	var n int
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&n))
	require.Zero(t, n)
	ok, err := NewUsageBillingRepository(inflightTestEntClient(t), inflightTestDB(t)).(service.BillingInflightRepository).ReserveBillingInflight(context.Background(), f.user.ID, uuid.NewString(), 1, false, time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
}
