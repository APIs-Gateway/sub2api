package service

import (
	"bytes"
	"context"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatResponsesContracts_HTTPOutbound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"named choice", "developer", "legacy"} {
			t.Run(fmt.Sprintf("%s/stream=%v", scenario, stream), func(t *testing.T) {
				messages := `[{"role":"user","content":"hello"}]`
				extras := ""
				switch scenario {
				case "named choice":
					extras = `,"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"lookup"}}`
				case "developer":
					messages = `[{"role":"developer","content":"Keep instruction priority."},{"role":"user","content":"hello"}]`
				case "legacy":
					messages = `[{"role":"assistant","function_call":{"name":"lookup","arguments":"{\"n\":1}"}},{"role":"function","name":"lookup","content":"done"},{"role":"user","content":"continue"}]`
				}
				body := []byte(fmt.Sprintf(`{"model":"public-model","stream":%v,"messages":%s%s}`, stream, messages, extras))
				sse := strings.Join([]string{
					`event: response.created`, `data: {"type":"response.created","response":{"id":"r1","model":"gpt-4o","status":"in_progress","output":[]}}`, "",
					`event: response.output_text.delta`, `data: {"type":"response.output_text.delta","delta":"ok","output_index":0,"content_index":0}`, "",
					`event: response.completed`, `data: {"type":"response.completed","response":{"id":"r1","object":"response","model":"gpt-4o","status":"completed","output":[{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":17,"output_tokens":8,"total_tokens":25,"input_tokens_details":{"cached_tokens":6}}}}`, "", "",
				}, "\n")
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "fixture-key"}, Extra: map[string]any{"openai_responses_supported": true}}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
				result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-4o")
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Len(t, upstream.requests, 1)
				require.Equal(t, "https://api.openai.com/v1/responses", upstream.lastReq.URL.String())
				require.Equal(t, "Bearer fixture-key", upstream.lastReq.Header.Get("Authorization"))
				require.Equal(t, "gpt-4o", gjson.GetBytes(upstream.lastBody, "model").String())
				require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
				require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
				switch scenario {
				case "named choice":
					require.Equal(t, "lookup", gjson.GetBytes(upstream.lastBody, "tool_choice.name").String())
					require.False(t, gjson.GetBytes(upstream.lastBody, "tool_choice.function").Exists())
				case "developer":
					require.Equal(t, "message", gjson.GetBytes(upstream.lastBody, "input.0.type").String())
					require.Equal(t, "developer", gjson.GetBytes(upstream.lastBody, "input.0.role").String())
					require.Equal(t, "Keep instruction priority.", gjson.GetBytes(upstream.lastBody, "input.0.content").String())
					require.Equal(t, "user", gjson.GetBytes(upstream.lastBody, "input.1.role").String())
				case "legacy":
					require.Equal(t, "function_call", gjson.GetBytes(upstream.lastBody, "input.0.type").String())
					require.Equal(t, "lookup", gjson.GetBytes(upstream.lastBody, "input.0.name").String())
					require.Equal(t, `{"n":1}`, gjson.GetBytes(upstream.lastBody, "input.0.arguments").String())
					require.Equal(t, "function_call_output", gjson.GetBytes(upstream.lastBody, "input.1.type").String())
					require.Equal(t, "call_legacy_1", gjson.GetBytes(upstream.lastBody, "input.0.call_id").String())
					require.Equal(t, "call_legacy_1", gjson.GetBytes(upstream.lastBody, "input.1.call_id").String())
				}
				require.Equal(t, "public-model", result.Model)
				require.Equal(t, "gpt-4o", result.BillingModel)
				require.Equal(t, "gpt-4o", result.UpstreamModel)
				require.Equal(t, 17, result.Usage.InputTokens)
				require.Equal(t, 8, result.Usage.OutputTokens)
				require.Equal(t, 6, result.Usage.CacheReadInputTokens)
				require.Equal(t, http.StatusOK, rec.Code)
				require.Contains(t, rec.Body.String(), "ok")
				if stream {
					require.Contains(t, rec.Body.String(), "[DONE]")
				} else {
					require.True(t, gjson.Valid(rec.Body.String()))
				}
			})
		}
	}
}

func TestChatResponsesContracts_RawChatUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"gpt-4o","stream":%v,"messages":[{"role":"developer","content":"instruction"},{"role":"assistant","function_call":{"name":"lookup","arguments":"{}"}},{"role":"function","name":"lookup","content":"result"}],"tool_choice":{"type":"function","function":{"name":"lookup"}}}`, stream))
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"controlled request rejection"}}`))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "fixture-key"}, Extra: map[string]any{"openai_responses_supported": false}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
			require.Error(t, err)
			require.Nil(t, result)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, "https://api.openai.com/v1/chat/completions", upstream.lastReq.URL.String())
			require.JSONEq(t, string(body), string(upstream.lastBody), "native raw Chat policy must be unchanged")
		})
	}
}
