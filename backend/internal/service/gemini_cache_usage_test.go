//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Upstream #8177: blocked prompts may report cached tokens without a prompt
// total. Cache reads remain billable; only the non-cache input is floored.
func TestGeminiCacheUsage_Extractor(t *testing.T) {
	cases := []struct {
		name     string
		metadata string
		want     ClaudeUsage
	}{
		{"cached_only", `{"cachedContentTokenCount":12980}`, ClaudeUsage{CacheReadInputTokens: 12980}},
		{"cached_greater_than_prompt", `{"promptTokenCount":10,"cachedContentTokenCount":30}`, ClaudeUsage{CacheReadInputTokens: 30}},
		{"cached_equal_to_prompt", `{"promptTokenCount":30,"cachedContentTokenCount":30}`, ClaudeUsage{CacheReadInputTokens: 30}},
		{"ordinary", `{"promptTokenCount":100,"cachedContentTokenCount":30}`, ClaudeUsage{InputTokens: 70, CacheReadInputTokens: 30}},
		{"zero", `{"promptTokenCount":0,"cachedContentTokenCount":0}`, ClaudeUsage{}},
		{"output_thought_image", `{"cachedContentTokenCount":30,"candidatesTokenCount":2,"thoughtsTokenCount":4,"candidatesTokensDetails":[{"modality":"TEXT","tokenCount":1},{"modality":"IMAGE","tokenCount":1}]}`, ClaudeUsage{OutputTokens: 6, CacheReadInputTokens: 30, ImageOutputTokens: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractGeminiUsage([]byte(`{"usageMetadata":` + tc.metadata + `}`))
			require.NotNil(t, got)
			require.Equal(t, tc.want, *got, "GEMINI_CACHE_USAGE_NORMALIZATION")
		})
	}
	t.Run("missing_metadata", func(t *testing.T) {
		require.Nil(t, extractGeminiUsage([]byte(`{"candidates":[]}`)))
	})
}

// Exercise all six public direct-Gemini boundaries. In particular, Chat
// Completions must contain text so its empty-completion guard cannot conceal
// a usage assertion. Preserve each existing wire format while checking full
// cache-read ownership in the actual ForwardResult used by settlement.
func TestGeminiCacheUsage_ForwardBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	counts := []struct {
		name   string
		prompt *int
		cached int
		input  int
	}{
		{"cached_only", nil, 30, 0},
		{"cached_greater_than_prompt", geminiCacheUsageInt(10), 30, 0},
		{"cached_equal_to_prompt", geminiCacheUsageInt(30), 30, 0},
		{"ordinary", geminiCacheUsageInt(100), 30, 70},
	}
	for _, route := range []string{"native", "claude", "chat"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range counts {
				t.Run(fmt.Sprintf("%s/stream_%t/%s", route, stream, tc.name), func(t *testing.T) {
					metadata := map[string]any{"cachedContentTokenCount": tc.cached, "candidatesTokenCount": 2, "thoughtsTokenCount": 4}
					if tc.prompt != nil {
						metadata["promptTokenCount"] = *tc.prompt
					}
					raw, err := json.Marshal(map[string]any{
						"candidates":    []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "cache response"}}, "role": "model"}, "finishReason": "STOP"}},
						"usageMetadata": metadata,
					})
					require.NoError(t, err)
					body, contentType := string(raw), "application/json"
					if stream {
						body, contentType = "data: "+body+"\n\n", "text/event-stream"
					}
					svc := newGeminiSignalService(contentType, body)
					c, rec := newGeminiNativeTestContext(t)
					c.Request = c.Request.WithContext(context.Background())
					var result *ForwardResult
					switch route {
					case "native":
						action := "generateContent"
						if stream {
							action = "streamGenerateContent"
						}
						result, err = svc.ForwardNative(c.Request.Context(), c, geminiSignalTestAccount(), "gemini-2.5-flash", action, stream, geminiSignalTestRequest())
					case "claude":
						request := []byte(fmt.Sprintf(`{"model":"gemini-2.5-flash","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":%t}`, stream))
						result, err = svc.Forward(c.Request.Context(), c, geminiSignalTestAccount(), request)
					case "chat":
						request := []byte(fmt.Sprintf(`{"model":"gemini-2.5-flash","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":%t,"stream_options":{"include_usage":true}}`, stream))
						result, err = svc.ForwardAsChatCompletions(c.Request.Context(), c, geminiSignalTestAccount(), request)
					}
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, http.StatusOK, rec.Code)
					require.Contains(t, rec.Body.String(), "cache response")
					require.Equal(t, tc.input, result.Usage.InputTokens, "GEMINI_CACHE_USAGE_NORMALIZATION")
					require.Equal(t, tc.cached, result.Usage.CacheReadInputTokens)
					require.Equal(t, 6, result.Usage.OutputTokens)
					require.Equal(t, stream, result.Stream)
					if route == "native" {
						require.Equal(t, body, rec.Body.String(), "native bytes must remain untouched")
					} else {
						wireUsage := gjson.Get(rec.Body.String(), "usage")
						if stream {
							for _, line := range strings.Split(rec.Body.String(), "\n") {
								if strings.HasPrefix(line, "data:") {
									frame := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
									if frame.Get("type").String() == "message_delta" || (route == "chat" && frame.Get("usage").Exists()) {
										wireUsage = frame.Get("usage")
									}
								}
							}
						}
						require.True(t, wireUsage.Exists(), rec.Body.String())
						if route == "chat" {
							// The existing nonstream converter omits cache-read in its
							// intermediate Claude wire map. Preserve that presentation;
							// settlement still sees the complete ForwardResult above.
							wantPrompt := tc.input
							if stream {
								wantPrompt += tc.cached
								require.EqualValues(t, tc.cached, wireUsage.Get("prompt_tokens_details.cached_tokens").Int())
							}
							require.EqualValues(t, wantPrompt, wireUsage.Get("prompt_tokens").Int())
							require.EqualValues(t, 6, wireUsage.Get("completion_tokens").Int())
						} else {
							// Existing direct Claude wire emits input/output only;
							// cache-read remains observable in ForwardResult above.
							require.EqualValues(t, tc.input, wireUsage.Get("input_tokens").Int())
							require.EqualValues(t, 6, wireUsage.Get("output_tokens").Int())
						}
					}
					require.Empty(t, opsStreamErrorsForTest(c))
				})
			}
		}
	}
}

func geminiCacheUsageInt(n int) *int { return &n }

func TestGeminiCacheUsage_BlockedNativeRetainsSignalAndWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%t", stream), func(t *testing.T) {
			body := `{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"cachedContentTokenCount":12980}}`
			contentType, action := "application/json", "generateContent"
			if stream {
				body, contentType, action = "data: "+body+"\n\n", "text/event-stream", "streamGenerateContent"
			}
			svc := newGeminiSignalService(contentType, body)
			c, rec := newGeminiNativeTestContext(t)
			result, err := svc.ForwardNative(c.Request.Context(), c, geminiSignalTestAccount(), "gemini-2.5-flash", action, stream, geminiSignalTestRequest())
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, body, rec.Body.String())
			require.Zero(t, result.Usage.InputTokens, "GEMINI_CACHE_USAGE_NORMALIZATION")
			require.Zero(t, result.Usage.OutputTokens)
			require.Equal(t, 12980, result.Usage.CacheReadInputTokens)
			signals := opsStreamErrorsForTest(c)
			require.Len(t, signals, 1)
			require.Equal(t, "invalid_request_error", signals[0].ErrType)
			require.Equal(t, "SAFETY", signals[0].Code)
			require.True(t, signals[0].RequestScoped)
			require.False(t, signals[0].CountTowardsSLA)
			require.False(t, signals[0].UpstreamAttributed)
			require.Equal(t, !stream, signals[0].NonStream)
			require.Empty(t, upstreamErrorEventsFromContext(t, c))
		})
	}
}
