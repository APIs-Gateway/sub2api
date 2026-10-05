//go:build integration

package repository

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const structuredHTTPRejection = `{"error":{"type":"invalid_request_error","code":"invalid_type","param":"input","message":"Invalid type for 'input': expected a string, but got an array instead."}}`
const structuredHTTPVLLMRejection = `{"error":{"message":"2 validation errors: [{'type':'string_type','loc':('body','input','str'),'msg':'Input should be a valid string','input':[{'role':'assistant','content':'private history'}]}, {'type':'list_type','loc':('body','input','list'),'msg':'invalid list'}]"}}`
const structuredHTTPChatStream = "data: {\"id\":\"structured_chat\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"structured_chat\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\ndata: [DONE]\n\n"

func structuredHTTPBody(stream bool) string {
	return fmt.Sprintf(`{"model":"gpt-5","max_completion_tokens":8,"stream":%t,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"text","text":"prior"},{"type":"thinking","thinking":"reason"}],"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"done"},{"role":"user","content":"now"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"vendor_option":"preserve"}`, stream)
}

func structuredHTTPSequence(t *testing.T, f *inflightHTTPFixture, rejection string, stream bool, secondGate chan struct{}, release chan struct{}) {
	t.Helper()
	f.upstream.observe = func(req *http.Request) {
		payload, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		switch f.upstream.calls.Load() {
		case 0:
			require.Equal(t, "/v1/responses", req.URL.Path)
			require.True(t, gjson.GetBytes(payload, "input").IsArray())
			f.upstream.status = 400
			f.upstream.response = rejection
			f.upstream.contentType = "application/json"
		case 1:
			require.Equal(t, "/v1/chat/completions", req.URL.Path)
			require.JSONEq(t, gjson.Get(structuredHTTPBody(stream), "messages").Raw, gjson.GetBytes(payload, "messages").Raw)
			require.JSONEq(t, gjson.Get(structuredHTTPBody(stream), "tools").Raw, gjson.GetBytes(payload, "tools").Raw)
			require.Equal(t, "preserve", gjson.GetBytes(payload, "vendor_option").String())
			f.upstream.status = 200
			f.upstream.response = inflightChatJSON
			f.upstream.contentType = "application/json"
			if stream {
				f.upstream.response = structuredHTTPChatStream
				f.upstream.contentType = "text/event-stream"
			}
			if secondGate != nil {
				close(secondGate)
				select {
				case <-release:
				case <-req.Context().Done():
					t.Error("second raw call canceled before test released it")
				}
			}
		default:
			t.Errorf("unexpected replay call %d", f.upstream.calls.Load()+1)
		}
	}
	close(f.upstream.release)
}

func assertStructuredHTTPSettlement(t *testing.T, f *inflightHTTPFixture, card bool, originalBalance float64) {
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
		require.InDelta(t, originalBalance, balance, 1e-10)
	} else {
		require.InDelta(t, originalBalance-cost, balance, 1e-10)
	}
	require.InDelta(t, 0, inflightHeld(t, f.user.ID), 1e-10)
	require.EqualValues(t, 2, f.upstream.calls.Load())
}

func TestStructuredInputFallbackHTTP_ActualWalletCardBillOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, rejection := range map[string]string{"canonical": structuredHTTPRejection, "vllm": structuredHTTPVLLMRejection} {
		for _, stream := range []bool{false, true} {
			for _, card := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream_%t/card_%t", name, stream, card), func(t *testing.T) {
					f := newInflightHTTPFixture(t, service.PlatformOpenAI, inflightChatJSON, "application/json")
					chatAudioWallet(t, f)
					if card {
						admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 1, 10, 20, 0, 0, 0)
					}
					structuredHTTPSequence(t, f, rejection, stream, nil, nil)
					rec := f.request(structuredHTTPBody(stream), "/v1/chat/completions", "", f.openAI.ChatCompletions)
					require.Equal(t, 200, rec.Code, rec.Body.String())
					require.Contains(t, rec.Body.String(), "ok")
					if stream {
						require.Contains(t, rec.Body.String(), "data: [DONE]")
						require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
					} else {
						require.Equal(t, "ok", gjson.Get(rec.Body.String(), "choices.0.message.content").String())
					}
					assertStructuredHTTPSettlement(t, f, card, 10)
				})
			}
		}
	}
}

func TestStructuredInputFallbackHTTP_ImmutableHoldBetweenCalls(t *testing.T) {
	for name, rejection := range map[string]string{"canonical": structuredHTTPRejection, "vllm": structuredHTTPVLLMRejection} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream_%t", name, stream), func(t *testing.T) {
				f := newInflightHTTPFixture(t, service.PlatformOpenAI, inflightChatJSON, "application/json")
				gate, release := make(chan struct{}), make(chan struct{})
				var released sync.Once
				defer released.Do(func() { close(release) })
				structuredHTTPSequence(t, f, rejection, stream, gate, release)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					done <- f.request(structuredHTTPBody(stream), "/v1/chat/completions", "", f.openAI.ChatCompletions)
				}()
				select {
				case <-gate:
				case rec := <-done:
					t.Fatalf("did not reach raw second call: %d %s", rec.Code, rec.Body.String())
				case <-time.After(10 * time.Second):
					t.Fatal("raw fallback did not dispatch")
				}
				hold := inflightHeld(t, f.user.ID)
				require.Positive(t, hold)
				var owners, attempts, obligations, logs, dedup int
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FILTER(WHERE phase='owner'), count(*) FILTER(WHERE phase='attempt'), count(*) FILTER(WHERE phase='pending') FROM billing_inflight_leases WHERE user_id=$1`, f.user.ID).Scan(&owners, &attempts, &obligations))
				require.Equal(t, 1, owners)
				require.Equal(t, 1, attempts)
				require.Zero(t, obligations)
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
				require.Zero(t, logs)
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
				require.Zero(t, dedup)
				second := f.request(structuredHTTPBody(false), "/v1/chat/completions", "", f.openAI.ChatCompletions)
				require.Equal(t, http.StatusForbidden, second.Code, second.Body.String())
				require.Contains(t, second.Body.String(), "billing_error")
				require.EqualValues(t, 1, f.upstream.calls.Load())
				require.InDelta(t, hold, inflightHeld(t, f.user.ID), 1e-10)
				released.Do(func() { close(release) })
				var rec *httptest.ResponseRecorder
				select {
				case rec = <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("raw fallback stuck")
				}
				require.Equal(t, 200, rec.Code, rec.Body.String())
				assertStructuredHTTPSettlement(t, f, false, f.user.Balance)
			})
		}
	}
}

func TestStructuredInputFallbackHTTP_UnmeteredProofNegative(t *testing.T) {
	cases := map[string]string{
		"usage":          `{"error":{"code":"invalid_type","param":"input","message":"Expected a string, but got an array instead."},"usage":{"input_tokens":10}}`,
		"escaped_usage":  `{"error":{"code":"invalid_type","param":"input","message":"Expected a string, but got an array instead.","details":{"us\u0061ge":{"input_tokens":10}}}}`,
		"duplicate":      `{"error":{"code":"invalid_type","co\u0064e":"server_error","param":"input","message":"Expected a string, but got an array instead."}}`,
		"quoted_history": `{"error":{"message":"1 validation errors: [{'input_value':{'loc':('body','input','str'),'msg':'Input should be a valid string'}}]"}}`,
	}
	for name, rejection := range cases {
		t.Run(name, func(t *testing.T) {
			f := newInflightHTTPFixture(t, service.PlatformOpenAI, rejection, "application/json")
			f.upstream.status = 400
			close(f.upstream.release)
			rec := f.request(structuredHTTPBody(false), "/v1/chat/completions", "", f.openAI.ChatCompletions)
			require.Equal(t, 400, rec.Code, rec.Body.String())
			require.EqualValues(t, 1, f.upstream.calls.Load())
			require.Positive(t, inflightHeld(t, f.user.ID), "unproved refusal retains bounded unknown hold")
			f.pool.Stop()
			var logs, dedup int
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
			require.Zero(t, logs)
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
			require.Zero(t, dedup)
		})
	}
}
