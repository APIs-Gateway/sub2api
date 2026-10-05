//go:build integration

package repository

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Real HTTP Responses provider, real WS gateway/Redis/PG/funding and worker.
// This exact public-API fixture is also runnable on the unchanged production.
func TestOpenAIWSBridgeHistoryHTTP_ThreeFundedTurns(t *testing.T) {
	for _, card := range []bool{false, true} {
		t.Run(fmt.Sprintf("card_%t", card), func(t *testing.T) {
			f := newWSInflightFixture(t, "bridge", service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": .5})
			if card {
				admissionCard(t, inflightTestEntClient(t), f.userID, 0, 2, 3, 4, 0, 0, 0)
				_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=0 WHERE id=$1`, f.userID)
				require.NoError(t, err)
				require.NoError(t, f.billing.InvalidateUserBalance(context.Background(), f.userID))
			}
			turns := make(chan wsInflightProviderTurn, 4)
			var calls atomic.Int64
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				n := calls.Add(1)
				turn := wsInflightProviderTurn{payload: body, release: make(chan struct{})}
				select {
				case turns <- turn:
				case <-r.Context().Done():
					return
				}
				select {
				case <-turn.release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				reasoning := fmt.Sprintf(`{"type":"reasoning","id":"rs_funded_%d","encrypted_content":"opaque-%d","opaque":9007199254740993}`, n, n)
				message := fmt.Sprintf(`{"type":"message","id":"msg_funded_%d","role":"assistant","content":[{"type":"output_text","text":"plan %d"}]}`, n, n)
				call := fmt.Sprintf(`{"type":"function_call","id":"fc_funded_%d","call_id":"call_funded_%d","name":"exec","arguments":"{}"}`, n, n)
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"item\":%s}\n\ndata: {\"type\":\"response.output_item.done\",\"item\":%s}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_funded_history_%d\",\"model\":\"gpt-5.4\",\"output\":[%s,%s,%s],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n", reasoning, message, n, reasoning, message, call)
			}))
			t.Cleanup(provider.Close)
			account, err := f.accounts.GetByID(context.Background(), f.accountID)
			require.NoError(t, err)
			account.Credentials["base_url"] = provider.URL
			require.NoError(t, f.accounts.Update(context.Background(), account))
			conn := f.dial(t)
			for turn := 1; turn <= 3; turn++ {
				if turn > 1 && !card {
					_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=balance+0.5 WHERE id=$1`, f.userID)
					require.NoError(t, err)
					require.NoError(t, f.billing.InvalidateUserBalance(context.Background(), f.userID))
				}
				input, previous := `"first input"`, ""
				if turn > 1 {
					input = fmt.Sprintf(`[{"type":"function_call_output","call_id":"call_funded_%d","output":"done"}]`, turn-1)
					previous = fmt.Sprintf(`,"previous_response_id":"resp_funded_history_%d"`, turn-1)
				}
				wsInflightWrite(t, conn, fmt.Sprintf(`{"type":"response.create","model":"gpt-5.4","tools":[{"type":"function","name":"exec","parameters":{"type":"object"}}],"max_output_tokens":8,"input":%s%s}`, input, previous))
				var pending wsInflightProviderTurn
				select {
				case pending = <-turns:
				case <-time.After(10 * time.Second):
					t.Fatal("real bridge HTTP dispatch did not occur")
				}
				var released sync.Once
				defer released.Do(func() { close(pending.release) })
				require.Equal(t, "gpt-5.4", gjson.GetBytes(pending.payload, "model").String())
				require.False(t, gjson.GetBytes(pending.payload, "previous_response_id").Exists())
				require.InDelta(t, .5, f.held(t), 1e-9)
				if !card {
					f.denyConcurrentHTTP(t, "gpt-5.4")
					require.EqualValues(t, turn, calls.Load(), "competing request must not reach new provider")
				}
				if turn > 1 {
					items := gjson.GetBytes(pending.payload, "input").Array()
					require.Len(t, items, 1+(turn-1)*4)
					require.Equal(t, "first input", items[0].String())
					for n := 1; n < turn; n++ {
						start := 1 + (n-1)*4
						require.Equal(t, fmt.Sprintf("rs_funded_%d", n), items[start].Get("id").String())
						require.Equal(t, fmt.Sprintf("opaque-%d", n), items[start].Get("encrypted_content").String())
						require.Equal(t, "9007199254740993", items[start].Get("opaque").Raw)
						require.Equal(t, fmt.Sprintf("msg_funded_%d", n), items[start+1].Get("id").String())
						require.Equal(t, fmt.Sprintf("call_funded_%d", n), items[start+2].Get("call_id").String())
						require.Equal(t, "function_call_output", items[start+3].Get("type").String())
						require.Equal(t, fmt.Sprintf("call_funded_%d", n), items[start+3].Get("call_id").String())
						require.Equal(t, 1, strings.Count(string(pending.payload), fmt.Sprintf(`"id":"msg_funded_%d"`, n)))
					}
				}
				released.Do(func() { close(pending.release) })
				wsInflightReadCompleted(t, conn)
				f.waitUsage(t, turn)
				var logs, dedup, inputTokens, outputTokens int
				var actualCost float64
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),sum(input_tokens),sum(output_tokens),sum(actual_cost) FROM usage_logs WHERE user_id=$1`, f.userID).Scan(&logs, &inputTokens, &outputTokens, &actualCost))
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
				require.Equal(t, turn, logs)
				require.Equal(t, turn, dedup)
				require.Equal(t, turn*2, inputTokens)
				require.Equal(t, turn, outputTokens)
				require.InDelta(t, float64(turn)*.5, actualCost, 1e-9)
				if card {
					var daily, weekly, monthly float64
					var err = inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.userID).Scan(&daily, &weekly, &monthly)
					require.NoError(t, err)
					for _, amount := range []float64{daily, weekly, monthly} {
						require.InDelta(t, actualCost, amount, 1e-9)
					}
					require.Zero(t, f.wallet(t))
				} else {
					require.InDelta(t, .25, f.wallet(t), 1e-9)
				}
			}
			require.EqualValues(t, 3, calls.Load())
			require.Zero(t, f.provider.calls.Load(), "all three turns used the intended provider")
		})
	}
}
