//go:build integration

package repository

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// This observer delegates every operation to the real PostgreSQL repository.
// Embedding the concrete repository also retains the actual inflight reservation
// methods; no billing, funding, deduplication or worker result is mocked.
type geminiCacheUsageBillingRecorder struct {
	*usageBillingRepository
	mu sync.Mutex
	commands []service.UsageBillingCommand
	applied []bool
}

func (r *geminiCacheUsageBillingRecorder) Apply(ctx context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	result, err := r.usageBillingRepository.Apply(ctx, cmd)
	if err == nil {
		r.mu.Lock()
		r.commands = append(r.commands, *cmd)
		r.applied = append(r.applied, result.Applied)
		r.mu.Unlock()
	}
	return result, err
}

// The fixture follows #1594's actual handler/worker/channel-pricing setup, with
// a Gemini API-key account (not the Antigravity OAuth routing branch). Its own
// migrated database and Redis namespace are cleaned by the existing harness.
func newGeminiCacheUsageFixture(t *testing.T, response, contentType string) (*inflightHTTPFixture, *geminiCacheUsageBillingRecorder) {
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
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@inflight-http.test", PasswordHash: "hash", Balance: 10, Concurrency: 100})
	group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: service.PlatformGemini, RateMultiplier: 1})
	group.Hydrated = true
	group.AllowImageGeneration = true
	group.AllowMessagesDispatch = true
	imagePrice := .5
	group.ImagePrice1K = &imagePrice
	group.ImagePrice2K = &imagePrice
	group.ImagePrice4K = &imagePrice
	_, err := inflightTestDB(t).Exec(`UPDATE groups SET allow_image_generation=true, allow_messages_dispatch=true, image_price_1k=.5,image_price_2k=.5,image_price_4k=.5 WHERE id=$1`, group.ID)
	require.NoError(t, err)
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: "sk-" + uuid.NewString(), Name: "inflight-http"})
	key.User = user
	key.Group = group
	schedulerCache := NewSchedulerCache(rdb)
	accounts := NewAccountRepository(client, inflightTestDB(t), schedulerCache)
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Concurrency: 100, Credentials: map[string]any{"api_key": "local-fixture"}, Extra: map[string]any{"privacy_mode": service.PrivacyModeTrainingOff, "openai_responses_supported": true}})
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
	atomicBilling := &geminiCacheUsageBillingRecorder{usageBillingRepository: NewUsageBillingRepository(client, inflightTestDB(t)).(*usageBillingRepository)}
	price := .01
	channel := &service.Channel{Name: "gemini-cache-" + uuid.NewString(), Status: service.StatusActive, BillingModelSource: service.BillingModelSourceUpstream, GroupIDs: []int64{group.ID}, ModelPricing: []service.ChannelModelPricing{
		{Platform: service.PlatformGemini, Models: []string{"gemini-2.5-flash"}, BillingMode: service.BillingModeToken, InputPrice: &price, OutputPrice: &price, CacheReadPrice: &price, CacheWritePrice: &price},
	}}
	channelRepo := NewChannelRepository(inflightTestDB(t))
	require.NoError(t, channelRepo.Create(context.Background(), channel))
	channels := service.NewChannelService(channelRepo, groups, nil, nil, nil)

	upstream := &inflightHTTPUpstream{started: make(chan struct{}), release: make(chan struct{}), response: response, contentType: contentType}
	wheel, err := service.NewTimingWheelService()
	require.NoError(t, err)
	t.Cleanup(wheel.Stop)
	deferred := service.NewDeferredService(accounts, wheel, time.Minute)
	t.Cleanup(deferred.Stop)
	gatewaySvc := service.NewGatewayService(accounts, groups, usage, atomicBilling, users, subs, rates, cache, cfg, snapshot, concurrency, billing, rateLimit, billingCache, nil, upstream, deferred, nil, nil, nil, nil, settings, nil, channels, service.NewModelPricingResolver(channels, billing), nil, nil)
	openAISvc := service.NewOpenAIGatewayService(accounts, usage, atomicBilling, users, subs, rates, cache, cfg, snapshot, concurrency, billing, rateLimit, billingCache, upstream, deferred, nil, nil, nil, nil, nil, nil, nil, nil, groups)
	t.Cleanup(openAISvc.CloseOpenAIWSPool)
	keyService := service.NewAPIKeyService(NewAPIKeyRepository(client, inflightTestDB(t)), users, groups, subs, rates, nil, cfg)
	gemini := service.NewGeminiMessagesCompatService(accounts, groups, cache, snapshot, nil, rateLimit, upstream, nil, cfg)
	options := service.UsageRecordWorkerPoolOptions{WorkerCount: 1, QueueSize: 16, TaskTimeout: 5 * time.Second}
	pool := service.NewUsageRecordWorkerPoolWithOptions(options)
	t.Cleanup(pool.Stop)
	fixture := &inflightHTTPFixture{user: user, key: key, pool: pool, upstream: upstream, openAIService: openAISvc, account: account,
		gateway: userhandler.NewGatewayHandler(gatewaySvc, gemini, nil, nil, nil, concurrency, billingCache, nil, nil, pool, nil, nil, nil, cfg, nil, openAISvc),
		openAI: userhandler.NewOpenAIGatewayHandler(openAISvc, concurrency, billingCache, keyService, pool, nil, nil, nil, cfg)}
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
	return fixture, atomicBilling
}

func TestGeminiCacheUsageHTTP_CacheOnlySettlesOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"native_blocked", "claude", "chat"} {
		for _, stream := range []bool{false, true} {
			for _, card := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream_%t/card_%t", route, stream, card), func(t *testing.T) {
					payload := `{"candidates":[{"content":{"parts":[{"text":"cache response"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"cachedContentTokenCount":3}}`
					if route == "native_blocked" {
						payload = `{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"cachedContentTokenCount":3}}`
					}
					response, contentType := payload, "application/json"
					if stream {
						response, contentType = "data: " + payload + "\n\n", "text/event-stream"
					}
					f, recorder := newGeminiCacheUsageFixture(t, response, contentType)
					if card {
						admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 1, 10, 20, 0, 0, 0)
					}
					close(f.upstream.release)
					body := fmt.Sprintf(`{"model":"gemini-2.5-flash","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"stream":%t,"stream_options":{"include_usage":true}}`, stream)
					path, action, serve := "/v1/messages", "", f.gateway.Messages
					if route == "chat" {
						path, serve = "/v1/chat/completions", f.gateway.ChatCompletions
					}
					if route == "native_blocked" {
						body = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":8}}`
						method := "generateContent"
						if stream {
							method = "streamGenerateContent"
						}
						path, action, serve = "/v1beta/models/gemini-2.5-flash:" + method, "/gemini-2.5-flash:" + method, f.gateway.GeminiV1BetaModels
					}
					rec := f.request(body, path, action, serve)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					if route == "native_blocked" {
						require.Equal(t, response, rec.Body.String())
					} else {
						require.Contains(t, rec.Body.String(), "cache response", "substantive content must cross the completion guard")
					}
					f.pool.Stop()
					require.EqualValues(t, 1, f.upstream.calls.Load(), "metered direct Gemini must not replay")
					assertState := func() {
						t.Helper()
						var logs, dedup, input, output, cache int
						var cost, balance, inputCost, cacheCost, totalCost float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),COALESCE(sum(cache_read_tokens),0),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &input, &output, &cache, &cost))
						require.Equal(t, 1, logs)
						require.Zero(t, input, "GEMINI_CACHE_USAGE_NORMALIZATION")
						require.Zero(t, output)
						require.Equal(t, 3, cache)
						require.InDelta(t, .03, cost, 1e-8, "complete cache reads retain their existing price")
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT input_cost,cache_read_cost,total_cost FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&inputCost, &cacheCost, &totalCost))
						require.Zero(t, inputCost)
						require.InDelta(t, .03, cacheCost, 1e-8)
						require.InDelta(t, .03, totalCost, 1e-8)
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
						require.Equal(t, 1, dedup)
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
						if card {
							var daily, weekly, monthly float64
							require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.user.ID).Scan(&daily, &weekly, &monthly))
							require.InDelta(t, .03, daily, 1e-8)
							require.InDelta(t, .03, weekly, 1e-8)
							require.InDelta(t, .03, monthly, 1e-8)
							require.InDelta(t, 10, balance, 1e-8)
						} else {
							require.InDelta(t, 9.97, balance, 1e-8)
						}
						require.Zero(t, inflightHeld(t, f.user.ID))
					}
					assertState()
					recorder.mu.Lock()
					commands := append([]service.UsageBillingCommand(nil), recorder.commands...)
					applied := append([]bool(nil), recorder.applied...)
					recorder.mu.Unlock()
					require.Len(t, commands, 1, "the actual worker must call the atomic PostgreSQL repository")
					require.Equal(t, []bool{true}, applied)
					require.Zero(t, commands[0].InputTokens)
					require.Equal(t, 3, commands[0].CacheReadTokens)
					for i := 0; i < 2; i++ {
						result, err := recorder.usageBillingRepository.Apply(context.Background(), &commands[0])
						require.NoError(t, err)
						require.False(t, result.Applied, "the exact real handler settlement command must be idempotent")
						assertState()
					}
				})
			}
		}
	}
}
