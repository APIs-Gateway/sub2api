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

func TestOpenAIGatewayServiceGrokForwardTransportAndStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID:       712,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token",
			"base_url":     "https://xai.test/v1",
			"expires_at":   time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	provider := NewGrokTokenProvider(nil, &grokUnauthorizedCacheStub{cacheMiss: true}, nil)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	transportService := &OpenAIGatewayService{
		grokTokenProvider: provider,
		httpUpstream:      &httpUpstreamStub{err: errors.New("upstream unavailable")},
	}
	_, err := transportService.forwardGrokResponses(context.Background(), c, account, []byte(`{"model":"grok"}`), "grok", false, time.Now())
	require.Error(t, err)

	streamRecorder := httptest.NewRecorder()
	streamContext, _ := gin.CreateTestContext(streamRecorder)
	streamContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	streamService := &OpenAIGatewayService{
		grokTokenProvider: provider,
		httpUpstream: &httpUpstreamStub{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-stream\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-stream\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\ndata: [DONE]\n\n")),
		}},
	}
	result, err := streamService.forwardGrokResponses(context.Background(), streamContext, account, []byte(`{"model":"grok","stream":true}`), "grok", true, time.Now())
	require.NoError(t, err)
	require.Equal(t, "resp-stream", result.ResponseID)
	require.Equal(t, 1, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func TestOpenAIGatewayServiceGrokForwardUpstreamFailoverAndRequestErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID:       713,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token",
			"base_url":     "https://xai.test/v1",
			"expires_at":   time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	provider := NewGrokTokenProvider(nil, &grokUnauthorizedCacheStub{cacheMiss: true}, nil)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	service := &OpenAIGatewayService{
		grokTokenProvider: provider,
		httpUpstream: &httpUpstreamStub{resp: &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"unauthorized"}}`)),
		}},
	}
	_, err := service.forwardGrokResponses(context.Background(), c, account, []byte(`{"model":"grok"}`), "grok", false, time.Now())
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusUnauthorized, failoverErr.StatusCode)

	badURLAccount := *account
	badURLAccount.Credentials = map[string]any{"access_token": "token", "base_url": "https://xai.test/\ninvalid"}
	service.httpUpstream = &httpUpstreamStub{}
	_, err = service.forwardGrokResponses(context.Background(), c, &badURLAccount, []byte(`{"model":"grok"}`), "grok", false, time.Now())
	require.Error(t, err)
}

func TestOpenAIGatewayServiceGrokHelperBranches(t *testing.T) {
	account := &Account{ID: 714, Platform: PlatformGrok, Type: AccountTypeOAuth}
	(&OpenAIGatewayService{}).handleGrokAccountUpstreamError(context.Background(), nil, http.StatusUnauthorized, nil, nil)
	(&OpenAIGatewayService{}).updateGrokUsageSnapshot(context.Background(), 1, nil)
	(&OpenAIGatewayService{}).tempUnscheduleGrok(context.Background(), nil, time.Minute, "ignored")

	service := &OpenAIGatewayService{}
	service.handleGrokAccountUpstreamError(context.Background(), account, http.StatusUnauthorized, nil, nil)
	_, blocked := service.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, blocked)
	service.handleGrokAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, nil)
	service.handleGrokAccountUpstreamError(context.Background(), account, http.StatusBadRequest, http.Header{}, nil)

	repo := &tokenRefreshAccountRepo{}
	service = &OpenAIGatewayService{accountRepo: repo}
	service.tempUnscheduleGrok(context.Background(), account, time.Minute, "test")
	require.Equal(t, 1, repo.setTempUnschedCalls)
	require.Nil(t, ptrStringOrNil(" "))
}

func TestPatchGrokResponsesBody_StripsReasoningContentNull(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "grok-latest",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking..."}],"content":null,"encrypted_content":null},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello!"}]}
		]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)
	require.True(t, json.Valid(patched))

	input := gjson.GetBytes(patched, "input")
	require.True(t, input.IsArray())

	items := input.Array()
	require.Len(t, items, 3)

	reasoning := items[1]
	require.Equal(t, "reasoning", reasoning.Get("type").String())
	require.True(t, reasoning.Get("summary").Exists(), "summary should be preserved")
	require.False(t, reasoning.Get("content").Exists(), "content: null should be stripped")
}

func TestPatchGrokResponsesBody_KeepsReasoningContentNonNull(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "grok-latest",
		"input": [
			{"type":"reasoning","summary":[{"type":"summary_text","text":"ok"}],"content":"real content"}
		]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)

	reasoning := gjson.GetBytes(patched, "input.0")
	require.Equal(t, "real content", reasoning.Get("content").String(), "non-null content must not be stripped")
}

func TestPatchGrokResponsesBody_MultipleReasoningContentNull(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "grok-latest",
		"input": [
			{"type":"reasoning","summary":[{"type":"summary_text","text":"r1"}],"content":null},
			{"type":"message","role":"user","content":"hi"},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"r2"}],"content":null}
		]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)

	items := gjson.GetBytes(patched, "input").Array()
	require.Len(t, items, 3)

	require.False(t, items[0].Get("content").Exists())
	require.False(t, items[2].Get("content").Exists())
}

func TestIsGrokImageGenerationModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model string
		want  bool
	}{
		{"grok-imagine", true},
		{"grok-imagine-image-quality", true},
		{"grok-imagine-edit", true},
		{"grok-imagine-image-hd", true},
		{"grok-4.5", false},
		{"grok-composer", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			require.Equal(t, tt.want, isGrokImageGenerationModel(tt.model))
		})
	}
}

// forwardGrokResponses 在到达任何需要 token/网络调用的分支之前，先按模型名
// 拒绝生图模型；空映射的 Account 足以驱动到这条检查且不会 panic。
func TestForwardGrokResponsesRejectsImageModel(t *testing.T) {
	t.Parallel()

	account := &Account{ID: 1, Type: AccountTypeOAuth}

	svc := &OpenAIGatewayService{}
	result, err := svc.forwardGrokResponses(context.Background(), nil, account, []byte(`{"model":"grok-imagine","input":"hi"}`), "grok-imagine", false, time.Now())

	require.Nil(t, result)
	require.ErrorContains(t, err, "not available on the Responses endpoint")
}
