//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const structuredHTTPRejection = `{"error":{"type":"invalid_request_error","code":"invalid_type","param":"input","message":"Invalid type for 'input': expected a string, but got an array instead."}}`
const structuredHTTPVLLMRejection = `{"error":{"type":"Bad Request","param":null,"code":400,"message":"2 validation errors: [{'type':'string_type','loc':('body','input','str'),'msg':'Input should be a valid string','input':[{'role':'assistant','content':'private history'}]}, {'type':'list_type','loc':('body','input','list'),'msg':'invalid list'}]"}}`
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
	var endpoint string
	require.NoError(t, inflightTestDB(t).QueryRow(`SELECT upstream_endpoint FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&endpoint))
	require.Equal(t, "/v1/chat/completions", endpoint)
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

func TestStructuredInputFallbackHTTP_SecondMeteredPartialBillsOnce(t *testing.T) {
	for name, rejection := range map[string]string{"canonical": structuredHTTPRejection, "vllm": structuredHTTPVLLMRejection} {
		for _, card := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/card_%t", name, card), func(t *testing.T) {
				f := newInflightHTTPFixture(t, service.PlatformOpenAI, inflightChatJSON, "application/json")
				chatAudioWallet(t, f)
				if card {
					admissionCard(t, inflightTestEntClient(t), f.user.ID, 0, 1, 10, 20, 0, 0, 0)
				}
				structuredHTTPSequence(t, f, rejection, true, nil, nil)
				observe := f.upstream.observe
				f.upstream.observe = func(req *http.Request) {
					observe(req)
					if f.upstream.calls.Load() == 1 {
						f.upstream.response = `data: {"id":"partial_structured","model":"gpt-5","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n"
						f.upstream.readErr = errors.New("provider interrupted after metered content")
					}
				}
				rec := f.request(structuredHTTPBody(true), "/v1/chat/completions", "", f.openAI.ChatCompletions)
				require.Equal(t, 200, rec.Code, rec.Body.String())
				require.Contains(t, rec.Body.String(), "ok")
				require.Contains(t, rec.Body.String(), "error")
				require.NotContains(t, rec.Body.String(), "data: [DONE]")
				assertStructuredHTTPSettlement(t, f, card, 10)
			})
		}
	}
}

func TestStructuredInputFallbackHTTP_SecondRefusalAndUnknownHold(t *testing.T) {
	for _, kind := range []string{"authentication", "permission", "raw_400", "raw_429", "raw_500", "raw_stream_read_failure"} {
		t.Run(kind, func(t *testing.T) {
			f := newInflightHTTPFixture(t, service.PlatformOpenAI, inflightChatJSON, "application/json")
			stream := kind == "raw_stream_read_failure"
			structuredHTTPSequence(t, f, structuredHTTPRejection, stream, nil, nil)
			observe := f.upstream.observe
			f.upstream.observe = func(req *http.Request) {
				observe(req)
				if f.upstream.calls.Load() != 1 {
					return
				}
				f.upstream.status = 400
				f.upstream.response = `{"error":{"type":"invalid_request_error","message":"other raw rejection"}}`
				f.upstream.contentType = "application/json"
				switch kind {
				case "authentication":
					f.upstream.status = 401
					f.upstream.response = `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"denied"}}`
				case "permission":
					f.upstream.status = 403
					f.upstream.response = `{"error":{"type":"permission_error","message":"denied"}}`
				case "raw_429":
					f.upstream.status = 429
				case "raw_500":
					f.upstream.status = 500
				case "raw_stream_read_failure":
					f.upstream.status = 200
					f.upstream.response = ""
					f.upstream.contentType = "text/event-stream"
					f.upstream.readErr = errors.New("unknown provider execution before content")
				}
			}
			rec := f.request(structuredHTTPBody(stream), "/v1/chat/completions", "", f.openAI.ChatCompletions)
			require.NotEqual(t, 200, rec.Code, rec.Body.String())
			require.EqualValues(t, 2, f.upstream.calls.Load(), "raw second refusal must not trigger an internal third send")
			f.pool.Stop()
			var logs, dedup int
			var balance float64
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
			require.Zero(t, logs)
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
			require.Zero(t, dedup)
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT balance FROM users WHERE id=$1`, f.user.ID).Scan(&balance))
			require.InDelta(t, f.user.Balance, balance, 1e-12)
			if kind == "authentication" || kind == "permission" {
				require.InDelta(t, 0, inflightHeld(t, f.user.ID), 1e-12)
			} else {
				require.Positive(t, inflightHeld(t, f.user.ID), "first validation cannot grant no-charge proof to a later unknown execution")
			}
		})
	}
}

func TestStructuredInputFallbackHTTP_CompleteBodyAndReprNegative(t *testing.T) {
	for _, kind := range []string{"reader_failure", "limit_tail_usage", "limit_tail_second_json", "truncated_repr", "duplicate_repr_loc"} {
		t.Run(kind, func(t *testing.T) {
			rejection := structuredHTTPRejection
			switch kind {
			case "limit_tail_usage":
				rejection += strings.Repeat(" ", 512<<10) + `{"usage":{"input_tokens":10}}`
			case "limit_tail_second_json":
				rejection += strings.Repeat(" ", 512<<10) + structuredHTTPRejection
			case "truncated_repr":
				rejection = `{"error":{"message":"1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':"}}`
			case "duplicate_repr_loc":
				rejection = `{"error":{"message":"1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':[],'loc':('body','model','str')}]"}}`
			}
			f := newInflightHTTPFixture(t, service.PlatformOpenAI, rejection, "application/json")
			f.upstream.status = 400
			if kind == "reader_failure" {
				f.upstream.readErr = errors.New("complete JSON prefix followed by non-EOF error")
			}
			close(f.upstream.release)
			rec := f.request(structuredHTTPBody(false), "/v1/chat/completions", "", f.openAI.ChatCompletions)
			require.Equal(t, 400, rec.Code, rec.Body.String())
			require.EqualValues(t, 1, f.upstream.calls.Load())
			require.Positive(t, inflightHeld(t, f.user.ID))
			f.pool.Stop()
			var logs, dedup int
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
			require.Zero(t, logs)
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
			require.Zero(t, dedup)
		})
	}
}

func structuredHTTPWithOps(t *testing.T, f *inflightHTTPFixture, serve func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	cfg := &config.Config{}
	cfg.Ops.Enabled = true
	ops := service.NewOpsService(NewOpsRepository(inflightTestDB(t)), NewSettingRepository(inflightTestEntClient(t)), cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(userhandler.InboundEndpointMiddleware(), func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, f.key.Group))
		c.Set(string(middleware.ContextKeyAPIKey), f.key)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: f.user.ID, Concurrency: 100})
		c.Next()
	}, userhandler.OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/chat/completions", serve)
	rec := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(structuredHTTPBody(false)))
	request.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	router.ServeHTTP(rec, request.WithContext(ctx))
	return rec
}

func TestStructuredInputFallbackHTTP_TransportOpsAndOuterAttemptReset(t *testing.T) {
	for _, recover := range []bool{false, true} {
		t.Run(fmt.Sprintf("outer_recovery_%t", recover), func(t *testing.T) {
			f := newInflightHTTPFixture(t, service.PlatformOpenAI, inflightChatJSON, "application/json")
			chatAudioWallet(t, f)
			close(f.upstream.release)
			var other *service.Account
			if recover {
				other = mustCreateAccount(t, inflightTestEntClient(t), &service.Account{Name: "structured-second", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Concurrency: 100, Priority: 10, Credentials: map[string]any{"api_key": "other-key", "base_url": "https://second.test", "pool_mode": true, "pool_mode_retry_count": 0}, Extra: map[string]any{"privacy_mode": service.PrivacyModeTrainingOff, "openai_responses_supported": true}})
				require.NoError(t, f.accounts.BindGroups(context.Background(), other.ID, []int64{*f.key.GroupID}))
			}
			var current *gin.Context
			rawEndpoint := ""
			finalEndpoint := ""
			f.upstream.script = func(req *http.Request, id int64) (*http.Response, error) {
				response := func(status int, payload, contentType string) *http.Response {
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(payload)), Request: req}
				}
				if id == f.account.ID {
					if f.upstream.calls.Load() == 1 {
						require.Equal(t, "/v1/responses", req.URL.Path)
						return response(400, structuredHTTPRejection, "application/json"), nil
					}
					require.Equal(t, "/v1/chat/completions", req.URL.Path)
					rawEndpoint = userhandler.GetUpstreamEndpoint(current, service.PlatformOpenAI)
					return nil, errors.New("header timeout before response")
				}
				require.True(t, recover)
				require.Equal(t, other.ID, id)
				require.Equal(t, "/v1/responses", req.URL.Path)
				require.Equal(t, "/v1/responses", userhandler.GetUpstreamEndpoint(current, service.PlatformOpenAI))
				return response(200, inflightResponsesSSE, "text/event-stream"), nil
			}
			rec := structuredHTTPWithOps(t, f, func(c *gin.Context) {
				current = c
				f.openAI.ChatCompletions(c)
				finalEndpoint = userhandler.GetUpstreamEndpoint(c, service.PlatformOpenAI)
			})
			require.Equal(t, "/v1/chat/completions", rawEndpoint)
			var opsEndpoint string
			require.Eventually(t, func() bool {
				return inflightTestDB(t).QueryRow(`SELECT upstream_endpoint FROM ops_error_logs ORDER BY id DESC LIMIT 1`).Scan(&opsEndpoint) == nil
			}, 10*time.Second, 20*time.Millisecond, "real Ops middleware must persist endpoint")
			if recover {
				require.Equal(t, 200, rec.Code, rec.Body.String())
				require.EqualValues(t, 3, f.upstream.calls.Load())
				require.Equal(t, "/v1/responses", finalEndpoint)
				require.Equal(t, "/v1/responses", opsEndpoint)
				f.pool.Stop()
				var logs, dedup int
				var accountID int64
				var endpoint string
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*),max(account_id),max(upstream_endpoint) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs, &accountID, &endpoint))
				require.Equal(t, 1, logs)
				require.Equal(t, other.ID, accountID)
				require.Equal(t, "/v1/responses", endpoint)
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, f.key.ID).Scan(&dedup))
				require.Equal(t, 1, dedup)
			} else {
				require.NotEqual(t, 200, rec.Code, rec.Body.String())
				require.EqualValues(t, 2, f.upstream.calls.Load())
				require.Equal(t, "/v1/chat/completions", finalEndpoint)
				require.Equal(t, "/v1/chat/completions", opsEndpoint)
				f.pool.Stop()
				var logs int
				require.NoError(t, inflightTestDB(t).QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&logs))
				require.Zero(t, logs)
			}
			require.Positive(t, inflightHeld(t, f.user.ID), "unproved transport attempt keeps TTL even when a later account succeeds")
		})
	}
}

// Keep this fixture on existing public APIs so the identical test also runs on
// the pre-fix production commit as valid RED evidence.
type structuredHTTPDelayedReader struct {
	io.Reader
	delayed bool
}

func (r *structuredHTTPDelayedReader) Read(p []byte) (int, error) {
	if !r.delayed {
		r.delayed = true
		time.Sleep(120 * time.Millisecond)
	}
	return r.Reader.Read(p)
}
func TestStructuredInputFallbackHTTP_FirstLegLatency(t *testing.T) {
	for _, kind := range []string{"buffered", "stream", "partial"} {
		t.Run(kind, func(t *testing.T) {
			f := newInflightHTTPFixture(t, service.PlatformOpenAI, inflightChatJSON, "application/json")
			chatAudioWallet(t, f)
			stream := kind != "buffered"
			f.upstream.script = func(req *http.Request, _ int64) (*http.Response, error) {
				if f.upstream.calls.Load() == 1 {
					require.Equal(t, "/v1/responses", req.URL.Path)
					return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(&structuredHTTPDelayedReader{Reader: strings.NewReader(structuredHTTPRejection)})}, nil
				}
				require.EqualValues(t, 2, f.upstream.calls.Load())
				require.Equal(t, "/v1/chat/completions", req.URL.Path)
				contentType := "application/json"
				var reader io.Reader = strings.NewReader(inflightChatJSON)
				if stream {
					contentType = "text/event-stream"
					reader = strings.NewReader(structuredHTTPChatStream)
				}
				if kind == "partial" {
					reader = io.MultiReader(strings.NewReader(`data: {"id":"latency_partial","model":"gpt-5","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`+"\n\n"), inflightHTTPReadError{errors.New("metered read failure")})
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(reader)}, nil
			}
			close(f.upstream.release)
			rec := f.request(structuredHTTPBody(stream), "/v1/chat/completions", "", f.openAI.ChatCompletions)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			assertStructuredHTTPSettlement(t, f, false, 10)
			var duration int
			var ttft *int
			require.NoError(t, inflightTestDB(t).QueryRow(`SELECT duration_ms,first_token_ms FROM usage_logs WHERE user_id=$1`, f.user.ID).Scan(&duration, &ttft))
			require.GreaterOrEqual(t, duration, 120, "persisted duration must include Responses rejection read")
			if stream {
				require.NotNil(t, ttft)
				require.GreaterOrEqual(t, *ttft, 120, "persisted TTFT must include first leg")
			}
			if kind == "partial" {
				require.Contains(t, rec.Body.String(), "error")
				require.NotContains(t, rec.Body.String(), "data: [DONE]")
			}
		})
	}
}
