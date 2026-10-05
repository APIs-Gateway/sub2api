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
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const startOutputMessagesBody = `{"model":"claude-sonnet-4-5","stream":true,"max_tokens":128,"messages":[{"role":"user","content":"hi"}]}`
const startOutputSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_start_bill\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"usage\":{\"output_tokens\":8}}}\n\n"
const startOutputVisibleText = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"
const startOutputStop = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

type startOutputHTTPBody struct {
	reader *strings.Reader
	cancel context.CancelFunc
	block  bool
	closed chan struct{}
	once   sync.Once
}

func (b *startOutputHTTPBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if err != io.EOF {
		return n, err
	}
	if b.cancel != nil {
		b.cancel()
		return 0, context.Canceled
	}
	if b.block {
		<-b.closed
		return 0, io.ErrClosedPipe
	}
	return n, err
}
func (b *startOutputHTTPBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

type startOutputHTTPFailedWriter struct{ *httptest.ResponseRecorder }

func (w *startOutputHTTPFailedWriter) Write([]byte) (int, error)       { return 0, io.ErrClosedPipe }
func (w *startOutputHTTPFailedWriter) WriteString(string) (int, error) { return 0, io.ErrClosedPipe }

func startOutputHTTPMessagesRequest(f *inflightHTTPFixture, mode string, cancel context.CancelFunc, ctx context.Context) *httptest.ResponseRecorder {
	f.requests.Add(1)
	defer f.requests.Done()
	defer cancel()
	rec := httptest.NewRecorder()
	var writer http.ResponseWriter = rec
	if mode == "write_failure" {
		writer = &startOutputHTTPFailedWriter{rec}
	}
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(startOutputMessagesBody)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request = c.Request.WithContext(context.WithValue(ctx, ctxkey.Group, f.key.Group))
	c.Set(string(middleware.ContextKeyAPIKey), f.key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: f.user.ID, Concurrency: 100})
	f.gateway.Messages(c)
	return rec
}

func TestAnthropicStartOutputHTTP_RealMessagesSettlement(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, funding := range []string{"wallet", "daily", "weekly", "monthly"} {
			for _, mode := range []string{"output_only_eof", "read_error", "idle_timeout", "cancel", "write_failure", "cumulative_complete"} {
				t.Run(fmt.Sprintf("passthrough_%t/%s/%s", passthrough, funding, mode), func(t *testing.T) {
					payload := startOutputSSE
					expectedOutput := 8
					if mode == "cumulative_complete" {
						payload += startOutputVisibleText + "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":20}}\n\n" + startOutputStop
						expectedOutput = 20
					}
					if mode == "write_failure" {
						payload += startOutputVisibleText + startOutputStop
					}
					f := newInflightHTTPFixture(t, service.PlatformAnthropic, payload, "text/event-stream")
					f.cfg.Gateway.StreamDataIntervalTimeout = 1
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if mode == "idle_timeout" || mode == "cancel" {
						f.upstream.script = func(req *http.Request, _ int64) (*http.Response, error) {
							b := &startOutputHTTPBody{reader: strings.NewReader(payload), closed: make(chan struct{}), block: mode == "idle_timeout"}
							if mode == "cancel" {
								b.cancel = cancel
							}
							return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"msg_start_bill"}}, Body: b, Request: req}, nil
						}
					}
					if mode == "read_error" {
						f.upstream.readErr = errors.New("fixture incomplete upstream reader")
					}
					if passthrough {
						_, err := inflightTestDB(t).Exec(`UPDATE accounts SET extra=extra || '{"anthropic_passthrough":true}'::jsonb WHERE id=$1`, f.accountID)
						require.NoError(t, err)
					}
					client := inflightTestEntClient(t)
					second := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Priority: f.account.Priority + 10, Concurrency: 100, Credentials: map[string]any{"api_key": "second-local", "pool_mode": true, "pool_mode_retry_count": 0}, Extra: map[string]any{"anthropic_passthrough": passthrough}})
					require.NoError(t, f.accounts.BindGroups(context.Background(), second.ID, []int64{*f.key.GroupID}))
					balance := .001
					if funding != "wallet" {
						balance = 0
					}
					_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=$1 WHERE id=$2`, balance, f.user.ID)
					require.NoError(t, err)
					f.key.User.Balance = balance
					if funding != "wallet" {
						daily, weekly, monthly := 10., 100., 1000.
						switch funding {
						case "daily":
							daily = .001
						case "weekly":
							weekly = .001
						case "monthly":
							monthly = .001
						}
						admissionCard(t, client, f.user.ID, 0, daily, weekly, monthly, 0, 0, 0)
					}
					done := make(chan *httptest.ResponseRecorder, 1)
					go func() { done <- startOutputHTTPMessagesRequest(f, mode, cancel, ctx) }()
					select {
					case <-f.upstream.started:
					case rec := <-done:
						t.Fatalf("rejected before provider: %d %s", rec.Code, rec.Body.String())
					case <-time.After(10 * time.Second):
						t.Fatal("provider did not start")
					}
					require.Positive(t, inflightHeld(t, f.user.ID), "existing financial admission remains active")
					competitor := f.request(startOutputMessagesBody, "/v1/messages", "", f.gateway.Messages)
					require.Equal(t, http.StatusForbidden, competitor.Code, competitor.Body.String())
					require.EqualValues(t, 1, f.upstream.calls.Load(), "wallet and limiting card window reject competing owner before provider I/O")
					close(f.upstream.release)
					var rec *httptest.ResponseRecorder
					select {
					case rec = <-done:
					case <-time.After(10 * time.Second):
						t.Fatal("first handler did not finish")
					}
					f.pool.Stop()
					require.EqualValues(t, 1, f.upstream.calls.Load(), "metered output-only attempt never replays to available second account")
					var logs, dedup, output, input int
					var actual float64
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(output_tokens),0),COALESCE(sum(input_tokens),0),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &output, &input, &actual))
					require.Equal(t, 1, logs, "actual partial/success usage is recorded once")
					require.Equal(t, expectedOutput, output)
					require.Zero(t, input)
					cfg := &config.Config{}
					cfg.Default.RateMultiplier = 1
					cost, costErr := service.NewBillingService(cfg, nil).CalculateCost("claude-sonnet-4-5", service.UsageTokens{OutputTokens: expectedOutput}, 1)
					require.NoError(t, costErr)
					require.Positive(t, cost.ActualCost)
					require.InDelta(t, cost.ActualCost, actual, 1e-9, "settles observed start or authoritative cumulative output, never adds both")
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
					require.Equal(t, 1, dedup)
					require.Zero(t, inflightHeld(t, f.user.ID))
					var owners int
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM billing_inflight_leases WHERE user_id=$1`, f.user.ID).Scan(&owners))
					require.Zero(t, owners)
					var wallet float64
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&wallet))
					if funding == "wallet" {
						require.InDelta(t, balance-cost.ActualCost, wallet, 1e-9)
					} else {
						require.Zero(t, wallet)
						var d, w, m float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.user.ID).Scan(&d, &w, &m))
						require.InDelta(t, cost.ActualCost, d, 1e-9)
						require.InDelta(t, cost.ActualCost, w, 1e-9)
						require.InDelta(t, cost.ActualCost, m, 1e-9)
					}
					if passthrough && mode == "cumulative_complete" {
						require.Equal(t, payload, rec.Body.String())
					}
					if !passthrough && mode == "output_only_eof" {
						require.NotContains(t, rec.Body.String(), "msg_start_bill", "staged upstream metadata does not leak through fallback error")
					}
				})
			}
		}
	}
}

func TestAnthropicStartOutputHTTP_UnmeteredRetryControl(t *testing.T) {
	f := newInflightHTTPFixture(t, service.PlatformAnthropic, "", "text/event-stream")
	close(f.upstream.release)
	_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=1 WHERE id=$1`, f.user.ID)
	require.NoError(t, err)
	f.upstream.script = func(req *http.Request, _ int64) (*http.Response, error) {
		payload := strings.ReplaceAll(startOutputSSE, `"output_tokens":8`, `"output_tokens":0`) + startOutputStop
		if f.upstream.calls.Load() > 1 {
			payload = strings.ReplaceAll(startOutputSSE, `"output_tokens":8`, `"output_tokens":0`) + startOutputVisibleText + "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":8}}\n\n" + startOutputStop
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
	}
	client := inflightTestEntClient(t)
	second := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Priority: f.account.Priority + 10, Concurrency: 100, Credentials: map[string]any{"api_key": "local-second", "pool_mode": true, "pool_mode_retry_count": 0}})
	require.NoError(t, f.accounts.BindGroups(context.Background(), second.ID, []int64{*f.key.GroupID}))
	rec := f.request(startOutputMessagesBody, "/v1/messages", "", f.gateway.Messages)
	f.pool.Stop()
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.EqualValues(t, 2, f.upstream.calls.Load())
	var logs, output int
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(output_tokens),0) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &output))
	require.Equal(t, 1, logs)
	require.Equal(t, 8, output)
}
