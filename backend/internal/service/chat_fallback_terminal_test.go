//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type fallbackTerminalUpstream struct {
	httpUpstreamRecorder
	calls   int
	observe func(*http.Request)
}

func (u *fallbackTerminalUpstream) Do(req *http.Request, proxy string, id int64, slots int) (*http.Response, error) {
	u.calls++
	if u.observe != nil {
		u.observe(req)
	}
	return u.httpUpstreamRecorder.Do(req, proxy, id, slots)
}

func (u *fallbackTerminalUpstream) DoWithTLS(req *http.Request, proxy string, id int64, slots int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, slots)
}

func fallbackTerminalChunk(t *testing.T, delta map[string]any, finish any) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"id": "chat_terminal", "model": "gpt-5.4", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	require.NoError(t, err)
	return "data: " + string(body) + "\n\n"
}

func fallbackTerminalTool(index int, id, name, args string) map[string]any {
	return map[string]any{"index": index, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args}}
}

func fallbackTerminalForward(t *testing.T, mode, wire string, readErr error, interrupted string) (*OpenAIForwardResult, error, string, *fallbackTerminalUpstream) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := "/v1/messages"
	body := []byte(`{"model":"public-model","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	account := rawChatCompletionsTestAccount()
	if mode == "messages_direct" {
		account = forceChatMessagesFallbackAccount()
	}
	if mode == "responses" {
		path = "/v1/responses"
		body = []byte(`{"model":"public-model","input":"hello","stream":true,"tools":[{"type":"custom","name":"freeform"}]}`)
		account = forceChatResponsesFallbackAccount()
	}
	account.Credentials["model_mapping"] = map[string]any{"public-model": "gpt-5.4"}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	if interrupted == "write_failure" {
		c.Writer = &openAIRawStreamDisconnectedWriter{ResponseWriter: c.Writer}
	}
	var reader io.Reader = strings.NewReader(wire)
	if readErr != nil {
		reader = &chatFallbackReadError{reader: strings.NewReader(wire), err: readErr}
	}
	upstream := &fallbackTerminalUpstream{httpUpstreamRecorder: httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"rid_terminal"}},
		Body:       io.NopCloser(reader),
	}}}
	if interrupted == "cancel_after_dispatch" {
		upstream.observe = func(*http.Request) { cancel() }
	}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	var result *OpenAIForwardResult
	var err error
	if mode == "responses" {
		result, err = svc.Forward(ctx, c, account, body)
	} else {
		result, err = svc.ForwardAsAnthropic(ctx, c, account, body, "", "")
	}
	require.Equal(t, 1, upstream.calls, "this real Forward attempt cannot replay")
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "/v1/chat/completions", upstream.lastReq.URL.Path)
	require.Equal(t, "gpt-5.4", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
	return result, err, rec.Body.String(), upstream
}

func fallbackTerminalAssertNoSuccess(t *testing.T, output string) {
	t.Helper()
	for _, forbidden := range []string{"event: response.completed\n", "event: response.incomplete\n", "event: response.function_call_arguments.done\n", "event: response.custom_tool_call_input.done\n", "event: message_stop\n", "data: [DONE]"} {
		require.NotContains(t, output, forbidden)
	}
}

func TestChatFallbackTerminal_NoMarker(t *testing.T) {
	for _, mode := range []string{"responses", "messages_indirect", "messages_direct"} {
		for _, tc := range []struct {
			name, wire string
		}{
			{"empty", ""},
			{"heartbeat", ": ping\n\nevent: keepalive\n\n"},
			{"text", fallbackTerminalChunk(t, map[string]any{"content": "partial text"}, nil)},
			{"reasoning", fallbackTerminalChunk(t, map[string]any{"reasoning_content": "late thinking", "content": "partial text"}, nil)},
			{"tool_partial", fallbackTerminalChunk(t, map[string]any{"tool_calls": []any{fallbackTerminalTool(0, "call_a", "exec", `{"cmd":`)}}, nil)},
			{"tool_valid", fallbackTerminalChunk(t, map[string]any{"tool_calls": []any{fallbackTerminalTool(0, "call_a", "exec", `{}`)}}, nil)},
			{"tool_empty", fallbackTerminalChunk(t, map[string]any{"tool_calls": []any{fallbackTerminalTool(0, "call_a", "exec", "")}}, nil)},
			{"custom_freeform", fallbackTerminalChunk(t, map[string]any{"tool_calls": []any{fallbackTerminalTool(0, "call_free", "freeform", "echo hello")}}, nil)},
			{"multiple_tools", fallbackTerminalChunk(t, map[string]any{"tool_calls": []any{fallbackTerminalTool(0, "call_a", "first", `{}`), fallbackTerminalTool(1, "call_b", "second", `{"x":1}`)}}, nil)},
			{"malformed_chunk", "data: {not-json}\n\n"},
			{"malformed_finish_json", "data: {\"choices\":[{\"finish_reason\":\"stop\"}]\n\n"},
			{"malformed_usage_json", "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}\n\n"},
			{"invalid_chunk_with_usage", "data: {\"choices\":\"invalid\",\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n"},
			{"nonobject_usage", "data: {\"choices\":[],\"usage\":null}\n\n"},
			{"empty_finish", fallbackTerminalChunk(t, map[string]any{"content": "partial text"}, " ")},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				result, err, output, _ := fallbackTerminalForward(t, mode, tc.wire, nil, "")
				require.ErrorContains(t, err, "missing Chat Completions terminal signal")
				require.Nil(t, result, "unknown provider execution must not become known zero-cost usage")
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover))
				require.False(t, IsBillingInflightNoChargeError(err))
				fallbackTerminalAssertNoSuccess(t, output)
				if mode != "responses" {
					require.Equal(t, 1, strings.Count(output, "event: error\n"))
					require.Contains(t, output, `"type":"api_error"`)
				}
				if strings.Contains(tc.name, "tool") || tc.name == "custom_freeform" {
					require.NotContains(t, output, "event: response.output_item.done\n")
				}
				if tc.name == "multiple_tools" {
					require.Contains(t, output, "call_a")
					require.Contains(t, output, "call_b")
				}
				if tc.name == "reasoning" {
					require.Contains(t, output, "late thinking")
					require.Contains(t, output, "partial text")
				}
			})
		}
	}
}

func TestChatFallbackTerminal_AcceptedUnion(t *testing.T) {
	for _, mode := range []string{"responses", "messages_indirect", "messages_direct"} {
		for _, terminal := range []string{"done", "stop", "length", "tool_calls", "content_filter", "usage_only"} {
			t.Run(mode+"/"+terminal, func(t *testing.T) {
				wire := fallbackTerminalChunk(t, map[string]any{"content": "complete text"}, nil)
				if terminal == "done" {
					wire += "data: [DONE]\n\n"
				} else if terminal == "usage_only" {
					wire += "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n"
				} else {
					wire += fallbackTerminalChunk(t, map[string]any{}, terminal)
				}
				result, err, output, _ := fallbackTerminalForward(t, mode, wire, nil, "")
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, "public-model", result.Model)
				require.Equal(t, "gpt-5.4", result.UpstreamModel)
				require.Equal(t, "rid_terminal", result.RequestID)
				require.Contains(t, output, "complete text")
				if terminal == "usage_only" {
					require.Equal(t, 10, result.Usage.InputTokens)
					require.Equal(t, 2, result.Usage.OutputTokens)
				}
				if mode == "responses" {
					kind := "response.completed"
					if terminal == "length" {
						kind = "response.incomplete"
						require.Contains(t, output, `"reason":"max_output_tokens"`)
					}
					require.Equal(t, 1, strings.Count(output, "event: "+kind+"\n"))
					require.Equal(t, 1, strings.Count(output, "data: [DONE]"))
				} else {
					require.Equal(t, 1, strings.Count(output, "event: message_stop\n"))
				}
			})
		}
	}
}

func TestChatFallbackTerminal_ScannerKeepsMeteredPartial(t *testing.T) {
	for _, mode := range []string{"responses", "messages_indirect", "messages_direct"} {
		t.Run(mode, func(t *testing.T) {
			wire := fallbackTerminalChunk(t, map[string]any{"content": "metered partial"}, nil) + "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n"
			result, err, output, _ := fallbackTerminalForward(t, mode, wire, fmt.Errorf("provider reader reset"), "")
			require.ErrorContains(t, err, "provider reader reset")
			require.NotNil(t, result)
			require.Equal(t, 10, result.Usage.InputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)
			require.False(t, IsBillingInflightNoChargeError(err))
			fallbackTerminalAssertNoSuccess(t, output)
		})
	}
}

func TestChatFallbackTerminal_DisconnectedClient(t *testing.T) {
	for _, mode := range []string{"responses", "messages_indirect", "messages_direct"} {
		for _, interruption := range []string{"cancel_after_dispatch", "write_failure"} {
			t.Run(mode+"/"+interruption, func(t *testing.T) {
				wire := fallbackTerminalChunk(t, map[string]any{"content": "partial text"}, nil)
				result, err, output, _ := fallbackTerminalForward(t, mode, wire, nil, interruption)
				require.Error(t, err)
				require.Nil(t, result)
				fallbackTerminalAssertNoSuccess(t, output)
				require.NotContains(t, output, "event: error\n")
			})
		}
	}
}

func TestChatFallbackTerminal_CompleteContentControls(t *testing.T) {
	for _, mode := range []string{"responses", "messages_indirect", "messages_direct"} {
		for _, kind := range []string{"function_empty", "function_json", "custom_wrapped", "multiple_tools", "late_reasoning"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				delta := map[string]any{"tool_calls": []any{fallbackTerminalTool(0, "call_a", "exec", "")}}
				switch kind {
				case "function_json":
					delta["tool_calls"] = []any{fallbackTerminalTool(0, "call_a", "exec", `{"cmd":"pwd"}`)}
				case "custom_wrapped":
					delta["tool_calls"] = []any{fallbackTerminalTool(0, "call_a", "freeform", `{"input":"echo hello"}`)}
				case "multiple_tools":
					delta["tool_calls"] = []any{fallbackTerminalTool(0, "call_a", "first", `{}`), fallbackTerminalTool(1, "call_b", "second", `{"x":1}`)}
				case "late_reasoning":
					delta = map[string]any{"content": "answer first"}
				}
				wire := fallbackTerminalChunk(t, delta, nil)
				if kind == "late_reasoning" {
					wire += fallbackTerminalChunk(t, map[string]any{"reasoning_content": "late thinking"}, nil)
				}
				wire += "data: [DONE]\n\n"
				result, err, output, _ := fallbackTerminalForward(t, mode, wire, nil, "")
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, "public-model", result.Model)
				if kind == "late_reasoning" {
					require.Contains(t, output, "answer first")
					require.Contains(t, output, "late thinking")
				} else {
					require.Contains(t, output, "call_a")
					if kind == "multiple_tools" {
						require.Contains(t, output, "call_b")
						require.Contains(t, output, "first")
						require.Contains(t, output, "second")
					}
					if kind == "custom_wrapped" && mode == "responses" {
						require.Contains(t, output, `"type":"custom_tool_call"`)
						require.Contains(t, output, "echo hello")
					}
				}
				if mode == "responses" {
					require.Equal(t, 1, strings.Count(output, "event: response.completed\n"))
					require.Equal(t, 1, strings.Count(output, "data: [DONE]"))
				} else {
					require.Equal(t, 1, strings.Count(output, "event: message_stop\n"))
				}
			})
		}
	}
}
