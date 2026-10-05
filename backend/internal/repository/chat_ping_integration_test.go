//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const chatPingFrame = "event: ping\ndata: {\"type\":\"ping\"}\n\n"
const chatPingOverload = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n"
const chatPingRequest = `{"model":"claude-sonnet-4-5","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`

func chatPingFunding(t *testing.T, f *inflightHTTPFixture, card bool) {
	t.Helper()
	f.user.Balance = 10
	_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=10 WHERE id=$1`, f.user.ID)
	require.NoError(t, err)
	if card {
		admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 1, 10, 20, 0, 0, 0)
	}
}

func chatPingAccountB(t *testing.T, f *inflightHTTPFixture) int64 {
	t.Helper()
	account := mustCreateAccount(t, inflightTestEntClient(t), &service.Account{
		Name: uuid.NewString(), Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Priority: 100, Concurrency: 100,
		Credentials: map[string]any{"api_key": "second-fixture", "base_url": "https://second.test", "pool_mode": true, "pool_mode_retry_count": 0},
	})
	require.NoError(t, f.accounts.BindGroups(context.Background(), account.ID, []int64{*f.key.GroupID}))
	return account.ID
}

func chatPingResponse(req *http.Request, body string, readErr error, status int) *http.Response {
	var reader io.Reader = strings.NewReader(body)
	if readErr != nil {
		reader = io.MultiReader(reader, inflightHTTPReadError{readErr})
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(reader), Request: req}
}

func chatPingBillOnce(t *testing.T, f *inflightHTTPFixture, card bool) {
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
	require.Positive(t, cost)
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
}

func TestChatPingHTTP_UnmeteredFailureUsesSecondAccount(t *testing.T) {
	for _, readFailure := range []bool{false, true} {
		for _, card := range []bool{false, true} {
			t.Run(fmt.Sprintf("read_%t/card_%t", readFailure, card), func(t *testing.T) {
				f := newInflightHTTPFixture(t, service.PlatformAnthropic, "", "text/event-stream")
				chatPingFunding(t, f, card)
				second := chatPingAccountB(t, f)
				var mu sync.Mutex
				var calls []int64
				f.upstream.script = func(req *http.Request, id int64) (*http.Response, error) {
					mu.Lock()
					calls = append(calls, id)
					mu.Unlock()
					if id == f.accountID {
						if readFailure {
							return chatPingResponse(req, chatPingFrame, errors.New("upstream reader reset"), http.StatusOK), nil
						}
						return chatPingResponse(req, chatPingFrame+chatPingOverload, nil, http.StatusOK), nil
					}
					return chatPingResponse(req, inflightAnthropicSSE, nil, http.StatusOK), nil
				}
				close(f.upstream.release)
				rec := f.request(chatPingRequest, "/v1/chat/completions", "", f.gateway.ChatCompletions)
				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
				require.True(t, strings.HasPrefix(rec.Body.String(), ": ping\n\n"), rec.Body.String())
				require.Contains(t, rec.Body.String(), `"content":"ok"`)
				require.Equal(t, 1, strings.Count(rec.Body.String(), "data: [DONE]\n\n"))
				mu.Lock()
				gotCalls := append([]int64(nil), calls...)
				mu.Unlock()
				require.Equal(t, []int64{f.accountID, second}, gotCalls)
				chatPingBillOnce(t, f, card)
				if readFailure {
					require.Positive(t, inflightHeld(t, f.user.ID), "unknown transport attempt must retain its bounded reservation")
				} else {
					require.Zero(t, inflightHeld(t, f.user.ID), "explicit complete overload refusal is existing no-charge proof")
				}
			})
		}
	}
}

func TestChatPingHTTP_ExhaustionAndLaterHTTPErrorStaySSE(t *testing.T) {
	for _, laterHTTPError := range []bool{false, true} {
		t.Run(fmt.Sprint(laterHTTPError), func(t *testing.T) {
			f := newInflightHTTPFixture(t, service.PlatformAnthropic, "", "text/event-stream")
			chatPingFunding(t, f, false)
			second := chatPingAccountB(t, f)
			f.upstream.script = func(req *http.Request, id int64) (*http.Response, error) {
				if laterHTTPError && id == second {
					return chatPingResponse(req, `{"error":{"type":"invalid_request_error","message":"bad request"}}`, nil, http.StatusBadRequest), nil
				}
				return chatPingResponse(req, chatPingFrame+chatPingOverload, nil, http.StatusOK), nil
			}
			close(f.upstream.release)
			rec := f.request(chatPingRequest, "/v1/chat/completions", "", f.gateway.ChatCompletions)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
			require.Contains(t, rec.Body.String(), `data: {"error":`)
			require.Equal(t, 1, strings.Count(rec.Body.String(), "data: [DONE]\n\n"))
			require.True(t, strings.HasSuffix(rec.Body.String(), "data: [DONE]\n\n"))
			require.EqualValues(t, 2, f.upstream.calls.Load())
			f.pool.Stop()
			var logs int
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
			require.Zero(t, logs)
			if !laterHTTPError {
				require.Zero(t, inflightHeld(t, f.user.ID))
			}
		})
	}
}

func TestChatPingHTTP_MeteredPartialCannotReplay(t *testing.T) {
	// Send an actual text delta before the failure. The shared fixture seeds
	// text at block start, which the converter flushes only at finalization.
	partialBody := strings.Replace(inflightAnthropicSSE, `"text":"ok"`, `"text":""`, 1)
	partialBody = strings.Replace(partialBody, "event: content_block_stop", "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop", 1)
	f := newInflightHTTPFixture(t, service.PlatformAnthropic, chatPingFrame+strings.Replace(partialBody, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", chatPingOverload, 1), "text/event-stream")
	chatPingFunding(t, f, false)
	chatPingAccountB(t, f)
	close(f.upstream.release)
	rec := f.request(chatPingRequest, "/v1/chat/completions", "", f.gateway.ChatCompletions)
	require.Contains(t, rec.Body.String(), `"content":"ok"`)
	require.NotContains(t, rec.Body.String(), "[DONE]", "partial error cannot fabricate success")
	require.EqualValues(t, 1, f.upstream.calls.Load())
	chatPingBillOnce(t, f, false)
	require.Zero(t, inflightHeld(t, f.user.ID))
}

type chatPingFailedWriter struct{ *httptest.ResponseRecorder }

func (w *chatPingFailedWriter) Write(data []byte) (int, error) {
	if string(data) == ": ping\n\n" {
		return 0, errors.New("client ping write failed")
	}
	return w.ResponseRecorder.Write(data)
}

func TestChatPingHTTP_FailedPingStillBillsWithoutReplay(t *testing.T) {
	f := newInflightHTTPFixture(t, service.PlatformAnthropic, chatPingFrame+inflightAnthropicSSE, "text/event-stream")
	chatPingFunding(t, f, false)
	chatPingAccountB(t, f)
	close(f.upstream.release)
	writer := &chatPingFailedWriter{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatPingRequest))
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, f.key.Group))
	c.Set(string(middleware.ContextKeyAPIKey), f.key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: f.user.ID, Concurrency: 100})
	f.gateway.ChatCompletions(c)
	require.Empty(t, writer.Body.String())
	require.False(t, writer.Flushed)
	require.EqualValues(t, 1, f.upstream.calls.Load())
	chatPingBillOnce(t, f, false)
	require.Zero(t, inflightHeld(t, f.user.ID))
}

type chatPingCancelReader struct {
	io.Reader
	cancel context.CancelFunc
	once   sync.Once
}

func (r *chatPingCancelReader) Read(data []byte) (int, error) {
	n, err := r.Reader.Read(data)
	if n > 0 {
		r.once.Do(r.cancel)
	}
	return n, err
}

func TestChatPingHTTP_CancellationStillBillsWithoutReplay(t *testing.T) {
	f := newInflightHTTPFixture(t, service.PlatformAnthropic, "", "text/event-stream")
	chatPingFunding(t, f, false)
	chatPingAccountB(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.upstream.script = func(req *http.Request, _ int64) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(&chatPingCancelReader{Reader: strings.NewReader(chatPingFrame + inflightAnthropicSSE), cancel: cancel}), Request: req}, nil
	}
	close(f.upstream.release)
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatPingRequest)).WithContext(ctx)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, f.key.Group))
	c.Set(string(middleware.ContextKeyAPIKey), f.key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: f.user.ID, Concurrency: 100})
	f.gateway.ChatCompletions(c)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Empty(t, writer.Body.String())
	require.False(t, writer.Flushed)
	require.EqualValues(t, 1, f.upstream.calls.Load())
	chatPingBillOnce(t, f, false)
	require.Zero(t, inflightHeld(t, f.user.ID))
}

func TestChatPingHTTP_UnknownHoldBlocksNextDispatchWithSSEError(t *testing.T) {
	f := newInflightHTTPFixture(t, service.PlatformAnthropic, "", "text/event-stream")
	chatPingFunding(t, f, false)
	chatPingAccountB(t, f)
	var held, newBalance float64
	f.upstream.script = func(req *http.Request, _ int64) (*http.Response, error) {
		held = inflightHeld(t, f.user.ID)
		require.Positive(t, held)
		// Simulate another request spending most of the wallet after this
		// attempt was dispatched. Its unknown reservation must stay in place.
		newBalance = held * 1.1
		_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=$1 WHERE id=$2`, newBalance, f.user.ID)
		require.NoError(t, err)
		return chatPingResponse(req, chatPingFrame, errors.New("upstream reader reset"), http.StatusOK), nil
	}
	close(f.upstream.release)
	rec := f.request(chatPingRequest, "/v1/chat/completions", "", f.gateway.ChatCompletions)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), `data: {"error":`)
	require.Equal(t, 1, strings.Count(rec.Body.String(), "data: [DONE]\n\n"))
	require.EqualValues(t, 1, f.upstream.calls.Load(), "the second account cannot dispatch without funding")
	f.pool.Stop()
	require.InDelta(t, held, inflightHeld(t, f.user.ID), 1e-10, "unknown first attempt cannot be released as free")
	var balance float64
	var logs int
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
	require.Zero(t, logs)
	require.InDelta(t, newBalance, balance, 1e-10)
}
