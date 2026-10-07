//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	redisclient "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func clientPolicyFundedAccount(t *testing.T, f *inflightHTTPFixture, restricted bool, priority int) *service.Account {
	t.Helper()
	a := mustCreateAccount(t, inflightTestEntClient(t), &service.Account{
		Name: uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Concurrency: 10, Priority: priority,
		Credentials: map[string]any{"api_key": "local-fixture", "base_url": "https://upstream.test", "pool_mode": true, "pool_mode_retry_count": 0},
		Extra:       map[string]any{"openai_responses_supported": true, "privacy_mode": service.PrivacyModeTrainingOff},
	})
	if restricted {
		a.Type = service.AccountTypeOAuth
		a.Credentials["access_token"] = "local-fixture-oauth"
		a.Extra["codex_cli_only"] = true
		require.NoError(t, f.accounts.Update(context.Background(), a))
	}
	require.NoError(t, f.accounts.BindGroups(context.Background(), a.ID, []int64{*f.key.GroupID}))
	return a
}

func TestClientRestrictedSelectionHTTP_AlreadyServedStableGroup(t *testing.T) {
	for _, allRestricted := range []bool{false, true} {
		for _, card := range []bool{false, true} {
			t.Run(fmt.Sprintf("allRestricted=%t/card=%t", allRestricted, card), func(t *testing.T) {
				f := newInflightHTTPFixtureForClientPolicy(t, service.PlatformOpenAI, inflightResponsesSSE, "text/event-stream", true)
				clientPolicyFunding(t, f, card)
				ctx := context.Background()
				original, err := f.accounts.GetByID(ctx, f.accountID)
				require.NoError(t, err)
				original.Priority = 10
				if allRestricted {
					original.Type = service.AccountTypeOAuth
					original.Credentials["access_token"] = "local-fixture-oauth"
					original.Extra["codex_cli_only"] = true
				}
				require.NoError(t, f.accounts.Update(ctx, original))
				require.NoError(t, f.accounts.BindGroups(ctx, original.ID, []int64{f.stableGroup.ID}))
				restricted := clientPolicyFundedAccount(t, f, true, -10)
				require.NoError(t, f.accounts.BindGroups(ctx, restricted.ID, []int64{f.stableGroup.ID}))
				// This higher tier is independently schedulable, but local policy
				// cannot make the already chosen served group climb into it.
				higher := clientPolicyFundedAccount(t, f, false, 1)
				require.NoError(t, f.accounts.BindGroups(ctx, higher.ID, []int64{f.stableNext.ID}))
				var mu sync.Mutex
				var calls []int64
				var models []string
				f.upstream.script = func(req *http.Request, id int64) (*http.Response, error) {
					var payload struct {
						Model string `json:"model"`
					}
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						return nil, err
					}
					mu.Lock()
					calls = append(calls, id)
					models = append(models, payload.Model)
					mu.Unlock()
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(strings.ReplaceAll(inflightResponsesSSE, "gpt-5", "gpt-5.1"))), Request: req}, nil
				}
				before := userBalance(t, f.user.ID)
				close(f.upstream.release)
				rec := f.request(`{"model":"gpt-5","max_completion_tokens":8,"messages":[{"role":"user","content":"hello"}]}`, "/v1/chat/completions", "", f.openAI.ChatCompletions)
				f.pool.Stop()
				mu.Lock()
				actualCalls := append([]int64(nil), calls...)
				mu.Unlock()
				t.Logf("actual stable served-group status=%d provider accounts=%v body=%s", rec.Code, actualCalls, rec.Body.String())
				if allRestricted {
					require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
					require.Empty(t, actualCalls)
					require.Zero(t, inflightHeld(t, f.user.ID))
					require.InDelta(t, before, userBalance(t, f.user.ID), 1e-10)
					var logs int
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
					require.Zero(t, logs)
				} else {
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Equal(t, []int64{original.ID}, actualCalls)
					require.Equal(t, []string{"gpt-5.1"}, models, "the served group's channel mapping must survive policy-only reselection")
					row := latestServedBillingRow(t, f.key.ID, 1)
					require.Equal(t, *f.key.GroupID, row.groupID.Int64)
					require.InDelta(t, 2, row.rate, 1e-9, "the actual already-selected served tier retains its rate")
					require.Positive(t, row.actualCost)
					require.InDelta(t, .2, row.totalCost, 1e-9)
					require.InDelta(t, .4, row.actualCost, 1e-9, "real PG bills the served channel price at the served tier rate, not home")
					require.Zero(t, inflightHeld(t, f.user.ID))
					if !card {
						require.InDelta(t, row.actualCost, before-userBalance(t, f.user.ID), 1e-8)
					}
					if card {
						var d, w, m float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.user.ID).Scan(&d, &w, &m))
						require.InDelta(t, row.actualCost, d, 1e-8)
						require.InDelta(t, row.actualCost, w, 1e-8)
						require.InDelta(t, row.actualCost, m, 1e-8)
					}
				}
			})
		}
	}
}

func clientPolicyFunding(t *testing.T, f *inflightHTTPFixture, card bool) {
	t.Helper()
	balance := 100.0
	if card {
		balance = 0
	}
	_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=$1 WHERE id=$2`, balance, f.user.ID)
	require.NoError(t, err)
	f.user.Balance = balance
	if card {
		admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 10, 100, 1000, 0, 0, 0)
	}
	require.NoError(t, NewBillingCache(f.rdb).InvalidateUserBalance(context.Background(), f.user.ID))
}

func TestClientRestrictedSelectionHTTP_FundingAndOriginalError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"responses", "chat"} {
		for _, card := range []bool{false, true} {
			for _, scenario := range []string{"mixed", "all_restricted", "compatible_control", "prior_held_control", "upstream_failure_then_local_rejection"} {
				t.Run(fmt.Sprintf("%s/card=%t/%s", route, card, scenario), func(t *testing.T) {
					f := newInflightHTTPFixture(t, service.PlatformOpenAI, inflightResponsesSSE, "text/event-stream")
					clientPolicyFunding(t, f, card)
					// The original fixture account is compatible and deliberately
					// lower-priority than the policy-restricted selected candidate.
					original, err := f.accounts.GetByID(context.Background(), f.accountID)
					require.NoError(t, err)
					original.Priority = 10
					require.NoError(t, f.accounts.Update(context.Background(), original))
					restricted := clientPolicyFundedAccount(t, f, true, -10)
					if scenario == "all_restricted" || scenario == "prior_held_control" {
						original.Type = service.AccountTypeOAuth
						original.Credentials["access_token"] = "local-fixture-oauth"
						original.Extra["codex_cli_only"] = true
						require.NoError(t, f.accounts.Update(context.Background(), original))
					}
					if scenario == "compatible_control" {
						restricted.Extra["codex_cli_only"] = false
						// API key control avoids introducing OAuth protocol behavior.
						restricted.Type = service.AccountTypeAPIKey
						require.NoError(t, f.accounts.Update(context.Background(), restricted))
					}
					if scenario == "upstream_failure_then_local_rejection" {
						original.Priority = -20
						require.NoError(t, f.accounts.Update(context.Background(), original))
					}
					ctx := context.Background()
					beforeWallet := userBalance(t, f.user.ID)
					priorHeld := 0.0
					if scenario == "prior_held_control" {
						repo := NewUsageBillingRepository(inflightTestEntClient(t), inflightTestDB(t)).(service.BillingInflightRepository)
						ok, err := repo.ReserveBillingInflight(ctx, f.user.ID, uuid.NewString(), .25, false, time.Minute)
						require.NoError(t, err)
						require.True(t, ok)
						priorHeld = inflightHeld(t, f.user.ID)
						require.InDelta(t, .25, priorHeld, 1e-8)
					}
					var mu sync.Mutex
					var called []int64
					f.upstream.script = func(req *http.Request, accountID int64) (*http.Response, error) {
						mu.Lock()
						called = append(called, accountID)
						mu.Unlock()
						if scenario == "upstream_failure_then_local_rejection" {
							return &http.Response{StatusCode: 520, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader("<html>unknown provider execution</html>")), Request: req}, nil
						}
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(inflightResponsesSSE)), Request: req}, nil
					}
					close(f.upstream.release)
					body, path, serve := `{"model":"gpt-5","max_output_tokens":8,"input":"hello"}`, "/v1/responses", f.openAI.Responses
					if route == "chat" {
						body, path, serve = `{"model":"gpt-5","max_completion_tokens":8,"messages":[{"role":"user","content":"hello"}]}`, "/v1/chat/completions", f.openAI.ChatCompletions
					}
					rec := f.request(body, path, "", serve)
					f.pool.Stop()
					mu.Lock()
					actualCalls := append([]int64(nil), called...)
					mu.Unlock()
					t.Logf("actual original-client HTTP status=%d provider accounts=%v body=%s", rec.Code, actualCalls, rec.Body.String())
					var logs, dedup int
					var actualCost float64
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &actualCost))
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
					if scenario == "mixed" || scenario == "compatible_control" {
						require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
						wanted := original.ID
						if scenario == "compatible_control" {
							wanted = restricted.ID
						}
						require.Equal(t, []int64{wanted}, actualCalls)
						require.Equal(t, 1, logs)
						require.Equal(t, 1, dedup)
						require.Positive(t, actualCost)
						require.Zero(t, inflightHeld(t, f.user.ID))
						if !card {
							require.InDelta(t, actualCost, beforeWallet-userBalance(t, f.user.ID), 1e-8)
						}
					} else {
						require.Zero(t, logs)
						require.Zero(t, dedup)
						require.InDelta(t, beforeWallet, userBalance(t, f.user.ID), 1e-10)
						if scenario == "upstream_failure_then_local_rejection" {
							require.GreaterOrEqual(t, rec.Code, 500, "real upstream error must take precedence over subsequent local policy")
							require.Equal(t, []int64{original.ID}, actualCalls)
							require.Positive(t, inflightHeld(t, f.user.ID), "unknown dispatched attempt must retain its existing bounded funding")
						} else {
							require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
							require.Contains(t, rec.Body.String(), service.CodexOfficialClientsOnlyMessage)
							require.Empty(t, actualCalls)
							require.InDelta(t, priorHeld, inflightHeld(t, f.user.ID), 1e-8, "local-only rejection neither dispatches a new lease nor erases a prior held attempt")
						}
					}
					if card {
						var d, w, m float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.user.ID).Scan(&d, &w, &m))
						require.InDelta(t, actualCost, d, 1e-8)
						require.InDelta(t, actualCost, w, 1e-8)
						require.InDelta(t, actualCost, m, 1e-8)
					}
					cache := NewConcurrencyCache(f.rdb, 15, 30)
					for _, id := range []int64{original.ID, restricted.ID} {
						active, err := cache.GetAccountConcurrency(ctx, id)
						require.NoError(t, err)
						require.Zero(t, active, "actual Redis slot must be released")
						waiting, err := cache.GetAccountWaitingCount(ctx, id)
						require.NoError(t, err)
						require.Zero(t, waiting, "policy-incompatible account must not retain a waiting ticket")
					}
				})
			}
		}
	}
}

// This runs the public handler through the actual chain resolver and runner,
// with real PG route rows, funding and usage, and the real Redis breaker/slots.
func TestClientRestrictedSelectionHTTP_MixedFailureChain(t *testing.T) {
	for _, route := range []string{"responses", "chat"} {
		for _, policyFirst := range []bool{false, true} {
			for _, nextFails := range []bool{false, true} {
				for _, card := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/policyFirst=%t/nextFails=%t/card=%t", route, policyFirst, nextFails, card), func(t *testing.T) {
						f := newInflightHTTPFixtureForClientPolicy(t, service.PlatformOpenAI, inflightResponsesSSE, "text/event-stream", true)
						clientPolicyFunding(t, f, card)
						ctx := context.Background()
						const homeModel, servedModel = "gpt-5.4", "gpt-5.4-mini"
						homeFamily := service.ModelFamily(homeModel)
						require.NotEmpty(t, homeFamily, "the actual home-hop model must enter the strict breaker family table")
						require.NotEmpty(t, service.ModelFamily(servedModel), "the actual mapped served-hop model must enter the strict breaker family table")
						// Reconfigure only these new chain fixtures through the existing
						// repository before the fixture's channel cache is first read.
						channelRepo := NewChannelRepository(inflightTestDB(t))
						for _, gid := range []int64{*f.key.GroupID, f.stableGroup.ID} {
							id, err := channelRepo.GetChannelIDByGroupID(ctx, gid)
							require.NoError(t, err)
							channel, err := channelRepo.GetByID(ctx, id)
							require.NoError(t, err)
							price := .2
							channel.ModelPricing = []service.ChannelModelPricing{{Platform: service.PlatformOpenAI, Models: []string{homeModel, servedModel}, BillingMode: service.BillingModePerRequest, PerRequestPrice: &price}}
							channel.ModelMapping = nil
							if gid == f.stableGroup.ID {
								channel.ModelMapping = map[string]map[string]string{service.PlatformOpenAI: {homeModel: servedModel}}
							}
							require.NoError(t, channelRepo.Update(ctx, channel))
						}
						f.key.StablePriorityEnabled = false
						f.key.HasGroupRoutes = true
						settingRepo := NewSettingRepository(inflightTestEntClient(t))
						previous, previousErr := settingRepo.GetValue(ctx, service.SettingKeyGroupFallbackEnabled)
						require.NoError(t, settingRepo.Set(ctx, service.SettingKeyGroupFallbackEnabled, "true"))
						t.Cleanup(func() {
							if previousErr != nil {
								_ = settingRepo.Delete(ctx, service.SettingKeyGroupFallbackEnabled)
							} else {
								_ = settingRepo.Set(ctx, service.SettingKeyGroupFallbackEnabled, previous)
							}
						})
						settings := service.NewSettingService(settingRepo, f.cfg)
						_, err := inflightTestDB(t).Exec(`INSERT INTO api_key_group_routes (api_key_id,group_id,platform,source,placement,position) VALUES ($1,$2,'openai','user','tail',0)`, f.key.ID, f.stableGroup.ID)
						require.NoError(t, err)
						groups := NewGroupRepository(inflightTestEntClient(t), inflightTestDB(t))
						routes := service.NewGroupRouteService(NewAPIKeyGroupRouteRepository(inflightTestDB(t)), groups, settings)
						chain, err := routes.ResolveEffectiveChain(ctx, f.key, f.user, service.ResolveOptions{})
						require.NoError(t, err)
						require.Len(t, chain.Hops, 2, "the user chain must pass real group/platform/permission eligibility")
						users := NewUserRepository(inflightTestEntClient(t), inflightTestDB(t))
						subs := NewUserSubscriptionRepository(inflightTestEntClient(t))
						rates := NewUserGroupRateRepository(inflightTestDB(t))
						billingCache := service.NewBillingCacheService(NewBillingCache(f.rdb), users, subs, nil, nil, rates, f.cfg, nil, settings)
						t.Cleanup(billingCache.Stop)
						concurrency := service.NewConcurrencyService(NewConcurrencyCache(f.rdb, 15, 30))
						keyService := service.NewAPIKeyService(NewAPIKeyRepository(inflightTestEntClient(t), inflightTestDB(t)), users, groups, subs, rates, nil, f.cfg)
						f.openAI = userhandler.ProvideOpenAIGatewayHandler(f.openAIService, concurrency, billingCache, keyService, f.pool, nil, nil, nil, f.cfg, nil, routes, settings, NewGroupChainBreaker(f.rdb))
						a, err := f.accounts.GetByID(ctx, f.accountID)
						require.NoError(t, err)
						a.Priority = -20
						require.NoError(t, f.accounts.Update(ctx, a))
						priority := -10
						if policyFirst {
							priority = -30
						}
						b := clientPolicyFundedAccount(t, f, true, priority)
						next := clientPolicyFundedAccount(t, f, false, 1)
						require.NoError(t, f.accounts.BindGroups(ctx, next.ID, []int64{f.stableGroup.ID}))
						fundingRepo := NewUsageBillingRepository(inflightTestEntClient(t), inflightTestDB(t)).(service.BillingInflightRepository)
						priorID := uuid.NewString()
						ok, err := fundingRepo.ReserveBillingInflight(ctx, f.user.ID, priorID, .25, false, time.Minute)
						require.NoError(t, err)
						require.True(t, ok)
						beforeWallet := userBalance(t, f.user.ID)
						var calls []int64
						var heldAtNext float64
						f.upstream.script = func(req *http.Request, id int64) (*http.Response, error) {
							calls = append(calls, id)
							var payload struct {
								Model string `json:"model"`
							}
							if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
								return nil, err
							}
							wantedModel := homeModel
							if id == next.ID {
								wantedModel = servedModel
							}
							require.Equal(t, wantedModel, payload.Model, "the real provider dispatch must match the recognized hop model, including served mapping")
							if id == next.ID {
								heldAtNext = inflightHeld(t, f.user.ID)
							}
							if id == a.ID || nextFails {
								return &http.Response{StatusCode: 520, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader("<html>unknown dispatched attempt</html>")), Request: req}, nil
							}
							return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(strings.ReplaceAll(inflightResponsesSSE, "gpt-5", servedModel))), Request: req}, nil
						}
						close(f.upstream.release)
						body, path, serve := `{"model":"gpt-5.4","max_output_tokens":8,"input":"hello"}`, "/v1/responses", f.openAI.Responses
						if route == "chat" {
							body, path, serve = `{"model":"gpt-5.4","max_completion_tokens":8,"messages":[{"role":"user","content":"hello"}]}`, "/v1/chat/completions", f.openAI.ChatCompletions
						}
						rec := f.request(body, path, "", serve)
						f.pool.Stop()
						t.Logf("actual runtime chain status=%d calls=%v held-at-next=%v held-final=%v body=%s", rec.Code, calls, heldAtNext, inflightHeld(t, f.user.ID), rec.Body.String())
						require.Equal(t, []int64{a.ID, next.ID}, calls)
						require.Greater(t, heldAtNext, .25, "the real dispatched first attempt remains funded when the next hop begins")
						require.Greater(t, inflightHeld(t, f.user.ID), .25, "later success/failure cannot erase the prior unknown dispatched lease or the independent control")
						var priorAmount, unknownAmount float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT amount FROM billing_inflight_leases WHERE user_id=$1 AND id=$2 AND phase='attempt'`, f.user.ID, priorID+":initial").Scan(&priorAmount))
						require.InDelta(t, .25, priorAmount, 1e-9, "the independent prior control must survive exactly")
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT COALESCE(sum(amount),0) FROM billing_inflight_leases WHERE user_id=$1 AND owner_id<>$2 AND phase='attempt'`, f.user.ID, priorID).Scan(&unknownAmount))
						require.Positive(t, unknownAmount, "the real earlier dispatched unknown attempt is retained independently of the prior control")
						afterB, err := f.accounts.GetByID(ctx, b.ID)
						require.NoError(t, err)
						require.Equal(t, b.Status, afterB.Status)
						require.Equal(t, b.Schedulable, afterB.Schedulable)
						require.Equal(t, b.ErrorMessage, afterB.ErrorMessage)
						require.Equal(t, b.RateLimitedAt, afterB.RateLimitedAt)
						require.Equal(t, b.OverloadUntil, afterB.OverloadUntil)
						homeBreakerKey, _, _ := groupChainBreakerKeys(service.BreakerKey{Platform: service.PlatformOpenAI, GroupID: *f.key.GroupID, Family: homeFamily})
						breakerCount, err := f.rdb.HGet(ctx, homeBreakerKey, "fail_count").Int64()
						if err == redisclient.Nil {
							breakerCount = 0
						} else {
							require.NoError(t, err)
						}
						t.Logf("actual recognized home family=%s exact Redis key=%s fail_count=%d", homeFamily, homeBreakerKey, breakerCount)
						require.Zero(t, breakerCount, "local policy never supplies whole-group breaker evidence; OLD_REVIEW next-success controls must reproduce a nonzero count at this exact key")
						var actualCost float64
						var count int
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE api_key_id=$1`, f.key.ID).Scan(&count, &actualCost))
						if nextFails {
							require.GreaterOrEqual(t, rec.Code, 500, rec.Body.String())
							require.Zero(t, count)
						} else {
							require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
							row := latestServedBillingRow(t, f.key.ID, 1)
							require.Equal(t, *f.key.GroupID, row.groupID.Int64)
							require.Equal(t, f.stableGroup.ID, row.servedGroup.Int64)
							require.EqualValues(t, 1, row.servedSource.Int64)
							require.InDelta(t, .2, row.totalCost, 1e-9)
							require.InDelta(t, .4, row.actualCost, 1e-9)
							var accountID int64
							require.NoError(t, inflightTestDB(t).QueryRow(`SELECT account_id FROM usage_logs WHERE api_key_id=$1`, f.key.ID).Scan(&accountID))
							require.Equal(t, next.ID, accountID)
						}
						if card {
							var d, w, m float64
							require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.user.ID).Scan(&d, &w, &m))
							require.InDelta(t, actualCost, d, 1e-8)
							require.InDelta(t, actualCost, w, 1e-8)
							require.InDelta(t, actualCost, m, 1e-8)
						} else {
							require.InDelta(t, actualCost, beforeWallet-userBalance(t, f.user.ID), 1e-8)
						}
						cache := NewConcurrencyCache(f.rdb, 15, 30)
						for _, id := range []int64{a.ID, b.ID, next.ID} {
							active, err := cache.GetAccountConcurrency(ctx, id)
							require.NoError(t, err)
							require.Zero(t, active)
							waiting, err := cache.GetAccountWaitingCount(ctx, id)
							require.NoError(t, err)
							require.Zero(t, waiting)
						}
					})
				}
			}
		}
	}
}
