//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
						require.Contains(t, req.URL.Path, "gemini-3.6-flash")
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
					rec := f.request(chatAudioBody("gemini-3.6-flash", "user", content, stream), "/v1/chat/completions", "", f.gateway.ChatCompletions)
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
		{"null_sibling", "user", `[null,` + valid + `]`},
		{"null_text_sibling", "user", `[{"type":"text","text":null},` + valid + `]`},
		{"null_type_sibling", "user", `[{"type":null},` + valid + `]`},
		{"missing_type_sibling", "user", `[{},` + valid + `]`},
		{"array_sibling", "user", `[[],` + valid + `]`},
		{"null_image_sibling", "user", `[{"type":"image_url","image_url":null},` + valid + `]`},
		{"null_file_sibling", "user", `[{"type":"file","file":null},` + valid + `]`},
		{"null_image_url_sibling", "user", `[{"type":"image_url","image_url":{"url":null}},` + valid + `]`},
		{"null_file_data_sibling", "user", `[{"type":"file","file":{"file_data":null}},` + valid + `]`},
	}
	for _, platform := range []string{service.PlatformGemini, service.PlatformAnthropic, service.PlatformOpenAI} {
		for _, tc := range cases {
			t.Run(platform+"/"+tc.name, func(t *testing.T) {
				response, contentType := inflightAnthropicSSE, "text/event-stream"
				model := "claude-sonnet-4-5"
				if platform == service.PlatformGemini {
					model, response, contentType = "gemini-3.6-flash", inflightGeminiJSON, "application/json"
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
				var activeLeases int
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM billing_inflight_leases WHERE user_id=$1 AND expires_at>clock_timestamp()`, f.user.ID).Scan(&activeLeases))
				require.Zero(t, activeLeases, "local rejection must not leave an exclusive zero-valued owner either")
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
	rec := f.request(chatAudioBody("gemini-3.6-flash", "user", content, false), "/v1/chat/completions", "", f.gateway.ChatCompletions)
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

func TestChatInputAudioHTTP_DefaultMessagesKeepOriginalDocumentFallback(t *testing.T) {
	for _, role := range []string{"user", "assistant"} {
		for _, mime := range []string{"audio/wav", "audio/unknown"} {
			for _, data := range []string{"aA==", "%%%"} {
				t.Run(role+"/"+mime+"/"+data, func(t *testing.T) {
					f := newInflightHTTPFixture(t, service.PlatformGemini, inflightGeminiJSON, "application/json")
					chatAudioWallet(t, f)
					block := map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": mime, "data": data}}
					body, err := json.Marshal(map[string]any{"allowValidatedChatAudio": true, "allow_audio": true, "model": "gemini-3.6-flash", "max_tokens": 8, "messages": []any{map[string]any{"role": role, "content": []any{block}}}})
					require.NoError(t, err)
					f.upstream.observe = func(req *http.Request) {
						payload, err := io.ReadAll(req.Body)
						require.NoError(t, err)
						parts := gjson.GetBytes(payload, "contents.0.parts").Array()
						require.Len(t, parts, 1)
						require.False(t, parts[0].Get("inlineData").Exists(), "new Chat audio option must not change native Messages document behavior")
						encoded, err := json.Marshal(block)
						require.NoError(t, err)
						require.JSONEq(t, string(encoded), parts[0].Get("text").String())
					}
					close(f.upstream.release)
					rec := f.request(string(body), "/v1/messages", "", f.gateway.Messages)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					assertAudioSettlement(t, f, false)
				})
			}
		}
	}
}

func TestChatInputAudioHTTP_MixedToolTurnPreserved(t *testing.T) {
	f := newInflightHTTPFixture(t, service.PlatformGemini, inflightGeminiJSON, "application/json")
	chatAudioWallet(t, f)
	f.upstream.observe = func(req *http.Request) {
		payload, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		contents := gjson.GetBytes(payload, "contents").Array()
		require.Len(t, contents, 2, "tool pairing must remain intact: %s", payload)
		require.Equal(t, "model", contents[0].Get("role").String())
		require.Equal(t, "echo", contents[0].Get("parts.0.functionCall.name").String())
		require.Equal(t, "hello", contents[0].Get("parts.0.functionCall.args.text").String())
		require.Equal(t, "user", contents[1].Get("role").String())
		parts := contents[1].Get("parts").Array()
		require.Len(t, parts, 4)
		require.Equal(t, "echo", parts[0].Get("functionResponse.name").String())
		require.Equal(t, "before", parts[1].Get("text").String())
		require.Equal(t, "audio/wav", parts[2].Get("inlineData.mimeType").String())
		require.Equal(t, "aGVsbG8=", parts[2].Get("inlineData.data").String())
		require.Equal(t, "after", parts[3].Get("text").String())
	}
	close(f.upstream.release)
	body := `{"model":"gemini-3.6-flash","max_tokens":8,"messages":[{"role":"assistant","content":"","tool_calls":[{"id":"call_audio","type":"function","function":{"name":"echo","arguments":"{\"text\":\"hello\"}"}}]},{"role":"tool","tool_call_id":"call_audio","content":"done"},{"role":"user","content":[{"type":"text","text":"before"},{"type":"input_audio","input_audio":{"data":"aGVsbG8=","format":"wav"}},{"type":"text","text":"after"}]}]}`
	rec := f.request(body, "/v1/chat/completions", "", f.gateway.ChatCompletions)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assertAudioSettlement(t, f, false)
}

// A real handler sees the exact writer state established by a wait-loop ping.
// Do not cancel its context: the failed frame itself must stop further Flush.
type chatAudioFrameWriter struct {
	gin.ResponseWriter
	fail              bool
	failed            bool
	errorFrames       int
	flushAfterFailure int
}

func (w *chatAudioFrameWriter) Write(data []byte) (int, error) {
	if strings.HasPrefix(string(data), "data: ") {
		w.errorFrames++
		if w.fail {
			w.failed = true
			return 0, errors.New("client stopped accepting error frame")
		}
	}
	return w.ResponseWriter.Write(data)
}
func (w *chatAudioFrameWriter) Flush() {
	if w.failed {
		w.flushAfterFailure++
	}
	w.ResponseWriter.Flush()
}

func TestChatInputAudioHTTP_RejectAfterWaitPingKeepsSSEAndFunding(t *testing.T) {
	for _, platform := range []string{service.PlatformGemini, service.PlatformAnthropic, service.PlatformOpenAI} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/write_failure_%t", platform, fail), func(t *testing.T) {
				model, response, contentType := "claude-sonnet-4-5", inflightAnthropicSSE, "text/event-stream"
				if platform == service.PlatformGemini {
					model, response, contentType = "gemini-3.6-flash", inflightGeminiJSON, "application/json"
				}
				if platform == service.PlatformOpenAI {
					model, response = "gpt-5", inflightResponsesSSE
				}
				f := newInflightHTTPFixture(t, platform, response, contentType)
				chatAudioWallet(t, f)
				close(f.upstream.release)
				content := `[{"type":"input_audio","input_audio":{"data":"%%%","format":"wav"}}]`
				var writer *chatAudioFrameWriter
				var opsValue any
				rec := f.request(chatAudioBody(model, "user", content, true), "/v1/chat/completions", "", func(c *gin.Context) {
					c.Header("Content-Type", "text/event-stream")
					_, err := c.Writer.WriteString(": ping\n\n")
					require.NoError(t, err)
					c.Writer.Flush()
					writer = &chatAudioFrameWriter{ResponseWriter: c.Writer, fail: fail}
					c.Writer = writer
					if platform == service.PlatformOpenAI {
						f.openAI.ChatCompletions(c)
					} else {
						f.gateway.ChatCompletions(c)
					}
					opsValue, _ = c.Get(service.OpsStreamErrorKey)
				})
				require.Equal(t, http.StatusOK, rec.Code, "the already committed wait transport status cannot change")
				require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
				ops, ok := opsValue.(service.OpsStreamError)
				require.True(t, ok)
				require.True(t, ops.RequestScoped)
				require.False(t, ops.CountTowardsSLA)
				require.False(t, ops.UpstreamAttributed)
				require.Equal(t, http.StatusBadRequest, ops.IntendedStatus)
				require.Equal(t, "invalid_request_error", ops.Code)
				require.Equal(t, 1, writer.errorFrames)
				require.Zero(t, writer.flushAfterFailure)
				if fail {
					require.Equal(t, ": ping\n\n", rec.Body.String())
				} else {
					require.True(t, strings.HasPrefix(rec.Body.String(), ": ping\n\ndata: "), rec.Body.String())
					frame := strings.TrimSpace(strings.TrimPrefix(rec.Body.String(), ": ping\n\ndata: "))
					require.True(t, gjson.Valid(frame), "one valid JSON error SSE frame")
					require.Equal(t, "invalid_request_error", gjson.Get(frame, "error.type").String())
					require.Equal(t, "invalid_request_error", gjson.Get(frame, "error.code").String())
				}
				f.pool.Stop()
				require.Zero(t, f.upstream.calls.Load(), "local refusal cannot call the provider")
				require.Zero(t, inflightHeld(t, f.user.ID))
				var logs, dedup, activeLeases int
				var balance float64
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM billing_inflight_leases WHERE user_id=$1 AND expires_at>clock_timestamp()`, f.user.ID).Scan(&activeLeases))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
				require.Zero(t, logs)
				require.Zero(t, dedup)
				require.Zero(t, activeLeases)
				require.InDelta(t, 10, balance, 1e-10)
			})
		}
	}
}

func TestChatInputAudioHTTP_LocalRejectDoesNotReportAccountHealth(t *testing.T) {
	f := newInflightHTTPFixture(t, service.PlatformOpenAI, inflightResponsesSSE, "text/event-stream")
	chatAudioWallet(t, f)
	close(f.upstream.release)
	f.rateLimit.SetSettingService(f.settings)
	original, err := f.settings.GetAllSettings(context.Background())
	require.NoError(t, err)
	enabled := *original
	enabled.OpenAIAdvancedSchedulerEnabled = true
	require.NoError(t, f.settings.UpdateSettings(context.Background(), &enabled))
	t.Cleanup(func() { require.NoError(t, f.settings.UpdateSettings(context.Background(), original)) })
	// The other account's seeded runtime observation must remain; the actual
	// locally rejected account must acquire neither a failure nor a success row.
	f.openAIService.ReportOpenAIAccountScheduleResult(9991598, false, nil)
	require.Equal(t, 1, f.openAIService.SnapshotOpenAIAccountSchedulerMetrics().RuntimeStatsAccountCount)
	body := chatAudioBody("gpt-5", "user", `[{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`, false)
	response := f.request(body, "/v1/chat/completions", "", f.openAI.ChatCompletions)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.Equal(t, "invalid_request_error", gjson.Get(response.Body.String(), "error.type").String())
	require.Equal(t, 1, f.openAIService.SnapshotOpenAIAccountSchedulerMetrics().RuntimeStatsAccountCount)
	require.Zero(t, f.upstream.calls.Load())
	require.Zero(t, inflightHeld(t, f.user.ID))
}

type chatAudioNoIOWriter struct {
	gin.ResponseWriter
	writes, flushes int
}

func (w *chatAudioNoIOWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.ResponseWriter.Write(p)
}
func (w *chatAudioNoIOWriter) WriteString(p string) (int, error) {
	w.writes++
	return w.ResponseWriter.WriteString(p)
}
func (w *chatAudioNoIOWriter) Flush() { w.flushes++; w.ResponseWriter.Flush() }

// Cancel after the real PG admission, then invoke each actual converter.
// A public handler canceled earlier would stop in concurrency acquisition and
// would not prove this local-validation/no-charge writer boundary.
func TestChatInputAudioHTTP_CanceledLocalServiceReleasesFundingWithoutIO(t *testing.T) {
	for _, platform := range []string{service.PlatformGemini, service.PlatformAnthropic, service.PlatformOpenAI} {
		for _, committed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/committed_%t", platform, committed), func(t *testing.T) {
				model := "claude-sonnet-4-5"
				if platform == service.PlatformGemini {
					model = "gemini-3.6-flash"
				}
				if platform == service.PlatformOpenAI {
					model = "gpt-5"
				}
				f := newInflightHTTPFixture(t, platform, inflightResponsesSSE, "text/event-stream")
				chatAudioWallet(t, f)
				close(f.upstream.release)
				body := []byte(chatAudioBody(model, "user", `[{"type":"input_audio","input_audio":{"data":"%%%","format":"wav"}}]`, true))
				lease, err := f.openAIService.ReserveBillingInflight(context.Background(), service.BillingInflightRequest{APIKey: f.key, Account: f.account, Model: model, Body: body})
				require.NoError(t, err)
				require.NotNil(t, lease)
				t.Cleanup(lease.HandlerDone)
				lease.MarkDispatched() // Same pre-Forward phase used by the HTTP wrapper.
				var activeBefore int
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM billing_inflight_leases WHERE user_id=$1 AND expires_at>clock_timestamp()`, f.user.ID).Scan(&activeBefore))
				require.Positive(t, activeBefore)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
				if committed {
					c.Header("Content-Type", "text/event-stream")
					_, err := c.Writer.WriteString(": ping\n\n")
					require.NoError(t, err)
					c.Writer.Flush()
				}
				initial := recorder.Body.String()
				writer := &chatAudioNoIOWriter{ResponseWriter: c.Writer}
				c.Writer = writer
				ctx, cancel := context.WithCancel(service.WithBillingInflightLease(c.Request.Context(), lease))
				c.Request = c.Request.WithContext(ctx)
				cancel()
				if platform == service.PlatformOpenAI {
					result, forwardErr := f.openAIService.ForwardAsChatCompletions(ctx, c, f.account, body, "", "")
					require.Nil(t, result)
					require.ErrorContains(t, forwardErr, "input_audio")
				} else if platform == service.PlatformGemini {
					result, forwardErr := f.geminiService.ForwardAsChatCompletions(ctx, c, f.account, body)
					require.Nil(t, result)
					require.ErrorContains(t, forwardErr, "input_audio")
				} else {
					result, forwardErr := f.gatewayService.ForwardAsChatCompletions(ctx, c, f.account, body, nil)
					require.Nil(t, result)
					require.ErrorContains(t, forwardErr, "input_audio")
				}
				lease.HandlerDone()
				require.Zero(t, writer.writes)
				require.Zero(t, writer.flushes)
				require.Equal(t, initial, recorder.Body.String())
				_, marked := c.Get(service.OpsStreamErrorKey)
				require.False(t, marked)
				require.Zero(t, f.upstream.calls.Load())
				require.Zero(t, inflightHeld(t, f.user.ID))
				var active, usage, dedup int
				var balance float64
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM billing_inflight_leases WHERE user_id=$1 AND expires_at>clock_timestamp()`, f.user.ID).Scan(&active))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&usage))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
				require.Zero(t, active)
				require.Zero(t, usage)
				require.Zero(t, dedup)
				require.InDelta(t, 10, balance, 1e-10)
			})
		}
	}
}
