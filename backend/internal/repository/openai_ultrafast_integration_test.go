//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Real gateway HTTP/WS, PG and Redis; only the upstream provider is controlled.
// The fixture and exported production APIs also exist on unchanged origin/main.
func TestAstraUltrafastHTTP_ActualWalletAndCard(t *testing.T) {
	for _, transport := range []string{"http", "bridge", "passthrough"} {
		for _, card := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/card_%t", transport, card), func(t *testing.T) {
				mode := transport
				if transport == "http" {
					mode = "bridge"
				}
				f := newWSInflightFixture(t, mode, service.BillingModelSourceUpstream, map[string]float64{"token:gpt-6-astra": .01})
				if card {
					admissionCard(t, inflightTestEntClient(t), f.userID, 0, .75, .9, 1.2, 0, 0, 0)
					_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=0 WHERE id=$1`, f.userID)
					require.NoError(t, err)
					require.NoError(t, f.billing.InvalidateUserBalance(context.Background(), f.userID))
				}
				var conn *coderws.Conn
				if transport != "http" {
					conn = f.dial(t)
				}
				total := 0.0
				for i, tc := range []struct {
					tier string
					cost float64
				}{{"ultrafast", .06}, {"priority", .01}, {"flex", .005}} {
					body := fmt.Sprintf(`{"model":"gpt-6-astra","service_tier":%q,"input":"hello","max_output_tokens":8}`, tc.tier)
					if transport == "http" {
						done := inflightRealResponsesRequest(f, body)
						turn := f.provider.next(t)
						require.Equal(t, tc.tier, gjson.GetBytes(turn.payload, "service_tier").String())
						close(turn.release)
						rec := inflightRealResponsesDone(t, done)
						require.Equal(t, 200, rec.Code, rec.Body.String())
					} else {
						wsInflightWrite(t, conn, `{"type":"response.create",`+body[1:])
						turn := f.provider.next(t)
						require.Equal(t, tc.tier, gjson.GetBytes(turn.payload, "service_tier").String())
						close(turn.release)
						wsInflightReadCompleted(t, conn)
					}
					f.waitUsage(t, i+1)
					var tier sql.NullString
					var cost float64
					var input, output, dedup int
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT service_tier,actual_cost,input_tokens,output_tokens FROM usage_logs WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, f.userID).Scan(&tier, &cost, &input, &output))
					require.Equal(t, 2, input)
					require.Equal(t, 1, output)
					require.InDelta(t, tc.cost, cost, 1e-9)
					require.True(t, tier.Valid)
					require.Equal(t, tc.tier, tier.String)
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
					require.Equal(t, i+1, dedup)
					total += tc.cost
					if card {
						var d, w, m float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.userID).Scan(&d, &w, &m))
						for _, v := range []float64{d, w, m} {
							require.InDelta(t, total, v, 1e-9)
						}
						require.Zero(t, f.wallet(t))
					} else {
						require.InDelta(t, .75-total, f.wallet(t), 1e-9)
					}
					require.Zero(t, f.held(t))
				}
			})
		}
	}
}
