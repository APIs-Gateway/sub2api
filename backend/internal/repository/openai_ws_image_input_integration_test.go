//go:build integration

package repository

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Same public APIs and provider frames are used on the unchanged production for
// RED. No new image-estimate hook or Usage field is referenced by this fixture.
type wsImageInputTurn struct {
	payload []byte
	reply   chan []byte
}
type wsImageInputProvider struct {
	server *httptest.Server
	turns  chan wsImageInputTurn
	stop   chan struct{}
	once   sync.Once
	calls  atomic.Int64
}

func newWSImageInputProvider(t *testing.T, f *wsInflightFixture) *wsImageInputProvider {
	t.Helper()
	p := &wsImageInputProvider{turns: make(chan wsImageInputTurn, 8), stop: make(chan struct{})}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			_, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if gjson.GetBytes(body, "type").String() == "session.update" {
				continue
			}
			p.calls.Add(1)
			pending := wsImageInputTurn{payload: body, reply: make(chan []byte, 1)}
			select {
			case p.turns <- pending:
			case <-p.stop:
				return
			case <-r.Context().Done():
				return
			}
			select {
			case reply := <-pending.reply:
				if reply == nil {
					return
				}
				if err := conn.Write(r.Context(), coderws.MessageText, reply); err != nil {
					return
				}
			case <-p.stop:
				return
			case <-r.Context().Done():
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
func (p *wsImageInputProvider) next(t *testing.T) wsImageInputTurn {
	t.Helper()
	select {
	case turn := <-p.turns:
		return turn
	case <-time.After(8 * time.Second):
		t.Fatal("actual WS provider not reached")
		return wsImageInputTurn{}
	}
}
func wsImageInputEvent(n int, kind, usage, hosted string) []byte {
	return []byte(fmt.Sprintf(`{"type":%q,"response":{"id":"resp_image_%d","model":"gpt-5.4-mini","status":"completed","output":[],"usage":%s%s}}`, kind, n, usage, hosted))
}
func wsImageInputPricing(t *testing.T, free bool) *service.BillingService {
	t.Helper()
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	cfg.Pricing.DataDir = t.TempDir()
	cfg.Pricing.UpdateIntervalHours = 24
	in, out := .1, .2
	if free {
		in, out = 0, 0
	}
	catalog := fmt.Sprintf(`{"gpt-5.4":{"input_cost_per_token":0,"output_cost_per_token":0,"input_cost_per_image_token":%g,"output_cost_per_image_token":%g,"mode":"chat","litellm_provider":"openai"},"gpt-5.4-mini":{"input_cost_per_token":0,"output_cost_per_token":0,"input_cost_per_image_token":%g,"output_cost_per_image_token":%g,"mode":"chat","litellm_provider":"openai"},"gpt-5.5":{"input_cost_per_token":0,"output_cost_per_token":0,"input_cost_per_image_token":1,"output_cost_per_image_token":2,"mode":"chat","litellm_provider":"openai"}}`, in/2, out/2, in, out)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Pricing.DataDir, "model_pricing.json"), []byte(catalog), 0600))
	pricing := service.NewPricingService(cfg, nil)
	require.NoError(t, pricing.Initialize())
	t.Cleanup(pricing.Stop)
	return service.NewBillingService(cfg, pricing)
}
func wsImageInputConfigure(t *testing.T, f *wsInflightFixture, mapped bool) {
	t.Helper()
	repo := NewChannelRepository(inflightTestDB(t))
	channels, err := repo.ListAll(context.Background())
	require.NoError(t, err)
	require.Len(t, channels, 1)
	channel := channels[0]
	if mapped {
		channel.ModelMapping = map[string]string{"gpt-5.4": "gpt-5.4-mini"}
	}
	require.NoError(t, repo.Update(context.Background(), &channel))
}
func wsImageInputLog(t *testing.T, f *wsInflightFixture, n int, in, out int, cost float64) {
	t.Helper()
	var gotIn, gotOut, count, dedup int
	var actual, imageCost float64
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT COUNT(*),COALESCE(SUM(image_input_tokens),0),COALESCE(SUM(image_output_tokens),0),COALESCE(SUM(actual_cost),0),COALESCE(SUM(image_input_cost),0) FROM usage_logs WHERE user_id=$1`, f.userID).Scan(&count, &gotIn, &gotOut, &actual, &imageCost))
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT COUNT(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
	require.Equal(t, n, count)
	require.Equal(t, n, dedup)
	require.Equal(t, in, gotIn)
	require.Equal(t, out, gotOut)
	require.InDelta(t, cost, actual, 1e-9)
	if in > 0 {
		require.Positive(t, imageCost)
	}
}

const wsImageInputPayload = `[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/image"}]}]`

func TestWSImageInputHTTP_FrozenModelFundingAndTextReset(t *testing.T) {
	for _, source := range []string{service.BillingModelSourceUpstream, service.BillingModelSourceRequested, service.BillingModelSourceChannelMapped} {
		for _, card := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/card_%t", source, card), func(t *testing.T) {
				f := newWSInflightFixture(t, "passthrough", source, map[string]float64{"token:gpt-5.4": 0, "token:gpt-5.4-mini": 0, "token:gpt-5.5": 0, "competitor": .5}, wsImageInputPricing(t, false))
				wsImageInputConfigure(t, f, true)
				if card {
					admissionCard(t, inflightTestEntClient(t), f.userID, 0, .75, .9, 1.2, 0, 0, 0)
					_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=0 WHERE id=$1`, f.userID)
					require.NoError(t, err)
					require.NoError(t, f.billing.InvalidateUserBalance(context.Background(), f.userID))
				}
				p := newWSImageInputProvider(t, f)
				conn := f.dial(t)
				rate := .1
				if source == service.BillingModelSourceRequested {
					rate = .05
				}
				previousAttempt := ""
				for n := 1; n <= 3; n++ {
					input, extra, model := wsImageInputPayload, "", "gpt-5.4"
					if n == 2 {
						input = `"describe previous image"`
						extra = `,"previous_response_id":"resp_image_1"`
						model = "gpt-5.5"
					}
					if n == 3 {
						input = `"independent text"`
						extra = `,"previous_response_id":null`
						model = "gpt-5.5"
					}
					wire := fmt.Sprintf(`{"type":"response.create","model":%q,"max_output_tokens":8,"input":%s%s}`, model, input, extra)
					wsInflightWrite(t, conn, wire)
					pending := p.next(t)
					expectedWireModel := model
					if n == 1 {
						expectedWireModel = "gpt-5.4-mini"
					}
					require.Equal(t, expectedWireModel, gjson.GetBytes(pending.payload, "model").String(), "later model remains on provider wire")
					if n < 3 {
						require.Positive(t, f.held(t), "ordinary zero-price tokens still reserve paid image input")
						f.denyConcurrentHTTP(t, "competitor")
						require.EqualValues(t, n, p.calls.Load(), "wallet and all three card windows reject competing provider dispatch")
					} else {
						require.Zero(t, f.held(t), "new text chain must remain known-free")
					}
					attemptID := ""
					if n < 3 {
						var ownerID string
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT a.id,o.id FROM billing_inflight_leases a JOIN billing_inflight_leases o ON a.owner_id=o.id WHERE a.user_id=$1 AND a.phase='attempt' AND o.phase='owner'`, f.userID).Scan(&attemptID, &ownerID))
						require.NotEmpty(t, ownerID)
						require.NotEqual(t, previousAttempt, attemptID)
						previousAttempt = attemptID
					}
					usage := `{"input_tokens":2,"output_tokens":1}`
					hosted := ""
					if n == 1 {
						usage = `{"input_tokens":2,"output_tokens":1,"input_tokens_details":{"image_tokens":2}}`
					}
					if n == 2 {
						hosted = `,"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":2}}}`
					}
					pending.reply <- wsImageInputEvent(n, "response.completed", usage, hosted)
					wsInflightReadCompleted(t, conn)
					f.waitUsage(t, n)
					select {
					case command := <-f.billingRepo.commands:
						require.Equal(t, attemptID, command.InflightObligationID, "settlement consumes this exact per-turn obligation")
					case <-time.After(5 * time.Second):
						t.Fatal("actual billing Apply missing")
					}
					billed := n
					if billed > 2 {
						billed = 2
					}
					cost := float64(billed) * 2 * rate
					wsImageInputLog(t, f, n, billed*2, 0, cost)
					if card {
						var d, w, m float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.userID).Scan(&d, &w, &m))
						for _, v := range []float64{d, w, m} {
							require.InDelta(t, cost, v, 1e-9)
						}
						require.Zero(t, f.wallet(t))
					} else {
						require.InDelta(t, .75-cost, f.wallet(t), 1e-9)
					}
				}
				require.EqualValues(t, 3, p.calls.Load())
				require.EqualValues(t, 3, f.billingRepo.calls.Load())
				require.Zero(t, f.provider.calls.Load())
			})
		}
	}
}
func TestWSImageInputHTTP_CombinedBucketsInheritedToolsAndUnknownParent(t *testing.T) {
	for _, mode := range []string{"both", "inherited_tools", "unknown_parent", "conversation"} {
		t.Run(mode, func(t *testing.T) {
			f := newWSInflightFixture(t, "passthrough", service.BillingModelSourceUpstream, map[string]float64{"token:gpt-5.4-mini": 0, "competitor": .5}, wsImageInputPricing(t, false))
			p := newWSImageInputProvider(t, f)
			conn := f.dial(t)
			// Establish a known-free first response so session.update and later turn
			// metadata are exercised through the real per-frame filter.
			wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4-mini","input":"text"}`)
			first := p.next(t)
			require.Zero(t, f.held(t))
			first.reply <- wsImageInputEvent(1, "response.completed", `{"input_tokens":2,"output_tokens":1}`, "")
			wsInflightReadCompleted(t, conn)
			f.waitUsage(t, 1)
			input, extra := wsImageInputPayload, `,"tools":[{"type":"image_generation"}]`
			if mode == "inherited_tools" {
				wsInflightWrite(t, conn, `{"type":"session.update","session":{"tools":[{"type":"image_generation"}]}}`)
				extra = ""
			}
			if mode == "unknown_parent" {
				input = `"stored image"`
				extra = `,"previous_response_id":"resp_external"`
			}
			if mode == "conversation" {
				input = `"stored image"`
				extra = `,"conversation":"conv_external"`
			}
			wire := fmt.Sprintf(`{"type":"response.create","model":"gpt-5.4-mini","input":%s,"max_output_tokens":8%s}`, input, extra)
			wsInflightWrite(t, conn, wire)
			pending := p.next(t)
			budget := float64((len(wire)+3)/4) * .1
			if mode == "both" || mode == "inherited_tools" {
				budget += 8 * .2
			}
			require.GreaterOrEqual(t, f.held(t)+1e-9, budget, "hold contains input and output simultaneously, not max(single bucket)")
			if mode == "inherited_tools" {
				require.False(t, gjson.GetBytes(pending.payload, "tools").Exists(), "estimate metadata never rewrites provider frame")
			}
			f.denyConcurrentHTTP(t, "competitor")
			require.EqualValues(t, 2, p.calls.Load())
			hosted := `,"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":2}}}`
			out := 0
			cost := .2
			if mode == "both" || mode == "inherited_tools" {
				hosted = `,"tool_usage":{"image_gen":{"input_tokens_details":{"image_tokens":2},"output_tokens_details":{"image_tokens":1}}}`
				out = 1
				cost = .4
			}
			pending.reply <- wsImageInputEvent(2, "response.completed", `{"input_tokens":2,"output_tokens":1}`, hosted)
			wsInflightReadCompleted(t, conn)
			f.waitUsage(t, 2)
			wsImageInputLog(t, f, 2, 2, out, cost)
			require.InDelta(t, .75-cost, f.wallet(t), 1e-9)
		})
	}
}
func TestWSImageInputHTTP_TrueFreeAndPerRequestZeroStayFree(t *testing.T) {
	for _, mode := range []string{"true_free", "per_request_zero"} {
		t.Run(mode, func(t *testing.T) {
			prices := map[string]float64{"token:gpt-5.4": 0}
			if mode == "per_request_zero" {
				prices = map[string]float64{"gpt-5.4": 0}
			}
			f := newWSInflightFixture(t, "passthrough", service.BillingModelSourceUpstream, prices, wsImageInputPricing(t, mode == "true_free"))
			p := newWSImageInputProvider(t, f)
			conn := f.dial(t)
			wsInflightWrite(t, conn, fmt.Sprintf(`{"type":"response.create","model":"gpt-5.4","input":%s,"tools":[{"type":"image_generation"}]}`, wsImageInputPayload))
			pending := p.next(t)
			require.Zero(t, f.held(t))
			pending.reply <- wsImageInputEvent(1, "response.completed", `{"input_tokens":2,"output_tokens":1,"input_tokens_details":{"image_tokens":2},"output_tokens_details":{"image_tokens":1}}`, "")
			wsInflightReadCompleted(t, conn)
			f.waitUsage(t, 1)
			var in, out int
			var cost float64
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT image_input_tokens,image_output_tokens,actual_cost FROM usage_logs WHERE user_id=$1`, f.userID).Scan(&in, &out, &cost))
			require.Equal(t, 2, in)
			require.Equal(t, 1, out)
			require.Zero(t, cost)
			require.InDelta(t, .75, f.wallet(t), 1e-9)
		})
	}
}
func TestWSImageInputHTTP_PartialAndUnknownDisconnect(t *testing.T) {
	for _, kind := range []string{"response.failed", "response.incomplete", "disconnect"} {
		t.Run(kind, func(t *testing.T) {
			f := newWSInflightFixture(t, "passthrough", service.BillingModelSourceUpstream, map[string]float64{"token:gpt-5.4-mini": 0, "competitor": .5}, wsImageInputPricing(t, false))
			p := newWSImageInputProvider(t, f)
			conn := f.dial(t)
			wsInflightWrite(t, conn, fmt.Sprintf(`{"type":"response.create","model":"gpt-5.4-mini","input":%s}`, wsImageInputPayload))
			pending := p.next(t)
			held := f.held(t)
			require.Positive(t, held)
			if kind == "disconnect" {
				pending.reply <- nil
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _, err := conn.Read(ctx)
				require.Error(t, err)
				require.NotEqual(t, context.DeadlineExceeded, ctx.Err())
				require.InDelta(t, held, f.held(t), 1e-9, "unknown upstream disconnect retains bounded attempt lease")
				require.InDelta(t, .75, f.wallet(t), 1e-9)
				f.denyConcurrentHTTP(t, "competitor")
				var count int
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.userID).Scan(&count))
				require.Zero(t, count)
				var ttl float64
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT EXTRACT(EPOCH FROM MAX(expires_at)-clock_timestamp()) FROM billing_inflight_leases WHERE user_id=$1 AND phase='attempt'`, f.userID).Scan(&ttl))
				require.Greater(t, ttl, 850.0)
				require.LessOrEqual(t, ttl, 900.0)
			} else {
				pending.reply <- wsImageInputEvent(1, kind, `{"input_tokens":2,"output_tokens":1,"input_tokens_details":{"image_tokens":2}}`, "")
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, body, err := conn.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, kind, gjson.GetBytes(body, "type").String())
				f.waitUsage(t, 1)
				wsImageInputLog(t, f, 1, 2, 0, .2)
				require.InDelta(t, .55, f.wallet(t), 1e-9)
			}
			require.EqualValues(t, 1, p.calls.Load(), "partial and disconnected turns do not replay")
		})
	}
}
func TestWSImageInputHTTP_QueuedWorkerOwnsOnlyItsObligation(t *testing.T) {
	f := newWSInflightFixture(t, "passthrough", service.BillingModelSourceUpstream, map[string]float64{"token:gpt-5.4-mini": 0, "competitor": 99.9}, wsImageInputPricing(t, false))
	p := newWSImageInputProvider(t, f)
	_, err := inflightTestDB(t).Exec(`UPDATE users SET balance=100 WHERE id=$1`, f.userID)
	require.NoError(t, err)
	require.NoError(t, f.billing.InvalidateUserBalance(context.Background(), f.userID))
	gate := make(chan struct{})
	f.billingRepo.firstApplyGate = gate
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	conn := f.dial(t)
	wire := fmt.Sprintf(`{"type":"response.create","model":"gpt-5.4-mini","input":%s}`, wsImageInputPayload)
	wsInflightWrite(t, conn, wire)
	first := p.next(t)
	require.Positive(t, f.held(t))
	first.reply <- wsImageInputEvent(1, "response.completed", `{"input_tokens":2,"output_tokens":1,"input_tokens_details":{"image_tokens":2}}`, "")
	wsInflightReadCompleted(t, conn)
	var old service.UsageBillingCommand
	select {
	case old = <-f.billingRepo.commands:
	case <-time.After(5 * time.Second):
		t.Fatal("real worker did not reach Apply")
	}
	require.NotEmpty(t, old.InflightObligationID)
	require.InDelta(t, .2, f.held(t), 1e-9)
	wsInflightWrite(t, conn, wire)
	second := p.next(t)
	secondHold := f.held(t) - .2
	require.Positive(t, secondHold)
	f.denyConcurrentHTTP(t, "competitor")
	require.EqualValues(t, 2, p.calls.Load())
	release.Do(func() { close(gate) })
	require.Eventually(t, func() bool { return f.wallet(t) == 99.8 && f.held(t) == secondHold }, 5*time.Second, 20*time.Millisecond, "old financial task cannot consume new attempt estimate")
	var oldPending int
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM billing_inflight_leases WHERE id=$1`, old.InflightObligationID).Scan(&oldPending))
	require.Zero(t, oldPending)
	second.reply <- wsImageInputEvent(2, "response.completed", `{"input_tokens":2,"output_tokens":1,"input_tokens_details":{"image_tokens":2}}`, "")
	wsInflightReadCompleted(t, conn)
	f.waitUsage(t, 2)
	latest := <-f.billingRepo.commands
	require.NotEqual(t, old.InflightObligationID, latest.InflightObligationID)
	wsImageInputLog(t, f, 2, 4, 0, .4)
	require.InDelta(t, 99.6, f.wallet(t), 1e-9)
}
func TestWSImageInputHTTP_ProviderReplayBillsOnce(t *testing.T) {
	f := newWSInflightFixture(t, "passthrough", service.BillingModelSourceUpstream, map[string]float64{"token:gpt-5.4-mini": 0}, wsImageInputPricing(t, false))
	p := newWSImageInputProvider(t, f)
	conn := f.dial(t)
	wire := fmt.Sprintf(`{"type":"response.create","model":"gpt-5.4-mini","input":%s}`, wsImageInputPayload)
	var previous string
	for n := 1; n <= 2; n++ {
		wsInflightWrite(t, conn, wire)
		pending := p.next(t)
		require.Positive(t, f.held(t))
		pending.reply <- wsImageInputEvent(1, "response.completed", `{"input_tokens":2,"output_tokens":1,"input_tokens_details":{"image_tokens":2}}`, "")
		wsInflightReadCompleted(t, conn)
		require.Eventually(t, func() bool { return f.billingRepo.calls.Load() == int64(n) && f.held(t) == 0 }, 5*time.Second, 20*time.Millisecond)
		cmd := <-f.billingRepo.commands
		require.NotEmpty(t, cmd.InflightObligationID)
		require.NotEqual(t, previous, cmd.InflightObligationID)
		previous = cmd.InflightObligationID
		wsImageInputLog(t, f, 1, 2, 0, .2)
		require.InDelta(t, .55, f.wallet(t), 1e-9)
	}
	require.EqualValues(t, 2, p.calls.Load())
}

func TestWSImageInputHTTP_ExistingNonImageModesKeepTheirTariff(t *testing.T) {
	for _, mode := range []string{"native", "passthrough", "bridge"} {
		t.Run(mode, func(t *testing.T) {
			f := newWSInflightFixture(t, mode, service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": .5})
			conn := f.dial(t)
			wsInflightWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"ordinary paid text"}`)
			pending := f.provider.next(t)
			require.InDelta(t, .5, f.held(t), 1e-9)
			f.denyConcurrentHTTP(t, "gpt-5.4")
			close(pending.release)
			wsInflightReadCompleted(t, conn)
			f.waitUsage(t, 1)
			wsImageInputLog(t, f, 1, 0, 0, .5)
			require.InDelta(t, .25, f.wallet(t), 1e-9)
			require.EqualValues(t, 1, f.provider.calls.Load())
		})
	}
}
