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

	"github.com/Wei-Shaw/sub2api/internal/config"
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

// 上游同一 commit 的测试还覆盖了一个 "Responses Lite additional tools"（把
// input 里 additional_tools 项的嵌套 tools 提升为顶层 tools）场景，但那是
// patchGrokResponsesBodyBase 更大流水线里另一个步骤的行为，不属于
// stripRedundantGrokViewImageTool 本身；fork 的 patchGrokResponsesBody 没有
// 那个前置提升步骤，因此只移植顶层 tools 这一个场景。
func TestPatchGrokResponsesBodyDropsRedundantViewImageForCurrentInlineImage(t *testing.T) {
	t.Parallel()

	body := `{
		"model":"grok-4.6",
		"input":[{"type":"message","role":"user","content":[
			{"type":"input_text","text":"What text is in this image?"},
			{"type":"input_image","image_url":"data:image/png;base64,AA=="}
		]}],
		"tools":[
			{"type":"function","name":"view_image","parameters":{"type":"object"}},
			{"type":"function","name":"shell_command","parameters":{"type":"object"}}
		]
	}`

	patched, err := patchGrokResponsesBody([]byte(body), "grok-4.6")
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, `tools.#(name=="view_image")`).Exists())
	require.Equal(t, "shell_command", gjson.GetBytes(patched, "tools.0.name").String())
}

func TestPatchGrokResponsesBodyKeepsNonRedundantViewImage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "current turn has no inline image",
			body: `{"input":[{"role":"user","content":[{"type":"input_text","text":"Inspect a local image"}]}],"tools":[{"type":"function","name":"view_image"}]}`,
		},
		{
			name: "inline image is only historical",
			body: `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]},{"role":"assistant","content":[{"type":"output_text","text":"Done"}]},{"role":"user","content":[{"type":"input_text","text":"Inspect another local image"}]}],"tools":[{"type":"function","name":"view_image"}]}`,
		},
		{
			name: "view image is explicitly selected",
			body: `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}],"tools":[{"type":"function","name":"view_image"}],"tool_choice":{"type":"function","name":"view_image"}}`,
		},
		{
			name: "required with view image as the only tool",
			body: `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}],"tools":[{"type":"function","name":"view_image"}],"tool_choice":"required"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			patched, err := patchGrokResponsesBody([]byte(tt.body), "grok-4.6")
			require.NoError(t, err)
			require.Equal(t, "view_image", gjson.GetBytes(patched, "tools.0.name").String())
		})
	}
}

// stripRedundantGrokViewImageTool（Responses 侧）只清 tools/parallel_tool_calls，
// 不清 tool_choice——这与 Chat 侧的 stripRedundantGrokChatViewImageTool 不同
// （Chat 侧额外在 tool_choice=="auto" 时把它也删掉），是上游这两个函数本身就有
// 的差异，不是移植遗漏。
func TestPatchGrokResponsesBodyDropsViewImageOnlyToolMetadata(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}],
		"tools":[{"type":"function","name":"view_image"}],
		"tool_choice":"auto",
		"parallel_tool_calls":true
	}`)
	patched, err := patchGrokResponsesBody(body, "grok-4.6")
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, "tools").Exists())
	require.Equal(t, "auto", gjson.GetBytes(patched, "tool_choice").String())
	require.False(t, gjson.GetBytes(patched, "parallel_tool_calls").Exists())
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

// 覆盖 forwardGrokResponses 里新增的 maxLineSize 接线：reqStream=true 时用
// s.cfg.Gateway.MaxLineSize（若配置了正值）而不是硬编码的 defaultMaxLineSize，
// 传给 newGrokResponsesBillingPingFilterBody。
func TestOpenAIGatewayServiceForwardGrokResponsesStreamingUsesConfiguredMaxLineSize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID:       706,
		Name:     "grok-forward-stream",
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"base_url": "https://xai.test/v1",
		},
	}
	provider := NewGrokTokenProvider(nil, &grokUnauthorizedCacheStub{}, nil)
	upstreamBody := strings.Join([]string{
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp-grok-stream-1","usage":{"input_tokens":3,"output_tokens":5}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamStub{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"req-grok-stream-1"},
		},
		Body: io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: provider,
		toolCorrector:     NewCodexToolCorrector(),
		cfg:               &config.Config{Gateway: config.GatewayConfig{MaxLineSize: 32 * 1024}},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("OpenAI-Beta", "responses=v1")

	result, err := svc.forwardGrokResponses(context.Background(), c, account, []byte(`{"model":"grok-4.3","stream":true}`), "grok-4.3", true, time.Now())
	require.NoError(t, err)
	require.Equal(t, "resp-grok-stream-1", result.ResponseID)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
}

func TestStripRedundantGrokViewImageToolLeavesEmptyOrMissingInputAlone(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "input is an empty array", body: `{"input":[]}`},
		{name: "input is not an array", body: `{"input":"not-an-array"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(tt.body)
			patched, err := stripRedundantGrokViewImageTool(body)
			require.NoError(t, err)
			require.Equal(t, body, patched)
		})
	}
}

// tool_choice.name 为空时回退读 tool_choice.function.name——与 Chat 侧
// stripRedundantGrokChatViewImageTool 的回退顺序（function.name 优先、name 兜底）
// 刚好相反，这是 Responses 与 Chat 两种 tool_choice 形状本身的差异，不是移植遗漏。
func TestStripRedundantGrokViewImageToolFallsBackToFunctionName(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}],
		"tools":[{"type":"function","name":"view_image"}],
		"tool_choice":{"type":"function","function":{"name":"view_image"}}
	}`)
	patched, err := stripRedundantGrokViewImageTool(body)
	require.NoError(t, err)
	require.Equal(t, body, patched)
}

func TestStripRedundantGrokViewImageToolLeavesMissingOrNonArrayToolsAlone(t *testing.T) {
	t.Parallel()
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}]}`)
	patched, err := stripRedundantGrokViewImageTool(body)
	require.NoError(t, err)
	require.Equal(t, body, patched)
}

func TestStripRedundantGrokViewImageToolNoViewImagePresentLeavesToolsUnchanged(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}],
		"tools":[{"type":"function","name":"shell_command"}]
	}`)
	patched, err := stripRedundantGrokViewImageTool(body)
	require.NoError(t, err)
	require.Equal(t, body, patched)
}
