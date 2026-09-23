package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResolveOpenAIWSClientFirstMessageTimeout(t *testing.T) {
	defaultTimeout := time.Duration(config.DefaultOpenAIWSClientFirstMessageTimeoutSeconds) * time.Second
	require.Equal(t, defaultTimeout, ResolveOpenAIWSClientFirstMessageTimeout(nil))

	cfg := &config.Config{}
	require.Equal(t, defaultTimeout, ResolveOpenAIWSClientFirstMessageTimeout(cfg))

	cfg.Gateway.OpenAIWS.ClientFirstMessageTimeoutSeconds = 120
	require.Equal(t, 120*time.Second, ResolveOpenAIWSClientFirstMessageTimeout(cfg))
}

func TestPrepareOpenAIWSHTTPBridgeBodyStripsWSFields(t *testing.T) {
	body, err := prepareOpenAIWSHTTPBridgeBody([]byte(`{"type":"response.create","generate":true,"model":"gpt-5","stream":false,"previous_response_id":"resp_prev","input":"hi"}`))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "type").Exists())
	require.False(t, gjson.GetBytes(body, "generate").Exists())
	require.False(t, gjson.GetBytes(body, "previous_response_id").Exists())
	require.Equal(t, "gpt-5", gjson.GetBytes(body, "model").String())
	require.True(t, gjson.GetBytes(body, "stream").Bool())
	require.Equal(t, "hi", gjson.GetBytes(body, "input").String())
}

func TestOpenAIWSHTTPBridgeSyntheticFailuresContinueObservedSequence(t *testing.T) {
	sequence := openAIResponsesSequenceTracker{}
	sequence.Observe([]byte(`{"type":"response.output_text.delta","sequence_number":11,"delta":"partial"}`))

	errorEvent := buildOpenAIWSHTTPBridgeErrorEvent(http.StatusBadGateway, "upstream failed", sequence.Next())

	require.Equal(t, int64(12), gjson.GetBytes(errorEvent, "sequence_number").Int())
}

func TestOpenAIWSHTTPBridgeDecisionKeepsSmallFramesOnWS(t *testing.T) {
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				OpenAIWS: config.GatewayOpenAIWSConfig{
					HTTPBridgeEnabled:        true,
					HTTPBridgeThresholdBytes: 100,
				},
			},
		},
	}

	require.False(t, svc.shouldBridgeOpenAIWSHTTP(99, ""))
	require.True(t, svc.shouldBridgeOpenAIWSHTTP(100, ""))
	require.False(t, svc.shouldBridgeOpenAIWSHTTP(1000, "resp_existing"))

	svc.cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = false
	require.False(t, svc.shouldBridgeOpenAIWSHTTP(1000, ""))
}

func TestOpenAIWSHTTPBridgeHTTP429FailsOverBeforeWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetUpstream429TrackerForTest()

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error","message":"slow down"}}`)),
	}}
	account := &Account{
		ID:          73,
		Name:        "api-key",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Status:      StatusActive,
	}
	repo := &openAIWSRateLimitSignalRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{*account}}}
	svc := &OpenAIGatewayService{
		cfg:              &config.Config{},
		httpUpstream:     upstream,
		rateLimitService: &RateLimitService{accountRepo: repo},
		toolCorrector:    NewCodexToolCorrector(),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)

	var written [][]byte
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(),
		c,
		account,
		"sk-test",
		[]byte(`{"type":"response.create","model":"gpt-5","stream":true,"input":"hi"}`),
		64,
		"gpt-5",
		"",
		"",
		"",
		1, openAIWSHTTPBridgeToolState{},
		func(message []byte) error {
			written = append(written, append([]byte(nil), message...))
			return nil
		},
	)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Nil(t, result)
	require.Empty(t, written)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	attempts, hits := countUpstream429EventsForTest(account.ID)
	require.Equal(t, 1, attempts)
	require.Equal(t, 1, hits)
	require.Empty(t, repo.rateLimitCalls)
}

func TestOpenAIWSHTTPBridgeTransportErrorOnlyFailsOverBeforeDownstreamWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tt := range []struct {
		name         string
		turn         int
		wantFailover bool
		wantWrites   int
	}{
		{name: "first_turn", turn: 1, wantFailover: true},
		{name: "later_turn", turn: 2, wantWrites: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: &httpUpstreamRecorder{err: io.EOF}}
			account := &Account{ID: 80, Name: "api-key", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test",
				[]byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`),
				64, "gpt-5", "", "", "", tt.turn, openAIWSHTTPBridgeToolState{},
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			if tt.wantFailover {
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			} else {
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
			}
			require.Len(t, writes, tt.wantWrites)
		})
	}
}

func TestOpenAIWSHTTPBridgeHTTPErrorOnlyFailsOverBeforeDownstreamWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tt := range []struct {
		name         string
		turn         int
		status       int
		wantFailover bool
		wantWrites   int
	}{
		{name: "first_turn_unauthorized", turn: 1, status: http.StatusUnauthorized, wantFailover: true},
		{name: "first_turn_server_error", turn: 1, status: http.StatusInternalServerError, wantFailover: true},
		{name: "later_turn_server_error", turn: 2, status: http.StatusInternalServerError, wantWrites: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: tt.status,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"server_error","message":"temporary upstream failure"}}`)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 83, Name: "api-key", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test",
				[]byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`),
				64, "gpt-5", "", "", "", tt.turn, openAIWSHTTPBridgeToolState{},
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			var failoverErr *UpstreamFailoverError
			if tt.wantFailover {
				require.Nil(t, result)
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, tt.status, failoverErr.StatusCode)
			} else {
				require.Nil(t, result)
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
			}
			require.Len(t, writes, tt.wantWrites)
		})
	}
}

type failingOpenAIWSHTTPBridgeBody struct{}

func (failingOpenAIWSHTTPBridgeBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failingOpenAIWSHTTPBridgeBody) Close() error             { return nil }

func TestOpenAIWSHTTPBridgeStreamReadErrorOnlyFailsOverBeforeDownstreamWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, turn := range []int{1, 2} {
		t.Run(fmt.Sprintf("turn_%d", turn), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       failingOpenAIWSHTTPBridgeBody{},
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 84, Name: "api-key", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test",
				[]byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`),
				64, "gpt-5", "", "", "", turn, openAIWSHTTPBridgeToolState{},
				func([]byte) error { return nil },
			)

			var failoverErr *UpstreamFailoverError
			if turn == 1 {
				require.Nil(t, result)
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			} else {
				require.NotNil(t, result)
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
				require.Contains(t, err.Error(), "read upstream http bridge stream")
			}
		})
	}
}

func TestOpenAIWSHTTPBridgeSSEErrorOnlyFailsOverBeforeDownstreamWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, turn := range []int{1, 2} {
		t.Run(fmt.Sprintf("turn_%d", turn), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					"data: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"code\":\"rate_limit_exceeded\",\"message\":\"limited\"}}\n\n",
				)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 81, Name: "api-key", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test",
				[]byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`),
				64, "gpt-5", "", "", "", turn, openAIWSHTTPBridgeToolState{},
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			var failoverErr *UpstreamFailoverError
			if turn == 1 {
				require.Nil(t, result)
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
				require.Empty(t, writes)
			} else {
				require.NotNil(t, result)
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
				require.Len(t, writes, 1)
			}
		})
	}
}

func TestOpenAIWSHTTPBridgeDoneWithoutTerminalEventIsIncomplete(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tt := range []struct {
		name         string
		body         string
		wantFailover bool
		wantWrites   int
	}{
		{name: "done_without_events", body: "data: [DONE]\n\n", wantFailover: true},
		{
			// response.created 在首个语义输出前被暂存，未写出任何事件，仍可安全 failover。
			name: "created_then_done",
			body: "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_truncated\"}}\n\n" +
				"data: [DONE]\n\n",
			wantFailover: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 82, Name: "api-key", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test",
				[]byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`),
				64, "gpt-5", "", "", "", 1, openAIWSHTTPBridgeToolState{},
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			var failoverErr *UpstreamFailoverError
			if tt.wantFailover {
				require.Nil(t, result)
				require.ErrorAs(t, err, &failoverErr)
			} else {
				require.NotNil(t, result)
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
				require.Contains(t, err.Error(), "sent [DONE] before terminal event")
			}
			require.Len(t, writes, tt.wantWrites)
		})
	}
}

func TestOpenAIWSHTTPBridgeHTTP429WithResetReturnsFailoverBeforeWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetUpstream429TrackerForTest()

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"Retry-After":  []string{"30"},
		},
		Body: io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error","message":"slow down"}}`)),
	}}
	account := &Account{
		ID:          74,
		Name:        "api-key",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Status:      StatusActive,
	}
	repo := &openAIWSRateLimitSignalRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{*account}}}
	svc := &OpenAIGatewayService{
		cfg:              &config.Config{},
		httpUpstream:     upstream,
		rateLimitService: &RateLimitService{accountRepo: repo},
		toolCorrector:    NewCodexToolCorrector(),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)

	var written [][]byte
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(),
		c,
		account,
		"sk-test",
		[]byte(`{"type":"response.create","model":"gpt-5","stream":true,"input":"hi"}`),
		64,
		"gpt-5",
		"",
		"",
		"",
		1, openAIWSHTTPBridgeToolState{},
		func(message []byte) error {
			written = append(written, append([]byte(nil), message...))
			return nil
		},
	)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Nil(t, result)
	require.Empty(t, written)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.True(t, ShouldSwitchAccountOn429(account.ID))
	require.Len(t, repo.rateLimitCalls, 1)
}

func TestOpenAIWSPassthroughFirstMessageBridgeDecision(t *testing.T) {
	const threshold = 100
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		OpenAIWS: config.GatewayOpenAIWSConfig{
			HTTPBridgeEnabled:        true,
			HTTPBridgeThresholdBytes: threshold,
		},
	}}}
	exactThresholdPayload := `{"type":"response.create","input":"` +
		strings.Repeat("x", threshold-len(`{"type":"response.create","input":""}`)) + `"}`

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{name: "oversized without previous response id bridges", payload: `{"type":"response.create","input":"` + strings.Repeat("x", 100) + `"}`, want: true},
		{name: "omitted type bridges as response create", payload: `{"input":"` + strings.Repeat("x", 100) + `"}`, want: true},
		{name: "blank type bridges as response create", payload: `{"type":"   ","padding":"` + strings.Repeat("x", 100) + `"}`, want: true},
		{name: "exact threshold bridges", payload: exactThresholdPayload, want: true},
		{name: "small stays passthrough", payload: `{"type":"response.create","input":"x"}`},
		{name: "previous response id stays passthrough", payload: `{"type":"response.create","previous_response_id":"resp_previous","input":"` + strings.Repeat("x", 100) + `"}`},
		{name: "duplicate type stays passthrough", payload: `{"type":"response.create","type":"response.create","input":"` + strings.Repeat("x", 100) + `"}`},
		{name: "duplicate previous response id stays passthrough", payload: `{"type":"response.create","previous_response_id":null,"previous_response_id":null,"input":"` + strings.Repeat("x", 100) + `"}`},
		{name: "null type bridges as response create", payload: `{"type":null,"padding":"` + strings.Repeat("x", 100) + `"}`, want: true},
		{name: "non-string type stays passthrough", payload: `{"type":123,"padding":"` + strings.Repeat("x", 100) + `"}`},
		{name: "oversized response cancel stays passthrough", payload: `{"type":"response.cancel","padding":"` + strings.Repeat("x", 100) + `"}`},
		{name: "oversized other event stays passthrough", payload: `{"type":"session.update","padding":"` + strings.Repeat("x", 100) + `"}`},
		{name: "oversized malformed JSON stays passthrough", payload: `{"type":"response.create","padding":"` + strings.Repeat("x", 100)},
		{name: "top-level array stays passthrough", payload: `[{"type":"response.create","padding":"` + strings.Repeat("x", 100) + `"}]`},
		{name: "non-string previous response id stays passthrough", payload: `{"type":"response.create","previous_response_id":123,"padding":"` + strings.Repeat("x", 100) + `"}`},
		{name: "nested values and escapes are skipped", payload: `{"input":[{"type":"input_text","text":"say \"}]\" and {x}"}],"meta":{"a":[1,{"b":"]"}]},"n":1,"type":"response.create","padding":"` + strings.Repeat("x", 100) + `"}`, want: true},
		{name: "nested previous response id is ignored", payload: `{"metadata":{"previous_response_id":"resp_nested"},"type":"response.create","padding":"` + strings.Repeat("x", 100) + `"}`, want: true},
		{name: "escaped top-level previous response id key stays passthrough", payload: `{"type":"response.create","previous_response_\u0069d":"resp_escaped","padding":"` + strings.Repeat("x", 100) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, svc.shouldBridgeOpenAIWSPassthroughFirstMessage([]byte(tt.payload)))
		})
	}
}

func TestProxyOpenAIWSHTTPBridgeTurnTransportErrorFailoverSafety(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		turn         int
		wantFailover bool
		wantWrites   int
	}{
		{name: "first_turn_fails_over_before_downstream_event", turn: 1, wantFailover: true},
		{name: "later_turn_does_not_replay_completed_turns", turn: 2, wantWrites: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{err: io.EOF}
			svc := &OpenAIGatewayService{
				cfg:          &config.Config{},
				httpUpstream: upstream,
			}
			account := &Account{
				ID:          8,
				Name:        "api-key",
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Concurrency: 1,
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload),
				"gpt-5", "", "", "", tt.turn, openAIWSHTTPBridgeToolState{},
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			if tt.wantFailover {
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
				require.JSONEq(t, string(openAITransportFailoverBody), string(failoverErr.ResponseBody))
			} else {
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
			}
			require.Len(t, writes, tt.wantWrites)
			if tt.wantWrites > 0 {
				require.Equal(t, "error", gjson.GetBytes(writes[0], "type").String())
				require.Equal(t, int64(http.StatusBadGateway), gjson.GetBytes(writes[0], "status").Int())
			}
		})
	}
}

func TestProxyOpenAIWSHTTPBridgeTurnHTTPStatusFailoverSafety(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		turn         int
		status       int
		wantFailover bool
		wantWrites   int
	}{
		{name: "first_turn_401", turn: 1, status: http.StatusUnauthorized, wantFailover: true},
		{name: "first_turn_429", turn: 1, status: http.StatusTooManyRequests, wantFailover: true},
		{name: "first_turn_500", turn: 1, status: http.StatusInternalServerError, wantFailover: true},
		{name: "later_turn_500_does_not_replay", turn: 2, status: http.StatusInternalServerError, wantWrites: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: tt.status,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"server_error","message":"temporary upstream failure"}}`)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload),
				"gpt-5", "", "", "", tt.turn, openAIWSHTTPBridgeToolState{},
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			if tt.wantFailover {
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, tt.status, failoverErr.StatusCode)
			} else {
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
			}
			require.Len(t, writes, tt.wantWrites)
		})
	}
}

func TestProxyOpenAIWSHTTPBridgeTurnSSEErrorFailoverSafety(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, turn := range []int{1, 2} {
		t.Run(fmt.Sprintf("turn_%d", turn), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					"data: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"code\":\"rate_limit_exceeded\",\"message\":\"limited\"}}\n\n",
				)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload),
				"gpt-5", "", "", "", turn, openAIWSHTTPBridgeToolState{},
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			var failoverErr *UpstreamFailoverError
			if turn == 1 {
				require.Nil(t, result)
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
				require.Empty(t, writes)
			} else {
				require.NotNil(t, result)
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
				require.Len(t, writes, 1)
			}
		})
	}
}

// 桥接转发 error / response.failed 给 WS 客户端前必须把容量降载码改写为可重试
// 的 server_error：Codex 对 server_is_overloaded/slow_down 判致命并终止会话。
// 账号状态判定使用改写前的原始事件，不受影响。
func TestProxyOpenAIWSHTTPBridgeTurnRewritesCapacityShedCodeForClient(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name    string
		turn    int
		body    string
		wantErr bool
	}{
		{
			name:    "turn2_error_frame",
			turn:    2,
			body:    "data: {\"type\":\"error\",\"error\":{\"type\":\"service_unavailable_error\",\"code\":\"server_is_overloaded\",\"message\":\"Our servers are currently overloaded. Please try again later.\"}}\n\n",
			wantErr: true,
		},
		{
			// 后续 turn 不允许 replay，容量错误必须改写后交给客户端重试。
			name: "turn2_bare_response_failed",
			turn: 2,
			body: "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_shed\",\"status\":\"failed\",\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"Our servers are currently overloaded. Please try again later.\"}}}\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte

			_, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload),
				"gpt-5", "", "", "", tt.turn, openAIWSHTTPBridgeToolState{},
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, writes, 1)
			require.Contains(t, string(writes[0]), `"code":"server_error"`)
			require.NotContains(t, string(writes[0]), "server_is_overloaded")
			require.Contains(t, string(writes[0]), "Our servers are currently overloaded")
		})
	}
}

func proxyOpenAIWSHTTPBridgeTurnForCapacityTest(t *testing.T, account *Account, turn int, header http.Header, body string) (*OpenAIForwardResult, [][]byte, error) {
	t.Helper()
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
	var writes [][]byte
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload),
		"gpt-5", "", "", "", turn, openAIWSHTTPBridgeToolState{},
		func(message []byte) error {
			writes = append(writes, append([]byte(nil), message...))
			return nil
		},
	)
	return result, writes, err
}

func TestProxyOpenAIWSHTTPBridgeTurnStagesMetadataBeforeCapacityFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_shed"}}`,
		"",
		`data: {"type":"response.in_progress","response":{"id":"resp_shed"}}`,
		"",
		`data: {"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`,
		"",
		`data: {"type":"response.failed","response":{"id":"resp_shed","status":"failed","error":{"message":"Our servers are currently overloaded. Please try again later."}}}`,
		"",
	}, "\n")
	account := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}

	result, writes, err := proxyOpenAIWSHTTPBridgeTurnForCapacityTest(t, account, 1,
		http.Header{"X-Request-Id": []string{"rid-ws-bridge-capacity"}}, body)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.True(t, failoverErr.RequestScopedTransient)
	require.Empty(t, writes)
}

func TestProxyOpenAIWSHTTPBridgeTurnCapacityErrorFrameIsRequestScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_shed"}}`,
		"",
		`data: {"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`,
		"",
	}, "\n")
	account := &Account{ID: 14, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}

	result, writes, err := proxyOpenAIWSHTTPBridgeTurnForCapacityTest(t, account, 1, http.Header{}, body)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.True(t, failoverErr.RequestScopedTransient)
	require.Empty(t, writes)
}

func TestProxyOpenAIWSHTTPBridgeTurnDoesNotReplayCapacityAfterSemanticOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureStructuredLog(t)
	defer restore()
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_partial"}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		"",
		`data: {"type":"response.failed","response":{"id":"resp_partial","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`,
		"",
	}, "\n")
	account := &Account{ID: 13, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}

	result, writes, err := proxyOpenAIWSHTTPBridgeTurnForCapacityTest(t, account, 1,
		http.Header{"X-Request-Id": []string{"rid-ws-bridge-post-output"}}, body)

	require.NotNil(t, result)
	require.NoError(t, err)
	require.Len(t, writes, 3)
	require.Contains(t, string(writes[0]), "response.created")
	require.Contains(t, string(writes[1]), "partial")
	require.Contains(t, string(writes[2]), `"code":"server_error"`)
	require.NotContains(t, string(writes[2]), "server_is_overloaded")
	require.True(t, logSink.ContainsMessage("gateway.failover_suppressed_after_semantic_output"))
	require.True(t, logSink.ContainsFieldValue("path", "ws_http_bridge"))
	require.True(t, logSink.ContainsFieldValue("upstream_request_id", "rid-ws-bridge-post-output"))
}

// 非可 failover 的 response.failed（如 content policy）在首轮暂存期同样作为终止事件
// 提交：暂存的元数据按原顺序先写出，再写终止事件。
func TestProxyOpenAIWSHTTPBridgeTurnCommitsStagedMetadataWithTerminalEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_policy"}}`,
		"",
		`data: {"type":"keepalive","sequence_number":1}`,
		"",
		`data: {"type":"response.failed","response":{"id":"resp_policy","status":"failed","error":{"type":"invalid_request_error","code":"content_policy_violation","message":"blocked"}}}`,
		"",
	}, "\n")
	account := &Account{ID: 15, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}

	result, writes, err := proxyOpenAIWSHTTPBridgeTurnForCapacityTest(t, account, 1, http.Header{}, body)

	require.NotNil(t, result)
	require.NoError(t, err)
	require.Len(t, writes, 3)
	// 心跳不暂存（维持客户端连接），元数据随终止事件一起提交。
	require.Contains(t, string(writes[0]), "keepalive")
	require.Contains(t, string(writes[1]), "response.created")
	require.Contains(t, string(writes[2]), "content_policy_violation")
}

func TestProxyOpenAIWSHTTPBridgeTurnStagingLimitFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	padding := strings.Repeat("x", int(openAIFirstOutputStageMaxBytes))
	body := `data: {"type":"response.created","response":{"id":"resp_big","metadata":{"p":"` + padding + `"}}}` + "\n\n"
	account := &Account{ID: 16, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{MaxLineSize: 2 * int(openAIFirstOutputStageMaxBytes)}},
		httpUpstream: upstream,
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
	var writes [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload),
		"gpt-5", "", "", "", 1, openAIWSHTTPBridgeToolState{},
		func(message []byte) error {
			writes = append(writes, append([]byte(nil), message...))
			return nil
		},
	)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Empty(t, writes)
}

func TestProxyOpenAIWSHTTPBridgeTurnRequiresTerminalEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		body         string
		wantFailover bool
		wantWrites   int
	}{
		{name: "done_without_events_fails_over", body: "data: [DONE]\n\n", wantFailover: true},
		{
			// response.created 在首个语义输出前被暂存，未向客户端写出任何事件，仍可安全 failover。
			name: "created_then_done_fails_over_before_semantic_output",
			body: "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_truncated\"}}\n\n" +
				"data: [DONE]\n\n",
			wantFailover: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload),
				"gpt-5", "", "", "", 1, openAIWSHTTPBridgeToolState{},
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			var failoverErr *UpstreamFailoverError
			if tt.wantFailover {
				require.Nil(t, result)
				require.ErrorAs(t, err, &failoverErr)
			} else {
				require.NotNil(t, result)
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
			}
			require.Len(t, writes, tt.wantWrites)
		})
	}
}

type openAIWSHTTPBridgeReadError struct{}

func (openAIWSHTTPBridgeReadError) Read([]byte) (int, error) {
	return 0, errors.New("upstream stream read failed")
}

func TestProxyOpenAIWSHTTPBridgeTurnStreamReadErrorFailsOverBeforeWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(openAIWSHTTPBridgeReadError{}),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test",
		[]byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`),
		64, "gpt-5", "", "", "", 1, openAIWSHTTPBridgeToolState{},
		func([]byte) error { return nil },
	)

	var failoverErr *UpstreamFailoverError
	require.Nil(t, result)
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
}

// 传输层心跳（keepalive）不是语义输出：只转发过心跳时，随后的可重试错误帧
// 仍必须走首轮 pre-output failover。
func TestProxyOpenAIWSHTTPBridgeTurnKeepaliveDoesNotBlockPreOutputFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.Join([]string{
		`data: {"type":"keepalive"}`,
		"",
		`data: {"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"limited"}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 13, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
	var writes [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload),
		"gpt-5", "", "", "", 1, openAIWSHTTPBridgeToolState{},
		func(message []byte) error {
			writes = append(writes, append([]byte(nil), message...))
			return nil
		},
	)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Len(t, writes, 1)
	require.JSONEq(t, `{"type":"keepalive"}`, string(writes[0]))
}

func TestProxyOpenAIWSHTTPBridgeTurnKeepaliveWriteFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name      string
		writeErr  error
		wantDrain bool
	}{
		{name: "write error", writeErr: errors.New("downstream write failed")},
		{name: "client disconnect", writeErr: io.EOF, wantDrain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Join([]string{
				`data: {"type":"keepalive"}`,
				"",
				`data: {"type":"response.completed","response":{"id":"resp_disconnect","usage":{"input_tokens":3,"output_tokens":2}}}`,
				"",
			}, "\n")
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(body)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 14, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload),
				"gpt-5", "", "", "", 1, openAIWSHTTPBridgeToolState{},
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return tc.writeErr
				},
			)

			require.Len(t, writes, 1)
			require.JSONEq(t, `{"type":"keepalive"}`, string(writes[0]))
			if tc.wantDrain {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, 3, result.Usage.InputTokens)
				require.Equal(t, 2, result.Usage.OutputTokens)
			} else {
				require.Nil(t, result)
				require.ErrorIs(t, err, tc.writeErr)
				var turnErr *openAIWSIngressTurnError
				require.ErrorAs(t, err, &turnErr)
				require.Equal(t, "write_client", turnErr.stage)
				require.False(t, turnErr.wroteDownstream)
			}
		})
	}
}

// 客户端已断开时，本次尝试只需 drain 上游以记账，不能再触发首轮 failover
// （换号重放对已离开的客户端没有意义，只会白白消耗另一个账号）。
func TestProxyOpenAIWSHTTPBridgeTurnDisconnectedBeforeOutputDoesNotFailOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name         string
		tail         string
		readErr      bool
		wantTerminal bool
	}{
		{
			name:         "failed with usage",
			tail:         "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_disconnect\",\"status\":\"failed\",\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"overloaded\"},\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n",
			wantTerminal: true,
		},
		{
			name: "retryable error frame",
			tail: "data: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"code\":\"rate_limit_exceeded\",\"message\":\"limited\"}}\n\n",
		},
		{name: "upstream read failure", readErr: true},
		{name: "upstream EOF without terminal"},
		{name: "done without terminal", tail: "data: [DONE]\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preamble := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_disconnect\"}}\n\n" +
				"data: {\"type\":\"keepalive\"}\n\n"
			var reader io.Reader = strings.NewReader(preamble + tc.tail)
			if tc.readErr {
				reader = io.MultiReader(reader, openAIWSHTTPBridgeReadError{})
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(reader),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 15, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload),
				"gpt-5", "", "", "", 1, openAIWSHTTPBridgeToolState{},
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					// The client goes away on the very first frame, before any
					// semantic output was delivered.
					return io.EOF
				},
			)

			require.NotNil(t, result)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr))
			require.Len(t, writes, 1)
			// response.created 在首个语义输出前暂存；心跳不暂存，是第一个写给客户端的帧。
			require.Equal(t, "keepalive", gjson.GetBytes(writes[0], "type").String())
			require.Nil(t, result.FirstTokenMs)
			if tc.wantTerminal {
				require.NoError(t, err)
				require.Equal(t, 3, result.Usage.InputTokens)
				require.Equal(t, 2, result.Usage.OutputTokens)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestProxyOpenAIWSHTTPBridgeTurnFallsBackToStatusText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{}`)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 13, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)

	var writes [][]byte
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test",
		[]byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`),
		64, "gpt-5", "", "", "", 2, openAIWSHTTPBridgeToolState{},
		func(message []byte) error {
			writes = append(writes, append([]byte(nil), message...))
			return nil
		},
	)

	require.Nil(t, result)
	require.Error(t, err)
	require.Len(t, writes, 1)
	require.Equal(t, "Internal Server Error", gjson.GetBytes(writes[0], "error.message").String())
}

func TestProxyOpenAIWSHTTPBridgeTurnSSEServerErrorFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\"}}\n\n",
		)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 14, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test",
		[]byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`),
		64, "gpt-5", "", "", "", 1, openAIWSHTTPBridgeToolState{},
		func([]byte) error { return nil },
	)

	var failoverErr *UpstreamFailoverError
	require.Nil(t, result)
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
}

func TestOpenAIWSHTTPBridgeRelaysSSEFramesAsWebSocketMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_bridge","model":"gpt-5.6-sol"}}`,
		"",
		`data: {"type":"response.output_text.delta","response":{"id":"resp_bridge"},"delta":"ok"}`,
		"",
		`data: {"type":"response.done","response":{"id":"resp_bridge","model":"gpt-5.6-sol","output":[{"id":"ig_bridge_1","type":"image_generation_call","status":"in_progress","result":"final-image"}],"usage":{"input_tokens":3,"output_tokens":2}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"rid_bridge"},
		},
		Body: io.NopCloser(strings.NewReader(sseBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				MaxLineSize: defaultMaxLineSize,
				OpenAIWS: config.GatewayOpenAIWSConfig{
					HTTPBridgeEnabled:        true,
					HTTPBridgeThresholdBytes: 1,
				},
			},
		},
		httpUpstream:  upstream,
		toolCorrector: NewCodexToolCorrector(),
	}
	account := &Account{
		ID:       7,
		Name:     "api-key",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"sol": "gpt-5.6-sol"},
		},
		Concurrency: 1,
		Status:      StatusActive,
	}
	payload := []byte(`{"type":"response.create","generate":true,"model":"sol","stream":true,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},"reasoning":{"effort":"max"},"input":"hi","parallel_tool_calls":true}`)

	type bridgeResult struct {
		result *OpenAIForwardResult
		err    error
	}
	resultCh := make(chan bridgeResult, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			resultCh <- bridgeResult{err: err}
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		ginCtx.Request = req

		writeClient := func(message []byte) error {
			writeCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			return conn.Write(writeCtx, coderws.MessageText, message)
		}
		result, bridgeErr := svc.proxyOpenAIWSHTTPBridgeTurn(
			r.Context(),
			ginCtx,
			account,
			"sk-test",
			payload,
			len(payload),
			"sol",
			"",
			"",
			"",
			1, openAIWSHTTPBridgeToolState{},
			writeClient,
		)
		resultCh <- bridgeResult{result: result, err: bridgeErr}
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	readEvent := func() []byte {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		msgType, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return event
	}

	created := readEvent()
	delta := readEvent()
	completed := readEvent()

	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	require.Equal(t, "response.output_text.delta", gjson.GetBytes(delta, "type").String())
	require.Equal(t, "response.done", gjson.GetBytes(completed, "type").String())
	require.Equal(t, "completed", gjson.GetBytes(completed, "response.output.0.status").String())

	select {
	case bridge := <-resultCh:
		require.NoError(t, bridge.err)
		require.NotNil(t, bridge.result)
		require.Equal(t, "resp_bridge", bridge.result.RequestID)
		require.Equal(t, 3, bridge.result.Usage.InputTokens)
		require.Equal(t, 2, bridge.result.Usage.OutputTokens)
		require.True(t, bridge.result.OpenAIWSMode)
		require.NotNil(t, bridge.result.ReasoningEffort)
		require.Equal(t, "max", *bridge.result.ReasoningEffort)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for bridge result")
	}

	require.NotNil(t, upstream.lastReq)
	require.Equal(t, http.MethodPost, upstream.lastReq.Method)
	require.False(t, gjson.GetBytes(upstream.lastBody, "type").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "generate").Exists())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "true", upstream.lastReq.Header.Get(responsesLiteHeader))
	require.True(t, gjson.GetBytes(upstream.lastBody, "parallel_tool_calls").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "parallel_tool_calls").Bool())
}

func TestOpenAIWSHTTPBridgeBodyPreservesNormalizedResponsesLiteTools(t *testing.T) {
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.6-terra",
		"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},
		"input":"hello",
		"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}],
		"tool_choice":{"type":"namespace","name":"collaboration"}
	}`)

	normalized, changed, err := normalizeOpenAIResponsesLiteToolsPayload(payload)
	require.NoError(t, err)
	require.True(t, changed)

	body, err := prepareOpenAIWSHTTPBridgeBody(normalized)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "type").Exists())
	require.Equal(t, "collaboration", gjson.GetBytes(body, `input.#(type=="additional_tools").tools.0.name`).String())
	require.Equal(t, "namespace", gjson.GetBytes(body, "tool_choice.type").String())
}

func TestOpenAIWSHTTPBridgeAcceptsFirstFrameAboveLegacy16MiB(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_large_bridge","model":"gpt-5"}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_large_bridge","model":"gpt-5","usage":{"input_tokens":9,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"rid_large_bridge"},
		},
		Body: io.NopCloser(strings.NewReader(sseBody)),
	}}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
			OpenAIWS: config.GatewayOpenAIWSConfig{
				Enabled:                  true,
				APIKeyEnabled:            true,
				ResponsesWebsocketsV2:    true,
				ModeRouterV2Enabled:      true,
				IngressModeDefault:       OpenAIWSIngressModeCtxPool,
				ClientReadLimitBytes:     64 * 1024 * 1024,
				HTTPBridgeEnabled:        true,
				HTTPBridgeThresholdBytes: 17*1024*1024 + 512,
			},
		},
	}
	svc := &OpenAIGatewayService{
		cfg:           cfg,
		httpUpstream:  upstream,
		toolCorrector: NewCodexToolCorrector(),
	}
	account := &Account{
		ID:       9,
		Name:     "api-key",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":        "sk-upstream",
			"base_url":       "https://env-openai.example/v1",
			"model_provider": "env-openai",
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
			"openai_apikey_responses_websockets_v2_mode":    OpenAIWSIngressModePassthrough,
		},
		Concurrency: 1,
		Status:      StatusActive,
	}

	payload := []byte(strings.Repeat(" ", 1024) + `{"type":"response.create","generate":true,"model":"gpt-5","stream":true,"input":"` + strings.Repeat("x", 17*1024*1024) + `"}`)
	require.Greater(t, len(payload), 16*1024*1024)
	require.GreaterOrEqual(t, int64(len(payload)), cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes)
	require.Less(t, int64(len(payload)), ResolveOpenAIWSClientReadLimitBytes(cfg))

	type turnOutcome struct {
		turn   int
		result *OpenAIForwardResult
		err    error
	}
	turnOutcomeCh := make(chan turnOutcome, 1)
	hooks := &OpenAIWSIngressHooks{
		AfterTurn: func(turn int, result *OpenAIForwardResult, turnErr error) {
			turnOutcomeCh <- turnOutcome{turn: turn, result: result, err: turnErr}
		},
	}

	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		conn.SetReadLimit(ResolveOpenAIWSClientReadLimitBytes(cfg))

		readCtx, cancelRead := context.WithTimeout(r.Context(), 10*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			errCh <- err
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			errCh <- NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "unexpected client websocket message type", nil)
			return
		}

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "codex_cli_rs/0.135.0")
		ginCtx.Request = req

		proxyCtx, cancelProxy := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancelProxy()
		errCh <- svc.ProxyResponsesWebSocketFromClient(proxyCtx, ginCtx, conn, account, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 5*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 20*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, payload)
	cancelWrite()
	require.NoError(t, err)

	var eventTypes []string
	for {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 10*time.Second)
		msgType, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)

		eventType := gjson.GetBytes(event, "type").String()
		eventTypes = append(eventTypes, eventType)
		if eventType == "response.completed" {
			break
		}
	}
	require.Contains(t, eventTypes, "response.created")
	require.Contains(t, eventTypes, "response.completed")

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case proxyErr := <-errCh:
		require.NoError(t, proxyErr)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for websocket bridge proxy to finish")
	}

	select {
	case outcome := <-turnOutcomeCh:
		require.NoError(t, outcome.err)
		require.Equal(t, 1, outcome.turn)
		require.NotNil(t, outcome.result)
		require.Equal(t, 9, outcome.result.Usage.InputTokens)
		require.Equal(t, 1, outcome.result.Usage.OutputTokens)
	default:
		t.Fatal("AfterTurn was not called for websocket HTTP bridge turn")
	}

	require.NotNil(t, upstream.lastReq)
	require.Equal(t, http.MethodPost, upstream.lastReq.Method)
	require.Equal(t, "https://env-openai.example/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "env-openai", account.GetCredential("model_provider"))
	require.Less(t, int64(len(upstream.lastBody)), cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes)
	require.Greater(t, len(upstream.lastBody), 16*1024*1024)
	require.False(t, gjson.GetBytes(upstream.lastBody, "type").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "generate").Exists())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "gpt-5", gjson.GetBytes(upstream.lastBody, "model").String())
}

func TestOpenAIWSHTTPBridgeKeepsContinuationFramesOnHTTPWithoutPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	firstSSEBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_bridge_first","model":"gpt-5.1","output":[{"type":"function_call","id":"fc_bridge_1","call_id":"call_bridge_1","name":"shell","arguments":"{}"}],"usage":{"input_tokens":9,"output_tokens":1}}}`,
		"",
	}, "\n")
	secondSSEBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_bridge_second","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(firstSSEBody)),
		},
		{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(secondSSEBody)),
		},
	}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
	cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 1
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     upstream,
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := &Account{
		ID:          19,
		Name:        "api-key-bridge-handoff",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-upstream"},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
		Concurrency: 1,
		Status:      StatusActive,
		Schedulable: true,
	}

	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			errCh <- err
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			errCh <- NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "unexpected client websocket message type", nil)
			return
		}

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "codex_cli_rs/0.135.0")
		ginCtx.Request = req

		errCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeMessage := func(payload string) {
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelWrite()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelRead()
		msgType, event, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return event
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"first"}`)
	firstTurnEvent := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(firstTurnEvent, "type").String())
	require.Equal(t, "resp_bridge_first", gjson.GetBytes(firstTurnEvent, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_bridge_first","input":[{"type":"function_call_output","call_id":"call_bridge_1","output":"ok"}]}`)
	secondTurnEvent := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(secondTurnEvent, "type").String())
	require.Equal(t, "resp_bridge_second", gjson.GetBytes(secondTurnEvent, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case proxyErr := <-errCh:
		require.NoError(t, proxyErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket bridge proxy to finish")
	}

	require.Len(t, upstream.bodies, 2, "进入 HTTP bridge 后同一客户端 WS 连接内应保持 HTTP/SSE bridge")
	require.False(t, gjson.GetBytes(upstream.bodies[0], "previous_response_id").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "previous_response_id").Exists())
	secondInput := gjson.GetBytes(upstream.bodies[1], "input").Array()
	require.Len(t, secondInput, 3)
	require.Equal(t, "first", secondInput[0].String())
	require.Equal(t, "function_call", secondInput[1].Get("type").String())
	require.Equal(t, "call_bridge_1", secondInput[1].Get("call_id").String())
	require.Equal(t, "function_call_output", secondInput[2].Get("type").String())
	require.Equal(t, "call_bridge_1", secondInput[2].Get("call_id").String())
	require.Equal(t, 0, captureDialer.DialCount())
	require.Empty(t, captureConn.writes)
}

// 以下两个用例覆盖 issue #5850“duplicate HTTP bridge replay”修复：
// 客户端不传 previous_response_id、每轮改为重发完整历史 input 时，
// bridge 侧 needsBridgeReplay 不能再单凭“存在任意工具输出”触发无脑重放——
// 必须结合 AnalyzeToolCallOutputContextCoverageBytes 判断本轮 input 是否
// 已经自带完整上下文，否则同一个 call_id 会在每一轮里被重复回放进 replay input。
func TestOpenAIWSHTTPBridgeFullCustomToolHistoryWithoutPreviousResponseIDDoesNotReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)

	completed := func(responseID string, output string) string {
		return "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"" + responseID + "\",\"model\":\"gpt-5.4\",\"output\":" + output + ",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(completed("resp_1", `[{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"exec","input":"pwd"}]`)))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(completed("resp_2", `[]`)))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(completed("resp_3", `[]`)))},
	}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
	cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 1
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	svc := &OpenAIGatewayService{
		cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(),
	}
	account := &Account{
		ID: 9002, Name: "oauth-full-context", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "test-token"}, Extra: map[string]any{"responses_websockets_v2_enabled": true},
		Concurrency: 1, Status: StatusActive, Schedulable: true,
	}

	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		_, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			errCh <- err
			return
		}
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = r.Clone(r.Context())
		errCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "test-token", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeAndRead := func(payload string) {
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
		cancelWrite()
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		_, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
	}

	writeAndRead(`{"type":"response.create","model":"gpt-5.1","input":"run pwd"}`)
	fullContext := `{"type":"response.create","model":"gpt-5.1","input":[{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"exec","input":"pwd"},{"type":"custom_tool_call_output","call_id":"call_1","output":"/tmp"},{"role":"user","content":"continue"}]}`
	writeAndRead(fullContext)
	writeAndRead(fullContext)

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case proxyErr := <-errCh:
		require.NoError(t, proxyErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket bridge proxy to finish")
	}

	// 关键断言：只要客户端每轮都自带完整、已配对的工具调用上下文，
	// bridge 就不应该再往里面追加历史 replay input——否则 input 长度会逐轮增长。
	require.Len(t, upstream.bodies, 3)
	for _, body := range upstream.bodies[1:] {
		input := gjson.GetBytes(body, "input").Array()
		require.Len(t, input, 3)
		require.Equal(t, "custom_tool_call", input[0].Get("type").String())
		require.Equal(t, "call_1", input[0].Get("call_id").String())
		require.Equal(t, "custom_tool_call_output", input[1].Get("type").String())
		require.Equal(t, "call_1", input[1].Get("call_id").String())
	}
}

func TestOpenAIWSHTTPBridgeObjectToolOutputWithoutPreviousResponseIDReplaysMatchingCall(t *testing.T) {
	gin.SetMode(gin.TestMode)

	completed := func(responseID string, output string) string {
		return "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"" + responseID + "\",\"model\":\"gpt-5.4\",\"output\":" + output + ",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(completed("resp_1", `[{"type":"custom_tool_call","id":"item_1","call_id":"call_1","name":"exec","input":"pwd"}]`)))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(completed("resp_2", `[]`)))},
	}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
	cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 1
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	svc := &OpenAIGatewayService{
		cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(),
	}
	account := &Account{
		ID: 9003, Name: "oauth-output-only", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "test-token"}, Extra: map[string]any{"responses_websockets_v2_enabled": true},
		Concurrency: 1, Status: StatusActive, Schedulable: true,
	}

	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		_, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			errCh <- err
			return
		}
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = r.Clone(r.Context())
		errCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "test-token", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeAndRead := func(payload string) {
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
		cancelWrite()
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		_, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
	}

	writeAndRead(`{"type":"response.create","model":"gpt-5.1","input":"run pwd"}`)
	// input 是单个 object（不是数组），覆盖 AnalyzeToolCallOutputContextCoverageBytes
	// 对单 object input 的支持：这类工具输出仍然缺少上下文，必须触发 replay 补全。
	writeAndRead(`{"type":"response.create","model":"gpt-5.1","input":{"type":"custom_tool_call_output","call_id":"call_1","output":"/tmp"}}`)

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case proxyErr := <-errCh:
		require.NoError(t, proxyErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket bridge proxy to finish")
	}

	require.Len(t, upstream.bodies, 2)
	secondInput := gjson.GetBytes(upstream.bodies[1], "input").Array()
	require.Len(t, secondInput, 3)
	require.Equal(t, "custom_tool_call", secondInput[1].Get("type").String())
	require.Equal(t, "call_1", secondInput[1].Get("call_id").String())
	require.Equal(t, "custom_tool_call_output", secondInput[2].Get("type").String())
	require.Equal(t, "call_1", secondInput[2].Get("call_id").String())
}

// Codex 0.147+ 在 WS HTTP bridge 场景下同样会声明 custom 工具，type=apikey
// 的二级中转商上游只认标准 function 工具，出站前必须降级，回程流式还原。
func TestProxyOpenAIWSHTTPBridgeTurnAPIKeyAdaptsClientTools(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sse := strings.Join([]string{
		`data: {"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"item_exec","call_id":"call_exec","name":"exec","status":"in_progress"}}`,
		"",
		`data: {"type":"response.function_call_arguments.done","sequence_number":1,"item_id":"item_exec","output_index":0,"call_id":"call_exec","name":"exec","arguments":"{\"input\":\"pwd\"}"}`,
		"",
		`data: {"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"type":"function_call","id":"item_exec","call_id":"call_exec","name":"exec","arguments":"{\"input\":\"pwd\"}","status":"completed"}}`,
		"",
		`data: {"type":"response.completed","sequence_number":3,"response":{"id":"resp_bridge_tools","status":"completed","output":[{"type":"function_call","id":"item_exec","call_id":"call_exec","name":"exec","arguments":"{\"input\":\"pwd\"}","status":"completed"}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 5659, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	payload := []byte(`{"type":"response.create","model":"gpt-5","stream":true,"tools":[{"type":"custom","name":"exec","description":"Run a command"}],"input":"pwd"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)

	var written [][]byte
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload),
		"gpt-5", "", "", "", 1, openAIWSHTTPBridgeToolState{},
		func(message []byte) error {
			written = append(written, append([]byte(nil), message...))
			return nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())

	lines := make([]string, 0, len(written))
	for _, message := range written {
		lines = append(lines, string(message))
	}
	output := strings.Join(lines, "\n")
	require.Contains(t, output, `"type":"custom_tool_call"`)
	require.Contains(t, output, `"input":"pwd"`)
	require.NotContains(t, output, `"input":{`)
	require.True(t, result.wsReplayInputExists)
	require.Len(t, result.wsReplayInput, 1)
	require.Equal(t, "custom_tool_call", gjson.GetBytes(result.wsReplayInput[0], "type").String())
	require.Equal(t, "pwd", gjson.GetBytes(result.wsReplayInput[0], "input").String())
}

// OAuth 账号走官方上游，官方原生认得 custom 工具，不该被降级/还原。
func TestProxyOpenAIWSHTTPBridgeTurnOAuthLeavesClientToolsUntouched(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sse := strings.Join([]string{
		`data: {"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"type":"custom_tool_call","id":"item_exec","call_id":"call_exec","name":"exec","input":"pwd","status":"completed"}}`,
		"",
		`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_bridge_oauth","status":"completed","output":[{"type":"custom_tool_call","id":"item_exec","call_id":"call_exec","name":"exec","input":"pwd","status":"completed"}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 5661, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
	payload := []byte(`{"type":"response.create","model":"gpt-5","stream":true,"tools":[{"type":"custom","name":"exec","description":"Run a command"}],"input":"pwd"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)

	var written [][]byte
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload),
		"gpt-5", "", "", "", 1, openAIWSHTTPBridgeToolState{},
		func(message []byte) error {
			written = append(written, append([]byte(nil), message...))
			return nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "custom", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())

	lines := make([]string, 0, len(written))
	for _, message := range written {
		lines = append(lines, string(message))
	}
	output := strings.Join(lines, "\n")
	require.Contains(t, output, `"type":"custom_tool_call"`)
	require.NotContains(t, output, `"type":"function_call"`)
}

// 续接（follow-up）请求省略 tools 声明字段时，必须从上一轮记录的
// openAIWSHTTPBridgeToolState 反推出这一轮该用的映射，而不是把它当成
// "没有客户端工具" 处理 -- 否则未降级的 custom_tool_call 历史/回程还原会整体失效。
func TestProxyOpenAIWSHTTPBridgeTurnAPIKeyInheritsClientToolMappingWhenToolsOmitted(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sse := strings.Join([]string{
		`data: {"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"item_exec","call_id":"call_exec","name":"exec","status":"in_progress"}}`,
		"",
		`data: {"type":"response.function_call_arguments.done","sequence_number":1,"item_id":"item_exec","output_index":0,"call_id":"call_exec","name":"exec","arguments":"{\"input\":\"ls\"}"}`,
		"",
		`data: {"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"type":"function_call","id":"item_exec","call_id":"call_exec","name":"exec","arguments":"{\"input\":\"ls\"}","status":"completed"}}`,
		"",
		`data: {"type":"response.completed","sequence_number":3,"response":{"id":"resp_bridge_tools_turn2","status":"completed","output":[{"type":"function_call","id":"item_exec","call_id":"call_exec","name":"exec","arguments":"{\"input\":\"ls\"}","status":"completed"}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 5662, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	// Turn 2's payload relies on the WS session remembering what turn 1
	// declared: it omits "tools" entirely instead of repeating it.
	payload := []byte(`{"type":"response.create","model":"gpt-5","stream":true,"input":"list files"}`)
	previousToolState := openAIWSHTTPBridgeToolState{
		ClientMapping: apicompat.ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}},
		LoweredTools: []any{
			map[string]any{"type": "function", "name": "exec", "description": "Run a command", "parameters": map[string]any{"type": "object"}},
		},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)

	var written [][]byte
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload),
		"gpt-5", "", "", "", 2, previousToolState,
		func(message []byte) error {
			written = append(written, append([]byte(nil), message...))
			return nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)

	// The lowered declaration from the previous turn must be reinstated
	// upstream even though this turn's payload never declared it.
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "exec", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())

	lines := make([]string, 0, len(written))
	for _, message := range written {
		lines = append(lines, string(message))
	}
	output := strings.Join(lines, "\n")
	require.Contains(t, output, `"type":"custom_tool_call"`)
	require.NotContains(t, output, `"type":"function_call"`)

	// The mapping keeps holding for a possible turn 3 that also omits "tools".
	require.True(t, result.wsClientToolState.ClientMapping.CustomTools["exec"])
	require.Len(t, result.wsClientToolState.LoweredTools, 1)
}

// 没有上一轮会话状态（例如第一轮请求就省略了 tools，或上一轮从未声明过客户端工具）
// 时，省略 tools 字段必须继续按“没有客户端工具”处理，原样透传给上游。
func TestProxyOpenAIWSHTTPBridgeTurnAPIKeyNoPreviousStateTreatsOmittedToolsAsNoClientTools(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sse := strings.Join([]string{
		`data: {"type":"response.completed","sequence_number":0,"response":{"id":"resp_bridge_no_state","status":"completed","output":[{"type":"function_call","id":"item_plain","call_id":"call_plain","name":"plain_tool","arguments":"{}","status":"completed"}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 5663, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	payload := []byte(`{"type":"response.create","model":"gpt-5","stream":true,"input":"list files"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)

	var written [][]byte
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload),
		"gpt-5", "", "", "", 1, openAIWSHTTPBridgeToolState{},
		func(message []byte) error {
			written = append(written, append([]byte(nil), message...))
			return nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, gjson.GetBytes(upstream.lastBody, "tools").Exists())

	lines := make([]string, 0, len(written))
	for _, message := range written {
		lines = append(lines, string(message))
	}
	output := strings.Join(lines, "\n")
	require.Contains(t, output, `"type":"function_call"`)
	require.NotContains(t, output, `"type":"custom_tool_call"`)

	require.False(t, hasResponsesClientToolMapping(result.wsClientToolState.ClientMapping))
	require.Nil(t, result.wsClientToolState.LoweredTools)
}

func TestOpenAIWSHTTPBridgeRejectsInvalidResponsesLitePayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 8, Name: "api-key", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Status: StatusActive}
	payload := []byte(`{"type":"response.create","model":"gpt-5","stream":true,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},"input":"hi","parallel_tool_calls":"no"}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload),
		"gpt-5", "", "", "", 1, openAIWSHTTPBridgeToolState{},
		func([]byte) error { return nil },
	)

	require.ErrorContains(t, err, "normalize responses Lite payload")
	require.ErrorContains(t, err, "parallel_tool_calls to be a boolean")
	require.Nil(t, result)
	require.Nil(t, upstream.lastReq)
}
