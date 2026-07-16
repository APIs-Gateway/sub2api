//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func newOpenAIQuotaOAuthTestAccount(id int64) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "oauth-quota-secret",
			"chatgpt_account_id": "quota-oauth-account",
			"expires_at":         time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
}

func TestOpenAIQuotaOAuthUsesBearerHeadersForUsageAndReset(t *testing.T) {
	account := newOpenAIQuotaOAuthTestAccount(203)
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
	var usageAuthorization, detailAuthorization, resetAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/wham/usage"):
			usageAuthorization = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"user_id":"oauth-user"}`))
		case strings.HasSuffix(r.URL.Path, "/wham/rate-limit-reset-credits"):
			detailAuthorization = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"available_count":1,"credits":[]}`))
		case strings.HasSuffix(r.URL.Path, "/wham/rate-limit-reset-credits/consume"):
			resetAuthorization = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"code":"ok","windows_reset":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := NewOpenAIQuotaService(repo, nil, NewOpenAITokenProvider(repo, nil, nil), func(string) (*req.Client, error) {
		return newResetCreditTestClient(server.URL)
	})

	usage, err := service.QueryUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, "oauth-user", usage.UserID)
	require.Equal(t, 1, usage.RateLimitResetCredits.AvailableCount)

	reset, err := service.ResetCredit(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, "ok", reset.Code)
	require.Equal(t, "Bearer oauth-quota-secret", usageAuthorization)
	require.Equal(t, usageAuthorization, detailAuthorization)
	require.Equal(t, usageAuthorization, resetAuthorization)
}

func TestOpenAIQuotaAgentIdentityRedactsTerminalUsageError(t *testing.T) {
	_, privateKey := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, privateKey)
	account.ID = 204
	account.Credentials["task_id"] = "task-quota-error"
	account.Credentials["chatgpt_account_id"] = "account-quota-error"
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/wham/usage") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key","message":"task-quota-error runtime-test-1 AgentAssertion leaked"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	service := NewOpenAIQuotaService(repo, nil, nil, func(string) (*req.Client, error) {
		return newResetCreditTestClient(server.URL)
	})
	_, err := service.QueryUsage(context.Background(), account.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "OPENAI_QUOTA_UPSTREAM_ERROR")
	require.NotContains(t, err.Error(), "task-quota-error")
	require.NotContains(t, err.Error(), "runtime-test-1")
	require.NotContains(t, err.Error(), "AgentAssertion leaked")
	require.Contains(t, err.Error(), "[redacted]")
}

func TestOpenAIQuotaAgentIdentityRedactsTerminalResetError(t *testing.T) {
	_, privateKey := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, privateKey)
	account.ID = 205
	account.Credentials["task_id"] = "task-reset-error"
	account.Credentials["chatgpt_account_id"] = "account-reset-error"
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/wham/rate-limit-reset-credits/consume") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key","message":"task-reset-error runtime-test-1 AgentAssertion leaked"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	service := NewOpenAIQuotaService(repo, nil, nil, func(string) (*req.Client, error) {
		return newResetCreditTestClient(server.URL)
	})
	_, err := service.ResetCredit(context.Background(), account.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "OPENAI_QUOTA_RESET_UPSTREAM_ERROR")
	require.NotContains(t, err.Error(), "task-reset-error")
	require.NotContains(t, err.Error(), "runtime-test-1")
	require.NotContains(t, err.Error(), "AgentAssertion leaked")
	require.Contains(t, err.Error(), "[redacted]")
}

func TestOpenAIQuotaHelpersHandleMissingAccountsAndInvalidCredentials(t *testing.T) {
	ctx := context.Background()
	service := &OpenAIQuotaService{}
	require.False(t, service.isAgentIdentityAccount(ctx, 1))
	var nilService *OpenAIQuotaService
	require.Error(t, nilService.recoverAgentIdentityTask(ctx, 1, "task"))
	headers, taskID, err := service.buildCodexQuotaHeaders(ctx, 1, "token", "account")
	require.NoError(t, err)
	require.Empty(t, taskID)
	require.Equal(t, "Bearer token", headers["authorization"])
	require.Equal(t, "body", service.redactQuotaErrorBody(ctx, 1, "body"))

	missingRepo := &agentIdentityRepoStub{}
	service = &OpenAIQuotaService{accountRepo: missingRepo}
	_, _, err = service.buildCodexQuotaHeaders(ctx, 1, "", "account")
	require.Error(t, err)
	_, _, err = service.buildCodexQuotaHeaders(ctx, 1, "token", "account")
	require.NoError(t, err)
	require.Error(t, service.recoverAgentIdentityTask(ctx, 1, "task"))
	require.Equal(t, "body", service.redactQuotaErrorBody(ctx, 1, "body"))

	account := &Account{
		ID:       206,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "oauth-account",
		},
	}
	normalRepo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
	service = &OpenAIQuotaService{accountRepo: normalRepo}
	require.False(t, service.isAgentIdentityAccount(ctx, account.ID))
	require.NoError(t, service.recoverAgentIdentityTask(ctx, account.ID, "task"))

	invalidAgent := newAgentIdentityTestAccount(t, "invalid-private-key")
	invalidAgent.ID = 207
	invalidAgent.Credentials["task_id"] = "task-invalid-key"
	normalRepo.accountsByID[invalidAgent.ID] = invalidAgent
	_, _, err = service.buildCodexQuotaHeaders(ctx, invalidAgent.ID, "", "account")
	require.Error(t, err)
}

func TestOpenAIQuotaPrepareAndRequestErrors(t *testing.T) {
	account := newOpenAIQuotaOAuthTestAccount(208)
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
	factory := func(string) (*req.Client, error) {
		client := req.C()
		client.WrapRoundTripFunc(func(req.RoundTripper) req.RoundTripFunc {
			return func(*req.Request) (*req.Response, error) {
				return nil, errors.New("transport unavailable")
			}
		})
		return client, nil
	}

	service := NewOpenAIQuotaService(repo, nil, nil, factory)
	_, err := service.QueryUsage(context.Background(), account.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "OPENAI_QUOTA_NOT_CONFIGURED")

	delete(account.Credentials, "access_token")
	service = NewOpenAIQuotaService(repo, nil, &OpenAITokenProvider{}, factory)
	_, err = service.QueryUsage(context.Background(), account.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "OPENAI_QUOTA_TOKEN_UNAVAILABLE")

	account.Credentials["access_token"] = "oauth-quota-secret"
	service = NewOpenAIQuotaService(repo, nil, NewOpenAITokenProvider(repo, nil, nil), factory)
	_, err = service.QueryUsage(context.Background(), account.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "OPENAI_QUOTA_REQUEST_FAILED")
	_, err = service.ResetCredit(context.Background(), account.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "OPENAI_QUOTA_RESET_REQUEST_FAILED")
}

func TestOpenAIQuotaAgentIdentityAuthErrors(t *testing.T) {
	account := newAgentIdentityTestAccount(t, "invalid-private-key")
	account.ID = 209
	account.Credentials["task_id"] = "task-invalid-key"
	account.Credentials["chatgpt_account_id"] = "account-invalid-key"
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
	service := NewOpenAIQuotaService(repo, nil, nil, func(string) (*req.Client, error) {
		return req.C(), nil
	})
	_, err := service.QueryUsage(context.Background(), account.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "OPENAI_QUOTA_AUTH_FAILED")
	_, err = service.ResetCredit(context.Background(), account.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "OPENAI_QUOTA_AUTH_FAILED")
	require.Nil(t, service.queryResetCreditDetailsForAccount(context.Background(), req.C(), "", "account", account.ID))
}

func TestOpenAIQuotaAgentIdentityRecoveryFailureIsReturned(t *testing.T) {
	_, privateKey := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, privateKey)
	account.ID = 210
	account.Credentials["task_id"] = "task-quota-old"
	account.Credentials["chatgpt_account_id"] = "account-quota-recovery"
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
		updateErr: errors.New("persist quota task failed"),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if strings.Contains(r.URL.Path, "/task/register") {
			_, _ = w.Write([]byte(`{"task_id":"task-quota-new"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_task_id"}}`))
	}))
	defer server.Close()

	originalAuthURL := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = server.URL
	t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = originalAuthURL })
	service := NewOpenAIQuotaService(repo, nil, nil, func(string) (*req.Client, error) {
		return newResetCreditTestClient(server.URL)
	})

	_, err := service.QueryUsage(context.Background(), account.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "OPENAI_QUOTA_AUTH_FAILED")
	_, err = service.ResetCredit(context.Background(), account.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "OPENAI_QUOTA_AUTH_FAILED")
}

func TestOpenAIQuotaQueryUsageAgentIdentityUsesAssertionAndRecoversInvalidTaskOnce(t *testing.T) {
	_, privateKey := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, privateKey)
	account.ID = 201
	account.Credentials["task_id"] = "task-usage-old"
	account.Credentials["chatgpt_account_id"] = "account-usage"
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}

	usageCalls := 0
	registerCalls := 0
	var usageTaskIDs []string
	var detailTaskID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/task/register"):
			registerCalls++
			_, _ = w.Write([]byte(`{"task_id":"task-usage-new"}`))
		case strings.HasSuffix(r.URL.Path, "/wham/usage"):
			usageCalls++
			usageTaskIDs = append(usageTaskIDs, agentTaskIDFromAuthorization(t, r.Header.Get("Authorization")))
			if usageCalls == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"invalid_task_id"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"user_id":"user-usage","rate_limit":{"allowed":true}}`))
		case strings.HasSuffix(r.URL.Path, "/wham/rate-limit-reset-credits"):
			detailTaskID = agentTaskIDFromAuthorization(t, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"available_count":2,"credits":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	originalAuthURL := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = server.URL
	t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = originalAuthURL })

	service := NewOpenAIQuotaService(repo, nil, nil, func(string) (*req.Client, error) {
		return newResetCreditTestClient(server.URL)
	})
	usage, err := service.QueryUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, "user-usage", usage.UserID)
	require.Equal(t, 2, usage.RateLimitResetCredits.AvailableCount)
	require.Equal(t, 2, usageCalls)
	require.Equal(t, 1, registerCalls)
	require.Equal(t, []string{"task-usage-old", "task-usage-new"}, usageTaskIDs)
	require.Equal(t, "task-usage-new", detailTaskID)
}

func TestOpenAIQuotaResetCreditAgentIdentityUsesAssertionAndRecoversInvalidTaskOnce(t *testing.T) {
	_, privateKey := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, privateKey)
	account.ID = 202
	account.Credentials["task_id"] = "task-reset-old"
	account.Credentials["chatgpt_account_id"] = "account-reset"
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}

	resetCalls := 0
	registerCalls := 0
	var resetTaskIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/task/register"):
			registerCalls++
			_, _ = w.Write([]byte(`{"task_id":"task-reset-new"}`))
		case strings.HasSuffix(r.URL.Path, "/wham/rate-limit-reset-credits/consume"):
			resetCalls++
			resetTaskIDs = append(resetTaskIDs, agentTaskIDFromAuthorization(t, r.Header.Get("Authorization")))
			if resetCalls == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"invalid_task_id"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"ok","windows_reset":2}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	originalAuthURL := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = server.URL
	t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = originalAuthURL })

	service := NewOpenAIQuotaService(repo, nil, nil, func(string) (*req.Client, error) {
		return newResetCreditTestClient(server.URL)
	})
	result, err := service.ResetCredit(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, "ok", result.Code)
	require.Equal(t, 2, result.WindowsReset)
	require.Equal(t, 2, resetCalls)
	require.Equal(t, 1, registerCalls)
	require.Equal(t, []string{"task-reset-old", "task-reset-new"}, resetTaskIDs)
}
