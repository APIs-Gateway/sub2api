//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// The frozen OLD/NEW fixture uses only pre-existing public handler APIs and
// existing unit harnesses. Funding assertions belong to the real PG/Redis suite.
func clientPolicyAccount(id int64, restricted bool) service.Account {
	a := chainRespAccount(id)
	a.Priority = int(id)
	if restricted {
		a.Type = service.AccountTypeOAuth
		a.Credentials["access_token"] = "local-fixture-oauth"
		a.Extra["codex_cli_only"] = true
		a.Extra["privacy_mode"] = service.PrivacyModeTrainingOff
	}
	return a
}

func clientPolicyRequest(hs *chainRespHarness, route, ua string, ctx context.Context) *httptest.ResponseRecorder {
	body := chainRespBodyJSON
	if route == "chat" {
		body = chainChatBodyJSON
	}
	path := "/openai/v1/responses"
	if route == "chat" {
		path = "/openai/v1/chat/completions"
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ua)
	if strings.HasPrefix(ua, "Claude Code/") {
		req.Header.Set("originator", "Claude Code")
	}
	rec := httptest.NewRecorder()
	hs.router.ServeHTTP(rec, req)
	return rec
}

func TestClientRestrictedSelection_Handler(t *testing.T) {
	for _, route := range []string{"responses", "chat"} {
		for _, chain := range []bool{false, true} {
			mode := "ordinary"
			if chain {
				mode = "group_chain"
			}
			t.Run(route+"/"+mode, func(t *testing.T) {
				setup := func() chainRespOptions {
					o := chainRespBase()
					o.noRuntime = !chain
					o.schedulable[1] = []service.Account{clientPolicyAccount(11, true), clientPolicyAccount(12, false)}
					return o
				}
				t.Run("mixed_accounts_select_original_client_compatible_account", func(t *testing.T) {
					o := setup()
					hs := newChainRespHarness(t, o)
					rec := clientPolicyRequest(hs, route, "unofficial-original-client/1.0", nil)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Equal(t, []int64{12}, hs.upstream.accountCalls(), "restricted account must receive zero upstream requests")
					usage := hs.waitUsage(t, 1)[0]
					require.EqualValues(t, 12, usage.AccountID)
					requireNoServedColumns(t, usage, 1)
					require.Empty(t, o.breaker.failedGroups(), "local selection exclusion does not affect group health")
				})
				t.Run("all_restricted_remains403_and_never_enters_other_group", func(t *testing.T) {
					o := setup()
					o.schedulable[1][1] = clientPolicyAccount(12, true)
					hs := newChainRespHarness(t, o)
					rec := clientPolicyRequest(hs, route, "unofficial-original-client/1.0", nil)
					require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
					require.Contains(t, rec.Body.String(), service.CodexOfficialClientsOnlyMessage)
					require.Empty(t, hs.upstream.accountCalls(), "eligible account21 in another group is not a policy escape")
					require.Empty(t, o.breaker.failedGroups())
					require.Empty(t, hs.usageLogs)
				})
				t.Run("model_admission_does_not_use_disallowed_compatible_account", func(t *testing.T) {
					o := setup()
					o.schedulable[1][1].Credentials["model_mapping"] = map[string]any{"different-model": "different-model"}
					hs := newChainRespHarness(t, o)
					rec := clientPolicyRequest(hs, route, "unofficial-original-client/1.0", nil)
					require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
					require.Empty(t, hs.upstream.accountCalls())
				})
				t.Run("compatible_control_unchanged", func(t *testing.T) {
					o := setup()
					o.schedulable[1] = []service.Account{clientPolicyAccount(12, false)}
					hs := newChainRespHarness(t, o)
					rec := clientPolicyRequest(hs, route, "unofficial-original-client/1.0", nil)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Equal(t, []int64{12}, hs.upstream.accountCalls())
					hs.waitUsage(t, 1)
				})
				t.Run("original_official_client_still_uses_restricted_account", func(t *testing.T) {
					hs := newChainRespHarness(t, setup())
					rec := clientPolicyRequest(hs, route, "codex_cli_rs/0.99.0", nil)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Equal(t, []int64{11}, hs.upstream.accountCalls())
					hs.waitUsage(t, 1)
				})
				t.Run("account_admin_allowed_client_still_uses_restricted_account", func(t *testing.T) {
					o := setup()
					o.schedulable[1][0].Extra["codex_cli_only_allowed_clients"] = []string{"claude_code"}
					hs := newChainRespHarness(t, o)
					rec := clientPolicyRequest(hs, route, "Claude Code/0.5.0 (Macos 15.5; arm64) iTerm2.app (Claude Code; 1.0.4)", nil)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Equal(t, []int64{11}, hs.upstream.accountCalls())
					hs.waitUsage(t, 1)
				})
				t.Run("prior_upstream_failure_wins_over_later_local_rejection", func(t *testing.T) {
					o := setup()
					o.schedulable[1] = []service.Account{clientPolicyAccount(10, false), clientPolicyAccount(11, true)}
					o.replies = map[int64]chainRespReply{10: chainRespUpstreamError()}
					// No additional group is permitted in this fixture; the genuine
					// upstream error must not be replaced by the later local403.
					o.noRuntime = true
					hs := newChainRespHarness(t, o)
					rec := clientPolicyRequest(hs, route, "unofficial-original-client/1.0", nil)
					require.GreaterOrEqual(t, rec.Code, 500, rec.Body.String())
					require.Equal(t, []int64{10}, hs.upstream.accountCalls())
				})
				t.Run("already_canceled_request_never_dispatches", func(t *testing.T) {
					hs := newChainRespHarness(t, setup())
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					clientPolicyRequest(hs, route, "unofficial-original-client/1.0", ctx)
					require.Empty(t, hs.upstream.accountCalls())
				})
			})
		}
	}
}

func TestClientRestrictedSelection_ForcedAccountIsNotReplaced(t *testing.T) {
	for _, route := range []string{"responses", "chat"} {
		t.Run(route, func(t *testing.T) {
			o := chainRespBase()
			o.noRuntime = true
			o.forced = []config.OpenAIForcedAccountRoute{{UserID: chainRespUserID, AccountID: 11}}
			o.schedulable[1] = []service.Account{clientPolicyAccount(11, true), clientPolicyAccount(12, false)}
			hs := newChainRespHarness(t, o)
			rec := clientPolicyRequest(hs, route, "unofficial-original-client/1.0", nil)
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			require.Empty(t, hs.upstream.accountCalls())
		})
	}
}

func TestClientRestrictedSelection_StableHomePolicyDoesNotEnterFallback(t *testing.T) {
	o := chainRespBase()
	o.noRuntime = true
	o.stableKey = true
	fallback := int64(2)
	o.primary.StablePriorityFallbackGroupID = &fallback
	store := &chainChatStableStore{}
	o.stableStore = store
	o.groupRepo = &chainChatGroupRepo{groups: map[int64]*service.Group{1: o.primary, 2: o.hops[1].Group}}
	o.schedulable[1] = []service.Account{clientPolicyAccount(11, true)}
	hs := newChainRespHarness(t, o)
	rec := clientPolicyRequest(hs, "chat", "unofficial-original-client/1.0", nil)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Empty(t, hs.upstream.accountCalls())
	store.mu.Lock()
	defer store.mu.Unlock()
	require.Zero(t, store.enters, "client policy exclusion must not enter stable fallback state")
}

// These cases keep the actual chain runtime enabled. The local exclusion is
// observed in either selection order and remains non-evidence after a later
// compatible account fails and the runner proceeds into an authorized hop.
func TestClientRestrictedSelection_MixedFailureChain(t *testing.T) {
	for _, route := range []string{"responses", "chat"} {
		for _, policyFirst := range []bool{false, true} {
			for _, nextFails := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/policyFirst=%t/nextFails=%t", route, policyFirst, nextFails), func(t *testing.T) {
					o := chainRespBase()
					a, b := clientPolicyAccount(10, false), clientPolicyAccount(11, true)
					if policyFirst {
						b.Priority = -10
					}
					o.schedulable[1] = []service.Account{a, b}
					o.replies = map[int64]chainRespReply{10: chainRespUpstreamError()}
					if nextFails {
						o.replies[21] = chainRespUpstreamError()
					}
					hs := newChainRespHarness(t, o)
					rec := clientPolicyRequest(hs, route, "unofficial-original-client/1.0", nil)
					require.Equal(t, []int64{10, 21}, hs.upstream.accountCalls(), "the incompatible account never dispatches; real failure still permits the authorized next hop")
					require.NotContains(t, o.breaker.failedGroups(), int64(1), "mixed local-policy exclusions cannot prove whole-group upstream failure")
					if nextFails {
						require.GreaterOrEqual(t, rec.Code, 500, rec.Body.String())
						require.Empty(t, hs.usageLogs)
					} else {
						require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
						usage := hs.waitUsage(t, 1)[0]
						require.EqualValues(t, 21, usage.AccountID)
						require.EqualValues(t, 1, *usage.GroupID)
						require.NotNil(t, usage.ServedGroupID)
						require.EqualValues(t, 2, *usage.ServedGroupID)
						require.Empty(t, o.breaker.failedGroups())
					}
				})
			}
		}
	}
}

// This counter observes the real admission call. It deliberately denies funding
// without creating a mocked lease; settlement is covered by the PG/Redis suite.
type clientPolicyCancelFundingRepo struct {
	service.UsageBillingRepository
	service.BillingInflightRepository
	reserves int32
}

func (r *clientPolicyCancelFundingRepo) ReserveBillingInflight(context.Context, int64, string, float64, bool, time.Duration) (bool, error) {
	atomic.AddInt32(&r.reserves, 1)
	return false, service.ErrInsufficientBalance
}

func TestClientRestrictedSelection_PostSelectionCancellation(t *testing.T) {
	for _, route := range []string{"responses", "chat"} {
		for _, chain := range []bool{false, true} {
			for _, cancelAfterAcquire := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/chain=%t/cancelAfterAcquire=%t", route, chain, cancelAfterAcquire), func(t *testing.T) {
					o := chainRespBase()
					o.noRuntime = !chain
					o.schedulable[1] = []service.Account{clientPolicyAccount(12, false)}
					hs := newChainRespHarness(t, o)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					var acquired int32
					cache := &concurrencyCacheMock{
						acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
						acquireAccountSlotFn: func(slotCtx context.Context, accountID int64, _ int, _ string) (bool, error) {
							require.NoError(t, slotCtx.Err(), "entry and selector must reach the live account acquisition")
							require.EqualValues(t, 12, accountID)
							atomic.AddInt32(&acquired, 1)
							if cancelAfterAcquire {
								cancel()
							}
							return true, nil
						},
					}
					concurrency := service.NewConcurrencyService(cache)
					funding := &clientPolicyCancelFundingRepo{}
					cfg := hs.handler.cfg
					cfg.Billing.InflightReservation.Enabled = true
					gateway := service.NewOpenAIGatewayService(
						&chainRespAccountRepo{schedulable: o.schedulable, configured: o.configured},
						&openAIWSUsageHandlerUsageLogRepoStub{created: hs.usageLogs}, funding,
						nil, nil, nil, nil, cfg, nil, concurrency, service.NewBillingService(cfg, nil),
						nil, hs.handler.billingCacheService, hs.upstream, &service.DeferredService{},
						nil, nil, nil, nil, nil, nil, nil, nil, nil,
					)
					hs.handler.gatewayService = gateway
					hs.handler.concurrencyHelper = NewConcurrencyHelper(concurrency, SSEPingFormatNone, time.Second)
					rec := clientPolicyRequest(hs, route, "unofficial-original-client/1.0", ctx)
					require.EqualValues(t, 1, atomic.LoadInt32(&acquired), "the selector actually acquired one account slot")
					require.EqualValues(t, 1, atomic.LoadInt32(&cache.releaseAccountCalled), "scheduler ownership is released exactly once, including handler completion")
					require.Empty(t, hs.upstream.accountCalls())
					require.Empty(t, hs.usageLogs)
					require.Empty(t, o.breaker.failedGroups())
					if cancelAfterAcquire {
						require.ErrorIs(t, ctx.Err(), context.Canceled)
						require.Zero(t, atomic.LoadInt32(&funding.reserves), "post-selection cancellation terminates before admission")
					} else {
						require.NoError(t, ctx.Err())
						require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
						require.EqualValues(t, 1, atomic.LoadInt32(&funding.reserves), "the live control proves enabled funding reaches the instrumented admission repository")
					}
				})
			}
		}
	}
}
