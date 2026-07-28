//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestPatchGrokResponsesBodySetsMappedModelAndDropsUnsupportedFields(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "grok",
		"input": "hello",
		"prompt_cache_retention": "24h",
		"safety_identifier": "user-1",
		"reasoning": {"effort": "high"}
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.3")
	require.NoError(t, err)
	require.True(t, json.Valid(patched))
	require.Equal(t, "grok-4.3", gjson.GetBytes(patched, "model").String())
	require.False(t, gjson.GetBytes(patched, "prompt_cache_retention").Exists())
	require.False(t, gjson.GetBytes(patched, "safety_identifier").Exists())
	require.Equal(t, "high", gjson.GetBytes(patched, "reasoning.effort").String())
	_, err = patchGrokResponsesBody([]byte("not-json"), "grok-4.3")
	require.EqualError(t, err, "invalid json request body")
}

func TestBuildGrokResponsesRequestUsesAccountBaseURLAndBearerToken(t *testing.T) {
	t.Parallel()

	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"base_url": "https://xai.test/v1/",
		},
	}

	req, err := buildGrokResponsesRequest(context.Background(), nil, account, []byte(`{"model":"grok-4.3"}`), "access-token")
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, req.Method)
	require.Equal(t, "https://xai.test/v1/responses", req.URL.String())
	require.Equal(t, "Bearer access-token", req.Header.Get("Authorization"))
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))
	require.Contains(t, req.Header.Get("Accept"), "text/event-stream")

	data, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, `{"model":"grok-4.3"}`, strings.TrimSpace(string(data)))
}

func TestBuildGrokResponsesRequestCopiesOpenAIBetaHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	c.Request.Header.Set("OpenAI-Beta", "responses=v1")
	req, err := buildGrokResponsesRequest(context.Background(), c, &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"base_url": "https://xai.test/v1"},
	}, []byte(`{"model":"grok-4.3"}`), "token")
	require.NoError(t, err)
	require.Equal(t, "responses=v1", req.Header.Get("OpenAI-Beta"))
}

func TestOpenAIGatewayServiceUpdateGrokUsageSnapshot(t *testing.T) {
	repo := &snapshotUpdateAccountRepo{updateExtraCalls: make(chan map[string]any, 1)}
	svc := &OpenAIGatewayService{accountRepo: repo}
	svc.updateGrokUsageSnapshot(context.Background(), 706, &xai.QuotaSnapshot{StatusCode: http.StatusOK})
	select {
	case updates := <-repo.updateExtraCalls:
		require.Contains(t, updates, grokQuotaSnapshotExtraKey)
	default:
		t.Fatal("expected Grok quota snapshot persistence")
	}
	svc.updateGrokUsageSnapshot(context.Background(), 0, nil)
}

func TestOpenAIGatewayServiceGrokUpstreamErrorCooldowns(t *testing.T) {
	account := &Account{ID: 708, Platform: PlatformGrok, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{}

	for _, tc := range []struct {
		status  int
		headers http.Header
	}{
		{status: http.StatusForbidden},
		{status: http.StatusTooManyRequests, headers: http.Header{"Retry-After": []string{"7"}}},
		{status: http.StatusBadGateway},
	} {
		svc.handleGrokAccountUpstreamError(context.Background(), account, tc.status, tc.headers, nil)
		until, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
		require.True(t, ok)
		require.IsType(t, time.Time{}, until)
	}
}

func TestOpenAIGatewayServiceForwardGrokResponsesNonStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID:       705,
		Name:     "grok-forward",
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"base_url": "https://xai.test/v1",
		},
	}
	provider := NewGrokTokenProvider(nil, &grokUnauthorizedCacheStub{}, nil)
	upstream := &httpUpstreamStub{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"req-grok-1"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp-grok-1","model":"grok-4.3","output":[],"usage":{"input_tokens":3,"output_tokens":2}}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream, grokTokenProvider: provider}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("OpenAI-Beta", "responses=v1")

	result, err := svc.forwardGrokResponses(context.Background(), c, account, []byte(`{"model":"grok-4.3","reasoning":{"effort":"high"}}`), "grok-4.3", false, time.Now())
	require.NoError(t, err)
	require.Equal(t, "req-grok-1", result.RequestID)
	require.Equal(t, "resp-grok-1", result.ResponseID)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Equal(t, "high", *result.ReasoningEffort)
	require.Contains(t, recorder.Body.String(), "resp-grok-1")
}

func TestOpenAIGatewayServiceForwardGrokResponsesRejectsNonOAuth(t *testing.T) {
	svc := &OpenAIGatewayService{}
	_, err := svc.forwardGrokResponses(context.Background(), nil, &Account{Type: AccountTypeAPIKey}, []byte(`{}`), "grok-4.3", false, time.Now())
	require.EqualError(t, err, "grok account type apikey is not supported by subscription forwarding")
}

func TestOpenAIGatewayServiceGrokUnauthorizedRefreshesBeforeBlocking(t *testing.T) {
	account := &Account{
		ID:       703,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "refresh-token",
		},
	}
	cache := &grokUnauthorizedCacheStub{}
	executor := &grokUnauthorizedRefreshExecutor{credentials: map[string]any{
		"access_token":  "new-token",
		"refresh_token": "refresh-token-2",
	}}
	provider, repo := newGrokUnauthorizedProvider(account, executor, cache)
	svc := &OpenAIGatewayService{accountRepo: repo, grokTokenProvider: provider}

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, nil)

	_, blocked := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.False(t, blocked)
	require.Equal(t, "new-token", account.GetGrokAccessToken())
}

func TestOpenAIGatewayServiceGrokUnauthorizedRefreshFailureBlocks(t *testing.T) {
	account := &Account{
		ID:       704,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "refresh-token",
		},
	}
	cache := &grokUnauthorizedCacheStub{}
	executor := &grokUnauthorizedRefreshExecutor{refreshErr: errors.New("invalid_grant")}
	provider, repo := newGrokUnauthorizedProvider(account, executor, cache)
	svc := &OpenAIGatewayService{accountRepo: repo, grokTokenProvider: provider}

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, nil)

	blocked, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, ok)
	require.IsType(t, time.Time{}, blocked)
}
