//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type wsEmptyTTFTTurn struct{ release chan struct{} }
type wsEmptyTTFTProvider struct {
	server *httptest.Server
	turns  chan wsEmptyTTFTTurn
	stop   chan struct{}
	once   sync.Once
	calls  atomic.Int64
}

// Actual controlled upstream, unchanged public gateway and funding APIs. Empty
// frames are delivered before real content; every turn is held before settling.
func newWSEmptyTTFTProvider(t *testing.T, f *wsInflightFixture, content bool) *wsEmptyTTFTProvider {
	t.Helper()
	p := &wsEmptyTTFTProvider{turns: make(chan wsEmptyTTFTTurn, 4), stop: make(chan struct{})}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var conn *coderws.Conn
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			var err error
			conn, err = coderws.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
		} else {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		write := func(data string) bool {
			if conn != nil {
				return conn.Write(r.Context(), coderws.MessageText, []byte(data)) == nil
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return false
			}
			w.(http.Flusher).Flush()
			return true
		}
		for {
			if conn != nil {
				_, payload, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				if gjson.GetBytes(payload, "type").String() != "response.create" {
					continue
				}
			}
			n := p.calls.Add(1)
			if !write(fmt.Sprintf(`{"type":"response.created","response":{"id":"resp_ttft_%d","model":"gpt-5.4"}}`, n)) {
				return
			}
			if !write(`{"type":"response.output_text.delta","delta":"","SSE-Keep-Alive":true}`) {
				return
			}
			turn := wsEmptyTTFTTurn{release: make(chan struct{})}
			select {
			case p.turns <- turn:
			case <-p.stop:
				return
			case <-r.Context().Done():
				return
			}
			select {
			case <-turn.release:
			case <-p.stop:
				return
			case <-r.Context().Done():
				return
			}
			// ID-less content must still timestamp the second passthrough turn.
			if content && !write(`{"type":"response.output_text.delta","delta":" "}`) {
				return
			}
			if !write(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_ttft_%d","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}}`, n)) {
				return
			}
			if conn == nil {
				return
			}
		}
	}))
	t.Cleanup(func() { p.once.Do(func() { close(p.stop) }); p.server.Close() })
	account, err := f.accounts.GetByID(context.Background(), f.accountID)
	require.NoError(t, err)
	account.Credentials["base_url"] = p.server.URL
	require.NoError(t, f.accounts.Update(context.Background(), account))
	return p
}
func (p *wsEmptyTTFTProvider) next(t *testing.T) wsEmptyTTFTTurn {
	t.Helper()
	select {
	case turn := <-p.turns:
		return turn
	case <-time.After(8 * time.Second):
		t.Fatal("actual controlled upstream not reached")
		return wsEmptyTTFTTurn{}
	}
}
func TestWSEmptyTTFTHTTP_TwoTurnsKeepWalletCardBilling(t *testing.T) {
	for _, mode := range []string{"native", "passthrough", "bridge"} {
		for _, card := range []bool{false, true} {
			for _, content := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/card_%t/content_%t", mode, card, content), func(t *testing.T) {
					f := newWSInflightFixture(t, mode, service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": .25})
					if card {
						admissionCard(t, inflightTestEntClient(t), f.userID, 0, .75, .9, 1.2, 0, 0, 0)
						_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=0 WHERE id=$1`, f.userID)
						require.NoError(t, err)
						require.NoError(t, f.billing.InvalidateUserBalance(context.Background(), f.userID))
					}
					p := newWSEmptyTTFTProvider(t, f, content)
					conn := f.dial(t)
					for n := 1; n <= 2; n++ {
						wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"ordinary text","max_output_tokens":8}`)
						turn := p.next(t)
						require.InDelta(t, .25, f.held(t), 1e-9, "each actual immutable attempt reserves once")
						time.Sleep(150 * time.Millisecond)
						close(turn.release)
						sawEmpty, sawContent := false, false
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						for {
							_, body, err := conn.Read(ctx)
							require.NoError(t, err)
							typ := gjson.GetBytes(body, "type").String()
							if typ == "response.output_text.delta" {
								delta := gjson.GetBytes(body, "delta").String()
								if delta == "" {
									sawEmpty = true
								} else {
									require.Equal(t, " ", delta)
									sawContent = true
								}
							}
							require.NotEqual(t, "error", typ, string(body))
							if typ == "response.completed" {
								break
							}
						}
						cancel()
						require.True(t, sawEmpty, "empty transport heartbeat must remain on the wire")
						require.Equal(t, content, sawContent)
						f.waitUsage(t, n)
						var ttft sql.NullInt64
						var input, output, count, dedup int
						var cost float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT first_token_ms,input_tokens,output_tokens,actual_cost FROM usage_logs WHERE user_id=$1 AND request_id=$2`, f.userID, fmt.Sprintf("resp_ttft_%d", n)).Scan(&ttft, &input, &output, &cost))
						require.Equal(t, 2, input)
						require.Equal(t, 1, output)
						require.InDelta(t, .25, cost, 1e-9)
						if content {
							require.True(t, ttft.Valid)
							require.GreaterOrEqual(t, ttft.Int64, int64(100), "real delayed content, including second ID-less turn, owns TTFT")
						} else {
							require.False(t, ttft.Valid, "usage-only settlement must not invent first output")
						}
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT COUNT(*) FROM usage_logs WHERE user_id=$1`, f.userID).Scan(&count))
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
						require.Equal(t, n, count)
						require.Equal(t, n, dedup)
						require.EqualValues(t, n, p.calls.Load())
						require.Zero(t, f.held(t))
						if card {
							var d, w, m float64
							require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.userID).Scan(&d, &w, &m))
							for _, v := range []float64{d, w, m} {
								require.InDelta(t, float64(n)*.25, v, 1e-9)
							}
							require.Zero(t, f.wallet(t))
						} else {
							require.InDelta(t, .75-float64(n)*.25, f.wallet(t), 1e-9)
						}
					}
				})
			}
		}
	}
}
