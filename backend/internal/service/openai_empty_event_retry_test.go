package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type emptyRetryEvent struct {
	name       string
	payload    string
	eventLabel string
}

// Literal protocol examples are independent of the production classifier.
var emptyRetryEvents = []emptyRetryEvent{
	{"text_delta", `{"type":"response.output_text.delta","delta":""}`, ""},
	{"summary_delta", `{"type":"response.reasoning_summary_text.delta","delta":""}`, ""},
	{"reasoning_delta", `{"type":"response.reasoning_text.delta","delta":""}`, ""},
	{"function_delta", `{"type":"response.function_call_arguments.delta","delta":""}`, ""},
	{"custom_delta", `{"type":"response.custom_tool_call_input.delta","delta":""}`, ""},
	{"text_done", `{"type":"response.output_text.done","text":""}`, ""},
	{"summary_done", `{"type":"response.reasoning_summary_text.done","text":""}`, ""},
	{"reasoning_done", `{"type":"response.reasoning_text.done","text":""}`, ""},
	{"refusal_delta", `{"type":"response.refusal.delta","delta":""}`, ""},
	{"refusal_done", `{"type":"response.refusal.done","refusal":""}`, ""},
	{"content_done", `{"type":"response.content_part.done","part":{"type":"output_text","text":""}}`, ""},
	{"summary_part_done", `{"type":"response.reasoning_summary_part.done","part":{"type":"summary_text","text":""}}`, ""},
	{"content_refusal_done", `{"type":"response.content_part.done","part":{"type":"refusal","refusal":""}}`, ""},
	{"queued", `{"type":"response.queued","response":{"id":"resp_empty"}}`, ""},
	{"metadata", `{"type":"response.metadata","openai_chatgpt_moderation_metadata":{"safe":true}}`, ""},
}

var committedRetryEvents = []emptyRetryEvent{
	{"whitespace_is_output", `{"type":"response.output_text.delta","delta":" "}`, ""},
	{"function_arguments", `{"type":"response.function_call_arguments.delta","delta":"{}"}`, ""},
	{"custom_input", `{"type":"response.custom_tool_call_input.delta","delta":"run"}`, ""},
	{"function_completion", `{"type":"response.function_call_arguments.done","arguments":""}`, ""},
	{"custom_completion", `{"type":"response.custom_tool_call_input.done","input":""}`, ""},
	{"encrypted_reasoning", `{"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"ciphertext"}}`, ""},
	{"unknown_event", `{"type":"response.new_kind.delta","delta":""}`, ""},
	{"event_label_cannot_hide_completion", `{"type":"response.function_call_arguments.done","arguments":""}`, "response.output_text.delta"},
}

const emptyRetryFailure = `{"type":"response.failed","response":{"id":"resp_empty","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."},"usage":{"input_tokens":3,"output_tokens":0}}}`

type emptyRetryExchange struct {
	method      string
	uri         string
	requestBody []byte
	written     int
	writeErr    error
	flushErr    error
}

type emptyRetryPeer struct {
	server    *httptest.Server
	client    *http.Client
	completed chan emptyRetryExchange
	requests  atomic.Int32
	stream    string
}

func newEmptyRetryPeer(t *testing.T, event emptyRetryEvent) *emptyRetryPeer {
	t.Helper()
	label := event.eventLabel
	if label == "" {
		label = gjson.Get(event.payload, "type").String()
	}
	p := &emptyRetryPeer{completed: make(chan emptyRetryExchange, 2), stream: "event: " + label + "\ndata: " + event.payload + "\n\n" +
		"event: response.failed\ndata: " + emptyRetryFailure + "\n\n"}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.requests.Add(1)
		e := emptyRetryExchange{method: r.Method, uri: r.URL.RequestURI()}
		defer func() { p.completed <- e }()
		var readErr error
		e.requestBody, readErr = io.ReadAll(io.LimitReader(r.Body, 4097))
		if readErr != nil || len(e.requestBody) > 4096 {
			e.writeErr = fmt.Errorf("fixture request body: %v bytes=%d", readErr, len(e.requestBody))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "rid-empty-event-wire")
		e.written, e.writeErr = io.WriteString(w, p.stream)
		e.flushErr = http.NewResponseController(w).Flush()
	}))
	p.client = &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	t.Cleanup(func() { p.client.CloseIdleConnections(); p.server.Close() })
	return p
}

func (p *emptyRetryPeer) assertComplete(t *testing.T, ctx context.Context) {
	t.Helper()
	var e emptyRetryExchange
	select {
	case e = <-p.completed:
	case <-ctx.Done():
		t.Fatalf("EMPTY_EVENT_FIXTURE: missing actual peer completion: %v", ctx.Err())
	}
	if e.method != http.MethodPost || e.uri != "/backend-api/codex/responses" ||
		e.writeErr != nil || e.flushErr != nil || e.written != len(p.stream) || p.requests.Load() != 1 ||
		!gjson.ValidBytes(e.requestBody) || !gjson.GetBytes(e.requestBody, "stream").Bool() {
		t.Fatalf("EMPTY_EVENT_FIXTURE: incomplete actual wire: exchange=%+v requests=%d", e, p.requests.Load())
	}
	t.Logf("EMPTY_EVENT_WIRE_COMPLETE %s method=POST path=%s bytes=%d sha256=%x", t.Name(), e.uri, e.written, sha256.Sum256([]byte(p.stream)))
}

// This bridge port delivers the production-built HTTP request to an owned real
// listener. It replaces only destination scheme/authority, never a response/body.
type emptyRetryWireUpstream struct{ peer *emptyRetryPeer }

func (u emptyRetryWireUpstream) Do(request *http.Request, proxyURL string, _ int64, _ int) (*http.Response, error) {
	if proxyURL != "" || request == nil || request.URL == nil {
		return nil, fmt.Errorf("EMPTY_EVENT_FIXTURE: unexpected upstream request/proxy")
	}
	target, err := url.Parse(u.peer.server.URL)
	if err != nil {
		return nil, err
	}
	actual := request.Clone(request.Context())
	copyURL := *request.URL
	copyURL.Scheme, copyURL.Host = target.Scheme, target.Host
	actual.URL, actual.Host = &copyURL, target.Host
	return u.peer.client.Do(actual)
}

func (u emptyRetryWireUpstream) DoWithTLS(request *http.Request, proxyURL string, id int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	if profile != nil {
		return nil, fmt.Errorf("EMPTY_EVENT_FIXTURE: unexpected TLS profile")
	}
	return u.Do(request, proxyURL, id, concurrency)
}

type emptyRetryOutcome struct {
	err         error
	output      string
	firstToken  *int
	inputTokens int
}

func runEmptyRetryWire(t *testing.T, path string, event emptyRetryEvent) emptyRetryOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peer := newEmptyRetryPeer(t, event)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	account := &Account{ID: 1750, Name: "empty-event-contract", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
	out := emptyRetryOutcome{}
	if path == "bridge" {
		svc.httpUpstream = emptyRetryWireUpstream{peer: peer}
		payload := []byte(`{"type":"response.create","model":"model","input":"hi"}`)
		_, out.err = svc.proxyOpenAIWSHTTPBridgeTurn(ctx, c, account, "synthetic-contract-token", payload, len(payload),
			"model", "", "", "", 1, openAIWSHTTPBridgeToolState{}, func(message []byte) error {
				out.output += string(message) + "\n"
				return nil
			})
	} else {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, peer.server.URL+"/backend-api/codex/responses", strings.NewReader(`{"stream":true,"model":"model"}`))
		require.NoError(t, err)
		response, err := peer.client.Do(request)
		require.NoError(t, err, "EMPTY_EVENT_FIXTURE: actual HTTP request failed")
		defer func() {
			require.NoError(t, response.Body.Close(), "EMPTY_EVENT_FIXTURE: closing actual peer response")
		}()
		if path == "native" {
			result, err := svc.handleStreamingResponse(ctx, response, c, account, time.Now(), "model", "model")
			out.err = err
			if result != nil {
				out.firstToken = result.firstTokenMs
				if result.usage != nil {
					out.inputTokens = result.usage.InputTokens
				}
			}
		} else {
			result, err := svc.handleStreamingResponsePassthrough(ctx, response, c, account, time.Now(), "model", "model")
			out.err = err
			if result != nil {
				out.firstToken = result.firstTokenMs
				if result.usage != nil {
					out.inputTokens = result.usage.InputTokens
				}
			}
		}
		out.output = recorder.Body.String()
	}
	peer.assertComplete(t, ctx)
	return out
}

func TestOpenAIEmptyEventPreOutputRetry(t *testing.T) {
	for _, path := range []string{"native", "passthrough", "bridge"} {
		t.Run(path, func(t *testing.T) {
			for _, event := range emptyRetryEvents {
				t.Run(event.name, func(t *testing.T) {
					out := runEmptyRetryWire(t, path, event)
					var failover *UpstreamFailoverError
					if !errors.As(out.err, &failover) || !failover.RequestScopedTransient || !failover.RetryableOnSameAccount || out.output != "" {
						t.Fatalf("OPENAI_EMPTY_PREOUTPUT_ASSERT: actual empty event consumed safe retry: err=%T %v output=%q", out.err, out.err, out.output)
					}
					require.False(t, failover.BillingNoCharge, "empty output is not a no-charge proof")
					require.False(t, failover.ForceCacheBilling, "safe retry must not invent a cache billing switch")
					if path != "bridge" {
						require.Nil(t, out.firstToken)
						require.Equal(t, 3, out.inputTokens, "preserve real terminal input usage")
					}
					t.Logf("OPENAI_EMPTY_PREOUTPUT_COMPLETE %s requestScoped=true outputBytes=0", t.Name())
				})
			}
		})
	}
}

func TestOpenAIEmptyEventCommittedOutputDoesNotReplay(t *testing.T) {
	for _, path := range []string{"native", "passthrough", "bridge"} {
		t.Run(path, func(t *testing.T) {
			for _, event := range committedRetryEvents {
				t.Run(event.name, func(t *testing.T) {
					out := runEmptyRetryWire(t, path, event)
					var failover *UpstreamFailoverError
					if errors.As(out.err, &failover) || out.output == "" || !strings.Contains(out.output, `"code":"server_error"`) || strings.Contains(out.output, `"code":"server_is_overloaded"`) {
						t.Fatalf("OPENAI_EMPTY_REPLAY_GUARD_ASSERT: committed event lost its no-replay boundary or failed code: err=%T %v output=%q", out.err, out.err, out.output)
					}
				})
			}
		})
	}
}

func TestOpenAIEmptyEventConservativeClassification(t *testing.T) {
	cases := []struct{ name, payload, eventType string }{
		{"delta_missing", `{"type":"response.output_text.delta"}`, "response.output_text.delta"},
		{"delta_null", `{"type":"response.output_text.delta","delta":null}`, "response.output_text.delta"},
		{"delta_object", `{"type":"response.output_text.delta","delta":{}}`, "response.output_text.delta"},
		{"delta_number", `{"type":"response.output_text.delta","delta":0}`, "response.output_text.delta"},
		{"delta_false", `{"type":"response.output_text.delta","delta":false}`, "response.output_text.delta"},
		{"text_missing", `{"type":"response.output_text.done"}`, "response.output_text.done"},
		{"text_null", `{"type":"response.output_text.done","text":null}`, "response.output_text.done"},
		{"refusal_null", `{"type":"response.refusal.done","refusal":null}`, "response.refusal.done"},
		{"part_missing", `{"type":"response.content_part.done"}`, "response.content_part.done"},
		{"part_null", `{"type":"response.content_part.done","part":null}`, "response.content_part.done"},
		{"part_array", `{"type":"response.content_part.done","part":[]}`, "response.content_part.done"},
		{"part_text_missing", `{"type":"response.content_part.done","part":{"type":"output_text"}}`, "response.content_part.done"},
		{"part_text_null", `{"type":"response.content_part.done","part":{"type":"output_text","text":null}}`, "response.content_part.done"},
		{"part_text_object", `{"type":"response.content_part.done","part":{"type":"output_text","text":{}}}`, "response.content_part.done"},
		{"part_refusal_missing", `{"type":"response.content_part.done","part":{"type":"refusal"}}`, "response.content_part.done"},
		{"part_unknown", `{"type":"response.content_part.done","part":{"type":"audio","text":""}}`, "response.content_part.done"},
		{"part_type_missing", `{"type":"response.content_part.done","part":{"text":""}}`, "response.content_part.done"},
		{"summary_text_missing", `{"type":"response.reasoning_summary_part.done","part":{"type":"summary_text"}}`, "response.reasoning_summary_part.done"},
		{"summary_text_null", `{"type":"response.reasoning_summary_part.done","part":{"type":"summary_text","text":null}}`, "response.reasoning_summary_part.done"},
		{"summary_wrong_type", `{"type":"response.reasoning_summary_part.done","part":{"type":"output_text","text":""}}`, "response.reasoning_summary_part.done"},
		{"invalid_json", `{"type":"response.output_text.delta","delta":""`, "response.output_text.delta"},
		{"payload_array", `[{"type":"response.output_text.delta","delta":""}]`, "response.output_text.delta"},
		{"type_missing", `{"delta":""}`, "response.output_text.delta"},
		{"type_null", `{"type":null,"delta":""}`, "response.output_text.delta"},
		{"contradictory_type", `{"type":"response.function_call_arguments.done","delta":""}`, "response.output_text.delta"},
		{"metadata_type_mismatch", `{"type":"response.output_text.delta","delta":"visible"}`, "response.metadata"},
		{"duplicate_type", `{"type":"response.output_text.delta","type":"response.function_call_arguments.done","delta":""}`, "response.output_text.delta"},
		{"duplicate_delta", `{"type":"response.output_text.delta","delta":"","delta":"visible"}`, "response.output_text.delta"},
		{"duplicate_part_text", `{"type":"response.content_part.done","part":{"type":"output_text","text":"","text":"visible"}}`, "response.content_part.done"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if !openAIStreamDataStartsClientOutput(test.payload, test.eventType) {
				t.Fatalf("OPENAI_EMPTY_CONSERVATIVE_ASSERT: ambiguous frame permitted replay: %s", test.payload)
			}
		})
	}
}
