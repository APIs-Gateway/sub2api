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

func chatAudioShapeHTTPInvalidSiblings() []struct{ name, part string } {
	return []struct{ name, part string }{
		{"missing_text", `{"type":"text"}`},
		{"missing_image_descriptor", `{"type":"image_url"}`},
		{"empty_image_descriptor", `{"type":"image_url","image_url":{}}`},
		{"missing_image_url", `{"type":"image_url","image_url":{"detail":"auto"}}`},
		{"empty_image_url", `{"type":"image_url","image_url":{"url":""}}`},
		{"empty_image_base64", `{"type":"image_url","image_url":{"url":"data:image/png;base64,"}}`},
		{"missing_file_descriptor", `{"type":"file"}`},
		{"empty_file_descriptor", `{"type":"file","file":{}}`},
		{"filename_only_file", `{"type":"file","file":{"filename":"note.pdf"}}`},
		{"empty_file_data", `{"type":"file","file":{"file_data":""}}`},
		{"empty_file_id", `{"type":"file","file":{"file_id":""}}`},
	}
}

func chatAudioShapeAccountHealth(t *testing.T, f *inflightHTTPFixture) string {
	t.Helper()
	var status, message string
	var schedulable bool
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT status,schedulable,COALESCE(error_message,'') FROM accounts WHERE id=$1`, f.account.ID).Scan(&status, &schedulable, &message))
	return fmt.Sprintf("%s/%t/%s", status, schedulable, message)
}

func assertAudioShapeNoCharge(t *testing.T, f *inflightHTTPFixture, card bool) {
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

func TestChatInputAudioShapeHTTP_LocalRejectActualGeminiAndFunding(t *testing.T) {
	const audio = `{"type":"input_audio","input_audio":{"data":"aGVsbG8=","format":"wav"}}`
	for _, tc := range chatAudioShapeHTTPInvalidSiblings() {
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
					health := chatAudioShapeAccountHealth(t, f)
					f.upstream.observe = func(req *http.Request) {
						payload, err := io.ReadAll(req.Body)
						require.NoError(t, err)
						require.Contains(t, req.URL.Path, "gemini-3.6-flash")
						require.JSONEq(t, `[{"inlineData":{"mimeType":"audio/wav","data":"aGVsbG8="}}]`, gjson.GetBytes(payload, "contents.0.parts").Raw)
					}
					close(f.upstream.release)
					content := "[" + tc.part + "," + audio + "]"
					rec := f.request(chatAudioBody("gemini-3.6-flash", "user", content, stream), "/v1/chat/completions", "", f.gateway.ChatCompletions)
					// OLD must first prove eligible account, real Do, stable provider
					// usage and PG wallet/card settlement. Only the shape assertions
					// below are expected RED, never setup/nil/error-wording failures.
					if f.upstream.calls.Load() != 0 {
						require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
						require.Contains(t, rec.Body.String(), "ok")
						assertAudioSettlement(t, f, card)
						t.Logf("SHAPE_ACTUAL_OLD_DISPATCH account=%d calls=%d input=10 output=5 once_usage_dedup=true card=%t", f.account.ID, f.upstream.calls.Load(), card)
					}
					require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
					require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
					assertAudioShapeNoCharge(t, f, card)
					require.Equal(t, health, chatAudioShapeAccountHealth(t, f))
				})
			}
		}
	}
}

func TestChatInputAudioShapeHTTP_KnownPartsOriginalWhitelistAndBillOnce(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, card := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream_%t/card_%t", stream, card), func(t *testing.T) {
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
					require.JSONEq(t, `[{"text":"before"},{"inlineData":{"mimeType":"audio/wav","data":"aGVsbG8="}},{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}},{"text":"after"}]`, gjson.GetBytes(payload, "contents.0.parts").Raw)
				}
				close(f.upstream.release)
				content := `[{"type":"text","text":""},{"type":"text","text":"before"},{"type":"input_audio","input_audio":{"data":"aGVsbG8=","format":"wav"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}},{"type":"file","file":{"file_data":"data:application/pdf;base64,aA=="}},{"type":"file","file":{"file_id":"existing-file"}},{"type":"provider_extra","opaque":42},{"type":"text","text":"after"}]`
				rec := f.request(chatAudioBody("gemini-3.6-flash", "user", content, stream), "/v1/chat/completions", "", f.gateway.ChatCompletions)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Contains(t, rec.Body.String(), "ok")
				assertAudioSettlement(t, f, card)
			})
		}
	}
}

func TestChatInputAudioShapeHTTP_NoAudioKnownShapesKeepPublicFiltering(t *testing.T) {
	for _, tc := range chatAudioShapeHTTPInvalidSiblings() {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream_%t", tc.name, stream), func(t *testing.T) {
				response, contentType := inflightGeminiJSON, "application/json"
				if stream {
					response, contentType = "data: "+inflightGeminiJSON+"\n\ndata: [DONE]\n\n", "text/event-stream"
				}
				f := newInflightHTTPFixture(t, service.PlatformGemini, response, contentType)
				chatAudioWallet(t, f)
				f.upstream.observe = func(req *http.Request) {
					payload, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.Contains(t, req.URL.Path, "gemini-3.6-flash")
					require.JSONEq(t, `[{"text":"before"},{"text":"after"}]`, gjson.GetBytes(payload, "contents.0.parts").Raw)
				}
				close(f.upstream.release)
				content := `[{"type":"text","text":"before"},` + tc.part + `,{"type":"text","text":"after"}]`
				rec := f.request(chatAudioBody("gemini-3.6-flash", "user", content, stream), "/v1/chat/completions", "", f.gateway.ChatCompletions)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Contains(t, rec.Body.String(), "ok")
				assertAudioSettlement(t, f, false)
			})
		}
	}
}

func TestChatInputAudioShapeHTTP_CommittedPingErrorAndNoCharge(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("write_failure_%t", fail), func(t *testing.T) {
			f := newInflightHTTPFixture(t, service.PlatformGemini, "data: "+inflightGeminiJSON+"\n\ndata: [DONE]\n\n", "text/event-stream")
			chatAudioWallet(t, f)
			close(f.upstream.release)
			content := `[{"type":"text"},{"type":"input_audio","input_audio":{"data":"aGVsbG8=","format":"wav"}}]`
			var writer *chatAudioFrameWriter
			var opsValue any
			rec := f.request(chatAudioBody("gemini-3.6-flash", "user", content, true), "/v1/chat/completions", "", func(c *gin.Context) {
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
				t.Logf("SHAPE_ACTUAL_OLD_COMMITTED_DISPATCH account=%d calls=%d input=10 output=5", f.account.ID, f.upstream.calls.Load())
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
			assertAudioShapeNoCharge(t, f, false)
		})
	}
}

func TestChatInputAudioShapeHTTP_CanceledLocalAttemptNoIO(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed_%t", committed), func(t *testing.T) {
			f := newInflightHTTPFixture(t, service.PlatformGemini, inflightGeminiJSON, "application/json")
			chatAudioWallet(t, f)
			close(f.upstream.release)
			body := []byte(chatAudioBody("gemini-3.6-flash", "user", `[{"type":"text"},{"type":"input_audio","input_audio":{"data":"aGVsbG8=","format":"wav"}}]`, true))
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
			assertAudioShapeNoCharge(t, f, false)
		})
	}
}
