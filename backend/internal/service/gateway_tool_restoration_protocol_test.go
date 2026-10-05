//go:build unit

package service

import (
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

func TestToolNameRestoreProtocol_FieldsAndOpaqueBytes(t *testing.T) {
	rw := &ToolNameRewrite{Reverse: map[string]string{"cc_ses_get": "session_get"}}
	for _, tc := range []struct {
		name string
		body string
	}{
		{"native tool", `{"type":"tool_use","name":"cc_ses_get","id":"cc_ses_get","input":{"name":"cc_ses_get","signature":"cc_ses_get"}}`},
		{"native server tool", `{"type":"server_tool_use","name":"cc_ses_get","input":{"text":"cc_ses_get"}}`},
		{"native message", `{"content":[{"type":"text","text":"cc_ses_get"},{"type":"thinking","signature":"cc_ses_get"},{"type":"tool_use","name":"cc_ses_get","input":{"text":"cc_ses_get"}}]}`},
		{"native start frame", "data:\t{\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"name\":\"cc_ses_get\",\"id\":\"cc_ses_get\"}}\r\n\r\n"},
		{"responses function", `{"type":"function_call","name":"cc_ses_get","arguments":"cc_ses_get","call_id":"cc_ses_get"}`},
		{"responses custom", `{"type":"custom_tool_call","name":"cc_ses_get","input":"cc_ses_get"}`},
		{"responses item frame", `data: {"type":"response.output_item.done","item":{"type":"function_call","name":"cc_ses_get","arguments":"cc_ses_get"}}`},
		{"responses completed", `{"response":{"output":[{"type":"function_call","name":"cc_ses_get","arguments":"cc_ses_get"},{"type":"reasoning","encrypted_content":"cc_ses_get"}]}}`},
		{"responses buffered", `{"output":[{"type":"function_call","name":"cc_ses_get","arguments":"cc_ses_get"}]}`},
		{"chat buffered", `{"choices":[{"message":{"tool_calls":[{"type":"function","id":"cc_ses_get","function":{"name":"cc_ses_get","arguments":"cc_ses_get"}}],"content":"cc_ses_get"}}]}`},
		{"chat delta", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"cc_ses_get\",\"arguments\":\"cc_ses_get\"}}]}}]}\n\n"},
		{"chat legacy function", `{"choices":[{"message":{"function_call":{"name":"cc_ses_get","arguments":"cc_ses_get"}}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The entire frame must match, including whitespace and opaque fields.
			want := strings.Replace(tc.body, `"name":"cc_ses_get"`, `"name":"session_get"`, 1)
			require.Equal(t, want, string(restoreToolNamesInBytes([]byte(tc.body), rw)))
		})
	}
}

func TestToolNameRestoreProtocol_NoSpeculativeRestoration(t *testing.T) {
	rw := &ToolNameRewrite{Reverse: map[string]string{"cc_ses_get": "session_get"}}
	for _, body := range []string{
		`{"type":"tool_use","name":"cc_ses_get_extended","input":{"text":"cc_ses_get"}}`,
		`{"type":"thinking","name":"cc_ses_get","thinking":"cc_ses_get","signature":"cc_ses_get"}`,
		`{"type":"text","text":"cc_ses_get cc_sess_list"}`,
		`{"name":"cc_ses_get","id":"cc_ses_get"}`,
		`{"type":"tool_use","name":"cc_ses_other"}`,
		`{"type":"tool_use","name":123,"input":"cc_ses_get"}`,
		`data: {"type":"tool_use","name":"cc_ses_get"`,
		": cc_ses_get\n\n",
		"data: [DONE]\n\n",
	} {
		t.Run(body, func(t *testing.T) {
			require.Equal(t, body, string(restoreToolNamesInBytes([]byte(body), rw)))
		})
	}
	for _, mapping := range []*ToolNameRewrite{nil, {}} {
		body := `{"type":"tool_use","name":"cc_ses_get","input":{"signature":"cc_ses_get"}}`
		require.Equal(t, body, string(restoreToolNamesInBytes([]byte(body), mapping)))
	}
}

func TestToolNameRestoreProtocol_EscapedAndLegacyMappings(t *testing.T) {
	body := []byte(`{"type":"tool_use","name":"cc_ses_\u4f60","input":{"signature":"cc_ses_\u4f60"}}`)
	rw := &ToolNameRewrite{Reverse: map[string]string{"cc_ses_你": "session_你"}}
	got := restoreToolNamesInBytes(body, rw)
	require.Equal(t, "session_你", gjson.GetBytes(got, "name").String())
	require.Contains(t, string(got), `"signature":"cc_ses_\u4f60"`)
	legacy := &ToolNameRewrite{ReverseOrdered: [][2]string{{"cc_ses_get", "session_get"}}}
	require.Equal(t, `{"type":"tool_use","name":"session_get","input":"cc_ses_get"}`,
		string(restoreToolNamesInBytes([]byte(`{"type":"tool_use","name":"cc_ses_get","input":"cc_ses_get"}`), legacy)))
}

func TestToolNameRestoreProtocol_ActualChatReaders(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "buffered"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			c.Set(toolNameRewriteKey, &ToolNameRewrite{Reverse: map[string]string{"cc_ses_get": "session_get"}, ReverseOrdered: [][2]string{{"cc_ses_get", "session_get"}}})
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(strings.Join([]string{
				`data: {"type":"message_start","message":{"id":"msg_opaque","role":"assistant","content":[],"model":"claude-sonnet-4.5","usage":{"input_tokens":10}}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cc_ses_get"}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"cc_ses_get","name":"cc_ses_get","input":{}}}`,
				`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"literal\":\"cc_ses_get\"}"}}`,
				`data: {"type":"content_block_stop","index":1}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
				`data: {"type":"message_stop"}`,
				"",
			}, "\n\n")))}
			var result *ForwardResult
			var err error
			if streaming {
				result, err = (&GatewayService{}).handleCCStreamingFromAnthropic(resp, c, "gpt-5", "claude-sonnet-4.5", nil, time.Now())
			} else {
				result, err = (&GatewayService{}).handleCCBufferedFromAnthropic(resp, c, "gpt-5", "claude-sonnet-4.5", nil, time.Now())
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 10, result.Usage.InputTokens)
			require.Equal(t, 5, result.Usage.OutputTokens)
			payloads := [][]byte{rec.Body.Bytes()}
			if streaming {
				payloads = nil
				for _, frame := range strings.Split(rec.Body.String(), "\n\n") {
					if strings.HasPrefix(frame, "data: ") && frame != "data: [DONE]" {
						payloads = append(payloads, []byte(strings.TrimPrefix(frame, "data: ")))
					}
				}
				require.Equal(t, 1, strings.Count(rec.Body.String(), "data: [DONE]\n\n"))
			}
			var text, arguments, restoredName, callID string
			for _, payload := range payloads {
				require.True(t, gjson.ValidBytes(payload))
				root := "choices.0.message"
				if streaming {
					root = "choices.0.delta"
				}
				text += gjson.GetBytes(payload, root+".content").String()
				arguments += gjson.GetBytes(payload, root+".tool_calls.0.function.arguments").String()
				restoredName += gjson.GetBytes(payload, root+".tool_calls.0.function.name").String()
				callID += gjson.GetBytes(payload, root+".tool_calls.0.id").String()
			}
			require.Equal(t, "session_get", restoredName)
			require.Equal(t, "cc_ses_get", text)
			require.Equal(t, "cc_ses_get", callID)
			require.JSONEq(t, `{"literal":"cc_ses_get"}`, arguments)
		})
	}
}
