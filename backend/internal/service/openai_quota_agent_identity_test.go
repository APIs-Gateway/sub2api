//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

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
