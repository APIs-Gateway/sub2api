//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const openAIModelNotFound400Body = `{"error":{"type":"invalid_request_error","code":"model_not_found","param":"model","message":"The requested model is unavailable on this account"}}`

func TestOpenAICompatibleModelNotFound400FailoverScope(t *testing.T) {
	svc := &OpenAIGatewayService{accountRepo: &rateLimitAccountRepoStub{}}
	body := []byte(openAIModelNotFound400Body)

	for _, tc := range []struct {
		name    string
		account *Account
		want    bool
	}{
		{name: "openai api key", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, want: true},
		{name: "openai oauth", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, want: true},
		{name: "compatible grok provider", account: &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey}, want: true},
		{name: "anthropic account", account: &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, want: false},
		{name: "gemini account", account: &Account{Platform: PlatformGemini, Type: AccountTypeAPIKey}, want: false},
		{name: "missing account", account: nil, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, svc.shouldFailoverOpenAIUpstreamResponse(
				tc.account, http.StatusBadRequest, "The requested model is unavailable on this account", body,
			))
		})
	}
}

// 其它确定性 400 仍然不切号：参数错误、非 model_not_found 的结构化 code、上下文超限。
func TestOpenAICompatibleNonModel400RemainsTerminal(t *testing.T) {
	svc := &OpenAIGatewayService{accountRepo: &rateLimitAccountRepoStub{}}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	for _, tc := range []struct {
		name string
		msg  string
		body string
	}{
		{name: "invalid parameter", msg: "Invalid value for temperature", body: `{"error":{"code":"invalid_request_error","message":"Invalid value for temperature"}}`},
		{name: "structured code overrides message", msg: "Model not found: x", body: `{"error":{"code":"invalid_request_error","message":"Model not found: x"}}`},
		{name: "cyber policy", msg: "blocked", body: `{"error":{"code":"cyber_policy","message":"blocked"}}`},
		{name: "context window", msg: "Your input exceeds the context window of this model", body: `{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(account, http.StatusBadRequest, tc.msg, []byte(tc.body)))
		})
	}
}

// model-not-found 只在 400 上额外放开；其它状态码继续走既有状态策略。
func TestOpenAICompatibleModelNotFoundOnlyWidens400(t *testing.T) {
	svc := &OpenAIGatewayService{accountRepo: &rateLimitAccountRepoStub{}}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(openAIModelNotFound400Body)

	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(account, http.StatusNotFound, "", body),
		"404 model-not-found keeps its existing (non-status) failover path via HandleUpstreamModelNotFound")
	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(account, http.StatusUnprocessableEntity, "", body))
	require.True(t, svc.shouldFailoverOpenAIUpstreamResponse(account, http.StatusUnauthorized, "", body))
}

// 裸 service（没有账号仓储 = 没有换号方）保持确定性 400，不返回无人消费的 failover 信号。
func TestOpenAICompatibleModelNotFound400WithoutManagedCandidatesRemainsTerminal(t *testing.T) {
	svc := &OpenAIGatewayService{}

	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(
		&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		http.StatusBadRequest,
		"model not found",
		[]byte(`{"error":{"code":"model_not_found","message":"model not found"}}`),
	))
}

func newOpenAIModelNotFoundForwardService(managed bool, respBody string) (*OpenAIGatewayService, *rateLimitAccountRepoStub, *httpUpstreamRecorder) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(respBody)),
	}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	repo := &rateLimitAccountRepoStub{}
	if managed {
		svc.accountRepo = repo
	}
	svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	return svc, repo, upstream
}

// 受管网关：API key 账号回 400 model_not_found → 返回可换号的 failover 错误，
// 不向下游写任何字节，也不处罚当前账号（不 SetError、不临时下线）。
func TestOpenAIForward_ModelNotFound400FailsOverWithoutAccountPenalty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, repo, upstream := newOpenAIModelNotFoundForwardService(true, openAIModelNotFound400Body)
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()

	result, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestNonStreamRequestBody))

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "want UpstreamFailoverError, got %v", err)
	require.Equal(t, http.StatusBadRequest, failoverErr.StatusCode)
	require.JSONEq(t, openAIModelNotFound400Body, string(failoverErr.ResponseBody))
	require.False(t, failoverErr.RetryableOnSameAccount, "the same account cannot serve the model")
	require.False(t, IsResponseCommitted(c), "failover must stay eligible before writing downstream")
	require.Zero(t, recorder.Body.Len())
	require.Len(t, upstream.requests, 1)
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.tempCalls)
}

// 受管网关：普通参数错误 400 仍直接回客户端，不切号。
func TestOpenAIForward_InvalidParameter400StaysTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, repo, _ := newOpenAIModelNotFoundForwardService(true, `{"error":{"code":"invalid_request_error","message":"Invalid value for temperature"}}`)
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()

	_, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestNonStreamRequestBody))

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "invalid parameters must not rotate accounts")
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, repo.setErrorCalls)
}

// 裸 service：没有换号方，model_not_found 400 仍按既有路径直接回 400。
func TestOpenAIForward_ModelNotFound400WithoutManagedRepoStaysTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, _, _ := newOpenAIModelNotFoundForwardService(false, openAIModelNotFound400Body)
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()

	_, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestNonStreamRequestBody))

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func newModelNotFoundResolveContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c
}

// 切号用尽后的 400 model_not_found：状态码保持 400，对外给脱敏后的上游文案
// （而不是误导性的「请检查请求参数」），ops 仍记录真实上游状态码。
func TestResolveUpstreamErrorResponse_OpenAIModelNotFound400ShowsSanitizedMessage(t *testing.T) {
	c := newModelNotFoundResolveContext()
	body := []byte(`{"error":{"code":"model_not_found","message":"Model not found; inspect https://upstream.example/debug?access_token=super-secret-value&model=x"}}`)

	status, errType, msg := ResolveUpstreamErrorResponse(c, PlatformOpenAI, http.StatusBadRequest, body)

	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "invalid_request_error", errType)
	require.NotContains(t, msg, "super-secret-value")
	require.Contains(t, msg, "access_token=***")
	require.Contains(t, msg, "Model not found")
	recordedStatus, ok := c.Get(OpsUpstreamStatusCodeKey)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, recordedStatus)
}

func TestResolveUpstreamErrorResponse_ModelNotFoundPassthroughScope(t *testing.T) {
	fixed := upstreamClientMessageForStatus(http.StatusBadRequest)

	t.Run("非 model-not-found 的 400 仍用固定安全文案", func(t *testing.T) {
		status, _, msg := ResolveUpstreamErrorResponse(newModelNotFoundResolveContext(), PlatformOpenAI, http.StatusBadRequest,
			[]byte(`{"error":{"code":"invalid_request_error","message":"secret internal detail"}}`))
		require.Equal(t, http.StatusBadRequest, status)
		require.Equal(t, fixed, msg)
	})

	t.Run("非 OpenAI 平台不放开", func(t *testing.T) {
		_, _, msg := ResolveUpstreamErrorResponse(newModelNotFoundResolveContext(), PlatformAnthropic, http.StatusBadRequest,
			[]byte(`{"error":{"code":"model_not_found","message":"model not found"}}`))
		require.Equal(t, fixed, msg)
	})

	t.Run("只有 code 没有文案时回退固定文案", func(t *testing.T) {
		status, _, msg := ResolveUpstreamErrorResponse(newModelNotFoundResolveContext(), PlatformOpenAI, http.StatusBadRequest,
			[]byte(`{"error":{"code":"model_not_found"}}`))
		require.Equal(t, http.StatusBadRequest, status)
		require.Equal(t, fixed, msg)
	})

	t.Run("401 model-not-found 仍按鉴权类映射", func(t *testing.T) {
		status, _, msg := ResolveUpstreamErrorResponse(newModelNotFoundResolveContext(), PlatformOpenAI, http.StatusUnauthorized,
			[]byte(`{"error":{"code":"model_not_found","message":"model not found"}}`))
		require.Equal(t, http.StatusBadGateway, status)
		require.NotContains(t, msg, "model not found")
	})
}
