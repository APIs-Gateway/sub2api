//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 上游模型不一致拦截：Responses 主路径（Codex transform）与 passthrough 路径，
// 流式 / 非流式 / SSE→JSON 各分支的端到端行为。
// 夹具复用 openai_gateway_responses_empty_completed_test.go（issue #5009）。

const upstreamModelMismatchTestRequestBody = `{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
const upstreamModelMismatchTestNonStreamRequestBody = `{"model":"gpt-5.6-sol","stream":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`

func upstreamModelMismatchSSEBody(createdModel, completedModel, delta string) string {
	created := `{"type":"response.created","response":{"id":"r1","object":"response","status":"in_progress"`
	if createdModel != "" {
		created += `,"model":"` + createdModel + `"`
	}
	created += `}}`
	completed := `{"type":"response.completed","response":{"id":"r1","object":"response","status":"completed","model":"` + completedModel + `","usage":{"input_tokens":596,"output_tokens":5,"total_tokens":601}}}`
	return "data: " + created + "\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + delta + "\"}\n\n" +
		"data: " + completed + "\n\n"
}

func newUpstreamModelMismatchSSEUpstream(body string) *httpUpstreamRecorder {
	return &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}}
}

func newUpstreamModelMismatchJSONUpstream(model string) *httpUpstreamRecorder {
	body := `{"id":"r1","object":"response","model":"` + model + `","status":"completed",` +
		`"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer"}]}],` +
		`"usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}`
	return &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}}
}

func requireUpstreamModelMismatchFailover(t *testing.T, err error) *UpstreamFailoverError {
	t.Helper()
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "expected UpstreamFailoverError, got: %v", err)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, "upstream_model_mismatch", gjson.GetBytes(failoverErr.ResponseBody, "error.code").String())
	return failoverErr
}

// passthrough 流式：response.created 带 gpt-6-sol → failover，客户端零字节
func TestUpstreamModelMismatch_PassthroughStreamFailsOverBeforeOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newUpstreamModelMismatchSSEUpstream(upstreamModelMismatchSSEBody("gpt-6-sol", "gpt-6-sol", "leak"))
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()
	account.Extra = map[string]any{"openai_responses_supported": true, "openai_passthrough": true}

	_, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestRequestBody))
	requireUpstreamModelMismatchFailover(t, err)
	require.Empty(t, recorder.Body.String(), "no upstream bytes may leak")

	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.Equal(t, "gpt-5.6-sol", mark.SentModel)
	require.Equal(t, "gpt-6-sol", mark.ResponseModel)
	require.Equal(t, account.ID, mark.AccountID)
	require.True(t, mark.Stream)
}

// 账号级映射 sol→luna，上游返回 luna → 放行（交接文档 §1.2(b)，账号 3233）
func TestUpstreamModelMismatch_AccountMappingIsExempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newUpstreamModelMismatchSSEUpstream(upstreamModelMismatchSSEBody("gpt-5.6-luna", "gpt-5.6-luna", "ok"))
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"gpt-5.6-sol": "gpt-5.6-luna"}

	result, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestRequestBody))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "gpt-5.6-luna", gjson.GetBytes(upstream.lastBody, "model").String(), "mapped model must be what we sent upstream")
	require.Contains(t, recorder.Body.String(), "ok")
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

// 主路径（非 passthrough）流式不一致 → failover，客户端零字节
func TestUpstreamModelMismatch_CodexStreamFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newUpstreamModelMismatchSSEUpstream(upstreamModelMismatchSSEBody("gpt-6-sol", "gpt-6-sol", "leak"))
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()

	_, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestRequestBody))
	requireUpstreamModelMismatchFailover(t, err)
	require.Empty(t, recorder.Body.String(), "no upstream bytes may leak")

	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.Equal(t, "gpt-5.6-sol", mark.SentModel)
	require.Equal(t, "gpt-6-sol", mark.ResponseModel)
	require.True(t, mark.Stream)
}

// 主路径首输出守卫（staging）模式下同样零泄漏
func TestUpstreamModelMismatch_CodexStreamFailsOverWithFirstOutputGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newUpstreamModelMismatchSSEUpstream(upstreamModelMismatchSSEBody("gpt-6-sol", "gpt-6-sol", "leak"))
	svc := newOpenAIImageGenerationControlTestService(upstream)
	svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 30
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()

	_, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestRequestBody))
	requireUpstreamModelMismatchFailover(t, err)
	require.Empty(t, recorder.Body.String(), "no staged bytes may leak")
	require.NotNil(t, GetOpsUpstreamModelMismatch(c))
}

// 非流式 JSON 不一致 → failover，且 c.Data 未被调用
func TestUpstreamModelMismatch_NonStreamFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newUpstreamModelMismatchJSONUpstream("gpt-6-sol")
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()

	_, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestNonStreamRequestBody))
	requireUpstreamModelMismatchFailover(t, err)
	require.Empty(t, recorder.Body.String(), "body must not be written to the client")

	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.Equal(t, "gpt-5.6-sol", mark.SentModel)
	require.Equal(t, "gpt-6-sol", mark.ResponseModel)
	require.False(t, mark.Stream)
	require.Equal(t, 12, mark.Usage.InputTokens, "usage parsed before the check must be carried on the mark")
	require.Equal(t, 3, mark.Usage.OutputTokens)
}

// 非流式 JSON 一致 → 正常放行
func TestUpstreamModelMismatch_NonStreamMatchPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newUpstreamModelMismatchJSONUpstream("gpt-5.6-sol")
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()

	result, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestNonStreamRequestBody))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, recorder.Body.String(), "answer")
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

// passthrough 非流式 JSON 不一致 → failover，客户端零字节
func TestUpstreamModelMismatch_PassthroughNonStreamFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newUpstreamModelMismatchJSONUpstream("gpt-6-sol")
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()
	account.Extra = map[string]any{"openai_responses_supported": true, "openai_passthrough": true}

	_, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestNonStreamRequestBody))
	requireUpstreamModelMismatchFailover(t, err)
	require.Empty(t, recorder.Body.String(), "body must not be written to the client")

	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.Equal(t, "gpt-5.6-sol", mark.SentModel)
	require.Equal(t, "gpt-6-sol", mark.ResponseModel)
	require.False(t, mark.Stream)
}

// 非流式请求但上游回 SSE（handleSSEToJSON）：终止事件里的 model 不一致 → failover
func TestUpstreamModelMismatch_SSEToJSONFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newUpstreamModelMismatchSSEUpstream(upstreamModelMismatchSSEBody("gpt-6-sol", "gpt-6-sol", "leak"))
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()

	_, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestNonStreamRequestBody))
	requireUpstreamModelMismatchFailover(t, err)
	require.Empty(t, recorder.Body.String(), "converted JSON must not be written to the client")

	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.Equal(t, "gpt-6-sol", mark.ResponseModel)
	require.False(t, mark.Stream)
}

// passthrough 非流式请求但上游回 SSE（handlePassthroughSSEToJSON）：终止事件里的 model 不一致 → failover
func TestUpstreamModelMismatch_PassthroughSSEToJSONFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newUpstreamModelMismatchSSEUpstream(upstreamModelMismatchSSEBody("gpt-6-sol", "gpt-6-sol", "leak"))
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()
	account.Extra = map[string]any{"openai_responses_supported": true, "openai_passthrough": true}

	_, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestNonStreamRequestBody))
	requireUpstreamModelMismatchFailover(t, err)
	require.Empty(t, recorder.Body.String(), "converted JSON must not be written to the client")

	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.Equal(t, "gpt-6-sol", mark.ResponseModel)
	require.False(t, mark.Stream)
}

// response.failed 自带不一致的 model：失败事件专用处理（cyber 打标 + 真实 usage）优先，不被模型比对抢跑
func TestUpstreamModelMismatch_ResponseFailedKeepsCyberPolicyHandling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		name := "codex"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			upstream := newUpstreamModelMismatchSSEUpstream(
				"data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"object\":\"response\",\"status\":\"in_progress\"}}\n\n" +
					"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-6-sol\",\"status\":\"failed\",\"error\":{\"code\":\"cyber_policy\",\"message\":\"blocked by network policy\"},\"usage\":{\"input_tokens\":1234,\"output_tokens\":7}}}\n\n")
			svc := newOpenAIImageGenerationControlTestService(upstream)
			c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
			account := newOpenAIImageGenerationControlTestAccount()
			if passthrough {
				account.Extra = map[string]any{"openai_responses_supported": true, "openai_passthrough": true}
			}

			_, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestRequestBody))
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr), "cyber policy refusal must not be turned into a model-mismatch failover, got: %v", err)
			require.Nil(t, GetOpsUpstreamModelMismatch(c))
			cyber := GetOpsCyberPolicy(c)
			require.NotNil(t, cyber)
			require.Equal(t, 1234, cyber.UpstreamInTok)
		})
	}
}

// 观察模式：开关关闭 → 正常放行，但 mark 存在
func TestUpstreamModelMismatch_ObserveModePassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newUpstreamModelMismatchSSEUpstream(upstreamModelMismatchSSEBody("gpt-6-sol", "gpt-6-sol", "leak"))
	svc := newOpenAIImageGenerationControlTestService(upstream)
	svc.cfg.Gateway.DisableUpstreamModelMismatchBlock = true
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()
	account.Extra = map[string]any{"openai_responses_supported": true, "openai_passthrough": true}

	result, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestRequestBody))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, recorder.Body.String(), "leak")

	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.Equal(t, "gpt-5.6-sol", mark.SentModel)
	require.Equal(t, "gpt-6-sol", mark.ResponseModel)
}

// model 只在 response.completed 出现且已有输出 → 不拦截，仅打标
func TestUpstreamModelMismatch_LateModelOnlyMarks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		name := "codex"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			upstream := newUpstreamModelMismatchSSEUpstream(upstreamModelMismatchSSEBody("", "gpt-6-sol", "hello"))
			svc := newOpenAIImageGenerationControlTestService(upstream)
			c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
			account := newOpenAIImageGenerationControlTestAccount()
			if passthrough {
				account.Extra = map[string]any{"openai_responses_supported": true, "openai_passthrough": true}
			}

			result, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestRequestBody))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Contains(t, recorder.Body.String(), "hello")

			mark := GetOpsUpstreamModelMismatch(c)
			require.NotNil(t, mark)
			require.Equal(t, "gpt-5.6-sol", mark.SentModel)
			require.Equal(t, "gpt-6-sol", mark.ResponseModel)
			require.True(t, mark.Stream)
		})
	}
}

// 一致的流式响应完全不受影响（主路径 + passthrough）
func TestUpstreamModelMismatch_StreamMatchPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		name := "codex"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			upstream := newUpstreamModelMismatchSSEUpstream(upstreamModelMismatchSSEBody("gpt-5.6-sol", "gpt-5.6-sol", "fine"))
			svc := newOpenAIImageGenerationControlTestService(upstream)
			c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
			account := newOpenAIImageGenerationControlTestAccount()
			if passthrough {
				account.Extra = map[string]any{"openai_responses_supported": true, "openai_passthrough": true}
			}

			result, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestRequestBody))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Contains(t, recorder.Body.String(), "fine")
			require.Nil(t, GetOpsUpstreamModelMismatch(c))
		})
	}
}
