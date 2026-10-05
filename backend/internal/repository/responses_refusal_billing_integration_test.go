//go:build integration

package repository

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The public HTTP handler, real channel tariff, PG wallet/card/lease/dedup,
// Redis admission cache and real asynchronous Apply are exercised together.
func TestResponsesRefusalHTTP_ActualFundedDelivery(t *testing.T) {
	for _, card := range []bool{false, true} {
		for _, tc := range []struct {
			name                      string
			stream, usage, empty, eof bool
			rawRefusal                string
		}{
			{"buffered_usage", false, true, false, false, ""},
			{"buffered_no_usage", false, false, false, false, ""},
			{"stream_usage", true, true, false, false, ""},
			{"stream_no_usage", true, false, false, false, ""},
			{"delivered_then_eof", true, false, false, true, ""},
			{"empty_then_eof", true, false, true, true, ""},
			{"raw_false_eof", true, false, true, true, "false"},
			{"raw_true_eof", true, false, true, true, "true"},
			{"raw_zero_eof", true, false, true, true, "0"},
			{"raw_number_eof", true, false, true, true, "1"},
			{"raw_object_eof", true, false, true, true, "{}"},
			{"raw_array_eof", true, false, true, true, "[]"},
			{"raw_null_eof", true, false, true, true, "null"},
			{"raw_empty_eof", true, false, true, true, `""`},
			{"raw_string_eof", true, false, false, true, `"cannot help"`},
		} {
			t.Run(fmt.Sprintf("%s/card=%t", tc.name, card), func(t *testing.T) {
				ctx := context.Background()
				f := newWSInflightFixture(t, "bridge", service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": .5})
				if card {
					_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=0 WHERE id=$1`, f.userID)
					require.NoError(t, err)
					admissionCard(t, inflightTestEntClient(t), f.userID, 0, 10, 100, 1000, 0, 0, 0)
					f.key.User.Balance = 0
					require.NoError(t, f.billing.InvalidateUserBalance(ctx, f.userID))
				}
				var calls atomic.Int32
				payloads := make(chan []byte, 2)
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					payload, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read provider request: %v", err)
						return
					}
					payloads <- payload
					expectedPath := "/v1/responses"
					if tc.rawRefusal != "" {
						expectedPath = "/v1/chat/completions"
					}
					if r.URL.Path != expectedPath {
						t.Errorf("expected real provider route %s, got %s", expectedPath, r.URL.Path)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if tc.rawRefusal != "" {
						_, _ = fmt.Fprintf(w, "data: {\"model\":\"gpt-5.4\",\"choices\":[{\"delta\":{\"refusal\":%s},\"finish_reason\":null}]}\n\n", tc.rawRefusal)
						return
					}
					_, _ = io.WriteString(w, `data: {"type":"response.created","response":{"id":"refusal_funded","model":"gpt-5.4"}}`+"\n\n")
					delta := "cannot help"
					if tc.empty {
						delta = ""
					}
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.refusal.delta\",\"delta\":%q}\n\n", delta)
					if tc.eof {
						return
					}
					usage := ""
					if tc.usage {
						usage = `,"usage":{"input_tokens":10,"output_tokens":2}`
					}
					_, _ = fmt.Fprintf(w, `data: {"type":"response.completed","response":{"id":"refusal_funded","model":"gpt-5.4","status":"completed","output":[{"type":"message","content":[]}]%s}}`+"\n\n", usage)
				}))
				t.Cleanup(provider.Close)
				account, err := f.accounts.GetByID(ctx, f.accountID)
				require.NoError(t, err)
				account.Credentials["base_url"] = provider.URL
				account.Extra["openai_responses_supported"] = tc.rawRefusal == ""
				require.NoError(t, f.accounts.Update(ctx, account))
				body := fmt.Sprintf(`{"model":"gpt-5.4","stream":%t,"max_completion_tokens":8,"messages":[{"role":"user","content":"hello"}]}`, tc.stream)
				rec := httptest.NewRecorder()
				requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(requestCtx))
				require.EqualValues(t, 1, calls.Load(), rec.Body.String())
				select {
				case payload := <-payloads:
					inputPath := "input"
					if tc.rawRefusal != "" {
						inputPath = "messages"
					}
					require.True(t, gjson.GetBytes(payload, inputPath).IsArray())
					require.Equal(t, "gpt-5.4", gjson.GetBytes(payload, "model").String())
				case <-time.After(time.Second):
					t.Fatal("missing actual provider request")
				}
				expected := .5
				if tc.empty {
					expected = 0
				}
				if !tc.empty {
					f.waitUsage(t, 1)
				}
				f.pool.Stop() // Drain actual asynchronous billing before asserting absence.
				var logs, dedup, input, output int
				var fee float64
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1`, f.userID).Scan(&logs, &input, &output, &fee))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
				if tc.empty {
					require.Zero(t, logs)
					require.Zero(t, dedup)
					require.Zero(t, f.billingRepo.calls.Load())
					require.InDelta(t, .5, f.held(t), 1e-9, "an unmetered EOF is unknown, never a proof to release its funded lease")
					var ttl float64
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT EXTRACT(EPOCH FROM expires_at-clock_timestamp()) FROM billing_inflight_leases WHERE user_id=$1 AND expires_at>clock_timestamp()`, f.userID).Scan(&ttl))
					require.Greater(t, ttl, 800.0)
					require.LessOrEqual(t, ttl, 900.0)
				} else {
					require.Equal(t, 1, logs)
					require.Equal(t, 1, dedup)
					require.EqualValues(t, 1, f.billingRepo.calls.Load())
					require.Zero(t, f.held(t))
				}
				require.InDelta(t, expected, fee, 1e-9)
				if tc.usage {
					require.Equal(t, 10, input)
					require.Equal(t, 2, output)
				} else {
					require.Zero(t, input)
					require.Zero(t, output)
				}
				if card {
					require.Zero(t, f.wallet(t))
					var d, w, m float64
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.userID).Scan(&d, &w, &m))
					require.InDelta(t, expected, d, 1e-9)
					require.InDelta(t, expected, w, 1e-9)
					require.InDelta(t, expected, m, 1e-9)
				} else {
					require.InDelta(t, .75-expected, f.wallet(t), 1e-9)
				}
				if !tc.empty {
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Contains(t, rec.Body.String(), `"refusal":"cannot help"`)
					if tc.stream && !tc.eof {
						require.Contains(t, rec.Body.String(), "data: [DONE]")
					}
				}
				if tc.eof {
					require.NotContains(t, rec.Body.String(), "data: [DONE]")
				}
			})
		}
	}
}
