//go:build integration

package repository

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func chatAudioOuterHTTPCases() []struct{ name, fields, selected string } {
	const audio = `[{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`
	var cases []struct{ name, fields, selected string }
	for _, key := range []struct{ name, content, messages, role string }{
		{"duplicate", `"content"`, `"messages"`, `"role"`},
		{"casefold", `"CONTENT"`, `"MESSAGES"`, `"ROLE"`},
		{"escaped", `"\u0063ontent"`, `"\u006dessages"`, `"\u0072ole"`},
	} {
		for _, reverse := range []bool{false, true} {
			first, last, selected := audio, `"hidden"`, "text"
			if reverse {
				first, last, selected = last, first, "audio"
			}
			cases = append(cases, struct{ name, fields, selected string }{
				fmt.Sprintf("content_%s/reverse_%t", key.name, reverse),
				`"messages":[{"role":"user","content":` + first + `,` + key.content + `:` + last + `}]`, selected,
			})
			cases = append(cases, struct{ name, fields, selected string }{
				fmt.Sprintf("messages_%s/reverse_%t", key.name, reverse),
				`"messages":[{"role":"user","content":` + first + `}],` + key.messages + `:[{"role":"user","content":` + last + `}]`, selected,
			})
			firstRole, lastRole := "assistant", "user"
			selected = "audio"
			if reverse {
				firstRole, lastRole, selected = lastRole, firstRole, "already_unsupported"
			}
			cases = append(cases, struct{ name, fields, selected string }{
				fmt.Sprintf("role_%s/reverse_%t", key.name, reverse),
				`"messages":[{"role":"` + firstRole + `",` + key.role + `:"` + lastRole + `","content":` + audio + `}]`, selected,
			})
		}
	}
	return cases
}

func chatAudioOuterAccountHealth(t *testing.T, f *inflightHTTPFixture) string {
	t.Helper()
	var status, message string
	var schedulable bool
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT status,schedulable,COALESCE(error_message,'') FROM accounts WHERE id=$1`, f.account.ID).Scan(&status, &schedulable, &message))
	return fmt.Sprintf("%s/%t/%s", status, schedulable, message)
}

func assertAudioOuterNoCharge(t *testing.T, f *inflightHTTPFixture, card bool) {
	t.Helper()
	f.pool.Stop()
	require.Zero(t, f.upstream.calls.Load())
	require.Zero(t, inflightHeld(t, f.user.ID))
	var logs, dedup, active int
	var balance float64
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM billing_inflight_leases WHERE user_id=$1 AND expires_at>clock_timestamp()`, f.user.ID).Scan(&active))
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.Zero(t, logs)
	require.Zero(t, dedup)
	require.Zero(t, active)
	require.InDelta(t, 10, balance, 1e-10)
	if card {
		var daily, weekly, monthly float64
		require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.user.ID).Scan(&daily, &weekly, &monthly))
		require.Zero(t, daily)
		require.Zero(t, weekly)
		require.Zero(t, monthly)
	}
}

func chatAudioOuterHTTPBody(fields string, stream bool) string {
	return fmt.Sprintf(`{"model":"gemini-3.6-flash","max_tokens":8,"stream":%t,"stream_options":{"include_usage":true},%s}`, stream, fields)
}

func TestChatInputAudioOuterHTTP_LocalRejectActualWireAndFunding(t *testing.T) {
	for _, tc := range chatAudioOuterHTTPCases() {
		for _, stream := range []bool{false, true} {
			for _, card := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream_%t/card_%t", tc.name, stream, card), func(t *testing.T) {
					response, contentType := inflightGeminiJSON, "application/json"
					if stream {
						response, contentType = "data: "+inflightGeminiJSON+"\n\ndata: [DONE]\n\n", "text/event-stream"
					}
					f := newInflightHTTPFixture(t, service.PlatformGemini, response, contentType)
					chatAudioWallet(t, f)
					if card {
						admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 1, 10, 20, 0, 0, 0)
					}
					health := chatAudioOuterAccountHealth(t, f)
					f.upstream.observe = func(req *http.Request) {
						payload, err := io.ReadAll(req.Body)
						require.NoError(t, err)
						require.Contains(t, req.URL.Path, "gemini-3.6-flash")
						if tc.selected == "audio" {
							require.JSONEq(t, `[{"inlineData":{"mimeType":"audio/wav","data":"aA=="}}]`, gjson.GetBytes(payload, "contents.0.parts").Raw)
						} else {
							require.JSONEq(t, `[{"text":"hidden"}]`, gjson.GetBytes(payload, "contents.0.parts").Raw)
						}
					}
					close(f.upstream.release)
					rec := f.request(chatAudioOuterHTTPBody(tc.fields, stream), "/v1/chat/completions", "", f.gateway.ChatCompletions)
					if f.upstream.calls.Load() != 0 {
						require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
						require.Contains(t, rec.Body.String(), "ok")
						assertAudioSettlement(t, f, card)
						t.Logf("OUTER_ACTUAL_OLD_DISPATCH account=%d calls=1 selected=%s input=10 output=5 once_usage_dedup=true card=%t", f.account.ID, tc.selected, card)
					}
					// Reverse role already selected assistant and was refused on OLD.
					// It remains a no-charge positive control, not business RED.
					require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
					require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
					assertAudioOuterNoCharge(t, f, card)
					require.Equal(t, health, chatAudioOuterAccountHealth(t, f))
				})
			}
		}
	}
}

func TestChatInputAudioOuterHTTP_OrdinaryAndUniqueAliasPreserved(t *testing.T) {
	const audio = `[{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]`
	for _, tc := range []struct{ name, fields, first, second string }{
		{"unique_outer_aliases", `"MESSAGES":[{"ROLE":"user","CONTENT":` + audio + `}]`, `[{"inlineData":{"mimeType":"audio/wav","data":"aA=="}}]`, ""},
		{"unique_escaped_outer_fields", `"\u006dessages":[{"\u0072ole":"user","\u0063ontent":` + audio + `}]`, `[{"inlineData":{"mimeType":"audio/wav","data":"aA=="}}]`, ""},
		{"ordinary_content_duplicate", `"messages":[{"role":"user","content":"before","content":"after"}]`, `[{"text":"after"}]`, ""},
		{"ordinary_messages_duplicate", `"messages":[{"role":"user","content":"before"}],"messages":[{"role":"user","content":"after"}]`, `[{"text":"after"}]`, ""},
		{"ordinary_other_message_duplicate", `"messages":[{"role":"user","content":` + audio + `},{"role":"assistant","content":"before","content":"after"}]`, `[{"inlineData":{"mimeType":"audio/wav","data":"aA=="}}]`, "after"},
		{"string_mention", `"messages":[{"role":"user","content":"input_audio"}]`, `[{"text":"input_audio"}]`, ""},
		{"metadata_mention_and_duplicates", `"metadata":{"type":"input_audio","messages":"before","messages":"after"},"messages":[{"role":"user","content":"plain"}]`, `[{"text":"plain"}]`, ""},
		{"unknown_and_file_filter", `"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"old-file"}},{"type":"provider_extra"},{"type":"text","text":"plain"}]}]`, `[{"text":"plain"}]`, ""},
	} {
		for _, stream := range []bool{false, true} {
			for _, card := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream_%t/card_%t", tc.name, stream, card), func(t *testing.T) {
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
						require.JSONEq(t, tc.first, gjson.GetBytes(payload, "contents.0.parts").Raw)
						if tc.second != "" {
							require.Equal(t, tc.second, gjson.GetBytes(payload, "contents.1.parts.0.text").String())
						}
					}
					close(f.upstream.release)
					rec := f.request(chatAudioOuterHTTPBody(tc.fields, stream), "/v1/chat/completions", "", f.gateway.ChatCompletions)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Contains(t, rec.Body.String(), "ok")
					assertAudioSettlement(t, f, card)
				})
			}
		}
	}
}

func TestChatInputAudioOuterHTTP_CommittedPingErrorAndNoCharge(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("write_failure_%t", fail), func(t *testing.T) {
			f := newInflightHTTPFixture(t, service.PlatformGemini, "data: "+inflightGeminiJSON+"\n\ndata: [DONE]\n\n", "text/event-stream")
			chatAudioWallet(t, f)
			f.upstream.observe = func(req *http.Request) {
				payload, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.JSONEq(t, `[{"text":"hidden"}]`, gjson.GetBytes(payload, "contents.0.parts").Raw)
			}
			close(f.upstream.release)
			fields := chatAudioOuterHTTPCases()[0].fields
			var writer *chatAudioFrameWriter
			var opsValue any
			rec := f.request(chatAudioOuterHTTPBody(fields, true), "/v1/chat/completions", "", func(c *gin.Context) {
				c.Header("Content-Type", "text/event-stream")
				_, err := c.Writer.WriteString(": ping\n\n")
				require.NoError(t, err)
				c.Writer.Flush()
				writer = &chatAudioFrameWriter{ResponseWriter: c.Writer, fail: fail}
				c.Writer = writer
				f.gateway.ChatCompletions(c)
				opsValue, _ = c.Get(service.OpsStreamErrorKey)
			})
			if f.upstream.calls.Load() != 0 {
				assertAudioSettlement(t, f, false)
				t.Logf("OUTER_ACTUAL_OLD_COMMITTED_DISPATCH account=%d calls=%d input=10 output=5", f.account.ID, f.upstream.calls.Load())
			}
			require.Equal(t, http.StatusOK, rec.Code)
			ops, ok := opsValue.(service.OpsStreamError)
			require.True(t, ok)
			require.True(t, ops.RequestScoped)
			require.False(t, ops.CountTowardsSLA)
			require.False(t, ops.UpstreamAttributed)
			require.Equal(t, http.StatusBadRequest, ops.IntendedStatus)
			require.Equal(t, 1, writer.errorFrames)
			require.Zero(t, writer.flushAfterFailure)
			if fail {
				require.Equal(t, ": ping\n\n", rec.Body.String())
			} else {
				require.True(t, strings.HasPrefix(rec.Body.String(), ": ping\n\ndata: "))
				frame := strings.TrimSpace(strings.TrimPrefix(rec.Body.String(), ": ping\n\ndata: "))
				require.True(t, gjson.Valid(frame))
				require.Equal(t, "invalid_request_error", gjson.Get(frame, "error.type").String())
				require.Equal(t, "invalid_request_error", gjson.Get(frame, "error.code").String())
			}
			assertAudioOuterNoCharge(t, f, false)
		})
	}
}

func TestChatInputAudioOuterHTTP_CanceledLocalAttemptNoIO(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed_%t", committed), func(t *testing.T) {
			f := newInflightHTTPFixture(t, service.PlatformGemini, inflightGeminiJSON, "application/json")
			chatAudioWallet(t, f)
			close(f.upstream.release)
			body := []byte(chatAudioOuterHTTPBody(chatAudioOuterHTTPCases()[0].fields, true))
			lease, err := f.openAIService.ReserveBillingInflight(context.Background(), service.BillingInflightRequest{APIKey: f.key, Account: f.account, Model: "gemini-3.6-flash", Body: body})
			require.NoError(t, err)
			require.NotNil(t, lease)
			t.Cleanup(lease.HandlerDone)
			lease.MarkDispatched()
			var before int
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM billing_inflight_leases WHERE user_id=$1 AND expires_at>clock_timestamp()`, f.user.ID).Scan(&before))
			require.Positive(t, before)
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
			result, forwardErr := f.geminiService.ForwardAsChatCompletions(ctx, c, f.account, body)
			require.Nil(t, result)
			require.ErrorIs(t, forwardErr, apicompat.ErrInvalidInputAudio)
			lease.HandlerDone()
			require.Zero(t, writer.writes)
			require.Zero(t, writer.flushes)
			require.Equal(t, initial, recorder.Body.String())
			_, marked := c.Get(service.OpsStreamErrorKey)
			require.False(t, marked)
			assertAudioOuterNoCharge(t, f, false)
		})
	}
}
