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

func newCacheWriteUsageHTTPFixture(t *testing.T, response, contentType string, direct bool) *inflightHTTPFixture {
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
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Platform: platform, Type: service.AccountTypeAPIKey, Concurrency: 100, Credentials: map[string]any{"api_key": "local-fixture", "base_url": "https://upstream.test", "pool_mode": true, "pool_mode_retry_count": 0}, Extra: map[string]any{"privacy_mode": service.PrivacyModeTrainingOff, "openai_responses_supported": false, "openai_responses_mode": map[bool]string{false: "auto", true: "force_chat_completions"}[direct]}})
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
	channel := &service.Channel{Name: "cache-write-" + uuid.NewString(), Status: service.StatusActive, BillingModelSource: service.BillingModelSourceUpstream, GroupIDs: []int64{group.ID}, ModelPricing: []service.ChannelModelPricing{{Platform: platform, Models: []string{"gpt-5"}, BillingMode: service.BillingModeToken, InputPrice: &inputPrice, OutputPrice: &outputPrice, CacheReadPrice: &readPrice, CacheWritePrice: &writePrice}}}
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
func cacheWriteHTTPWireUsage(body string, stream, responses bool) gjson.Result {
	if !stream {
		return gjson.Get(body, "usage")
	}
	var usage gjson.Result
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		event := gjson.Parse(strings.TrimPrefix(line, "data: "))
		if !responses && event.Get("type").String() == "message_delta" {
			usage = event.Get("usage")
		}
		if responses && event.Get("type").String() == "response.completed" {
			usage = event.Get("response.usage")
		}
	}
	return usage
}

func TestCacheWriteUsageHTTP_WireAndWalletCardMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, fields string
		want         int
		free         bool
	}{
		{"creation_input", `"cache_creation_input_tokens":800`, 800, false},
		{"creation", `"cache_creation_tokens":800`, 800, false},
		{"write", `"cache_write_tokens":800`, 800, false},
		{"top_priority", `"cache_creation_input_tokens":800,"cache_creation_tokens":700,"cache_write_tokens":600`, 800, false},
		{"nested_zero", `"prompt_tokens_details":{"cache_write_tokens":0,"cache_creation_tokens":800},"cache_creation_input_tokens":900`, 0, false},
		{"nested_null", `"prompt_tokens_details":{"cache_write_tokens":null,"cache_creation_tokens":800},"cache_creation_input_tokens":900`, 0, false},
		{"ordinary", `"vendor":1`, 0, false},
		{"free_zero", `"cache_creation_input_tokens":0`, 0, true},
	} {
		for _, route := range []string{"direct_messages", "legacy_messages", "responses"} {
			for _, stream := range []bool{false, true} {
				for _, card := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/stream_%t/card_%t", tc.name, route, stream, card), func(t *testing.T) {
						prompt, completion := 1000, 50
						if tc.free {
							prompt, completion = 0, 0
						}
						usage := fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,%s}`, prompt, completion, prompt+completion, tc.fields)
						wire := `{"id":"cache_usage","model":"gpt-5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":` + usage + `}`
						contentType := "application/json"
						if stream {
							contentType = "text/event-stream"
							wire = "data: {\"id\":\"cache_usage\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"cache_usage\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":" + usage + "}\n\ndata: [DONE]\n\n"
						}
						f := newCacheWriteUsageHTTPFixture(t, wire, contentType, route == "direct_messages")
						if card {
							admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 10, 20, 30, 0, 0, 0)
						}
						f.upstream.observe = func(req *http.Request) { require.Equal(t, "/v1/chat/completions", req.URL.Path) }
						close(f.upstream.release)
						path := "/v1/messages"
						body := fmt.Sprintf(`{"model":"gpt-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":%t}`, stream)
						serve := f.openAI.Messages
						if route == "responses" {
							path = "/v1/responses"
							body = fmt.Sprintf(`{"model":"gpt-5","max_output_tokens":64,"input":"hi","stream":%t}`, stream)
							serve = f.openAI.Responses
						}
						rec := f.request(body, path, "", serve)
						require.Equal(t, 200, rec.Code, rec.Body.String())
						require.Contains(t, rec.Body.String(), "ok")
						client := cacheWriteHTTPWireUsage(rec.Body.String(), stream, route == "responses")
						require.True(t, client.IsObject(), rec.Body.String())
						require.EqualValues(t, tc.want, client.Get("cache_creation_input_tokens").Int())
						clientInput := prompt
						if route != "responses" {
							clientInput -= tc.want
						}
						require.EqualValues(t, clientInput, client.Get("input_tokens").Int())
						require.EqualValues(t, completion, client.Get("output_tokens").Int())
						f.pool.Stop()
						var logs, dedup, input, output, creation int
						var cost, balance float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),COALESCE(sum(cache_creation_tokens),0),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &input, &output, &creation, &cost))
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
						require.Equal(t, 1, logs)
						require.Equal(t, 1, dedup)
						require.Equal(t, prompt-tc.want, input)
						require.Equal(t, completion, output)
						require.Equal(t, tc.want, creation)
						wantCost := float64(prompt-tc.want)*.001 + float64(completion)*.002 + float64(tc.want)*.0005
						require.InDelta(t, wantCost, cost, 1e-9, "independent published channel prices")
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
					})
				}
			}
		}
	}
}
