//go:build unit

package service

import (
	"context"
	"errors"
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
}

func (r *openAI401Repo) GetByID(context.Context, int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.readErr != nil {
		return nil, r.readErr
	}
	if r.account == nil {
		return nil, nil
	}
	a := *r.account
	a.Credentials = cloneCredentials(a.Credentials)
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

func (r *openAI401Repo) Update(context.Context, *Account) error {
	panic("full account update forbidden")
}

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
	for _, name := range []string{"refresh_future_expiry", "durable_winner", "unchanged_token", "read_failure", "nil_account", "save_failure", "lock_failure", "disabled", "platform_changed", "proxy_changed", "header_changed", "cancel_during_refresh", "disable_during_refresh", "winner_during_refresh", "refresh_token_changed", "refresh_failure"} {
		t.Run(name, func(t *testing.T) {
			account := openAI401Account()
			snapshot, ok := snapshotOpenAI401Account(account)
			require.True(t, ok)
			repo := &openAI401Repo{account: account}
			cache := &refreshAPICacheStub{lockResult: true}
			executor := &openAI401Executor{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "durable_winner":
				account.Credentials["access_token"] = "fixture-winner"
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
				require.Zero(t, repo.updates)
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
	for _, name := range []string{"nil", "apikey", "setup", "PAT", "agent", "missing_rt", "disabled", "same_proxy_id_new_endpoint", "same_proxy_id_new_auth", "extra_changed"} {
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
			case "extra_changed":
				a.Extra = map[string]any{"openai_passthrough": true}
			}
			require.False(t, snapshot.matches(a))
		})
	}
}
