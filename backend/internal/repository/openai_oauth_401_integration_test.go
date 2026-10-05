//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type oauth401RefreshExecutor struct {
	url       string
	calls     atomic.Int32
	before    func()
	repo      *oauth401AccountRepository
	accountID int64
}

func (e *oauth401RefreshExecutor) CacheKey(a *service.Account) string {
	return service.OpenAITokenCacheKey(a)
}
func (e *oauth401RefreshExecutor) CanRefresh(a *service.Account) bool                { return a.IsOpenAIOAuth() }
func (e *oauth401RefreshExecutor) NeedsRefresh(*service.Account, time.Duration) bool { return false }
func (e *oauth401RefreshExecutor) Refresh(ctx context.Context, a *service.Account) (map[string]any, error) {
	e.calls.Add(1)
	if e.before != nil {
		e.before()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, strings.NewReader(a.GetOpenAIRefreshToken()))
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	credentials := make(map[string]any, len(a.Credentials))
	for key, value := range a.Credentials {
		credentials[key] = value
	}
	var rotation map[string]any
	if err := json.NewDecoder(response.Body).Decode(&rotation); err != nil {
		return nil, err
	}
	for key, value := range rotation {
		credentials[key] = value
	}
	return credentials, nil
}

// The hook runs after the recovery's last database read, immediately before
// the real repository obtains its row lock. It models a writer outside OAuth locks.
type oauth401AccountRepository struct {
	service.AccountRepository
	beforeCAS func(*service.Account)
}

func (r *oauth401AccountRepository) CompareAndSwapCredentials(ctx context.Context, expected *service.Account, credentials map[string]any) (bool, error) {
	if r.beforeCAS != nil {
		r.beforeCAS(expected)
	}
	updater := r.AccountRepository.(interface {
		CompareAndSwapCredentials(context.Context, *service.Account, map[string]any) (bool, error)
	})
	return updater.CompareAndSwapCredentials(ctx, expected, credentials)
}

type oauth401Upstream struct {
	mu                           sync.Mutex
	auths, bodies, urls, proxies []string
	onRetry                      func()
	onRejected                   func()
	rejectRetry                  bool
	rejection                    string
	readErr                      error
}

func (u *oauth401Upstream) Do(request *http.Request, proxy string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.auths = append(u.auths, request.Header.Get("Authorization"))
	u.bodies = append(u.bodies, string(body))
	u.urls = append(u.urls, request.URL.String())
	u.proxies = append(u.proxies, proxy)
	call := len(u.auths)
	u.mu.Unlock()
	status, response := http.StatusUnauthorized, `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"access token expired"}}`
	if request.Header.Get("Authorization") == "Bearer fixture-old" {
		if u.onRejected != nil {
			u.onRejected()
		}
	} else if call > 1 && !u.rejectRetry {
		if u.onRetry != nil {
			u.onRetry()
		}
		status, response = http.StatusOK, strings.ReplaceAll(inflightResponsesJSON, `"gpt-5"`, `"gpt-5.4"`)
		response = strings.ReplaceAll(response, "resp_inflight", fmt.Sprintf("resp_oauth401_%d", call))
	}
	if status == http.StatusUnauthorized && u.rejection != "" {
		response = u.rejection
	}
	var reader io.Reader = strings.NewReader(response)
	if u.readErr != nil && status == http.StatusUnauthorized {
		reader = io.MultiReader(reader, inflightHTTPReadError{u.readErr})
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(reader), Request: request}, nil
}
func (u *oauth401Upstream) DoWithTLS(request *http.Request, proxy string, id int64, slots int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(request, proxy, id, slots)
}

func newOAuth401HTTPFixture(t *testing.T, passthrough bool) (*inflightHTTPFixture, *oauth401RefreshExecutor, *oauth401Upstream) {
	t.Helper()
	f := newInflightHTTPFixture(t, service.PlatformOpenAI, "", "application/json")
	client, db, rdb := inflightTestEntClient(t), inflightTestDB(t), testRedis(t)
	accounts := NewAccountRepository(client, db, nil)
	var id int64
	require.NoError(t, db.QueryRow(`SELECT account_id FROM account_groups WHERE group_id=$1`, *f.key.GroupID).Scan(&id))
	account, err := accounts.GetByID(context.Background(), id)
	require.NoError(t, err)
	account.Type = service.AccountTypeOAuth
	account.Credentials = map[string]any{"access_token": "fixture-old", "refresh_token": "fixture-rt", "expires_at": time.Now().Add(2 * time.Hour).Format(time.RFC3339)}
	account.Extra = map[string]any{"privacy_mode": service.PrivacyModeTrainingOff, "openai_passthrough": passthrough, "openai_oauth_responses_websockets_v2_mode": "off"}
	require.NoError(t, accounts.Update(context.Background(), account))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		require.Equal(t, "fixture-rt", string(body))
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"access_token":"fixture-new","refresh_token":"fixture-next"}`)
	}))
	t.Cleanup(server.Close)
	guardedAccounts := &oauth401AccountRepository{AccountRepository: accounts}
	executor := &oauth401RefreshExecutor{url: server.URL, repo: guardedAccounts, accountID: id}
	tokenCache := NewGeminiTokenCache(rdb)
	provider := service.NewOpenAITokenProvider(guardedAccounts, tokenCache, nil)
	provider.SetRefreshAPI(service.NewOAuthRefreshAPI(guardedAccounts, tokenCache), executor)
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	cfg.Billing.InflightReservation.Enabled = true
	cfg.Billing.InflightReservation.TTLSeconds = 900
	cfg.Billing.InflightReservation.DefaultMaxOutputTokens = 8
	cfg.Gateway.Scheduling.DbFallbackEnabled = true
	groups, users, subs, rates := NewGroupRepository(client, db), NewUserRepository(client, db), NewUserSubscriptionRepository(client), NewUserGroupRateRepository(db)
	settings := service.NewSettingService(NewSettingRepository(client), cfg)
	billingCache := service.NewBillingCacheService(NewBillingCache(rdb), users, subs, nil, nil, rates, cfg, nil, settings)
	t.Cleanup(billingCache.Stop)
	concurrency := service.NewConcurrencyService(NewConcurrencyCache(rdb, 15, 30))
	snapshot := service.NewSchedulerSnapshotService(NewSchedulerCache(rdb), nil, accounts, groups, cfg)
	t.Cleanup(snapshot.Stop)
	upstream := &oauth401Upstream{}
	wheel, err := service.NewTimingWheelService()
	require.NoError(t, err)
	t.Cleanup(wheel.Stop)
	deferred := service.NewDeferredService(accounts, wheel, time.Minute)
	t.Cleanup(deferred.Stop)
	svc := service.NewOpenAIGatewayService(accounts, NewUsageLogRepository(client, db), NewUsageBillingRepository(client, db), users, subs, rates, NewGatewayCache(rdb), cfg, snapshot, concurrency, service.NewBillingService(cfg, nil), service.NewRateLimitService(accounts, nil, cfg, nil, nil), billingCache, upstream, deferred, provider, nil, nil, nil, nil, settings, nil, nil, groups)
	t.Cleanup(svc.CloseOpenAIWSPool)
	keys := service.NewAPIKeyService(NewAPIKeyRepository(client, db), users, groups, subs, rates, nil, cfg)
	f.openAIService = svc
	f.openAI = userhandler.NewOpenAIGatewayHandler(svc, concurrency, billingCache, keys, f.pool, nil, nil, nil, cfg)
	_, err = db.Exec(`UPDATE users SET balance=10 WHERE id=$1`, f.user.ID)
	require.NoError(t, err)
	f.user.Balance = 10
	return f, executor, upstream
}

const oauth401Request = `{"model":"gpt-5.4","max_output_tokens":8,"stream":false,"instructions":"fixture","input":[{"role":"user","content":"hi"}]}`

func TestOpenAI401HTTP_RealRecoveryBillsOnce(t *testing.T) {
	for _, tc := range []struct {
		name              string
		passthrough, card bool
	}{
		{"forward_wallet", false, false}, {"forward_card", false, true},
		{"passthrough_wallet", true, false}, {"passthrough_card", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, executor, upstream := newOAuth401HTTPFixture(t, tc.passthrough)
			if tc.card {
				admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 10, 20, 30, 0, 0, 0)
			}
			rec := f.request(oauth401Request, "/v1/responses", "", f.openAI.Responses)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), "ok")
			require.EqualValues(t, 1, executor.calls.Load())
			upstream.mu.Lock()
			auths, bodies, urls, proxies := append([]string(nil), upstream.auths...), append([]string(nil), upstream.bodies...), append([]string(nil), upstream.urls...), append([]string(nil), upstream.proxies...)
			upstream.mu.Unlock()
			require.Equal(t, []string{"Bearer fixture-old", "Bearer fixture-new"}, auths)
			require.Equal(t, bodies[0], bodies[1])
			require.Equal(t, urls[0], urls[1])
			require.Equal(t, proxies[0], proxies[1])
			f.pool.Stop()
			var logs, dedup int
			var cost, balance float64
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &cost))
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
			require.Equal(t, 1, logs)
			require.Equal(t, 1, dedup)
			require.Positive(t, cost)
			if tc.card {
				var daily, weekly, monthly float64
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.user.ID).Scan(&daily, &weekly, &monthly))
				require.InDelta(t, cost, daily, 1e-10)
				require.InDelta(t, cost, weekly, 1e-10)
				require.InDelta(t, cost, monthly, 1e-10)
				require.InDelta(t, 10, balance, 1e-10)
			} else {
				require.InDelta(t, 10-cost, balance, 1e-10)
			}
			require.Zero(t, inflightHeld(t, f.user.ID))
		})
	}
}

func TestOpenAI401HTTP_RetryKeepsFundedAttempt(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough_%t", passthrough), func(t *testing.T) { testOAuth401FundedAttempt(t, passthrough) })
	}
}
func testOAuth401FundedAttempt(t *testing.T, passthrough bool) {
	f, executor, upstream := newOAuth401HTTPFixture(t, passthrough)
	refreshStarted, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var held float64
	executor.before = func() {
		held = inflightHeld(t, f.user.ID)
		require.Positive(t, held)
		_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=$1 WHERE id=$2`, held*1.1, f.user.ID)
		require.NoError(t, err)
		close(refreshStarted)
		<-release
	}
	upstream.onRetry = func() {
		require.InDelta(t, held, inflightHeld(t, f.user.ID), 1e-10, "internal retry must keep its original reservation")
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.request(oauth401Request, "/v1/responses", "", f.openAI.Responses) }()
	select {
	case <-refreshStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}
	second := f.request(oauth401Request, "/v1/responses", "", f.openAI.Responses)
	require.Equal(t, http.StatusForbidden, second.Code, second.Body.String())
	upstream.mu.Lock()
	calls := len(upstream.auths)
	upstream.mu.Unlock()
	require.Equal(t, 1, calls)
	close(release)
	select {
	case rec := <-done:
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not complete")
	}
	f.pool.Stop()
	require.Zero(t, inflightHeld(t, f.user.ID))
}

func TestOpenAI401HTTP_ConcurrentCredentialWriterWins(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, stage := range []string{"before_recovery_refresh_token", "before_persist_access_refresh", "before_persist_auth_headers", "durable_rotated_winner"} {
			t.Run(fmt.Sprintf("passthrough_%t/%s", passthrough, stage), func(t *testing.T) {
				f, executor, upstream := newOAuth401HTTPFixture(t, passthrough)
				repository := executor.repo.AccountRepository
				expected, err := repository.GetByID(context.Background(), executor.accountID)
				require.NoError(t, err)
				winner := make(map[string]any, len(expected.Credentials))
				for k, v := range expected.Credentials {
					winner[k] = v
				}
				switch stage {
				case "before_recovery_refresh_token":
					winner["refresh_token"] = "fixture-admin-rt"
				case "before_persist_access_refresh", "durable_rotated_winner":
					winner["access_token"] = "fixture-admin-at"
					winner["refresh_token"] = "fixture-admin-rt"
				case "before_persist_auth_headers":
					winner["header_overrides"] = map[string]any{"X-Fixture-Owner": "admin"}
				}
				writeWinner := func() {
					require.NoError(t, repository.(interface {
						UpdateCredentials(context.Context, int64, map[string]any) error
					}).UpdateCredentials(context.Background(), executor.accountID, winner))
				}
				if strings.HasPrefix(stage, "before_persist") {
					executor.repo.beforeCAS = func(*service.Account) { writeWinner() }
				} else {
					upstream.onRejected = writeWinner
				}
				rec := f.request(oauth401Request, "/v1/responses", "", f.openAI.Responses)
				f.pool.Stop()
				current, err := repository.GetByID(context.Background(), executor.accountID)
				require.NoError(t, err)
				require.Equal(t, winner, current.Credentials, "the concurrent writer must not be overwritten")
				upstream.mu.Lock()
				auths := append([]string(nil), upstream.auths...)
				upstream.mu.Unlock()
				var logs, dedup int
				var cost, balance float64
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &cost))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
				if stage == "durable_rotated_winner" {
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Equal(t, []string{"Bearer fixture-old", "Bearer fixture-admin-at"}, auths)
					require.Zero(t, executor.calls.Load(), "reuse an existing access-token winner without another grant")
					require.Equal(t, 1, logs)
					require.Equal(t, 1, dedup)
					require.Positive(t, cost)
					require.InDelta(t, 10-cost, balance, 1e-10)
				} else {
					require.NotEqual(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Equal(t, []string{"Bearer fixture-old"}, auths, "stale recovery must not dispatch again")
					expectedGrants := int32(1)
					if stage == "before_recovery_refresh_token" {
						expectedGrants = 0
					}
					require.Equal(t, expectedGrants, executor.calls.Load())
					require.Zero(t, logs)
					require.Zero(t, dedup)
					require.Zero(t, cost)
					require.InDelta(t, 10, balance, 1e-10)
				}
				require.Zero(t, inflightHeld(t, f.user.ID), "terminal complete auth refusal releases its own attempt")
			})
		}
	}
}

func TestOpenAI401HTTP_TerminalRecoveryControls(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, name := range []string{"retry_rejected", "model_not_found", "cloudflare_html", "incomplete_auth_body"} {
			t.Run(fmt.Sprintf("passthrough_%t/%s", passthrough, name), func(t *testing.T) {
				f, executor, upstream := newOAuth401HTTPFixture(t, passthrough)
				switch name {
				case "retry_rejected":
					upstream.rejectRetry = true
				case "model_not_found":
					upstream.rejection = `{"error":{"code":"model_not_found","message":"model unavailable"}}`
				case "cloudflare_html":
					upstream.rejection = "<html>Cloudflare Unauthorized</html>"
				case "incomplete_auth_body":
					upstream.readErr = io.ErrUnexpectedEOF
				}
				rec := f.request(oauth401Request, "/v1/responses", "", f.openAI.Responses)
				require.NotEqual(t, http.StatusOK, rec.Code, rec.Body.String())
				f.pool.Stop()
				expectedGrants, expectedDispatch := int32(0), 1
				if name == "retry_rejected" {
					expectedGrants, expectedDispatch = 1, 2
				}
				require.Equal(t, expectedGrants, executor.calls.Load())
				upstream.mu.Lock()
				calls := len(upstream.auths)
				upstream.mu.Unlock()
				require.Equal(t, expectedDispatch, calls)
				var logs, dedup int
				var balance float64
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
				require.Zero(t, logs)
				require.Zero(t, dedup)
				require.InDelta(t, 10, balance, 1e-10)
				if name == "retry_rejected" {
					require.Zero(t, inflightHeld(t, f.user.ID))
				} else {
					require.Positive(t, inflightHeld(t, f.user.ID), "unknown/provider-specific refusal retains its bounded attempt hold")
				}
			})
		}
	}
}

func TestOpenAI401HTTP_ConcurrentRejectedRequestsShareRefresh(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough_%t", passthrough), func(t *testing.T) {
			f, executor, upstream := newOAuth401HTTPFixture(t, passthrough)
			var rejected atomic.Int32
			bothRejected := make(chan struct{})
			upstream.onRejected = func() {
				if rejected.Add(1) == 2 {
					close(bothRejected)
				}
			}
			executor.before = func() {
				select {
				case <-bothRejected:
				case <-time.After(5 * time.Second):
					t.Error("second request did not dispatch original rejected token")
				}
			}
			done := make(chan *httptest.ResponseRecorder, 2)
			for range 2 {
				go func() { done <- f.request(oauth401Request, "/v1/responses", "", f.openAI.Responses) }()
			}
			for range 2 {
				select {
				case rec := <-done:
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				case <-time.After(10 * time.Second):
					t.Fatal("concurrent recovery did not finish")
				}
			}
			f.pool.Stop()
			require.EqualValues(t, 1, executor.calls.Load(), "shared existing local/Redis refresh locks allow only one grant")
			upstream.mu.Lock()
			auths := append([]string(nil), upstream.auths...)
			upstream.mu.Unlock()
			require.Len(t, auths, 4)
			require.Equal(t, 2, strings.Count(strings.Join(auths, "|"), "Bearer fixture-old"))
			require.Equal(t, 2, strings.Count(strings.Join(auths, "|"), "Bearer fixture-new"))
			var logs, dedup int
			var cost, balance float64
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &cost))
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
			require.Equal(t, 2, logs)
			require.Equal(t, 2, dedup)
			require.Positive(t, cost)
			require.InDelta(t, 10-cost, balance, 1e-10)
			require.Zero(t, inflightHeld(t, f.user.ID))
		})
	}
}
