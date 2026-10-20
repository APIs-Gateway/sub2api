//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestChatFallbackTerminalHTTP_FundingAndWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"responses", "messages_indirect", "messages_direct"} {
		for _, card := range []bool{false, true} {
			for _, scenario := range []string{"unknown_text", "unknown_valid_tool", "unknown_partial_tool", "unknown_reasoning", "unknown_malformed_terminal", "metered_read_error", "usage_terminal"} {
				t.Run(fmt.Sprintf("%s/card_%t/%s", mode, card, scenario), func(t *testing.T) {
					delta := `{"content":"partial text"}`
					switch scenario {
					case "unknown_valid_tool":
						delta = `{"tool_calls":[{"index":0,"id":"call_eof","type":"function","function":{"name":"exec","arguments":"{}"}}]}`
					case "unknown_partial_tool":
						delta = `{"tool_calls":[{"index":0,"id":"call_eof","type":"function","function":{"name":"exec","arguments":"{\"cmd\":"}}]}`
					case "unknown_reasoning":
						delta = `{"reasoning_content":"unfinished thinking","content":"partial text"}`
					}
					wire := fmt.Sprintf("data: {\"id\":\"chat_eof\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":%s}]}\n\n", delta)
					if scenario == "unknown_malformed_terminal" {
						wire += "data: {\"choices\":\"invalid\",\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n"
					}
					known := scenario == "metered_read_error" || scenario == "usage_terminal"
					if known {
						wire += "data: {\"id\":\"chat_eof\",\"model\":\"gpt-5\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n"
					}
					f := newChatLengthTerminalHTTPFixture(t, wire, "text/event-stream")
					if mode == "messages_direct" {
						f.account.Extra["openai_responses_mode"] = "force_chat_completions"
						require.NoError(t, f.accounts.Update(context.Background(), f.account))
					}
					if scenario == "metered_read_error" {
						f.upstream.readErr = errors.New("provider reader reset after measured usage")
					}
					if card {
						admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 10, 20, 30, 0, 0, 0)
					}
					priorID := uuid.NewString()
					billingRepo := NewUsageBillingRepository(inflightTestEntClient(t), inflightTestDB(t)).(service.BillingInflightRepository)
					ok, err := billingRepo.ReserveBillingInflight(context.Background(), f.user.ID, priorID, .25, false, 15*time.Minute)
					require.NoError(t, err)
					require.True(t, ok)
					f.upstream.observe = func(req *http.Request) {
						require.Equal(t, "/v1/chat/completions", req.URL.Path)
						body, readErr := io.ReadAll(req.Body)
						require.NoError(t, readErr)
						req.Body = io.NopCloser(strings.NewReader(string(body)))
						require.Equal(t, "gpt-5", gjson.GetBytes(body, "model").String())
						require.True(t, gjson.GetBytes(body, "stream_options.include_usage").Bool())
					}
					close(f.upstream.release)
					path := "/v1/messages"
					body := `{"model":"gpt-5","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"stream":true}`
					serve := f.openAI.Messages
					if mode == "responses" {
						path = "/v1/responses"
						body = `{"model":"gpt-5","max_output_tokens":64,"input":"hello","stream":true}`
						serve = f.openAI.Responses
					}
					rec := f.request(body, path, "", serve)
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					f.pool.Stop()
					require.EqualValues(t, 1, f.upstream.calls.Load(), "partial delivery cannot trigger a provider replay")
					out := rec.Body.String()
					if scenario == "usage_terminal" {
						if mode == "responses" {
							require.Equal(t, 1, strings.Count(out, "event: response.completed\n"))
							require.Equal(t, 1, strings.Count(out, "data: [DONE]"))
						} else {
							require.Equal(t, 1, strings.Count(out, "event: message_stop\n"))
						}
					} else {
						for _, forbidden := range []string{"event: response.completed\n", "event: response.incomplete\n", "event: message_stop\n", "data: [DONE]"} {
							require.NotContains(t, out, forbidden)
						}
						if mode == "responses" {
							require.Equal(t, 1, strings.Count(out, "event: response.failed\n"), "handler communicates this ordinary failure exactly once")
						} else if !known {
							require.Equal(t, 1, strings.Count(out, "event: error\n"))
							require.Contains(t, out, `"type":"api_error"`)
						}
					}
					if strings.Contains(scenario, "tool") {
						require.Contains(t, out, "call_eof")
						require.NotContains(t, out, "event: response.output_item.done\n")
					} else {
						require.Contains(t, out, "partial text")
					}
					if scenario == "unknown_reasoning" {
						require.Contains(t, out, "unfinished thinking")
					}
					var logs, dedup, input, output int
					var cost, wallet float64
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &input, &output, &cost))
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&wallet))
					wantCost := 0.0
					if known {
						wantCost = .014
						require.Equal(t, 1, logs)
						require.Equal(t, 1, dedup)
						require.Equal(t, 10, input)
						require.Equal(t, 2, output)
						require.InDelta(t, .014, cost, 1e-9)
						var model string
						var accountID, groupID int64
						var rate float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT model,account_id,group_id,rate_multiplier FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&model, &accountID, &groupID, &rate))
						require.Equal(t, "gpt-5", model)
						require.Equal(t, f.account.ID, accountID)
						require.Equal(t, *f.key.GroupID, groupID)
						require.Equal(t, 1.0, rate)
						require.InDelta(t, .25, inflightHeld(t, f.user.ID), 1e-9, "known cost settles only this attempt")
					} else {
						require.Zero(t, logs, "unknown usage cannot be settled as known zero")
						require.Zero(t, dedup)
						require.Zero(t, cost)
						require.Greater(t, inflightHeld(t, f.user.ID), .25, "unknown dispatched attempt remains funded")
						var count int
						var seconds, unknownAmount float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),COALESCE(sum(amount),0),COALESCE(max(EXTRACT(EPOCH FROM expires_at-clock_timestamp())),0) FROM billing_inflight_leases WHERE user_id=$1 AND owner_id<>$2 AND phase='attempt' AND expires_at>clock_timestamp()`, f.user.ID, priorID).Scan(&count, &unknownAmount, &seconds))
						require.Equal(t, 1, count)
						require.Positive(t, unknownAmount)
						require.Greater(t, seconds, 0.0)
						require.LessOrEqual(t, seconds, 900.0)
					}
					var prior float64
					require.NoError(t, inflightTestDB(t).QueryRow(`SELECT amount FROM billing_inflight_leases WHERE id=$1 AND phase='attempt'`, priorID+":initial").Scan(&prior))
					require.InDelta(t, .25, prior, 1e-9, "unrelated immutable hold remains untouched")
					if card {
						var daily, weekly, monthly float64
						require.NoError(t, inflightTestDB(t).QueryRow(`SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE user_id=$1 AND status='active'`, f.user.ID).Scan(&daily, &weekly, &monthly))
						require.InDelta(t, wantCost, daily, 1e-9)
						require.InDelta(t, wantCost, weekly, 1e-9)
						require.InDelta(t, wantCost, monthly, 1e-9)
						require.InDelta(t, 10, wallet, 1e-9)
					} else {
						require.InDelta(t, 10-wantCost, wallet, 1e-9)
					}
					rdb := f.accounts.(*accountRepository).schedulerCache.(*schedulerCache).rdb
					slots := NewConcurrencyCache(rdb, 15, 30)
					accountSlots, err := slots.GetAccountConcurrency(context.Background(), f.account.ID)
					require.NoError(t, err)
					require.Zero(t, accountSlots)
					userSlots, err := slots.GetUserConcurrency(context.Background(), f.user.ID)
					require.NoError(t, err)
					require.Zero(t, userSlots)
					keyCache, supportsKeys := slots.(service.APIKeyConcurrencyCache)
					require.True(t, supportsKeys, "actual Redis cache must support API key concurrency")
					keySlots, err := keyCache.GetAPIKeyConcurrencyBatch(context.Background(), []int64{f.key.ID})
					require.NoError(t, err)
					require.Zero(t, keySlots[f.key.ID])
					t.Logf("mode=%s card=%t scenario=%s provider_calls=1 usage_logs=%d dedup=%d cost=%.3f held=%.6f prior=%.2f account=%d group=%d slots=%d/%d/%d", mode, card, scenario, logs, dedup, cost, inflightHeld(t, f.user.ID), prior, f.account.ID, *f.key.GroupID, accountSlots, userSlots, keySlots[f.key.ID])
				})
			}
		}
	}
}
