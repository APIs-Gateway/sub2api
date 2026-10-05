//go:build unit

package service

import (
	"fmt"
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

func TestChatLengthTerminal_RealReaders(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		tool         bool
	}{
		{"length_text", "length", false}, {"length_tool", "length", true},
		{"stop", "stop", false}, {"tool_calls", "tool_calls", true}, {"content_filter", "content_filter", false},
	} {
		for _, route := range []string{"responses", "legacy_messages"} {
			t.Run(tc.name+"/"+route, func(t *testing.T) {
				delta := `{"content":"partial text"}`
				if tc.tool {
					delta = `{"tool_calls":[{"index":0,"id":"call_limit","type":"function","function":{"name":"exec","arguments":"{}"}}]}`
				}
				wire := fmt.Sprintf("data: {\"id\":\"chat_length\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":%s}]}\n\ndata: {\"id\":\"chat_length\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\ndata: [DONE]\n\n", delta, tc.reason)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+route, strings.NewReader(`{}`))
				s := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(wire))}
				account := rawChatCompletionsTestAccount()
				var result *OpenAIForwardResult
				var err error
				if route == "responses" {
					result, err = s.streamChatCompletionsAsResponses(c, resp, account, "gpt-5", nil, nil, false, nil, "gpt-5", "gpt-5", nil, nil, time.Now())
				} else {
					result, err = s.streamChatCompletionsAsAnthropic(c, resp, account, "gpt-5", "gpt-5", "gpt-5", nil, nil, time.Now(), false)
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, 10, result.Usage.InputTokens)
				require.Equal(t, 2, result.Usage.OutputTokens)
				terminalCount := 0
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					event := gjson.Parse(strings.TrimPrefix(line, "data: "))
					kind := event.Get("type").String()
					if route == "responses" && (kind == "response.completed" || kind == "response.incomplete") {
						terminalCount++
						wantKind, wantStatus := "response.completed", "completed"
						if tc.reason == "length" {
							wantKind, wantStatus = "response.incomplete", "incomplete"
							require.Equal(t, "max_output_tokens", event.Get("response.incomplete_details.reason").String())
						}
						require.Equal(t, wantKind, kind)
						require.Equal(t, wantStatus, event.Get("response.status").String())
						require.EqualValues(t, 10, event.Get("response.usage.input_tokens").Int())
						require.EqualValues(t, 2, event.Get("response.usage.output_tokens").Int())
					} else if route == "legacy_messages" && kind == "message_delta" {
						terminalCount++
						wantStop := "end_turn"
						if tc.reason == "length" {
							wantStop = "max_tokens"
						} else if tc.tool {
							wantStop = "tool_use"
						}
						require.Equal(t, wantStop, event.Get("delta.stop_reason").String())
						require.EqualValues(t, 10, event.Get("usage.input_tokens").Int())
						require.EqualValues(t, 2, event.Get("usage.output_tokens").Int())
					}
				}
				require.Equal(t, 1, terminalCount, rec.Body.String())
				if route == "responses" {
					require.Equal(t, 1, strings.Count(rec.Body.String(), "data: [DONE]"))
				} else {
					require.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_stop\n"))
				}
				if tc.tool {
					require.Contains(t, rec.Body.String(), "call_limit")
					require.Contains(t, rec.Body.String(), "exec")
				} else {
					require.Contains(t, rec.Body.String(), "partial text")
				}
			})
		}
	}
}
