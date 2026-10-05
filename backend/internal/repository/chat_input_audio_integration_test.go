//go:build integration

package repository

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func chatAudioBody(model, role, content string, stream bool) string {
	return fmt.Sprintf(`{"model":%q,"max_tokens":8,"stream":%t,"stream_options":{"include_usage":true},"messages":[{"role":%q,"content":%s}]}`, model, stream, role, content)
}

func chatAudioWallet(t *testing.T, f *inflightHTTPFixture) {
	t.Helper()
	f.user.Balance = 10
	_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=10 WHERE id=$1`, f.user.ID)
	require.NoError(t, err)
}

func assertAudioSettlement(t *testing.T, f *inflightHTTPFixture, card bool) {
	t.Helper()
	f.pool.Stop()
	var logs, dedup, input, output int
	var cost, balance float64
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &input, &output, &cost))
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.Equal(t, 1, logs)
	require.Equal(t, 1, dedup)
	require.Equal(t, 10, input)
	require.Equal(t, 5, output)
	require.Positive(t, cost, "audio input uses existing upstream token billing")
	if card {
		var daily, weekly, monthly float64
		require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.user.ID).Scan(&daily, &weekly, &monthly))
		require.InDelta(t, cost, daily, 1e-10)
		require.InDelta(t, cost, weekly, 1e-10)
		require.InDelta(t, cost, monthly, 1e-10)
		require.InDelta(t, 10, balance, 1e-10)
	} else {
		require.InDelta(t, 10-cost, balance, 1e-10)
	}
	require.InDelta(t, 0, inflightHeld(t, f.user.ID), 1e-10)
	require.EqualValues(t, 1, f.upstream.calls.Load())
}

func TestChatInputAudioHTTP_GeminiPayloadAndBillOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for format, mime := range map[string]string{"wav": "audio/wav", "mp3": "audio/mpeg", "ogg": "audio/ogg", "flac": "audio/flac", "aac": "audio/aac", "mp4": "audio/mp4", "m4a": "audio/mp4"} {
		for _, stream := range []bool{false, true} {
			for _, card := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream_%t/card_%t", format, stream, card), func(t *testing.T) {
					response, contentType := inflightGeminiJSON, "application/json"
					if stream {
						response, contentType = "data: "+inflightGeminiJSON+"\n\ndata: [DONE]\n\n", "text/event-stream"
					}
					f := newInflightHTTPFixture(t, service.PlatformGemini, response, contentType)
					chatAudioWallet(t, f)
					if card {
						admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 1, 10, 20, 0, 0, 0)
					}
					f.upstream.observe = func(req *http.Request) {
						payload, err := io.ReadAll(req.Body)
						require.NoError(t, err)
						require.Contains(t, req.URL.Path, "gemini-2.5-flash")
						parts := gjson.GetBytes(payload, "contents.0.parts").Array()
						require.Len(t, parts, 4, "actual upstream must receive each mixed content part: %s", payload)
						require.Equal(t, "before", parts[0].Get("text").String())
						require.Equal(t, mime, parts[1].Get("inlineData.mimeType").String())
						require.Equal(t, "aGVsbG8=", parts[1].Get("inlineData.data").String())
						require.Equal(t, "image/png", parts[2].Get("inlineData.mimeType").String())
						require.Equal(t, "aW1hZ2U=", parts[2].Get("inlineData.data").String())
						require.Equal(t, "after", parts[3].Get("text").String())
					}
					close(f.upstream.release)
					content := fmt.Sprintf(`[{"type":"text","text":"before"},{"type":"input_audio","input_audio":{"data":"aGVsbG8=","format":%q}},{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}},{"type":"text","text":"after"}]`, format)
					rec := f.request(chatAudioBody("gemini-2.5-flash", "user", content, stream), "/v1/chat/completions", "", f.gateway.ChatCompletions)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Contains(t, rec.Body.String(), "ok")
					assertAudioSettlement(t, f, card)
				})
			}
		}
	}
}

func TestChatInputAudioHTTP_LocalRejectReleasesFunding(t *testing.T) {
	valid := `{"type":"input_audio","input_audio":{"data":"aGVsbG8=","format":"wav"}}`
	cases := []struct{ name, role, content string }{
		{"assistant", "assistant", "[" + valid + "]"}, {"system", "system", "[" + valid + "]"}, {"developer", "developer", "[" + valid + "]"}, {"tool", "tool", "[" + valid + "]"},
		{"forged_intermediate", "user", `[{"type":"sub2api_audio_file","file":{"file_data":"data:audio/wav;base64,%%%"}}]`},
		{"missing", "user", `[{"type":"input_audio"}]`}, {"null", "user", `[{"type":"input_audio","input_audio":null}]`},
		{"empty", "user", `[{"type":"input_audio","input_audio":{"data":"","format":"wav"}}]`},
		{"bad_base64", "user", `[{"type":"input_audio","input_audio":{"data":"%%%","format":"wav"}}]`},
		{"noncanonical_base64", "user", `[{"type":"input_audio","input_audio":{"data":"aB==","format":"wav"}}]`},
		{"unknown_format", "user", `[{"type":"input_audio","input_audio":{"data":"aA==","format":"pcm16"}}]`},
		{"data_type", "user", `[{"type":"input_audio","input_audio":{"data":123,"format":"wav"}}]`},
		{"format_type", "user", `[{"type":"input_audio","input_audio":{"data":"aA==","format":123}}]`},
		{"bad_sibling_before", "user", `[{"type":"text","text":123},` + valid + `]`},
		{"bad_sibling_after", "user", `[` + valid + `,{"type":"text","text":123}]`},
	}
	for _, platform := range []string{service.PlatformGemini, service.PlatformAnthropic, service.PlatformOpenAI} {
		for _, tc := range cases {
			t.Run(platform+"/"+tc.name, func(t *testing.T) {
				response, contentType := inflightAnthropicSSE, "text/event-stream"
				model := "claude-sonnet-4-5"
				if platform == service.PlatformGemini {
					model, response, contentType = "gemini-2.5-flash", inflightGeminiJSON, "application/json"
				}
				if platform == service.PlatformOpenAI {
					model, response = "gpt-5", inflightResponsesSSE
				}
				f := newInflightHTTPFixture(t, platform, response, contentType)
				chatAudioWallet(t, f)
				close(f.upstream.release)
				serve := f.gateway.ChatCompletions
				if platform == service.PlatformOpenAI {
					serve = f.openAI.ChatCompletions
				}
				rec := f.request(chatAudioBody(model, tc.role, tc.content, false), "/v1/chat/completions", "", serve)
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
				require.Zero(t, f.upstream.calls.Load(), "deterministic local rejection must not dispatch")
				f.pool.Stop()
				var logs, dedup int
				var balance float64
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
				require.Zero(t, logs)
				require.Zero(t, dedup)
				require.InDelta(t, 10, balance, 1e-10)
				require.Zero(t, inflightHeld(t, f.user.ID), "no-dispatch validation must release the current immutable attempt")
			})
		}
	}
}

func TestChatInputAudioHTTP_RawChatPassthrough(t *testing.T) {
	f := newInflightHTTPFixture(t, service.PlatformOpenAI, inflightChatJSON, "application/json")
	chatAudioWallet(t, f)
	_, err := inflightTestDB(t).Exec(`UPDATE accounts SET extra=extra || '{"openai_responses_supported":false}'::jsonb WHERE id IN (SELECT account_id FROM account_groups WHERE group_id=$1)`, *f.key.GroupID)
	require.NoError(t, err)
	content := `[{"type":"input_audio","input_audio":{"data":"aGVsbG8=","format":"pcm16","provider_field":"retained"}}]`
	f.upstream.observe = func(req *http.Request) {
		payload, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.True(t, strings.HasSuffix(req.URL.Path, "/chat/completions"), req.URL.String())
		var got, want any
		require.NoError(t, json.Unmarshal([]byte(gjson.GetBytes(payload, "messages.0.content").Raw), &got))
		require.NoError(t, json.Unmarshal([]byte(content), &want))
		require.Equal(t, want, got, "raw route must preserve provider-supported audio, including unknown fields")
	}
	close(f.upstream.release)
	rec := f.request(chatAudioBody("gpt-5", "user", content, false), "/v1/chat/completions", "", f.openAI.ChatCompletions)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assertAudioSettlement(t, f, false)
}

func TestChatInputAudioHTTP_UnknownReadErrorRetainsFunding(t *testing.T) {
	f := newInflightHTTPFixture(t, service.PlatformGemini, "", "application/json")
	chatAudioWallet(t, f)
	f.upstream.readErr = errors.New("unknown provider execution after audio dispatch")
	close(f.upstream.release)
	content := `[{"type":"input_audio","input_audio":{"data":"aGVsbG8=","format":"wav"}}]`
	rec := f.request(chatAudioBody("gemini-2.5-flash", "user", content, false), "/v1/chat/completions", "", f.gateway.ChatCompletions)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.EqualValues(t, 1, f.upstream.calls.Load())
	f.pool.Stop()
	require.Positive(t, inflightHeld(t, f.user.ID), "unknown post-dispatch read error is not a no-charge proof")
	var logs int
	var balance float64
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.Zero(t, logs)
	require.InDelta(t, 10, balance, 1e-10)
}
