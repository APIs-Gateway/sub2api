//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Shared OLD/NEW fixture: real public handlers, real GatewayService and real
// HTTP provider. Repository/usage mocks do not prove wallet/card settlement.
type ownedMessageHTTPTransport struct {
	client *http.Client
	calls  atomic.Int64
}

func (u *ownedMessageHTTPTransport) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls.Add(1)
	return u.client.Do(req)
}

func (u *ownedMessageHTTPTransport) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

func ownedMessagePublicRequest(t *testing.T, route, mode, providerBody, contentType string) (*httptest.ResponseRecorder, *ownedMessageHTTPTransport, *service.UsageLog) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	requests := make(chan *http.Request, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(providerBody))
	}))
	t.Cleanup(provider.Close)
	u := &ownedMessageHTTPTransport{client: provider.Client()}
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeStandard
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	account := chainRespAccount(11)
	account.Credentials["base_url"] = provider.URL
	account.Extra["openai_passthrough"] = strings.HasPrefix(mode, "passthrough")
	group := chainRespGroup(1, 1, 0)
	group.AllowMessagesDispatch = true
	logs := make(chan *service.UsageLog, 4)
	billingCache := service.NewBillingCacheService(nil, &chainRespUserRepo{}, nil, nil, nil, &hopRateRepo{}, cfg, nil, nil)
	t.Cleanup(billingCache.Stop)
	concurrency := service.NewConcurrencyService(&concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	})
	gateway := service.NewOpenAIGatewayService(
		&chainRespAccountRepo{schedulable: map[int64][]service.Account{1: {account}}},
		&openAIWSUsageHandlerUsageLogRepoStub{created: logs},
		&openAIWSPrewarmUsageBillingRepoStub{commands: make(chan *service.UsageBillingCommand, 4)},
		nil, nil, nil, nil, cfg, nil, concurrency, service.NewBillingService(cfg, nil),
		nil, billingCache, u, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := NewOpenAIGatewayHandler(gateway, concurrency, billingCache, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	h.concurrencyHelper = NewConcurrencyHelper(concurrency, SSEPingFormatNone, time.Second)
	groupID := int64(1)
	key := &service.APIKey{ID: 1698, UserID: chainRespUserID, GroupID: &groupID, Group: group, User: &service.User{ID: chainRespUserID, Status: service.StatusActive, Balance: 100}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyAPIKey), key)
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: chainRespUserID, Concurrency: 1})
	})
	router.POST("/openai/v1/responses", h.Responses)
	router.POST("/openai/v1/chat/completions", h.ChatCompletions)
	router.POST("/openai/v1/messages", h.Messages)
	stream := mode == "normal-stream" || mode == "passthrough-stream"
	body := fmt.Sprintf(`{"model":"gpt-5.4","stream":%t,"input":"hello"}`, stream)
	if route != "responses" {
		body = `{"model":"gpt-5.4","stream":false,"max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`
	}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/"+route, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "curl/8.0")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.EqualValues(t, 1, u.calls.Load(), "one provider attempt, no new retry policy")
	upstreamRequest := <-requests
	require.Equal(t, http.MethodPost, upstreamRequest.Method)
	require.Contains(t, upstreamRequest.URL.Path, "responses")
	var usage *service.UsageLog
	select {
	case usage = <-logs:
	case <-time.After(5 * time.Second):
		t.Fatal("successful public request did not record usage")
	}
	require.EqualValues(t, 7, usage.InputTokens)
	require.EqualValues(t, 3, usage.OutputTokens)
	return rec, u, usage
}

func ownedMessageSSE(events ...string) string {
	return "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
}

func ownedMessageTerminal(kind, status, output string) string {
	return fmt.Sprintf(`{"type":%q,"response":{"id":"resp_owned","object":"response","created_at":1,"model":"gpt-5.4","status":%q,"vendor":{"n":9007199254740993},"output":%s,"usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}}}`, kind, status, output)
}

func ownedMessageFinals(t *testing.T, rec *httptest.ResponseRecorder, mode string) []gjson.Result {
	t.Helper()
	// The existing normal unary path retains the upstream SSE Content-Type
	// while writing its extracted JSON body. Decode the actual body first;
	// this fixture does not change that pre-existing header contract.
	if mode == "normal-stream" || mode == "passthrough-stream" {
		require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
		require.False(t, json.Valid(rec.Body.Bytes()), "stream mode keeps SSE framing")
	}
	if json.Valid(rec.Body.Bytes()) {
		wire := gjson.ParseBytes(rec.Body.Bytes())
		require.True(t, wire.IsObject(), rec.Body.String())
		return []gjson.Result{wire}
	}
	require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	var final []gjson.Result
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		e := gjson.Parse(strings.TrimPrefix(line, "data: "))
		if e.Get("response").IsObject() && e.Get("type").String() != "response.created" {
			final = append(final, e.Get("response"))
		}
	}
	require.NotEmpty(t, final, rec.Body.String())
	return final
}

func requireOwnedMessageWire(t *testing.T, item gjson.Result, id, status string, texts ...string) {
	t.Helper()
	require.Equal(t, "message", item.Get("type").String())
	if id == "" {
		require.NotEmpty(t, item.Get("id").String())
	} else {
		require.Equal(t, id, item.Get("id").String())
	}
	require.Equal(t, status, item.Get("status").String())
	require.Equal(t, "assistant", item.Get("role").String())
	parts := item.Get("content").Array()
	require.Len(t, parts, len(texts))
	for i, text := range texts {
		require.Equal(t, text, parts[i].Get("text").String())
		require.Equal(t, "output_text", parts[i].Get("type").String())
		require.True(t, parts[i].Get("annotations").IsArray(), parts[i].Raw)
		require.True(t, parts[i].Get("logprobs").IsArray(), parts[i].Raw)
	}
}

func TestOwnedResponsesMessage_PublicHTTP(t *testing.T) {
	added := `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_observed","role":"assistant","status":"in_progress","vendor_id":9007199254740993,"content":[]}}`
	delta := `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_observed","delta":"hello"}`
	for _, mode := range []string{"normal-stream", "normal-unary", "passthrough-unary"} {
		t.Run(mode, func(t *testing.T) {
			t.Run("observed_added_identity_and_owned_fields", func(t *testing.T) {
				rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(added, delta, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
				f := ownedMessageFinals(t, rec, mode)[0]
				requireOwnedMessageWire(t, f.Get("output.0"), "msg_observed", "completed", "hello")
				require.Equal(t, "9007199254740993", f.Get("output.0.vendor_id").Raw)
			})
			t.Run("delta_identity_without_added", func(t *testing.T) {
				rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(delta, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
				requireOwnedMessageWire(t, ownedMessageFinals(t, rec, mode)[0].Get("output.0"), "msg_observed", "completed", "hello")
			})
			t.Run("anonymous_nonempty_generated_identity", func(t *testing.T) {
				d := `{"type":"response.output_text.delta","delta":"hello"}`
				rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(d, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
				requireOwnedMessageWire(t, ownedMessageFinals(t, rec, mode)[0].Get("output.0"), "", "completed", "hello")
			})
			t.Run("two_messages_are_not_collapsed", func(t *testing.T) {
				d2 := `{"type":"response.output_text.delta","output_index":1,"content_index":0,"item_id":"msg_second","delta":"world"}`
				rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(added, delta, d2, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
				f := ownedMessageFinals(t, rec, mode)[0]
				require.Len(t, f.Get("output").Array(), 2)
				requireOwnedMessageWire(t, f.Get("output.0"), "msg_observed", "completed", "hello")
				requireOwnedMessageWire(t, f.Get("output.1"), "msg_second", "completed", "world")
			})
			for _, done := range []string{
				`{"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"msg_observed"}`,
				`{"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"msg_observed","text":""}`,
				`{"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"msg_observed","part":{"type":"output_text","text":"","annotations":[]}}`,
			} {
				t.Run("empty_done_retains_text_"+gjson.Get(done, "type").String()+fmt.Sprint(len(done)), func(t *testing.T) {
					rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(added, delta, done, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
					requireOwnedMessageWire(t, ownedMessageFinals(t, rec, mode)[0].Get("output.0"), "msg_observed", "completed", "hello")
				})
			}
			for _, kind := range []string{"response.incomplete", "response.cancelled", "response.canceled"} {
				t.Run("unfinished_"+kind, func(t *testing.T) {
					// Deliberately contradictory outer completed status: the actual
					// unfinished event controls only the new message's status.
					eventKind, outerStatus := kind, "completed"
					if mode != "normal-stream" {
						// Existing unary consumers accept completed/done only; they
						// must use an unfinished actual response status in that path.
						eventKind, outerStatus = "response.completed", "incomplete"
					}
					rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(added, delta, ownedMessageTerminal(eventKind, outerStatus, "[]")), "text/event-stream")
					f := ownedMessageFinals(t, rec, mode)[0]
					requireOwnedMessageWire(t, f.Get("output.0"), "msg_observed", "incomplete", "hello")
					require.Equal(t, outerStatus, f.Get("status").String(), "do not rewrite the raw envelope")
				})
			}
			t.Run("invalid_added_role_hint_does_not_change_owned_assistant_role", func(t *testing.T) {
				seed := `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_observed","role":"developer","status":"in_progress","content":[]}}`
				rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(seed, delta, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
				requireOwnedMessageWire(t, ownedMessageFinals(t, rec, mode)[0].Get("output.0"), "msg_observed", "completed", "hello")
			})
			t.Run("multiple_parts_refusal_and_metadata", func(t *testing.T) {
				part := `{"type":"response.content_part.added","output_index":0,"content_index":0,"item_id":"msg_observed","part":{"type":"output_text","text":"","annotations":[{"type":"url_citation","url":"https://example.test","title":"source","start_index":0,"end_index":5}],"logprobs":[{"token":"hello","logprob":-0.1}],"vendor_part":9007199254740993,"nested":{"id":9223372036854775807},"cache_unknown":{"value":9007199254740993}}}`
				refusal := `{"type":"response.refusal.delta","output_index":0,"content_index":1,"item_id":"msg_observed","delta":"declined"}`
				rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(added, part, delta, refusal,
					`{"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"msg_observed","part":{"type":"output_text","text":"","incoming_vendor":9007199254740995}}`, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
				parts := ownedMessageFinals(t, rec, mode)[0].Get("output.0.content").Array()
				require.Len(t, parts, 2)
				require.Equal(t, "hello", parts[0].Get("text").String())
				require.Len(t, parts[0].Get("annotations").Array(), 1)
				require.Len(t, parts[0].Get("logprobs").Array(), 1)
				require.Equal(t, "9007199254740993", parts[0].Get("vendor_part").Raw)
				require.Equal(t, "9223372036854775807", parts[0].Get("nested.id").Raw)
				require.Equal(t, "9007199254740993", parts[0].Get("cache_unknown.value").Raw)
				require.Equal(t, "9007199254740995", parts[0].Get("incoming_vendor").Raw)
				require.Equal(t, "refusal", parts[1].Get("type").String())
				require.Equal(t, "declined", parts[1].Get("refusal").String())
				require.False(t, parts[1].Get("annotations").Exists())
				require.False(t, parts[1].Get("text").Exists())
			})
			t.Run("opaque_added_parts_survive_owned_text_reconstruction", func(t *testing.T) {
				opaque := `{"type":"vendor_media","payload":{"id":9223372036854775807,"nested":[9007199254740993]},"mime":"application/vendor"}`
				seed := `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_opaque","role":"assistant","status":"in_progress","content":[` + opaque + `,{"type":"output_text","text":""}]}}`
				d := `{"type":"response.output_text.delta","output_index":0,"content_index":1,"item_id":"msg_opaque","delta":"B"}`
				rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(seed, d, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
				f := ownedMessageFinals(t, rec, mode)[0]
				require.Len(t, f.Get("output.0.content").Array(), 2)
				require.JSONEq(t, opaque, f.Get("output.0.content.0").Raw)
				require.Equal(t, "9223372036854775807", f.Get("output.0.content.0.payload.id").Raw)
				require.Equal(t, "9007199254740993", f.Get("output.0.content.0.payload.nested.0").Raw)
				require.Equal(t, "B", f.Get("output.0.content.1.text").String())
			})
			t.Run("opaque_part_event_survives_but_never_becomes_text", func(t *testing.T) {
				opaque := `{"type":"vendor_media","payload":{"id":9223372036854775807}}`
				part := `{"type":"response.content_part.added","output_index":0,"content_index":0,"item_id":"msg_observed","part":` + opaque + `}`
				d := `{"type":"response.output_text.delta","output_index":0,"content_index":1,"item_id":"msg_observed","delta":"B"}`
				rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(added, part, d, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
				f := ownedMessageFinals(t, rec, mode)[0]
				require.JSONEq(t, opaque, f.Get("output.0.content.0").Raw)
				require.False(t, f.Get("output.0.content.0.annotations").Exists())
				require.Equal(t, "B", f.Get("output.0.content.1.text").String())
			})
			t.Run("nonempty_done_text_is_authoritative", func(t *testing.T) {
				done := `{"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"msg_observed","text":"authoritative-done"}`
				rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(added, delta, done, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
				requireOwnedMessageWire(t, ownedMessageFinals(t, rec, mode)[0].Get("output.0"), "msg_observed", "completed", "authoritative-done")
			})
			t.Run("raw_done_and_terminal_remain_authoritative", func(t *testing.T) {
				item := `{"type":"message","id":"msg_raw","status":"completed","role":"assistant","vendor":{"big":9007199254740993},"content":[{"type":"output_text","text":"authoritative","annotations":null,"logprobs":null,"extra":true}]}`
				done := `{"type":"response.output_item.done","output_index":0,"item":` + item + `}`
				rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(delta, done, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
				require.Equal(t, item, ownedMessageFinals(t, rec, mode)[0].Get("output.0").Raw)
			})
			t.Run("raw_reasoning_delta_does_not_add_unannounced_reasoning", func(t *testing.T) {
				reasoning := `{"type":"response.reasoning_text.delta","output_index":1,"delta":"private-thinking"}`
				rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(added, delta, reasoning, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
				f := ownedMessageFinals(t, rec, mode)[0]
				require.Len(t, f.Get("output").Array(), 1, "existing1411 ordering boundary")
				require.Equal(t, "message", f.Get("output.0.type").String())
			})
		})
	}
	for _, route := range []string{"chat/completions", "messages"} {
		t.Run("converted_"+route, func(t *testing.T) {
			t.Run("explicit_missing_content_index_keeps_authoritative_first_part", func(t *testing.T) {
				d := `{"type":"response.output_text.delta","output_index":0,"content_index":1,"item_id":"msg_observed","delta":"B"}`
				terminal := `[{"type":"message","id":"msg_observed","status":"completed","role":"assistant","content":[{"type":"output_text","text":"A"}]}]`
				rec, _, _ := ownedMessagePublicRequest(t, route, "normal-unary", ownedMessageSSE(added, d, ownedMessageTerminal("response.completed", "completed", terminal)), "text/event-stream")
				text := gjson.Get(rec.Body.String(), "choices.0.message.content").String()
				if route == "messages" {
					text = ""
					for _, part := range gjson.Get(rec.Body.String(), "content").Array() {
						if part.Get("type").String() == "text" {
							text += part.Get("text").String()
						}
					}
				}
				require.Equal(t, "AB", text)
			})
			t.Run("ambiguous_unindexed_delta_does_not_claim_first_message_identity", func(t *testing.T) {
				d := `{"type":"response.output_text.delta","delta":"C"}`
				terminal := `[{"type":"message","id":"msg_A","status":"completed","role":"assistant","content":[{"type":"output_text","text":"A"}]},{"type":"message","id":"msg_B","status":"completed","role":"assistant","content":[{"type":"output_text","text":"B"}]}]`
				rec, _, _ := ownedMessagePublicRequest(t, route, "normal-unary", ownedMessageSSE(d, ownedMessageTerminal("response.completed", "completed", terminal)), "text/event-stream")
				text := gjson.Get(rec.Body.String(), "choices.0.message.content").String()
				if route == "messages" {
					text = ""
					for _, part := range gjson.Get(rec.Body.String(), "content").Array() {
						if part.Get("type").String() == "text" {
							text += part.Get("text").String()
						}
					}
				}
				require.Equal(t, "ABC", text)
			})
			t.Run("empty_done_retains_reply", func(t *testing.T) {
				done := `{"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"msg_observed","part":{"type":"output_text"}}`
				rec, _, _ := ownedMessagePublicRequest(t, route, "normal-unary", ownedMessageSSE(added, delta, done, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
				text := gjson.Get(rec.Body.String(), "choices.0.message.content").String()
				if route == "messages" {
					text = ""
					for _, part := range gjson.Get(rec.Body.String(), "content").Array() {
						if part.Get("type").String() == "text" {
							text += part.Get("text").String()
						}
					}
				}
				require.Equal(t, "hello", text)
			})
		})
	}
	t.Run("normal_stream_staged_provider_identity_is_reserved_before_first_flush", func(t *testing.T) {
		late := `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_conflict","role":"assistant","status":"in_progress","content":[]}}`
		// Both added frames have no semantic content and enter the first-output
		// stage. The first reserved raw identity is retained when delta commits it.
		rec, _, _ := ownedMessagePublicRequest(t, "responses", "normal-stream", ownedMessageSSE(added, late, delta, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
		requireOwnedMessageWire(t, ownedMessageFinals(t, rec, "normal-stream")[0].Get("output.0"), "msg_observed", "completed", "hello")
		require.Contains(t, rec.Body.String(), `"id":"msg_observed"`)
	})
	for _, mode := range []string{"normal-stream", "normal-unary", "passthrough-unary"} {
		t.Run(mode+"_late_observed_identity_before_generated_terminal", func(t *testing.T) {
			d := `{"type":"response.output_text.delta","output_index":0,"delta":"hello"}`
			late := `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_late","role":"assistant","status":"in_progress","content":[]}}`
			rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(d, late, ownedMessageTerminal("response.completed", "completed", "[]")), "text/event-stream")
			requireOwnedMessageWire(t, ownedMessageFinals(t, rec, mode)[0].Get("output.0"), "msg_late", "completed", "hello")
		})
	}
	t.Run("normal_stream_first_queued_identity_and_duplicate_terminals", func(t *testing.T) {
		late := `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_conflict","role":"assistant","status":"in_progress","content":[]}}`
		terminal := ownedMessageTerminal("response.completed", "completed", "[]")
		rec, _, _ := ownedMessagePublicRequest(t, "responses", "normal-stream", ownedMessageSSE(added, delta, late, terminal, terminal), "text/event-stream")
		finals := ownedMessageFinals(t, rec, "normal-stream")
		require.Len(t, finals, 1, "existing reader stops at its first terminal")
		for _, final := range finals {
			requireOwnedMessageWire(t, final.Get("output.0"), "msg_observed", "completed", "hello")
		}
		require.Contains(t, rec.Body.String(), `"id":"msg_observed"`, "first staged added payload is kept verbatim")
	})
	t.Run("normal_stream_first_terminal_stops_before_late_provider_identity", func(t *testing.T) {
		d := `{"type":"response.output_text.delta","output_index":0,"delta":"hello"}`
		late := `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_late","role":"assistant","status":"in_progress","content":[]}}`
		terminal := ownedMessageTerminal("response.completed", "completed", "[]")
		rec, _, _ := ownedMessagePublicRequest(t, "responses", "normal-stream", ownedMessageSSE(d, terminal, late, terminal), "text/event-stream")
		finals := ownedMessageFinals(t, rec, "normal-stream")
		require.Len(t, finals, 1, "existing reader stops at its first terminal")
		id := finals[0].Get("output.0.id").String()
		require.NotEmpty(t, id)
		require.NotEqual(t, "msg_late", id)
		// Frames after the first terminal are not consumed by this public path.
		// Repeated builds after explicit wire commitment are NEW-only state
		// controls; do not expand the reader's acceptance boundary here.
		requireOwnedMessageWire(t, finals[0].Get("output.0"), id, "completed", "hello")
		require.NotContains(t, rec.Body.String(), `"id":"msg_late"`, "frames after the first terminal are not forwarded")
	})
	for _, mode := range []string{"normal-unary", "passthrough-unary"} {
		t.Run(mode+"_unfinished_event_keeps_existing_SSE_acceptance_boundary", func(t *testing.T) {
			terminal := ownedMessageTerminal("response.incomplete", "incomplete", "[]")
			rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, ownedMessageSSE(added, delta, terminal), "text/event-stream")
			require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
			f := ownedMessageFinals(t, rec, mode)[0]
			require.Empty(t, f.Get("output").Array(), "no new unary terminal acceptance or synthesized completion")
			require.Equal(t, "incomplete", f.Get("status").String())
		})
	}
	// Pure passthrough streaming remains provider-owned, including missing fields.
	t.Run("passthrough_stream_is_not_a_global_normalizer", func(t *testing.T) {
		terminal := ownedMessageTerminal("response.completed", "completed", `[{"type":"message","content":[{"type":"output_text","text":"raw"}]}]`)
		rec, _, _ := ownedMessagePublicRequest(t, "responses", "passthrough-stream", ownedMessageSSE(delta, terminal), "text/event-stream")
		require.JSONEq(t, gjson.Get(terminal, "response.output").Raw, ownedMessageFinals(t, rec, "passthrough-stream")[0].Get("output").Raw)
	})
	for _, mode := range []string{"normal-unary", "passthrough-unary"} {
		t.Run(mode+"_raw_json_not_normalized", func(t *testing.T) {
			response := gjson.Get(ownedMessageTerminal("response.completed", "completed", `[{"type":"message","content":[{"type":"output_text","text":"raw"}]}]`), "response").Raw
			rec, _, _ := ownedMessagePublicRequest(t, "responses", mode, response, "application/json")
			require.JSONEq(t, gjson.Get(response, "output").Raw, gjson.Get(rec.Body.String(), "output").Raw)
		})
	}
}
