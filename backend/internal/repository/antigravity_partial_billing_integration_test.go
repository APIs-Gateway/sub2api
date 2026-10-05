//go:build integration

package repository

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type agMeteredBillingFixture struct {
	*inflightHTTPFixture
	provider *agMeteredBillingTransport
}

type agMeteredBillingTransport struct {
	*inflightHTTPUpstream
	cancel context.CancelFunc
}

func (u *agMeteredBillingTransport) Do(req *http.Request, proxy string, id int64, slots int) (*http.Response, error) {
	resp, err := u.inflightHTTPUpstream.Do(req, proxy, id, slots)
	if err == nil && u.cancel != nil {
		resp.Body = &agMeteredCancelBody{ReadCloser: resp.Body, cancel: u.cancel}
	}
	return resp, err
}

func (u *agMeteredBillingTransport) DoWithTLS(req *http.Request, proxy string, id int64, slots int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, slots)
}

type agMeteredCancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	read   bool
}

func (r *agMeteredCancelBody) Read(p []byte) (int, error) {
	if r.read {
		return 0, context.Canceled
	}
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.read = true
		r.cancel()
	}
	return n, err
}

func newAGMeteredBillingFixture(t *testing.T, response string) *agMeteredBillingFixture {
	t.Helper()
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
	group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: service.PlatformAntigravity, RateMultiplier: 1})
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
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth, Concurrency: 100, Credentials: map[string]any{"access_token": "local-fixture", "project_id": "local-project", "pool_mode": true, "pool_mode_retry_count": 0, "model_mapping": map[string]any{"claude-sonnet-4-5": "claude-sonnet-4-5", "gemini-2.5-flash": "gemini-2.5-flash", "gemini-3.1-flash-image": "gemini-3.1-flash-image"}}, Extra: map[string]any{"privacy_mode": service.PrivacyModeTrainingOff, "openai_responses_supported": true}})
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
	price := .01
	channel := &service.Channel{Name: "ag-metered-" + uuid.NewString(), Status: service.StatusActive, BillingModelSource: service.BillingModelSourceUpstream, GroupIDs: []int64{group.ID}, ModelPricing: []service.ChannelModelPricing{
		{Platform: service.PlatformAntigravity, Models: []string{"claude-sonnet-4-5", "gemini-2.5-flash"}, BillingMode: service.BillingModeToken, InputPrice: &price, OutputPrice: &price, CacheReadPrice: &price, CacheWritePrice: &price},
	}}
	channelRepo := NewChannelRepository(inflightTestDB(t))
	require.NoError(t, channelRepo.Create(context.Background(), channel))
	channels := service.NewChannelService(channelRepo, groups, nil, nil, nil)

	upstream := &inflightHTTPUpstream{started: make(chan struct{}), release: make(chan struct{}), response: response, contentType: "text/event-stream"}
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
	provider := &agMeteredBillingTransport{inflightHTTPUpstream: upstream}
	ag := service.NewAntigravityGatewayService(accounts, cache, snapshot, service.NewAntigravityTokenProvider(accounts, nil, nil), rateLimit, provider, settings, nil)
	fixture := &inflightHTTPFixture{user: user, key: key, pool: pool, upstream: upstream, openAIService: openAISvc,
		gateway: userhandler.NewGatewayHandler(gatewaySvc, gemini, ag, nil, nil, concurrency, billingCache, nil, nil, pool, nil, nil, nil, cfg, nil, openAISvc),
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
	return &agMeteredBillingFixture{inflightHTTPFixture: fixture, provider: provider}
}
func TestAntigravityPartialBilling_RealHandlerSettlesOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, native := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, card := range []bool{false, true} {
				for _, failure := range []string{"empty", "read_error", "provider_error"} {
					name := map[bool]string{false: "claude", true: "gemini"}[native] + map[bool]string{false: "_buffered", true: "_stream"}[stream] + map[bool]string{false: "_wallet", true: "_card"}[card] + "/" + failure
					t.Run(name, func(t *testing.T) {
						payload := `{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":3,"candidatesTokenCount":2,"thoughtsTokenCount":4}}}`
						if failure == "read_error" {
							payload = `{"response":{"candidates":[{"content":{"parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":3,"candidatesTokenCount":2,"thoughtsTokenCount":4}}}`
						}
						response := "data: " + payload + "\n\n"
						if failure == "provider_error" {
							response += "data: " + `{"response":{"error":{"code":403,"status":"PERMISSION_DENIED","message":"projects/private account@pool"}}}` + "\n\n"
						}
						f := newAGMeteredBillingFixture(t, response)
						if failure == "read_error" {
							f.upstream.readErr = errors.New("provider interrupted after metering")
						}
						if card {
							admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 1, 10, 20, 0, 0, 0)
						}
						close(f.upstream.release)
						body := `{"model":"claude-sonnet-4-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"stream":` + map[bool]string{false: "false", true: "true"}[stream] + `}`
						path, action := "/v1/messages", ""
						serve := f.gateway.Messages
						if native {
							body = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":8}}`
							method := map[bool]string{false: "generateContent", true: "streamGenerateContent"}[stream]
							path = "/v1beta/models/gemini-2.5-flash:" + method
							action = "/gemini-2.5-flash:" + method
							serve = func(c *gin.Context) {
								c.Set(string(middleware.ContextKeyForcePlatform), service.PlatformAntigravity)
								f.gateway.GeminiV1BetaModels(c)
							}
						}
						rec := f.request(body, path, action, serve)
						require.NotContains(t, rec.Body.String(), "message_stop")
						require.NotContains(t, rec.Body.String(), "private")
						f.pool.Stop()
						require.EqualValues(t, 1, f.upstream.calls.Load(), "metered work must not replay")
						var logs, dedup, input, output, cache int
						var cost, balance float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),COALESCE(sum(cache_read_tokens),0),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &input, &output, &cache, &cost))
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
						require.Equal(t, 1, logs)
						require.Equal(t, 1, dedup)
						require.Equal(t, 7, input)
						require.Equal(t, 6, output)
						require.Equal(t, 3, cache)
						require.InDelta(t, .16, cost, 1e-8)
						if card {
							var daily, weekly, monthly float64
							require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.user.ID).Scan(&daily, &weekly, &monthly))
							require.InDelta(t, cost, daily, 1e-8)
							require.InDelta(t, cost, weekly, 1e-8)
							require.InDelta(t, cost, monthly, 1e-8)
							require.InDelta(t, 10, balance, 1e-8)
						} else {
							require.InDelta(t, 10-cost, balance, 1e-8)
						}
						require.Zero(t, inflightHeld(t, f.user.ID), "immutable actual obligation settles and consumes the attempt estimate")
					})
				}
			}
		}
	}
}

func TestAntigravityPartialBilling_ObservedImageUsesImagePrice(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, card := range []bool{false, true} {
			for _, kind := range []string{"image_tokens", "image_only", "text_no_image", "cancel_image_tokens", "cancel_image_only", "cancel_text_no_image"} {
				t.Run(map[bool]string{false: "buffered", true: "stream"}[stream]+map[bool]string{false: "_wallet", true: "_card"}[card]+"/"+kind, func(t *testing.T) {
					parts := `[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]`
					usage := `,"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2}`
					if strings.HasSuffix(kind, "image_only") {
						usage = ""
					}
					if strings.HasSuffix(kind, "text_no_image") {
						parts = `[{"text":"not an image"}]`
					}
					response := "data: " + `{"response":{"candidates":[{"content":{"parts":` + parts + `}}]` + usage + `}}` + "\n\n"
					f := newAGMeteredBillingFixture(t, response)
					f.upstream.readErr = errors.New("upstream read interrupted after partial image")
					if card {
						admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 1, 10, 20, 0, 0, 0)
					}
					close(f.upstream.release)
					method := map[bool]string{false: "generateContent", true: "streamGenerateContent"}[stream]
					body := `{"contents":[{"role":"user","parts":[{"text":"draw"}]}],"generationConfig":{"maxOutputTokens":8}}`
					var clientContext context.Context
					rec := f.request(body, "/v1beta/models/gemini-3.1-flash-image:"+method, "/gemini-3.1-flash-image:"+method, func(c *gin.Context) {
						if strings.HasPrefix(kind, "cancel_") {
							ctx, cancel := context.WithCancel(c.Request.Context())
							defer cancel()
							c.Request = c.Request.WithContext(ctx)
							clientContext = ctx
							f.provider.cancel = cancel
						}
						c.Set(string(middleware.ContextKeyForcePlatform), service.PlatformAntigravity)
						f.gateway.GeminiV1BetaModels(c)
					})
					require.NotContains(t, rec.Body.String(), "message_stop")
					if strings.HasPrefix(kind, "cancel_") {
						require.ErrorIs(t, clientContext.Err(), context.Canceled, "provider read must cancel the actual handler context")
					}
					f.pool.Stop()
					require.EqualValues(t, 1, f.upstream.calls.Load())
					var logs, images, dedup int
					var cost, balance float64
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(image_count),0),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &images, &cost))
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
					require.Equal(t, 1, logs)
					require.Equal(t, 1, dedup)
					if !strings.HasSuffix(kind, "text_no_image") {
						require.Equal(t, 1, images)
						require.InDelta(t, .5, cost, 1e-8)
					} else {
						require.Zero(t, images)
						require.NotEqual(t, .5, cost, "an error with text/token evidence must not synthesize a generated image")
					}
					if card {
						var daily, weekly, monthly float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.user.ID).Scan(&daily, &weekly, &monthly))
						require.InDelta(t, cost, daily, 1e-8)
						require.InDelta(t, cost, weekly, 1e-8)
						require.InDelta(t, cost, monthly, 1e-8)
						require.InDelta(t, 10, balance, 1e-8)
					} else {
						require.InDelta(t, 10-cost, balance, 1e-8)
					}
					require.Zero(t, inflightHeld(t, f.user.ID))
				})
			}
		}
	}
}

func TestAntigravityPartialBilling_UnmeteredUnknownKeepsBoundedHold(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(map[bool]string{false: "claude", true: "gemini"}[native]+map[bool]string{false: "_buffered", true: "_stream"}[stream], func(t *testing.T) {
				f := newAGMeteredBillingFixture(t, "")
				f.upstream.readErr = errors.New("unknown transport error before usage")
				close(f.upstream.release)
				body := `{"model":"claude-sonnet-4-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"stream":` + map[bool]string{false: "false", true: "true"}[stream] + `}`
				path, action := "/v1/messages", ""
				serve := f.gateway.Messages
				if native {
					body = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":8}}`
					method := map[bool]string{false: "generateContent", true: "streamGenerateContent"}[stream]
					path = "/v1beta/models/gemini-2.5-flash:" + method
					action = "/gemini-2.5-flash:" + method
					serve = func(c *gin.Context) {
						c.Set(string(middleware.ContextKeyForcePlatform), service.PlatformAntigravity)
						f.gateway.GeminiV1BetaModels(c)
					}
				}
				rec := f.request(body, path, action, serve)
				require.Contains(t, rec.Body.String(), "error", "unknown interrupted response must not be an empty successful body")
				f.pool.Stop()
				require.EqualValues(t, 1, f.upstream.calls.Load())
				var logs, dedup int
				var balance float64
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
				require.Zero(t, logs)
				require.Zero(t, dedup)
				require.InDelta(t, 10, balance, 1e-8)
				require.Positive(t, inflightHeld(t, f.user.ID), "transport failure is not no-charge proof")
				var maxSeconds float64
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT EXTRACT(EPOCH FROM max(expires_at)-clock_timestamp()) FROM billing_inflight_leases WHERE user_id=$1 AND phase='attempt'`, f.user.ID).Scan(&maxSeconds))
				require.Positive(t, maxSeconds)
				require.LessOrEqual(t, maxSeconds, 900.0)
				_, err := inflightTestDB(t).Exec(`UPDATE billing_inflight_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE user_id=$1`, f.user.ID)
				require.NoError(t, err)
				require.Zero(t, inflightHeld(t, f.user.ID), "unknown exposure is bounded by the existing lease TTL")
			})
		}
	}
}
