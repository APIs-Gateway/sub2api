//go:build integration

package repository

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type oauthWebSearchHTTPProvider struct {
	client *http.Client
	target *url.URL
}

// Sends the production-built request through a real socket while retaining
// its path, body and OAuth headers. Funding uses isolated actual PG/Redis.
func (u *oauthWebSearchHTTPProvider) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	next := req.Clone(req.Context())
	endpoint := *req.URL
	endpoint.Scheme, endpoint.Host = u.target.Scheme, u.target.Host
	next.URL = &endpoint
	next.Host = u.target.Host
	return u.client.Do(next)
}

func (u *oauthWebSearchHTTPProvider) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func newOAuthWebSearchHTTPFixture(t *testing.T, upstream *oauthWebSearchHTTPProvider, passthrough bool) *inflightHTTPFixture {
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
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Platform: platform, Type: service.AccountTypeOAuth, Concurrency: 100, Credentials: map[string]any{"access_token": "fixture-token", "chatgpt_account_id": "fixture-account"}, Extra: map[string]any{"privacy_mode": service.PrivacyModeTrainingOff, "openai_responses_supported": true, "openai_passthrough": passthrough}})
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
	channel := &service.Channel{Name: "oauth-search-" + uuid.NewString(), Status: service.StatusActive, BillingModelSource: service.BillingModelSourceUpstream, GroupIDs: []int64{group.ID}, ModelPricing: []service.ChannelModelPricing{{Platform: platform, Models: []string{"gpt-5.4"}, BillingMode: service.BillingModeToken, InputPrice: &inputPrice, OutputPrice: &outputPrice, CacheReadPrice: &readPrice, CacheWritePrice: &writePrice}}}
	channelRepo := NewChannelRepository(inflightTestDB(t))
	require.NoError(t, channelRepo.Create(context.Background(), channel))
	channels := service.NewChannelService(channelRepo, groups, nil, nil, nil)
	rateLimit := service.NewRateLimitService(accounts, nil, cfg, nil, nil)
	usage := NewUsageLogRepository(client, inflightTestDB(t))
	atomicBilling := NewUsageBillingRepository(client, inflightTestDB(t))
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
	fixture := &inflightHTTPFixture{rdb: rdb, cfg: cfg, account: account, accounts: accounts, accountID: account.ID, gatewayService: gatewaySvc, geminiService: gemini, rateLimit: rateLimit, settings: settings, user: user, key: key, pool: pool, openAIService: openAISvc,
		gateway: userhandler.NewGatewayHandler(gatewaySvc, gemini, nil, nil, nil, concurrency, billingCache, nil, nil, pool, nil, nil, nil, cfg, nil, openAISvc),
		openAI:  userhandler.NewOpenAIGatewayHandler(openAISvc, concurrency, billingCache, keyService, pool, nil, nil, nil, cfg)}
	t.Cleanup(func() {
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

func TestOAuthWebSearchHistoryHTTP_ActualWalletCardSettlement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		for _, lite := range []bool{false, true} {
			for _, card := range []bool{false, true} {
				for _, namespace := range []bool{false, true} {
					t.Run(fmt.Sprintf("passthrough_%t/lite_%t/card_%t/namespace_%t", passthrough, lite, card, namespace), func(t *testing.T) {
						captures := make(chan []byte, 4)
						provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							body, err := io.ReadAll(r.Body)
							if err != nil {
								w.WriteHeader(http.StatusBadRequest)
								return
							}
							select {
							case captures <- body:
							default:
								w.WriteHeader(http.StatusInternalServerError)
								return
							}
							search := gjson.GetBytes(body, `tools.#(type=="web_search")`)
							if lite {
								search = gjson.GetBytes(body, `input.#(type=="additional_tools").tools.#(type=="web_search")`)
							}
							if !search.Exists() || !search.Get("external_web_access").Exists() || search.Get("external_web_access").Bool() || r.Header.Get("Authorization") != "Bearer fixture-token" || r.URL.Path != "/backend-api/codex/responses" {
								w.WriteHeader(http.StatusBadRequest)
								_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"response protection is unavailable"}}`)
								return
							}
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"resp_funded_search","model":"gpt-5.4","status":"completed","output":[{"type":"compaction","encrypted_content":"sealed_funded_history"},{"type":"message","id":"msg_funded","role":"assistant","content":[{"type":"output_text","text":"compacted"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`+"\n\n")
						}))
						t.Cleanup(provider.Close)
						target, err := url.Parse(provider.URL)
						require.NoError(t, err)
						f := newOAuthWebSearchHTTPFixture(t, &oauthWebSearchHTTPProvider{client: &http.Client{Timeout: 5 * time.Second}, target: target}, passthrough)
						if card {
							admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 10, 20, 30, 0, 0, 0)
						}
						tools, choice := `[]`, `"auto"`
						if namespace {
							tools = `[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]}]`
							choice = `{"type":"namespace","name":"collaboration"}`
						}
						body := fmt.Sprintf(`{"model":"gpt-5.4","instructions":"summarize","stream":false,"input":[{"type":"web_search_call","id":"ws_funded","status":"completed","unknown":9007199254740993},{"type":"compaction_trigger"}],"tools":%s,"tool_choice":%s,"parallel_tool_calls":false,"custom_extension":{"large":123456789012345678901234567890}}`, tools, choice)
						rec := f.request(body, "/v1/responses", "", func(c *gin.Context) {
							c.Request.Header.Set("User-Agent", "codex_cli_rs/0.159.0")
							if lite {
								c.Request.Header.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
							}
							f.openAI.Responses(c)
						})
						require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
						require.Contains(t, rec.Body.String(), "sealed_funded_history")
						f.pool.Stop()
						var wire []byte
						select {
						case wire = <-captures:
						case <-time.After(time.Second):
							t.Fatal("actual provider received no request")
						}
						require.Empty(t, captures, "no duplicate upstream call")
						items := gjson.GetBytes(wire, "input").Array()
						require.GreaterOrEqual(t, len(items), 2)
						require.Equal(t, "compaction_trigger", items[len(items)-1].Get("type").String())
						require.Equal(t, "9007199254740993", items[0].Get("unknown").Raw)
						require.Equal(t, "123456789012345678901234567890", gjson.GetBytes(wire, "custom_extension.large").Raw)
						if namespace {
							require.JSONEq(t, choice, gjson.GetBytes(wire, "tool_choice").Raw)
						} else {
							require.Equal(t, "none", gjson.GetBytes(wire, "tool_choice").String())
						}
						var logs, dedup, input, output int
						var cost, balance float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &input, &output, &cost))
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
						require.Equal(t, 1, logs)
						require.Equal(t, 1, dedup)
						require.Equal(t, 10, input)
						require.Equal(t, 5, output)
						const wantCost = 10*.001 + 5*.002
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
						require.Zero(t, inflightHeld(t, f.user.ID))
					})
				}
			}
		}
	}
}
