//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFetchCodexModelsManifestAgentIdentityRecoversInvalidTaskOnce(t *testing.T) {
	_, privateKey := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, privateKey)
	account.Credentials["task_id"] = "task-models-old"
	account.Credentials["chatgpt_account_id"] = "account-models"
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}

	modelsCalls := 0
	registerCalls := 0
	var taskIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if strings.Contains(r.URL.Path, "/task/register") {
			registerCalls++
			_, _ = w.Write([]byte(`{"task_id":"task-models-new"}`))
			return
		}
		modelsCalls++
		taskIDs = append(taskIDs, agentTaskIDFromAuthorization(t, r.Header.Get("Authorization")))
		if modelsCalls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_task_id"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	originalModelsURL := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	t.Cleanup(func() { chatgptCodexModelsURL = originalModelsURL })
	originalAuthURL := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = server.URL
	t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = originalAuthURL })

	service := &OpenAIGatewayService{accountRepo: repo}
	manifest, err := service.FetchCodexModelsManifest(context.Background(), account, "0.137.0", "")
	require.NoError(t, err)
	require.Equal(t, `{"models":[]}`, string(manifest.Body))
	require.Equal(t, 2, modelsCalls)
	require.Equal(t, 1, registerCalls)
	require.Equal(t, []string{"task-models-old", "task-models-new"}, taskIDs)
}

func TestFetchCodexModelsManifestAgentIdentityRedactsUpstreamErrors(t *testing.T) {
	_, privateKey := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, privateKey)
	account.Credentials["task_id"] = "task-secret"
	account.Credentials["agent_runtime_id"] = "runtime-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":"%s %s %s AgentAssertion leaked"}`, "runtime-secret", "task-secret", privateKey)
	}))
	defer server.Close()

	originalModelsURL := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	t.Cleanup(func() { chatgptCodexModelsURL = originalModelsURL })

	service := &OpenAIGatewayService{}
	_, err := service.FetchCodexModelsManifest(context.Background(), account, "0.137.0", "")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "runtime-secret")
	require.NotContains(t, err.Error(), "task-secret")
	require.NotContains(t, err.Error(), privateKey)
	require.NotContains(t, err.Error(), "AgentAssertion leaked")
	require.Contains(t, err.Error(), "[redacted]")
}
