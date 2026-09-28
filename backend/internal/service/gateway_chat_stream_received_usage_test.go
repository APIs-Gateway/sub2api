//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestForwardAsChatCompletionsForwardsOnlyReceivedStreamUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, option := range []struct {
		name string
		json string
	}{
		{name: "omitted"},
		{name: "true", json: `,"stream_options":{"include_usage":true}`},
		{name: "false", json: `,"stream_options":{"include_usage":false}`},
	} {
		for _, tc := range []struct {
			name              string
			startUsage        string
			deltaUsage        string
			wantUsage         bool
			wantInput         int
			wantOutput        int
			wantCacheRead     int
			wantCacheCreation int
		}{
			{
				name:              "cached",
				startUsage:        `,"usage":{"input_tokens":308,"cache_read_input_tokens":241,"cache_creation_input_tokens":17}`,
				deltaUsage:        `,"usage":{"output_tokens":49}`,
				wantUsage:         true,
				wantInput:         308,
				wantOutput:        49,
				wantCacheRead:     241,
				wantCacheCreation: 17,
			},
			{
				name:          "compatible_prompt_cache",
				startUsage:    `,"usage":{"input_tokens":1200,"prompt_tokens":1200}`,
				deltaUsage:    `,"usage":{"output_tokens":30,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}}`,
				wantUsage:     true,
				wantInput:     400,
				wantOutput:    30,
				wantCacheRead: 800,
			},
			{
				name:       "explicit_zero",
				startUsage: `,"usage":{"input_tokens":0,"output_tokens":0}`,
				wantUsage:  true,
			},
			{
				name: "missing",
			},
			{
				name:       "null",
				startUsage: `,"usage":null`,
				deltaUsage: `,"usage":null`,
			},
		} {
			for _, terminal := range []string{"stop", "eof"} {
				t.Run(option.name+"/"+tc.name+"/"+terminal, func(t *testing.T) {
					sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_usage","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5"` + tc.startUsage + `}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}` + tc.deltaUsage + `}

`
					if terminal == "stop" {
						sse += "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
					}
					body := `{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}],"stream":true` + option.json + `}`
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
					upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:       io.NopCloser(strings.NewReader(sse)),
					}}
					account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-key"}}
					svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}

					result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(body), nil)
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, tc.wantInput, result.Usage.InputTokens)
					require.Equal(t, tc.wantOutput, result.Usage.OutputTokens)
					require.Equal(t, tc.wantCacheRead, result.Usage.CacheReadInputTokens)
					require.Equal(t, tc.wantCacheCreation, result.Usage.CacheCreationInputTokens)

					usageChunks, done := 0, 0
					for _, line := range strings.Split(rec.Body.String(), "\n") {
						if !strings.HasPrefix(line, "data: ") {
							continue
						}
						payload := strings.TrimPrefix(line, "data: ")
						if payload == "[DONE]" {
							done++
							continue
						}
						require.Zero(t, done, "chunk emitted after [DONE]")
						var chunk apicompat.ChatCompletionsChunk
						require.NoError(t, json.Unmarshal([]byte(payload), &chunk))
						if chunk.Usage == nil {
							continue
						}
						usageChunks++
						require.Empty(t, chunk.Choices)
						require.Equal(t, tc.wantInput+tc.wantCacheRead+tc.wantCacheCreation, chunk.Usage.PromptTokens)
						require.Equal(t, tc.wantOutput, chunk.Usage.CompletionTokens)
						require.Equal(t, chunk.Usage.PromptTokens+tc.wantOutput, chunk.Usage.TotalTokens)
						if tc.wantCacheRead > 0 || tc.wantCacheCreation > 0 {
							require.NotNil(t, chunk.Usage.PromptTokensDetails)
							require.Equal(t, tc.wantCacheRead, chunk.Usage.PromptTokensDetails.CachedTokens)
							require.Equal(t, tc.wantCacheCreation, chunk.Usage.PromptTokensDetails.CacheCreationTokens)
						}
					}
					require.Equal(t, 1, done)
					if tc.wantUsage {
						require.Equal(t, 1, usageChunks)
					} else {
						require.Zero(t, usageChunks)
					}
				})
			}
		}
	}
}
