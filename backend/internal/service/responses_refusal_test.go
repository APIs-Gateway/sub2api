package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesRefusal_ActualChatHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, usage := range []bool{false, true} {
			t.Run(fmtRefusalCase(stream, usage), func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				terminal := `{"id":"resp_r","model":"gpt-5.5","status":"completed","output":[{"type":"message","content":[]}]}`
				if usage {
					terminal = `{"id":"resp_r","model":"gpt-5.5","status":"completed","output":[{"type":"message","content":[]}],"usage":{"input_tokens":10,"output_tokens":2}}`
				}
				body := `data: {"type":"response.created","response":{"id":"resp_r","model":"gpt-5.5"}}` + "\n\n" + `data: {"type":"response.refusal.delta","delta":"cannot help"}` + "\n\n" + `data: {"type":"response.completed","response":` + terminal + `}` + "\n\n"
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
				svc := &OpenAIGatewayService{cfg: &config.Config{}}
				account := &Account{ID: 1, Name: "oauth", Platform: PlatformOpenAI}
				var result *OpenAIForwardResult
				var err error
				if stream {
					result, err = svc.handleChatStreamingResponse(resp, c, account, "gpt-5.5", "gpt-5.5", "gpt-5.5", time.Now(), openAISilentRefusalMinRequestBodyBytes)
				} else {
					result, err = svc.handleChatBufferedStreamingResponse(resp, c, account, "gpt-5.5", "gpt-5.5", "gpt-5.5", time.Now())
				}
				require.NoError(t, err, "explicit refusal must not become an empty-stream failover")
				require.NotNil(t, result)
				require.Equal(t, http.StatusOK, rec.Code)
				require.NotContains(t, rec.Body.String(), "openai_silent_refusal")
				if stream {
					require.Contains(t, rec.Body.String(), `"refusal":"cannot help"`)
					require.Contains(t, rec.Body.String(), "data: [DONE]")
				} else {
					require.Equal(t, "cannot help", gjson.Get(rec.Body.String(), "choices.0.message.refusal").String())
				}
				if usage {
					require.Equal(t, 10, result.Usage.InputTokens)
					require.Equal(t, 2, result.Usage.OutputTokens)
				} else {
					require.Zero(t, result.Usage.InputTokens)
					require.Zero(t, result.Usage.OutputTokens)
				}
			})
		}
	}
}

func fmtRefusalCase(stream, usage bool) string {
	s := "buffered"
	if stream {
		s = "stream"
	}
	if usage {
		return s + "/usage"
	}
	return s + "/no_usage"
}

func TestResponsesRefusal_DetectorBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		want          bool
	}{
		{"chat nonempty", `{"choices":[{"delta":{"refusal":"no"}}]}`, true},
		{"chat empty", `{"choices":[{"delta":{"refusal":""}}]}`, false},
		{"chat null", `{"choices":[{"delta":{"refusal":null}}]}`, false},
		{"response delta", `{"type":"response.refusal.delta","delta":"no"}`, true},
		{"response empty delta", `{"type":"response.refusal.delta","delta":""}`, false},
		{"response terminal", `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"refusal","refusal":"no"}]}]}}`, true},
		{"response empty terminal", `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"refusal","refusal":""}]}]}}`, false},
		{"wrong response type", `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"other","refusal":"not answer"}]}]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newOpenAIChatSilentRefusalDetector(openAISilentRefusalMinRequestBodyBytes)
			d.ObservePayload([]byte(tc.payload))
			require.Equal(t, tc.want, d.HasSemanticOutput())
			require.Equal(t, tc.want, d.ShouldReleaseClientOutput())
			d.ObservePayload([]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))
			require.Equal(t, !tc.want, d.IsSilentRefusal())
		})
	}
	for _, refusal := range []string{"", "no"} {
		t.Run("typed/"+refusal, func(t *testing.T) {
			var chunk apicompat.ChatCompletionsChunk
			encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"refusal": refusal}}}})
			require.NoError(t, json.Unmarshal(encoded, &chunk))
			d := newOpenAIChatSilentRefusalDetector(0)
			d.ObserveChatChunk(chunk)
			require.Equal(t, refusal != "", d.HasSemanticOutput())
			require.Equal(t, refusal != "", d.ShouldReleaseClientOutput())
		})
	}
}

func TestResponsesRefusal_SharedResponsesReconstruction(t *testing.T) {
	for _, evt := range []string{"response.refusal.delta", "response.other.delta"} {
		t.Run(evt, func(t *testing.T) {
			raw := `data: {"type":"` + evt + `","delta":"no"}` + "\n\n"
			output, ok := reconstructResponseOutputFromSSE(raw)
			require.Equal(t, evt == "response.refusal.delta", ok)
			if ok {
				require.Equal(t, "no", gjson.GetBytes(output, "0.content.0.refusal").String())
				require.Equal(t, "refusal", gjson.GetBytes(output, "0.content.0.type").String())
			}
		})
	}
}
