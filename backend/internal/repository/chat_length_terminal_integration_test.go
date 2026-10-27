//go:build integration

package repository

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newChatLengthTerminalHTTPFixture(t *testing.T, response, contentType string) *inflightHTTPFixture {
	t.Helper()
	platform := service.PlatformOpenAI
	logger.InitBootstrap()
	client := inflightTestEntClient(t)
	rdb := inflightIsolatedRedis(t)
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	cfg.Billing.InflightReservation.Enabled = true
	cfg.Billing.InflightReservation.TTLSeconds = 900
	cfg.Billing.InflightReservation.DefaultMaxOutputTokens = 8
	cfg.Gateway.Scheduling.DbFallbackEnabled = true
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@inflight-http.test", PasswordHash: "hash", Balance: 10, Concurrency: 100})
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
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Platform: platform, Type: service.AccountTypeAPIKey, Concurrency: 100, Credentials: map[string]any{"api_key": "local-fixture", "base_url": "https://upstream.test", "pool_mode": true, "pool_mode_retry_count": 0}, Extra: map[string]any{"privacy_mode": service.PrivacyModeTrainingOff, "openai_responses_supported": false, "openai_responses_mode": "auto"}})
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
	inputPrice, outputPrice, readPrice, writePrice := .001, .002, .0002, .0005
	channel := &service.Channel{Name: "chat-length-" + uuid.NewString(), Status: service.StatusActive, BillingModelSource: service.BillingModelSourceUpstream, GroupIDs: []int64{group.ID}, ModelPricing: []service.ChannelModelPricing{{Platform: platform, Models: []string{"gpt-5"}, BillingMode: service.BillingModeToken, InputPrice: &inputPrice, OutputPrice: &outputPrice, CacheReadPrice: &readPrice, CacheWritePrice: &writePrice}}}
	channelRepo := NewChannelRepository(inflightTestDB(t))
	require.NoError(t, channelRepo.Create(context.Background(), channel))
	channels := service.NewChannelService(channelRepo, groups, nil, nil, nil)
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
	openAISvc := service.NewOpenAIGatewayService(accounts, usage, atomicBilling, users, subs, rates, cache, cfg, snapshot, concurrency, billing, rateLimit, billingCache, upstream, deferred, nil, nil, service.NewModelPricingResolver(channels, billing), channels, nil, settings, nil, nil, groups)
	t.Cleanup(openAISvc.CloseOpenAIWSPool)
	keyService := service.NewAPIKeyService(NewAPIKeyRepository(client, inflightTestDB(t)), users, groups, subs, rates, nil, cfg)
	gemini := service.NewGeminiMessagesCompatService(accounts, groups, cache, snapshot, nil, rateLimit, upstream, nil, cfg)
	options := service.UsageRecordWorkerPoolOptions{WorkerCount: 1, QueueSize: 16, TaskTimeout: 5 * time.Second}
	pool := service.NewUsageRecordWorkerPoolWithOptions(options)
	t.Cleanup(pool.Stop)
	fixture := &inflightHTTPFixture{account: account, accounts: accounts, accountID: account.ID, gatewayService: gatewaySvc, geminiService: gemini, rateLimit: rateLimit, settings: settings, user: user, key: key, pool: pool, upstream: upstream, openAIService: openAISvc,
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

func TestChatLengthTerminalHTTP_UsageAndBillingOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, reason string
		tool         bool
	}{
		{"length_text", "length", false}, {"length_tool", "length", true},
		{"stop", "stop", false}, {"tool_calls", "tool_calls", true}, {"content_filter", "content_filter", false},
	} {
		for _, route := range []string{"responses", "legacy_messages"} {
			for _, card := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/card_%t", tc.name, route, card), func(t *testing.T) {
					delta := `{"content":"partial text"}`
					if tc.tool {
						delta = `{"tool_calls":[{"index":0,"id":"call_limit","type":"function","function":{"name":"exec","arguments":"{}"}}]}`
					}
					wire := fmt.Sprintf("data: {\"id\":\"chat_length\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":%s}]}\n\ndata: {\"id\":\"chat_length\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\ndata: [DONE]\n\n", delta, tc.reason)
					f := newChatLengthTerminalHTTPFixture(t, wire, "text/event-stream")
					if card {
						admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 10, 20, 30, 0, 0, 0)
					}
					f.upstream.observe = func(req *http.Request) {
						require.Equal(t, "/v1/chat/completions", req.URL.Path)
						require.Equal(t, "auto", f.account.Extra["openai_responses_mode"], "Messages uses the indirect bridge")
					}
					close(f.upstream.release)
					path := "/v1/messages"
					body := `{"model":"gpt-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`
					serve := f.openAI.Messages
					if route == "responses" {
						path = "/v1/responses"
						body = `{"model":"gpt-5","max_output_tokens":64,"input":"hi","stream":true}`
						serve = f.openAI.Responses
					}
					rec := f.request(body, path, "", serve)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					f.pool.Stop()
					var logs, dedup, input, output int
					var cost, balance float64
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &input, &output, &cost))
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
					require.Equal(t, 1, logs)
					require.Equal(t, 1, dedup)
					require.Equal(t, 10, input)
					require.Equal(t, 2, output)
					const wantCost = 10*.001 + 2*.002
					require.InDelta(t, wantCost, cost, 1e-9)
					if card {
						var daily, weekly, monthly float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.user.ID).Scan(&daily, &weekly, &monthly))
						require.InDelta(t, wantCost, daily, 1e-9)
						require.InDelta(t, wantCost, weekly, 1e-9)
						require.InDelta(t, wantCost, monthly, 1e-9)
						require.InDelta(t, 10, balance, 1e-9)
					} else {
						require.InDelta(t, 10-wantCost, balance, 1e-9)
					}
					require.EqualValues(t, 1, f.upstream.calls.Load())
					require.Zero(t, inflightHeld(t, f.user.ID))
					terminalCount := 0
					for _, line := range strings.Split(rec.Body.String(), "\n") {
						if !strings.HasPrefix(line, "data: ") {
							continue
						}
						event := gjson.Parse(strings.TrimPrefix(line, "data: "))
						kind := event.Get("type").String()
						if route == "responses" && (kind == "response.completed" || kind == "response.incomplete") {
							terminalCount++
							wantKind, wantStatus := "response.completed", "completed"
							if tc.reason == "length" {
								wantKind, wantStatus = "response.incomplete", "incomplete"
								require.Equal(t, "max_output_tokens", event.Get("response.incomplete_details.reason").String())
							}
							require.Equal(t, wantKind, kind)
							require.Equal(t, wantStatus, event.Get("response.status").String())
							require.EqualValues(t, 10, event.Get("response.usage.input_tokens").Int())
							require.EqualValues(t, 2, event.Get("response.usage.output_tokens").Int())
						} else if route == "legacy_messages" && kind == "message_delta" {
							terminalCount++
							wantStop := "end_turn"
							if tc.reason == "length" {
								wantStop = "max_tokens"
							} else if tc.tool {
								wantStop = "tool_use"
							}
							require.Equal(t, wantStop, event.Get("delta.stop_reason").String())
							require.EqualValues(t, 10, event.Get("usage.input_tokens").Int())
							require.EqualValues(t, 2, event.Get("usage.output_tokens").Int())
						}
					}
					require.Equal(t, 1, terminalCount, rec.Body.String())
					if tc.tool {
						require.Contains(t, rec.Body.String(), "call_limit")
						require.Contains(t, rec.Body.String(), "exec")
					} else {
						require.Contains(t, rec.Body.String(), "partial text")
					}
					if route == "responses" {
						require.Equal(t, 1, strings.Count(rec.Body.String(), "data: [DONE]"))
					} else {
						require.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_stop\n"))
					}
				})
			}
		}
	}
}
