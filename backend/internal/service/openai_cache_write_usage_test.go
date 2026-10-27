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

func cacheWriteClientUsage(body string, stream, responses bool) gjson.Result {
	if !stream {
		return gjson.Get(body, "usage")
	}
	var usage gjson.Result
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		event := gjson.Parse(strings.TrimPrefix(line, "data: "))
		if !responses && event.Get("type").String() == "message_delta" {
			usage = event.Get("usage")
		}
		if responses && event.Get("type").String() == "response.completed" {
			usage = event.Get("response.usage")
		}
	}
	return usage
}

func TestCacheWriteUsage_ActualChatReadersMatchRawAccounting(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		want         int
	}{
		{"creation_input", `"cache_creation_input_tokens":800`, 800},
		{"creation", `"cache_creation_tokens":800`, 800},
		{"write", `"cache_write_tokens":800`, 800},
		{"top_priority", `"cache_creation_input_tokens":800,"cache_creation_tokens":700,"cache_write_tokens":600`, 800},
		{"nested_zero", `"prompt_tokens_details":{"cache_write_tokens":0,"cache_creation_tokens":800},"cache_creation_input_tokens":900`, 0},
		{"nested_null", `"prompt_tokens_details":{"cache_write_tokens":null,"cache_creation_tokens":800},"cache_creation_input_tokens":900`, 0},
		{"nested_negative", `"prompt_tokens_details":{"cache_write_tokens":-1,"cache_creation_tokens":800},"cache_creation_input_tokens":900`, 0},
		{"nested_write_winner", `"prompt_tokens_details":{"cache_write_tokens":600,"cache_creation_tokens":800}`, 600},
		{"input_null_winner", `"input_tokens_details":{"cache_write_tokens":null},"prompt_tokens_details":{"cache_creation_tokens":800}`, 0},
		{"unsupported_alias", `"cache_write_input_tokens":800`, 0},
		{"ordinary", `"vendor":1`, 0},
	} {
		for _, route := range []string{"direct_messages", "legacy_messages", "responses"} {
			for _, stream := range []bool{false, true} {
				t.Run(tc.name+"/"+route+map[bool]string{false: "/buffered", true: "/stream"}[stream], func(t *testing.T) {
					usage := `{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050,` + tc.fields + `}`
					require.Equal(t, tc.want, openAICacheWriteTokens(gjson.Parse(usage)), "unchanged HTTP oracle")
					wire := `{"id":"cache_usage","model":"gpt-5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":` + usage + `}`
					if stream {
						wire = "data: {\"id\":\"cache_usage\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"cache_usage\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":" + usage + "}\n\ndata: [DONE]\n\n"
					}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
					s := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
					resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(wire))}
					account := rawChatCompletionsTestAccount()
					var result *OpenAIForwardResult
					var err error
					if route == "responses" {
						if stream {
							result, err = s.streamChatCompletionsAsResponses(c, resp, account, "gpt-5", nil, nil, false, nil, "gpt-5", "gpt-5", nil, nil, time.Now())
						} else {
							result, err = s.bufferChatCompletionsAsResponses(c, resp, account, "gpt-5", nil, nil, false, nil, "gpt-5", "gpt-5", nil, nil, time.Now())
						}
					} else if stream {
						result, err = s.streamChatCompletionsAsAnthropic(c, resp, account, "gpt-5", "gpt-5", "gpt-5", nil, nil, time.Now(), route == "direct_messages")
					} else {
						result, err = s.bufferChatCompletionsAsAnthropic(c, resp, account, "gpt-5", "gpt-5", "gpt-5", nil, nil, time.Now(), route == "direct_messages")
					}
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, 1000, result.Usage.InputTokens)
					require.Equal(t, 50, result.Usage.OutputTokens)
					require.Equal(t, tc.want, result.Usage.CacheCreationInputTokens)
					client := cacheWriteClientUsage(rec.Body.String(), stream, route == "responses")
					require.True(t, client.IsObject(), rec.Body.String())
					require.EqualValues(t, tc.want, client.Get("cache_creation_input_tokens").Int())
					wantInput := 1000
					if route != "responses" {
						wantInput -= tc.want
					}
					require.EqualValues(t, wantInput, client.Get("input_tokens").Int())
					require.EqualValues(t, 50, client.Get("output_tokens").Int())
					require.Contains(t, rec.Body.String(), "ok")
				})
			}
		}
	}
}
