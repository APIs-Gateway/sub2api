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

func TestPatchGrokResponsesBodyDropsOrphanToolChoice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		body           string
		wantToolChoice bool
	}{
		{
			name: "no tools with string tool_choice is dropped",
			body: `{"input":"hello","tool_choice":"auto"}`,
		},
		{
			name: "no tools with object tool_choice is dropped",
			body: `{"input":"hello","tool_choice":{"type":"function","name":"lookup"}}`,
		},
		{
			name:           "tools present keeps tool_choice",
			body:           `{"input":"hello","tools":[{"type":"function","name":"lookup"}],"tool_choice":"auto"}`,
			wantToolChoice: true,
		},
		{
			name: "no tools and no tool_choice stays absent",
			body: `{"input":"hello"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patched, err := patchGrokResponsesBody([]byte(tt.body), "grok-4.3")
			require.NoError(t, err)
			require.True(t, json.Valid(patched))
			require.Equal(t, tt.wantToolChoice, gjson.GetBytes(patched, "tool_choice").Exists())
		})
	}
}

// stripRedundantGrokViewImageTool 本身只清 tools/parallel_tool_calls，不碰
// tool_choice——这与 Chat 侧的 stripRedundantGrokChatViewImageTool 不同（Chat
// 侧额外在 tool_choice=="auto" 时把它也删掉），是上游这两个函数本身就有的差异，
// 不是移植遗漏。但 patchGrokResponsesBody 的管线里，strip 之后紧接着有一段
// orphan tool_choice 清理（tools 不存在但 tool_choice 存在时删除 tool_choice），
// 所以 strip 把唯一的 view_image 工具连同 tools 一起删掉后，残留的 tool_choice
// 会被这一步兜底清掉，而不是原样透传给 xAI（xAI 对「有 tool_choice、没有
// tools」的 Responses 请求会直接 400）。
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
	require.False(t, gjson.GetBytes(patched, "tool_choice").Exists())
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

func TestBuildGrokCompactRequestBodyUsesResponsesCompactionTurn(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"tools":[{"type":"function","name":"shell"}],"stream":true}`)

	patched, err := buildGrokCompactRequestBody(body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, "stream").Bool())
	require.False(t, gjson.GetBytes(patched, "store").Bool())
	require.Equal(t, "none", gjson.GetBytes(patched, "tool_choice").String())
	require.Equal(t, "reasoning.encrypted_content", gjson.GetBytes(patched, "include.0").String())
	require.Equal(t, "hello", gjson.GetBytes(patched, "input.0.content.0.text").String())
	prompt := gjson.GetBytes(patched, "input.1.content.0.text").String()
	require.Contains(t, prompt, "1. Primary Request and Intent")
	require.Contains(t, prompt, "9. Optional Next Step")
	require.Contains(t, prompt, "Respond with ONLY the <summary>...</summary> block")
	require.NotContains(t, prompt, "<summary_request>")
}

func TestConvertGrokResponseToOpenAICompact(t *testing.T) {
	body := []byte(`{
		"id":"resp_grok_1",
		"object":"response",
		"status":"completed",
		"model":"grok-4.5",
		"output":[
			{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"grok-encrypted-state"},
			{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"summary text"}]}
		],
		"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}
	}`)

	converted, err := convertGrokResponseToOpenAICompact(body)
	require.NoError(t, err)
	require.Equal(t, "resp_grok_1", gjson.GetBytes(converted, "id").String())
	require.Len(t, gjson.GetBytes(converted, "output").Array(), 1)
	require.Equal(t, "compaction", gjson.GetBytes(converted, "output.0.type").String())
	require.Equal(t, "grok-encrypted-state", gjson.GetBytes(converted, "output.0.encrypted_content").String())
	require.Equal(t, "summary text", gjson.GetBytes(converted, "output.0.summary.0.text").String())
	require.Equal(t, int64(14), gjson.GetBytes(converted, "usage.total_tokens").Int())
}

func TestPatchGrokResponsesBodyRestoresCompactInput(t *testing.T) {
	body := []byte(`{
		"model":"grok-4.5",
		"input":[
			{"id":"cmp_1","type":"compaction","status":"completed","encrypted_content":"grok-encrypted-state","summary":[{"type":"summary_text","text":"summary text"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
		]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)
	require.Equal(t, "reasoning", gjson.GetBytes(patched, "input.0.type").String())
	require.Equal(t, "grok-encrypted-state", gjson.GetBytes(patched, "input.0.encrypted_content").String())
	require.Equal(t, "message", gjson.GetBytes(patched, "input.1.type").String())
	require.Contains(t, gjson.GetBytes(patched, "input.1.content.0.text").String(), "summary text")
	require.Equal(t, "continue", gjson.GetBytes(patched, "input.2.content.0.text").String())
}

func TestConvertGrokResponseToOpenAICompactRequiresEncryptedContent(t *testing.T) {
	_, err := convertGrokResponseToOpenAICompact([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"summary"}]}]}`))
	require.ErrorContains(t, err, "reasoning.encrypted_content")
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

func TestIsGrokContentPolicyRejection(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "new sensitive code",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"new_sensitive","message":"image is sensitive"}}`,
			want:   true,
		},
		{
			name:   "moderation feature unavailable",
			status: http.StatusForbidden,
			body:   `{"error":{"message":"The moderation feature is not available for this request"}}`,
			want:   true,
		},
		{
			name:   "entitlement forbidden",
			status: http.StatusForbidden,
			body:   `{"error":{"message":"subscription required"}}`,
			want:   false,
		},
		{
			name:   "structured account suspension overrides policy reason",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"account_suspended","reason":"policy_violation","message":"account suspended due to policy violation"}}`,
			want:   false,
		},
		{
			name:   "ambiguous policy violation code is not enough",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"policy_violation","message":"policy violation"}}`,
			want:   false,
		},
		{
			name:   "policy violation with request scoped message",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"policy_violation","message":"request blocked by policy"}}`,
			want:   true,
		},
		{
			name:   "wrong status",
			status: http.StatusBadRequest,
			body:   `{"error":{"code":"new_sensitive"}}`,
			want:   false,
		},
		{
			name:   "empty body",
			status: http.StatusForbidden,
			body:   "",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isGrokContentPolicyRejection(tt.status, []byte(tt.body)))
		})
	}
}

func TestHandleGrokAccountUpstreamErrorSkipsContentPolicyRejection(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 720, Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"code":"new_sensitive","message":"text is sensitive"}}`)

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusForbidden, nil, body)

	require.Zero(t, repo.setTempUnschedCalls)
	_, blocked := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.False(t, blocked)
	require.False(t, svc.shouldFailoverGrokUpstreamError(http.StatusForbidden, body))
}

func TestHandleGrokAccountUpstreamError403UsesConfiguredForbiddenRule(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{
		ID:       721,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusForbidden),
					"keywords":         []any{"subscription"},
					"duration_minutes": float64(7),
				},
			},
		},
	}
	before := time.Now()

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusForbidden, nil,
		[]byte(`{"error":{"message":"subscription required"}}`),
	)

	require.Equal(t, 1, repo.setTempUnschedCalls)
	_, blocked := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, blocked)
	until, _ := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.WithinDuration(t, before.Add(7*time.Minute), until.(time.Time), time.Second)
}

func TestHandleGrokAccountUpstreamError5xxRespectsPoolMode(t *testing.T) {
	t.Run("pool mode keeps scheduling state", func(t *testing.T) {
		repo := &tokenRefreshAccountRepo{}
		svc := &OpenAIGatewayService{accountRepo: repo}
		account := &Account{
			ID:          724,
			Platform:    PlatformGrok,
			Type:        AccountTypeAPIKey,
			Credentials: map[string]any{"pool_mode": true},
		}

		svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusBadGateway, nil, nil)

		require.Zero(t, repo.setTempUnschedCalls)
		_, blocked := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
		require.False(t, blocked)
	})

	t.Run("non-pool mode keeps two minute cooldown", func(t *testing.T) {
		repo := &tokenRefreshAccountRepo{}
		svc := &OpenAIGatewayService{accountRepo: repo}
		account := &Account{ID: 725, Platform: PlatformGrok, Type: AccountTypeAPIKey}
		before := time.Now()

		svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusBadGateway, nil, nil)

		require.Equal(t, 1, repo.setTempUnschedCalls)
		until, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
		require.True(t, ok)
		require.WithinDuration(t, before.Add(2*time.Minute), until.(time.Time), time.Second)
	})
}

func TestHandleGrokAccountUpstreamErrorPoolModeSkipsAllDefaultStateChanges(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{
		ID:          726,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"pool_mode": true},
	}

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusTooManyRequests,
		http.Header{"Retry-After": []string{"45"}}, nil,
	)

	require.Zero(t, repo.setTempUnschedCalls)
	_, blocked := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.False(t, blocked)
}

func TestHandleGrokAccountUpstreamErrorPoolModeStillHonorsConfiguredForbiddenRule(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{
		ID:       727,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode":                  true,
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusForbidden),
					"keywords":         []any{"subscription"},
					"duration_minutes": float64(7),
				},
			},
		},
	}
	before := time.Now()

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusForbidden, nil,
		[]byte(`{"error":{"message":"subscription required"}}`),
	)

	require.Equal(t, 1, repo.setTempUnschedCalls)
	until, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, ok)
	require.WithinDuration(t, before.Add(7*time.Minute), until.(time.Time), time.Second)
}

func TestHandleGrokAccountUpstreamError402CooldownsForThirtyMinutes(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 723, Platform: PlatformGrok, Type: AccountTypeOAuth}
	before := time.Now()

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusPaymentRequired, nil, nil)

	require.Equal(t, 1, repo.setTempUnschedCalls)
	until, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, ok)
	require.WithinDuration(t, before.Add(30*time.Minute), until.(time.Time), time.Second)
}

func TestHandleGrokAccountUpstreamError403ConfiguredRuleUnmatchedKeepsDefaultCooldown(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{
		ID:       722,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusForbidden),
					"keywords":         []any{"different failure"},
					"duration_minutes": float64(7),
				},
			},
		},
	}
	before := time.Now()

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusForbidden, nil,
		[]byte(`{"error":{"message":"subscription required"}}`),
	)

	require.Equal(t, 1, repo.setTempUnschedCalls)
	until, _ := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.WithinDuration(t, before.Add(30*time.Minute), until.(time.Time), time.Second)
}

func TestShouldFailoverGrokUpstreamError_DelegatesToDefaultForNonContentPolicy(t *testing.T) {
	svc := &OpenAIGatewayService{}

	require.True(t, svc.shouldFailoverGrokUpstreamError(http.StatusInternalServerError, nil), "5xx must remain eligible for account failover")
	require.False(t, svc.shouldFailoverGrokUpstreamError(http.StatusBadRequest, nil), "generic 400 must not trigger failover")
	require.True(t, svc.shouldFailoverGrokUpstreamError(http.StatusForbidden, nil), "403 without a content-policy body still delegates to the default rule")
}

func TestNormalizeGrokErrorMarker(t *testing.T) {
	require.Equal(t, "new_sensitive", normalizeGrokErrorMarker("New-Sensitive"))
	require.Equal(t, "content_policy_violation", normalizeGrokErrorMarker(" Content Policy Violation "))
	require.Equal(t, "account_suspended", normalizeGrokErrorMarker("ACCOUNT-SUSPENDED"))
}

func TestGrokStructuredMarkers_RecurseThroughArraysAndNestedObjects(t *testing.T) {
	t.Run("content policy marker inside array", func(t *testing.T) {
		body := []byte(`{"errors":[{"unrelated":true},{"code":"New-Sensitive"}]}`)
		require.True(t, isGrokContentPolicyRejection(http.StatusForbidden, body))
	})

	t.Run("account access marker inside array", func(t *testing.T) {
		body := []byte(`{"errors":[{"reason":"unrelated"},{"error_code":"ACCOUNT_SUSPENDED"}]}`)
		require.False(t, isGrokContentPolicyRejection(http.StatusForbidden, body),
			"account-access marker anywhere in the payload must suppress the content-policy classification")
	})

	t.Run("content policy marker nested two levels deep", func(t *testing.T) {
		body := []byte(`{"error":{"details":{"category":"content-moderation"}}}`)
		require.True(t, isGrokContentPolicyRejection(http.StatusForbidden, body))
	})

	t.Run("account access marker nested two levels deep suppresses classification", func(t *testing.T) {
		body := []byte(`{"error":{"details":{"type":"plan_required"},"code":"new_sensitive"}}`)
		require.False(t, isGrokContentPolicyRejection(http.StatusForbidden, body))
	})

	t.Run("no marker anywhere returns false", func(t *testing.T) {
		body := []byte(`{"errors":[{"unrelated":"x"},{"nested":{"also_unrelated":1}}]}`)
		require.False(t, isGrokContentPolicyRejection(http.StatusForbidden, body))
	})
}

func TestApplyGrokForbiddenPolicy_ReturnsFalseWhenTempUnschedulableDisabled(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 728, Platform: PlatformGrok, Type: AccountTypeOAuth}

	require.False(t, svc.applyGrokForbiddenPolicy(context.Background(), account, []byte(`{"error":{"message":"subscription required"}}`)))
}

func TestApplyGrokForbiddenPolicy_UsesRateLimitServiceAccountRepoWhenAvailable(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	svc := &OpenAIGatewayService{
		accountRepo:      repo,
		rateLimitService: NewRateLimitService(repo, nil, nil, nil, nil),
	}
	account := &Account{
		ID:       729,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusForbidden),
					"keywords":         []any{"subscription"},
					"duration_minutes": float64(9),
				},
			},
		},
	}

	handled := svc.applyGrokForbiddenPolicy(context.Background(), account, []byte(`{"error":{"message":"subscription required"}}`))

	require.True(t, handled)
	require.Equal(t, 1, repo.setTempUnschedCalls, "should route through rateLimitService.tryTempUnschedulable -> accountRepo.SetTempUnschedulable")
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
