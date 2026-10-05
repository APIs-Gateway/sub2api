//go:build unit

package service

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func openAI401Account() *Account {
	return &Account{ID: 5542, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "fixture-old", "refresh_token": "fixture-rt", "expires_at": time.Now().Add(2 * time.Hour).Format(time.RFC3339), "header_overrides": map[string]any{"X-Test": "keep"}}}
}

func TestOpenAI401RecoveryClassifier(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		readErr    error
		want       bool
	}{
		{"root_array", `[{"error":{"type":"authentication_error"}}]`, 401, nil, false},
		{"error_string", `{"error":"Unauthorized"}`, 401, nil, false},
		{"nested_array_fault", `{"error":{"type":"authentication_error"},"metadata":["account disabled"]}`, 401, nil, false},
		{"nested_array_safe", `{"error":{"type":"authentication_error"},"metadata":["fixture"]}`, 401, nil, true},
		{"outer_conflicting", `{"type":"error","code":"server_error","status":500,"error":{"type":"authentication_error"}}`, 401, nil, false},
		{"outer_status_mismatch", `{"status":500,"error":{"type":"authentication_error"}}`, 401, nil, false},
		{"outer_code_mismatch", `{"code":403,"error":{"type":"authentication_error"}}`, 401, nil, false},
		{"outer_object_marker", `{"code":{"value":"authentication_error"},"error":{"type":"authentication_error"}}`, 401, nil, false},
		{"outer_string_status", `{"status":"success","error":{"type":"authentication_error"}}`, 401, nil, false},
		{"outer_consistent_status", `{"type":"error","code":401,"status":401,"error":{"type":"authentication_error"}}`, 401, nil, true},
		{"outer_consistent_auth", `{"code":"invalid_api_key","status":"error","error":{"type":"authentication_error"}}`, 401, nil, true},
		{"authentication", `{"error":{"type":"authentication_error","message":"token expired"}}`, 401, nil, true},
		{"expired", `{"error":{"type":"invalid_request_error","code":"token_expired"}}`, 401, nil, true},
		{"numeric_code", `{"error":{"type":"authentication_error","code":401}}`, 401, nil, true},
		{"unknown_model", `{"error":{"type":"authentication_error","message":"Unknown model gpt-5"}}`, 401, nil, false},
		{"model_code", `{"error":{"type":"authentication_error","code":"model_not_found"}}`, 401, nil, false},
		{"cloudflare", `{"error":{"type":"authentication_error","message":"Cloudflare Error 1010"}}`, 401, nil, false},
		{"revoked", `{"error":{"type":"authentication_error","code":"token_revoked"}}`, 401, nil, false},
		{"escaped_revoked", `{"error":{"type":"authentication_error","message":"token has been re\u0076oked"}}`, 401, nil, false},
		{"scope", `{"error":{"type":"authentication_error","message":"Missing scopes: model.read"}}`, 401, nil, false},
		{"unauthorized_detail", `{"error":{"type":"authentication_error"},"detail":"Unauthorized"}`, 401, nil, false},
		{"duplicate", `{"error":{"type":"authentication_error","code":"token_revoked","code":"token_expired"}}`, 401, nil, false},
		{"conflicting", `{"error":{"type":"authentication_error","code":"server_error"}}`, 401, nil, false},
		{"metered", `{"error":{"type":"authentication_error"},"usage":{"input_tokens":1}}`, 401, nil, false},
		{"output", `{"error":{"type":"authentication_error"},"output":[{"text":"paid"}]}`, 401, nil, false},
		{"wrong_outer_type", `{"type":"response","error":{"type":"authentication_error"}}`, 401, nil, false},
		{"wrong_numeric_code", `{"error":{"type":"authentication_error","code":403}}`, 401, nil, false},
		{"object_marker", `{"error":{"type":{"value":"authentication_error"}}}`, 401, nil, false},
		{"html", `<html>Unauthorized</html>`, 401, nil, false},
		{"plain", `Unauthorized`, 401, nil, false},
		{"trailing", `{"error":{"type":"authentication_error"}} {}`, 401, nil, false},
		{"read_error", `{"error":{"type":"authentication_error"}}`, 401, errors.New("reset"), false},
		{"non401", `{"error":{"type":"authentication_error"}}`, 403, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isRecoverableOpenAIHTTP401(tc.status, []byte(tc.body), tc.readErr))
		})
	}
}

type openAI401Repo struct {
	mockAccountRepoForGemini
	mu               sync.Mutex
	account          *Account
	readErr, saveErr error
	updates          int
	reads            int
	getErrOn         int
	afterGet         func()
}

func (r *openAI401Repo) GetByID(context.Context, int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if r.getErrOn == r.reads {
		return nil, errors.New("read unavailable")
	}
	if r.readErr != nil {
		return nil, r.readErr
	}
	if r.account == nil {
		return nil, nil
	}
	a := *r.account
	a.Credentials = cloneCredentials(a.Credentials)
	if r.afterGet != nil {
		r.afterGet()
	}
	return &a, nil
}

func (r *openAI401Repo) UpdateCredentials(_ context.Context, id int64, credentials map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return r.saveErr
	}
	if r.account == nil || r.account.ID != id {
		return errors.New("missing account")
	}
	r.updates++
	r.account.Credentials = cloneCredentials(credentials)
	return nil
}

func (r *openAI401Repo) CompareAndSwapCredentials(_ context.Context, expected *Account, credentials map[string]any) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return false, r.saveErr
	}
	if r.account == nil || r.account.ID != expected.ID || !reflect.DeepEqual(r.account.Credentials, expected.Credentials) {
		return false, nil
	}
	r.updates++
	r.account.Credentials = cloneCredentials(credentials)
	return true, nil
}

func (r *openAI401Repo) Update(context.Context, *Account) error {
	panic("full account update forbidden")
}

type openAI401FailureCache struct {
	refreshAPICacheStub
	deleteErr error
}

func (c *openAI401FailureCache) DeleteAccessToken(context.Context, string) error { return c.deleteErr }

type openAI401Executor struct {
	calls   atomic.Int32
	refresh func(context.Context, *Account) (map[string]any, error)
}

func (e *openAI401Executor) CanRefresh(a *Account) bool                { return a.IsOpenAIOAuth() }
func (e *openAI401Executor) NeedsRefresh(*Account, time.Duration) bool { return false }
func (e *openAI401Executor) CacheKey(a *Account) string                { return OpenAITokenCacheKey(a) }
func (e *openAI401Executor) Refresh(ctx context.Context, a *Account) (map[string]any, error) {
	e.calls.Add(1)
	if e.refresh != nil {
		return e.refresh(ctx, a)
	}
	credentials := cloneCredentials(a.Credentials)
	credentials["access_token"] = "fixture-new"
	credentials["refresh_token"] = "fixture-rotated"
	return credentials, nil
}

func TestOpenAI401RecoveryStrictLifecycle(t *testing.T) {
	for _, name := range []string{"refresh_future_expiry", "durable_winner", "unchanged_token", "read_failure", "nil_account", "save_failure", "lock_failure", "disabled", "platform_changed", "proxy_changed", "header_changed", "cancel_during_refresh", "disable_during_refresh", "winner_during_refresh", "refresh_token_changed", "refresh_token_changed_before_lock", "refresh_failure", "cancel_on_read", "reread_failure_after_save", "cache_failure_after_save", "durable_empty_token"} {
		t.Run(name, func(t *testing.T) {
			account := openAI401Account()
			snapshot, ok := snapshotOpenAI401Account(account)
			require.True(t, ok)
			repo := &openAI401Repo{account: account}
			cache := &openAI401FailureCache{refreshAPICacheStub: refreshAPICacheStub{lockResult: true}}
			executor := &openAI401Executor{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "cancel_on_read":
				repo.afterGet = cancel
			case "reread_failure_after_save":
				repo.getErrOn = 3
			case "cache_failure_after_save":
				cache.deleteErr = errors.New("cache invalidation unavailable")
			case "durable_empty_token":
				account.Credentials["access_token"] = ""
			case "durable_winner":
				account.Credentials["access_token"] = "fixture-winner"
				account.Credentials["refresh_token"] = "fixture-winner-rt"
			case "refresh_token_changed_before_lock":
				account.Credentials["refresh_token"] = "admin-new"
			case "unchanged_token":
				executor.refresh = func(_ context.Context, a *Account) (map[string]any, error) {
					return cloneCredentials(a.Credentials), nil
				}
			case "read_failure":
				repo.readErr = errors.New("database unavailable")
			case "nil_account":
				repo.account = nil
			case "save_failure":
				repo.saveErr = errors.New("save unavailable")
			case "lock_failure":
				cache.lockErr = errors.New("cache unavailable")
			case "disabled":
				account.Status = "disabled"
			case "platform_changed":
				account.Platform = PlatformAnthropic
			case "proxy_changed":
				id := int64(7)
				account.ProxyID = &id
			case "header_changed":
				account.Credentials["header_overrides"] = map[string]any{"X-Test": "changed"}
			case "refresh_failure":
				executor.refresh = func(context.Context, *Account) (map[string]any, error) { return nil, errors.New("refresh denied") }
			case "cancel_during_refresh", "disable_during_refresh", "winner_during_refresh", "refresh_token_changed":
				executor.refresh = func(_ context.Context, a *Account) (map[string]any, error) {
					credentials := cloneCredentials(a.Credentials)
					credentials["access_token"] = "fixture-new"
					switch name {
					case "cancel_during_refresh":
						credentials["refresh_token"] = "fixture-next"
						cancel()
					case "disable_during_refresh":
						account.Status = "disabled"
					case "winner_during_refresh":
						account.Credentials["access_token"] = "fixture-winner"
					case "refresh_token_changed":
						account.Credentials["refresh_token"] = "admin-new"
					}
					return credentials, nil
				}
			}
			api := NewOAuthRefreshAPI(repo, cache)
			token, err := api.refreshRejectedOpenAIToken(ctx, snapshot, executor, "fixture-old")
			if name == "refresh_future_expiry" || name == "durable_winner" || name == "winner_during_refresh" {
				require.NoError(t, err)
				require.NotEmpty(t, token)
				require.NotEqual(t, "fixture-old", token)
				if name == "refresh_future_expiry" {
					require.EqualValues(t, 1, executor.calls.Load())
					require.Equal(t, 1, repo.updates)
				} else {
					require.Equal(t, 0, repo.updates)
				}
			} else {
				require.Error(t, err)
				require.Empty(t, token)
				if name == "cancel_during_refresh" || name == "reread_failure_after_save" || name == "cache_failure_after_save" {
					require.Equal(t, 1, repo.updates)
					require.Equal(t, "fixture-new", repo.account.GetOpenAIAccessToken())
					expectedRT := "fixture-rotated"
					if name == "cancel_during_refresh" {
						expectedRT = "fixture-next"
					}
					require.Equal(t, expectedRT, repo.account.GetOpenAIRefreshToken())
				} else {
					require.Zero(t, repo.updates)
				}
				if name == "refresh_token_changed_before_lock" {
					require.Zero(t, executor.calls.Load())
				}
			}
		})
	}
}

func TestOpenAI401RecoveryCancellationWhileWaitingSameLocalLock(t *testing.T) {
	account := openAI401Account()
	snapshot, ok := snapshotOpenAI401Account(account)
	require.True(t, ok)
	api := NewOAuthRefreshAPI(&openAI401Repo{account: account}, &refreshAPICacheStub{lockResult: true})
	executor := &openAI401Executor{}
	mu := api.getLocalLock(executor.CacheKey(account))
	mu.Lock()
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	token, err := api.refreshRejectedOpenAIToken(ctx, snapshot, executor, "fixture-old")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, token)
	require.Less(t, time.Since(start), time.Second)
	require.Zero(t, executor.calls.Load())
}

func TestOpenAI401RecoverySnapshotTransportAndCredentialExclusions(t *testing.T) {
	for _, name := range []string{"nil", "apikey", "setup", "PAT", "agent", "missing_rt", "disabled", "same_proxy_id_new_endpoint", "same_proxy_id_new_auth", "extra_changed", "unserializable_auth"} {
		t.Run(name, func(t *testing.T) {
			a := openAI401Account()
			id := int64(7)
			a.ProxyID = &id
			a.Proxy = &Proxy{ID: 7, Status: StatusActive, Protocol: "http", Host: "selected.test", Port: 8080, Username: "fixture-user", Password: "fixture-pass"}
			snapshot, ok := snapshotOpenAI401Account(a)
			require.True(t, ok)
			switch name {
			case "nil":
				a = nil
			case "apikey":
				a.Type = AccountTypeAPIKey
			case "setup":
				a.Type = AccountTypeSetupToken
			case "PAT":
				a.Credentials["auth_mode"] = OpenAIAuthModePersonalAccessToken
			case "agent":
				a.Credentials["auth_mode"] = OpenAIAuthModeAgentIdentity
			case "missing_rt":
				delete(a.Credentials, "refresh_token")
			case "disabled":
				a.Status = "disabled"
			case "same_proxy_id_new_endpoint":
				a.Proxy.Host = "new.test"
			case "same_proxy_id_new_auth":
				a.Proxy.Password = "changed"
			case "unserializable_auth":
				a.Credentials["custom"] = func() {}
			case "extra_changed":
				a.Extra = map[string]any{"openai_passthrough": true}
			}
			require.False(t, snapshot.matches(a))
		})
	}
}

func TestOpenAI401RecoveryDependenciesAndDistributedWaitFailClosed(t *testing.T) {
	for _, name := range []string{"nil_api", "nil_repository", "nil_cache", "nil_executor", "missing_conditional_updater", "already_canceled", "distributed_lock_deadline"} {
		t.Run(name, func(t *testing.T) {
			account := openAI401Account()
			snapshot, ok := snapshotOpenAI401Account(account)
			require.True(t, ok)
			repo := &openAI401Repo{account: account}
			cache := &refreshAPICacheStub{lockResult: true}
			executor := &openAI401Executor{}
			var refreshExecutor OAuthRefreshExecutor = executor
			api := NewOAuthRefreshAPI(repo, cache)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "nil_api":
				api = nil
			case "nil_repository":
				api.accountRepo = nil
			case "nil_cache":
				api.tokenCache = nil
			case "nil_executor":
				refreshExecutor = nil
			case "missing_conditional_updater":
				api.accountRepo = &mockAccountRepoForGemini{}
			case "already_canceled":
				cancel()
			case "distributed_lock_deadline":
				cache.lockResult = false
				api.lockTTL = 40 * time.Millisecond
			}
			started := time.Now()
			token, err := api.refreshRejectedOpenAIToken(ctx, snapshot, refreshExecutor, "fixture-old")
			require.Error(t, err)
			require.Empty(t, token)
			require.Zero(t, executor.calls.Load())
			require.Zero(t, repo.updates)
			require.Less(t, time.Since(started), time.Second, "failed locking must be bounded and must not bypass the shared namespace")
		})
	}
}
