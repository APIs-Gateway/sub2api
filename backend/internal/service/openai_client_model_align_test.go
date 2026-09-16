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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 客户端可见 model 对齐（v0.1.151）：上游返回的模型与请求不一致且「拦不住」（观察模式 /
// 内容已开始下发 / grok 只记录不拦截）时，客户端拿到的每个带 model 的事件 / JSON 都必须是
// 原始请求模型；而审计侧 UpstreamModelMismatchMark.ResponseModel 与 result.UpstreamModel 保持上游真实值。
//
// 夹具复用 openai_gateway_upstream_model_mismatch_test.go / _paths_test.go / openai_ws_upstream_model_mismatch_test.go。

// requireClientVisibleModelAligned 逐个检查 SSE data 事件（或整包 JSON）里出现的 model /
// response.model / message.model，都必须等于 want；forbidden（上游真实名）不能出现在任何客户端字节里。
// 返回带 model 的事件数，调用方据此确认「确实有事件被检查」。
func requireClientVisibleModelAligned(t *testing.T, body, want, forbidden string) int {
	t.Helper()
	require.NotContains(t, body, forbidden, "upstream model name leaked to the client")
	checkPayload := func(payload string) int {
		count := 0
		for _, path := range []string{"model", "response.model", "message.model"} {
			if v := gjson.Get(payload, path); v.Exists() {
				require.Equal(t, want, v.String(), "payload %s: %s", path, payload)
				count++
			}
		}
		return count
	}
	if gjson.Valid(strings.TrimSpace(body)) {
		return checkPayload(strings.TrimSpace(body))
	}
	total := 0
	for _, line := range strings.Split(body, "\n") {
		data, ok := extractOpenAISSEDataLine(line)
		if !ok || strings.TrimSpace(data) == "" || strings.TrimSpace(data) == "[DONE]" {
			continue
		}
		total += checkPayload(data)
	}
	return total
}

func requireClientModelAlignMark(t *testing.T, c *gin.Context, sent, got string) {
	t.Helper()
	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark, "audit mark must still be recorded")
	require.Equal(t, sent, mark.SentModel)
	require.Equal(t, got, mark.ResponseModel, "audit must keep the real upstream model")
	require.False(t, mark.Blocked)
}

// ---------- 助手 ----------

func TestAlignClientVisibleModel(t *testing.T) {
	t.Run("empty original returns same slice", func(t *testing.T) {
		payload := []byte(`{"model":"gpt-6-sol"}`)
		out := alignClientVisibleModel(payload, "")
		require.Equal(t, &payload[0], &out[0])
	})
	t.Run("no model field is not added", func(t *testing.T) {
		payload := []byte(`{"type":"response.output_text.delta","delta":"model"}`)
		out := alignClientVisibleModel(payload, "gpt-5.6-sol")
		require.Equal(t, string(payload), string(out))
	})
	t.Run("equal model is zero copy", func(t *testing.T) {
		payload := []byte(`{"type":"response.created","response":{"model":"gpt-5.6-sol"}}`)
		out := alignClientVisibleModel(payload, "gpt-5.6-sol")
		require.Equal(t, &payload[0], &out[0])
	})
	t.Run("top level and nested are both aligned", func(t *testing.T) {
		payload := []byte(`{"model":"gpt-6-sol","response":{"id":"r1","model":"codex-auto-review"}}`)
		out := alignClientVisibleModel(payload, "gpt-5.6-sol")
		require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(out, "model").String())
		require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(out, "response.model").String())
		require.Equal(t, "r1", gjson.GetBytes(out, "response.id").String())
	})
	t.Run("non string model is left alone", func(t *testing.T) {
		payload := []byte(`{"model":null}`)
		out := alignClientVisibleModel(payload, "gpt-5.6-sol")
		require.Equal(t, string(payload), string(out))
	})
}

func TestAlignClientVisibleModelInSSELine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{name: "done", line: "data: [DONE]", want: "data: [DONE]"},
		{name: "empty data", line: "data:", want: "data:"},
		{name: "comment", line: ": OPENROUTER PROCESSING", want: ": OPENROUTER PROCESSING"},
		{name: "event line", line: "event: response.created", want: "event: response.created"},
		{name: "no model", line: `data: {"type":"response.output_text.delta","delta":"x"}`, want: `data: {"type":"response.output_text.delta","delta":"x"}`},
		{name: "nested", line: `data: {"type":"response.created","response":{"model":"gpt-6-sol"}}`, want: `data: {"type":"response.created","response":{"model":"gpt-5.6-sol"}}`},
		{name: "top level chat chunk", line: `data:{"id":"c1","model":"gpt-6-sol","choices":[]}`, want: `data: {"id":"c1","model":"gpt-5.6-sol","choices":[]}`},
		{name: "already aligned", line: `data: {"model":"gpt-5.6-sol"}`, want: `data: {"model":"gpt-5.6-sol"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, alignClientVisibleModelInSSELine(tc.line, "gpt-5.6-sol"))
		})
	}
	t.Run("empty original is identity", func(t *testing.T) {
		line := `data: {"model":"gpt-6-sol"}`
		require.Equal(t, line, alignClientVisibleModelInSSELine(line, ""))
	})
}

func TestAlignClientVisibleModelInSSEBody(t *testing.T) {
	body := "data: {\"type\":\"response.created\",\"response\":{\"model\":\"gpt-6-sol\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
		"data: {\"type\":\"response.incomplete\",\"response\":{\"model\":\"gpt-6-sol\"}}\n\n"
	out := alignClientVisibleModelInSSEBody(body, "gpt-5.6-sol")
	require.Equal(t, 2, requireClientVisibleModelAligned(t, out, "gpt-5.6-sol", "gpt-6-sol"))
	require.Contains(t, out, `"delta":"hi"`)
	require.Equal(t, body, alignClientVisibleModelInSSEBody(body, ""))
}

// ---------- Responses 主路径 / passthrough（openai_gateway_service.go） ----------

func clientModelAlignResponsesVariants() []struct {
	name        string
	passthrough bool
} {
	return []struct {
		name        string
		passthrough bool
	}{{name: "codex", passthrough: false}, {name: "passthrough", passthrough: true}}
}

// result.UpstreamModel 是发给上游的模型（A），对齐只改客户端字节、不能碰它：
// codex 路径为规范化后的请求模型；passthrough 路径 body 原样转发、mappedModel 为空（现状，见 sentModelForCheck）。
func clientModelAlignWantUpstreamModel(passthrough bool) string {
	if passthrough {
		return ""
	}
	return "gpt-5.6-sol"
}

func newClientModelAlignResponsesService(t *testing.T, upstream *httpUpstreamRecorder, passthrough bool) (*OpenAIGatewayService, *gin.Context, *httptest.ResponseRecorder, *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := newOpenAIImageGenerationControlTestService(upstream)
	svc.cfg.Gateway.DisableUpstreamModelMismatchBlock = true
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()
	if passthrough {
		account.Extra = map[string]any{"openai_responses_supported": true, "openai_passthrough": true}
	}
	return svc, c, recorder, account
}

// 观察模式流式：response.created / response.completed 都带 gpt-6-sol → 客户端全部看到 gpt-5.6-sol
func TestClientModelAlign_ResponsesStreamObserveMode(t *testing.T) {
	for _, tc := range clientModelAlignResponsesVariants() {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newUpstreamModelMismatchSSEUpstream(upstreamModelMismatchSSEBody("gpt-6-sol", "gpt-6-sol", "leak"))
			svc, c, recorder, account := newClientModelAlignResponsesService(t, upstream, tc.passthrough)

			result, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestRequestBody))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Contains(t, recorder.Body.String(), "leak")
			require.Equal(t, 2, requireClientVisibleModelAligned(t, recorder.Body.String(), "gpt-5.6-sol", "gpt-6-sol"))
			require.Equal(t, clientModelAlignWantUpstreamModel(tc.passthrough), result.UpstreamModel)
			requireClientModelAlignMark(t, c, "gpt-5.6-sol", "gpt-6-sol")
		})
	}
}

// 拦截开关开着但 model 只在 response.completed 才出现（内容已下发，拦不住）→ completed 里的 model 仍要对齐
func TestClientModelAlign_ResponsesStreamLateModel(t *testing.T) {
	for _, tc := range clientModelAlignResponsesVariants() {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newUpstreamModelMismatchSSEUpstream(upstreamModelMismatchSSEBody("", "gpt-6-sol", "hello"))
			svc, c, recorder, account := newClientModelAlignResponsesService(t, upstream, tc.passthrough)
			svc.cfg.Gateway.DisableUpstreamModelMismatchBlock = false

			result, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestRequestBody))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Contains(t, recorder.Body.String(), "hello")
			require.Equal(t, 1, requireClientVisibleModelAligned(t, recorder.Body.String(), "gpt-5.6-sol", "gpt-6-sol"))
			requireClientModelAlignMark(t, c, "gpt-5.6-sol", "gpt-6-sol")
		})
	}
}

func TestClientModelAlign_ResponsesNonStreamObserveMode(t *testing.T) {
	for _, tc := range clientModelAlignResponsesVariants() {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newUpstreamModelMismatchJSONUpstream("gpt-6-sol")
			svc, c, recorder, account := newClientModelAlignResponsesService(t, upstream, tc.passthrough)

			result, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestNonStreamRequestBody))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Contains(t, recorder.Body.String(), "answer")
			require.Equal(t, 1, requireClientVisibleModelAligned(t, recorder.Body.String(), "gpt-5.6-sol", "gpt-6-sol"))
			require.Equal(t, clientModelAlignWantUpstreamModel(tc.passthrough), result.UpstreamModel)
			requireClientModelAlignMark(t, c, "gpt-5.6-sol", "gpt-6-sol")
		})
	}
}

// 非流式请求、上游回 SSE（SSE→JSON）：转换出的 JSON 顶层 model 对齐
func TestClientModelAlign_ResponsesSSEToJSONObserveMode(t *testing.T) {
	for _, tc := range clientModelAlignResponsesVariants() {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newUpstreamModelMismatchSSEUpstream(upstreamModelMismatchSSEBody("gpt-6-sol", "gpt-6-sol", "leak"))
			svc, c, recorder, account := newClientModelAlignResponsesService(t, upstream, tc.passthrough)

			result, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestNonStreamRequestBody))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, gjson.Valid(recorder.Body.String()), "SSE→JSON must produce a JSON body: %s", recorder.Body.String())
			require.Equal(t, 1, requireClientVisibleModelAligned(t, recorder.Body.String(), "gpt-5.6-sol", "gpt-6-sol"))
			requireClientModelAlignMark(t, c, "gpt-5.6-sol", "gpt-6-sol")
		})
	}
}

// 非流式请求、上游回没有 completed/done 的 SSE（response.incomplete 收尾，原样回写 SSE 的 !ok 分支）：每个带 model 的事件都对齐
func TestClientModelAlign_ResponsesSSEToJSONIncompleteObserveMode(t *testing.T) {
	for _, tc := range clientModelAlignResponsesVariants() {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newUpstreamModelMismatchSSEUpstream(upstreamModelMismatchIncompleteSSEBody("gpt-6-sol", "fine"))
			svc, c, recorder, account := newClientModelAlignResponsesService(t, upstream, tc.passthrough)

			result, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestNonStreamRequestBody))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Contains(t, recorder.Body.String(), "fine")
			require.Contains(t, recorder.Body.String(), "response.incomplete")
			require.Equal(t, 2, requireClientVisibleModelAligned(t, recorder.Body.String(), "gpt-5.6-sol", "gpt-6-sol"))
			requireClientModelAlignMark(t, c, "gpt-5.6-sol", "gpt-6-sol")
		})
	}
}

// ---------- Chat Completions 入站（openai_gateway_chat_completions.go） ----------

func clientModelAlignObserveConfig() *OpenAIGatewayService {
	cfg := rawChatCompletionsTestConfig()
	cfg.Gateway.DisableUpstreamModelMismatchBlock = true
	return &OpenAIGatewayService{cfg: cfg}
}

func TestClientModelAlign_HandleChatStreamingResponse(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", nil)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_align_chat_stream", upstreamModelMismatchResponsesSSE(upstreamModelMismatchGotModel))
	svc := clientModelAlignObserveConfig()

	result, err := svc.handleChatStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now(), 0)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"content":"leak"`)
	require.Greater(t, requireClientVisibleModelAligned(t, rec.Body.String(), upstreamModelMismatchSentModel, upstreamModelMismatchGotModel), 0)
	require.Equal(t, upstreamModelMismatchSentModel, result.UpstreamModel)
	requireClientModelAlignMark(t, c, upstreamModelMismatchSentModel, upstreamModelMismatchGotModel)
}

func TestClientModelAlign_HandleChatBufferedStreamingResponse(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", nil)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_align_chat_buffered", upstreamModelMismatchResponsesSSE(upstreamModelMismatchGotModel))
	svc := clientModelAlignObserveConfig()

	result, err := svc.handleChatBufferedStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "leak", gjson.Get(rec.Body.String(), "choices.0.message.content").String())
	require.Equal(t, 1, requireClientVisibleModelAligned(t, rec.Body.String(), upstreamModelMismatchSentModel, upstreamModelMismatchGotModel))
	requireClientModelAlignMark(t, c, upstreamModelMismatchSentModel, upstreamModelMismatchGotModel)
}

// ---------- raw Chat Completions（openai_gateway_chat_completions_raw.go） ----------

func TestClientModelAlign_StreamRawChatCompletions(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("text/event-stream", "rid_align_raw_stream", upstreamModelMismatchChatSSE(upstreamModelMismatchGotModel))}
	svc := clientModelAlignObserveConfig()
	svc.httpUpstream = upstream

	result, err := svc.forwardAsRawChatCompletions(context.Background(), c, rawChatCompletionsTestAccount(), body, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"content":"leak"`)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
	require.Equal(t, 4, requireClientVisibleModelAligned(t, rec.Body.String(), "gpt-5.5", upstreamModelMismatchGotModel), "every chunk carries model")
	require.Equal(t, "gpt-5.5", result.UpstreamModel)
	requireClientModelAlignMark(t, c, "gpt-5.5", upstreamModelMismatchGotModel)
}

func TestClientModelAlign_BufferRawChatCompletions(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", body)
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("application/json", "rid_align_raw_json", upstreamModelMismatchChatJSON(upstreamModelMismatchGotModel))}
	svc := clientModelAlignObserveConfig()
	svc.httpUpstream = upstream

	result, err := svc.forwardAsRawChatCompletions(context.Background(), c, rawChatCompletionsTestAccount(), body, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "leak", gjson.Get(rec.Body.String(), "choices.0.message.content").String())
	require.Equal(t, 1, requireClientVisibleModelAligned(t, rec.Body.String(), "gpt-5.5", upstreamModelMismatchGotModel))
	requireClientModelAlignMark(t, c, "gpt-5.5", upstreamModelMismatchGotModel)
}

// raw 路径此前连 model_mapping 都不反向改写：账号把 sol 映射成 gpt-5.6-sol，上游回显 gpt-5.6-sol（一致，不打标），
// 客户端也必须看到自己请求的 sol。
func TestClientModelAlign_RawChatCompletionsModelMappingReversed(t *testing.T) {
	for _, stream := range []bool{true, false} {
		name := "buffer"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			streamField := "false"
			upstreamBody := upstreamModelMismatchChatJSON("gpt-5.6-sol")
			contentType := "application/json"
			wantEvents := 1
			if stream {
				streamField = "true"
				upstreamBody = upstreamModelMismatchChatSSE("gpt-5.6-sol")
				contentType = "text/event-stream"
				wantEvents = 4
			}
			body := []byte(`{"model":"sol","messages":[{"role":"user","content":"hi"}],"stream":` + streamField + `}`)
			c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", body)
			upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse(contentType, "rid_align_raw_mapping", upstreamBody)}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			account := rawChatCompletionsTestAccount()
			account.Credentials["model_mapping"] = map[string]any{"sol": "gpt-5.6-sol"}

			result, err := svc.forwardAsRawChatCompletions(context.Background(), c, account, body, "")

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, wantEvents, requireClientVisibleModelAligned(t, rec.Body.String(), "sol", "gpt-5.6-sol"))
			require.Equal(t, "gpt-5.6-sol", result.UpstreamModel)
			require.Nil(t, GetOpsUpstreamModelMismatch(c))
		})
	}
}

// ---------- Messages 入站（openai_gateway_messages.go） ----------

func TestClientModelAlign_HandleAnthropicStreamingResponse(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", nil)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_align_msg_stream", upstreamModelMismatchResponsesSSE(upstreamModelMismatchGotModel))
	svc := clientModelAlignObserveConfig()

	result, err := svc.handleAnthropicStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "event: message_start")
	require.Contains(t, rec.Body.String(), `"text":"leak"`)
	require.Greater(t, requireClientVisibleModelAligned(t, rec.Body.String(), upstreamModelMismatchSentModel, upstreamModelMismatchGotModel), 0)
	requireClientModelAlignMark(t, c, upstreamModelMismatchSentModel, upstreamModelMismatchGotModel)
}

func TestClientModelAlign_HandleAnthropicBufferedStreamingResponse(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", nil)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_align_msg_buffered", upstreamModelMismatchResponsesSSE(upstreamModelMismatchGotModel))
	svc := clientModelAlignObserveConfig()

	result, err := svc.handleAnthropicBufferedStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "leak", gjson.Get(rec.Body.String(), "content.0.text").String())
	require.Equal(t, 1, requireClientVisibleModelAligned(t, rec.Body.String(), upstreamModelMismatchSentModel, upstreamModelMismatchGotModel))
	requireClientModelAlignMark(t, c, upstreamModelMismatchSentModel, upstreamModelMismatchGotModel)
}

// ---------- Messages 走 raw chat 降级（openai_gateway_messages_chat_fallback.go） ----------

func TestClientModelAlign_ChatCompletionsAsAnthropic(t *testing.T) {
	for _, stream := range []bool{true, false} {
		name := "buffer"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			streamField, upstreamBody, contentType := "false", upstreamModelMismatchChatJSON(upstreamModelMismatchGotModel), "application/json"
			if stream {
				streamField, upstreamBody, contentType = "true", upstreamModelMismatchChatSSE(upstreamModelMismatchGotModel), "text/event-stream"
			}
			body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":` + streamField + `}`)
			c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", body)
			svc := clientModelAlignObserveConfig()
			svc.httpUpstream = &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse(contentType, "rid_align_msg_chat", upstreamBody)}

			result, err := svc.forwardAnthropicViaRawChatCompletions(context.Background(), c, forceChatMessagesFallbackAccount(), body, "")

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Contains(t, rec.Body.String(), "leak")
			require.Greater(t, requireClientVisibleModelAligned(t, rec.Body.String(), "gpt-5.4", upstreamModelMismatchGotModel), 0)
			requireClientModelAlignMark(t, c, "gpt-5.4", upstreamModelMismatchGotModel)
		})
	}
}

// ---------- Responses 走 raw chat 降级（openai_gateway_responses_chat_fallback.go） ----------

func TestClientModelAlign_ChatCompletionsAsResponses(t *testing.T) {
	for _, stream := range []bool{true, false} {
		name := "buffer"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			streamField, upstreamBody, contentType := "false", upstreamModelMismatchChatJSON(upstreamModelMismatchGotModel), "application/json"
			if stream {
				streamField, upstreamBody, contentType = "true", upstreamModelMismatchChatSSE(upstreamModelMismatchGotModel), "text/event-stream"
			}
			body := []byte(`{"model":"gpt-5.4","input":"hello","stream":` + streamField + `}`)
			c, rec := newUpstreamModelMismatchPathContext(t, "/v1/responses", body)
			svc := clientModelAlignObserveConfig()
			svc.httpUpstream = &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse(contentType, "rid_align_resp_chat", upstreamBody)}

			result, err := svc.forwardResponsesViaRawChatCompletions(context.Background(), c, forceChatResponsesFallbackAccount(), body)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Contains(t, rec.Body.String(), "leak")
			require.Greater(t, requireClientVisibleModelAligned(t, rec.Body.String(), "gpt-5.4", upstreamModelMismatchGotModel), 0)
			requireClientModelAlignMark(t, c, "gpt-5.4", upstreamModelMismatchGotModel)
		})
	}
}

// ---------- grok（openai_gateway_grok.go，只记录不拦截） ----------

func TestClientModelAlign_GrokResponsesStream(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/responses", nil)
	upstreamBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_grok","object":"response","model":"grok-3","status":"in_progress"}}`,
		"",
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"leak"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_grok","model":"grok-3","status":"completed","usage":{"input_tokens":1,"output_tokens":2}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	svc := newUpstreamModelMismatchGrokService(upstreamModelMismatchHTTPResponse("text/event-stream", "rid_align_grok_stream", upstreamBody))

	result, err := svc.forwardGrokResponses(context.Background(), c, upstreamModelMismatchGrokAccount(),
		[]byte(`{"model":"grok-4.3","input":"hi","stream":true}`), "grok-4.3", true, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"delta":"leak"`)
	require.Equal(t, 2, requireClientVisibleModelAligned(t, rec.Body.String(), "grok-4.3", "grok-3"))
	require.Equal(t, "grok-4.3", result.UpstreamModel)
	requireClientModelAlignMark(t, c, "grok-4.3", "grok-3")
}

func TestClientModelAlign_GrokResponsesNonStream(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/responses", nil)
	svc := newUpstreamModelMismatchGrokService(upstreamModelMismatchHTTPResponse("application/json", "rid_align_grok_json",
		`{"id":"resp_grok_json","object":"response","model":"grok-3","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"leak"}]}],"usage":{"input_tokens":3,"output_tokens":2}}`))

	result, err := svc.forwardGrokResponses(context.Background(), c, upstreamModelMismatchGrokAccount(),
		[]byte(`{"model":"grok-4.3","input":"hi"}`), "grok-4.3", false, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "resp_grok_json")
	require.Equal(t, 1, requireClientVisibleModelAligned(t, rec.Body.String(), "grok-4.3", "grok-3"))
	requireClientModelAlignMark(t, c, "grok-4.3", "grok-3")
}

// ---------- WS v2：HTTP 入站 → WS 上游（openai_ws_forwarder.go forwardOpenAIWSV2） ----------

func TestClientModelAlign_WSForwardV2ObserveMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{true, false} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			cfg := newUpstreamModelMismatchWSConfig()
			cfg.Gateway.DisableUpstreamModelMismatchBlock = true
			captureConn := &openAIWSCaptureConn{events: upstreamModelMismatchWSEvents("gpt-4o-mini")}
			pool := newOpenAIWSConnPool(cfg)
			pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: captureConn})
			defer pool.Close()

			svc := &OpenAIGatewayService{
				cfg:              cfg,
				httpUpstream:     &httpUpstreamRecorder{},
				cache:            &stubGatewayCache{},
				openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
				toolCorrector:    NewCodexToolCorrector(),
				openaiWSPool:     pool,
			}
			account := &Account{
				ID: 1611, Name: "openai-ws-model-align", Platform: PlatformOpenAI,
				Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.example.test"},
				Extra:       map[string]any{"responses_websockets_v2_enabled": true},
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)

			streamField := "false"
			if stream {
				streamField = "true"
			}
			result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":`+streamField+`,"input":"hello"}`))

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "resp_model_check_1", result.RequestID)
			if stream {
				require.Contains(t, rec.Body.String(), `"delta":"hello"`)
				require.Equal(t, 2, requireClientVisibleModelAligned(t, rec.Body.String(), "gpt-5.1", "gpt-4o-mini"))
			} else {
				require.Equal(t, 1, requireClientVisibleModelAligned(t, rec.Body.String(), "gpt-5.1", "gpt-4o-mini"))
			}
			require.Equal(t, "gpt-5.1", result.UpstreamModel)
			requireClientModelAlignMark(t, c, "gpt-5.1", "gpt-4o-mini")
		})
	}
}

// ---------- WS v2：WS 入站 → HTTP 上游 SSE（openai_ws_http_bridge.go） ----------

// turn>=2 命中只打标不拦截：三个事件照常下发，但 created / completed 里的 model 必须是客户端请求的 gpt-5.1
func TestClientModelAlign_WSHTTPBridgeTurn2(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_bridge_align\",\"model\":\"gpt-4o-mini\",\"status\":\"in_progress\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\",\"sequence_number\":1}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_bridge_align\",\"model\":\"gpt-4o-mini\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"req_bridge_align"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}}
	svc := &OpenAIGatewayService{cfg: newUpstreamModelMismatchWSConfig(), httpUpstream: upstream, toolCorrector: NewCodexToolCorrector()}
	account := &Account{ID: 1613, Name: "bridge-model-align", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"hi"}`)
	var writes [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload),
		"gpt-5.1", "", "", "", 2,
		openAIWSHTTPBridgeToolState{},
		func(message []byte) error {
			writes = append(writes, append([]byte(nil), message...))
			return nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, writes, 3)
	require.Equal(t, "hello", gjson.GetBytes(writes[1], "delta").String())
	joined := string(writes[0]) + "\n" + string(writes[1]) + "\n" + string(writes[2])
	require.NotContains(t, joined, "gpt-4o-mini")
	require.Equal(t, "gpt-5.1", gjson.GetBytes(writes[0], "response.model").String())
	require.Equal(t, "gpt-5.1", gjson.GetBytes(writes[2], "response.model").String())
	require.Equal(t, "gpt-5.1", result.UpstreamModel)
	requireClientModelAlignMark(t, c, "gpt-5.1", "gpt-4o-mini")
}

// passthrough 流式：上游以 response.failed（server_error，带 gpt-6-sol）收尾且客户端尚无输出 → 走 failover；
// ops 事件里记录的上游原文必须仍是 gpt-6-sol（审计保留真实值），不能被客户端可见的对齐覆盖。
func TestClientModelAlign_PassthroughStreamFailedEventKeepsRawModelInOpsEvent(t *testing.T) {
	failed := `{"type":"response.failed","response":{"id":"r1","object":"response","status":"failed","model":"gpt-6-sol",` +
		`"error":{"code":"server_error","message":"upstream exploded"},"usage":{"input_tokens":12,"output_tokens":0,"total_tokens":12}}}`
	upstream := newUpstreamModelMismatchSSEUpstream("data: " + failed + "\n\n")
	svc, c, recorder, account := newClientModelAlignResponsesService(t, upstream, true)
	svc.cfg.Gateway.LogUpstreamErrorBody = true
	svc.cfg.Gateway.LogUpstreamErrorBodyMaxBytes = 4096

	_, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestRequestBody))
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "response.failed(server_error) before output must fail over, got %v", err)
	require.Empty(t, recorder.Body.String(), "no upstream bytes may leak")

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.NotEmpty(t, events)
	require.Contains(t, events[len(events)-1].Detail, `"model":"gpt-6-sol"`, "ops detail must keep the raw upstream model")
	require.NotContains(t, events[len(events)-1].Detail, `"model":"gpt-5.6-sol"`)
}
