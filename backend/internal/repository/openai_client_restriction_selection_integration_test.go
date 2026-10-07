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

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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
