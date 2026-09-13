//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newUpstreamModelMismatchWSConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	return cfg
}

// 上游先推 response.created（带 model），随后 delta 与 completed：
// 命中时必须在 created 处就中断，后续事件一个都不能到达下游。
func upstreamModelMismatchWSEvents(responseModel string) [][]byte {
	return [][]byte{
		[]byte(`{"type":"response.created","response":{"id":"resp_model_check_1","model":"` + responseModel + `","status":"in_progress"}}`),
		[]byte(`{"type":"response.output_text.delta","delta":"hello","sequence_number":1}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_model_check_1","model":"` + responseModel + `","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
}

// 上游没有 response.created，model 首次出现在 completed 事件里（同时带 usage）。
func upstreamModelMismatchWSCompletedOnlyEvents(responseModel string) [][]byte {
	return [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_model_check_1","model":"` + responseModel + `","usage":{"input_tokens":7,"output_tokens":3}}}`),
	}
}

// forwardOpenAIWSV2（HTTP 入站 → WS 上游）
func TestUpstreamModelMismatch_WSForwardV2(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name          string
		stream        bool
		responseModel string
		completedOnly bool
		wantBlock     bool
	}{
		{name: "stream_mismatch_blocks", stream: true, responseModel: "gpt-4o-mini", wantBlock: true},
		{name: "nonstream_mismatch_blocks", stream: false, responseModel: "gpt-4o-mini", wantBlock: true},
		{name: "stream_match_passes", stream: true, responseModel: "gpt-5.1", wantBlock: false},
		{name: "completed_only_mismatch_carries_usage", stream: false, responseModel: "gpt-4o-mini", completedOnly: true, wantBlock: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newUpstreamModelMismatchWSConfig()
			events := upstreamModelMismatchWSEvents(tc.responseModel)
			if tc.completedOnly {
				events = upstreamModelMismatchWSCompletedOnlyEvents(tc.responseModel)
			}
			captureConn := &openAIWSCaptureConn{events: events}
			pool := newOpenAIWSConnPool(cfg)
			pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: captureConn})
			defer pool.Close()

			svc := &OpenAIGatewayService{
				cfg:              cfg,
				httpUpstream:     &httpUpstreamRecorder{},
				cache:            &stubGatewayCache{},
				openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
				toolCorrector:    NewCodexToolCorrector(),
				openaiWSPool:     pool,
			}
			account := &Account{
				ID: 1601, Name: "openai-ws-model-check", Platform: PlatformOpenAI,
				Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.example.test"},
				Extra:       map[string]any{"responses_websockets_v2_enabled": true},
			}

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)

			streamField := "false"
			if tc.stream {
				streamField = "true"
			}
			result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":`+streamField+`,"input":"hello"}`))

			if !tc.wantBlock {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, "resp_model_check_1", result.RequestID)
				require.Contains(t, rec.Body.String(), `"delta":"hello"`)
				require.Nil(t, GetOpsUpstreamModelMismatch(c))
				return
			}

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			require.Contains(t, string(failoverErr.ResponseBody), "upstream_model_mismatch")
			require.Nil(t, result)
			require.Empty(t, rec.Body.String(), "拦截后下游必须零字节")
			require.False(t, rec.Flushed)
			require.True(t, captureConn.closed, "命中后 lease 必须 MarkBroken，不得回池")
			mark := GetOpsUpstreamModelMismatch(c)
			require.NotNil(t, mark)
			require.Equal(t, "gpt-5.1", mark.SentModel)
			require.Equal(t, "gpt-4o-mini", mark.ResponseModel)
			require.Equal(t, int64(1601), mark.AccountID)
			require.Equal(t, tc.stream, mark.Stream)
			if tc.completedOnly {
				require.Equal(t, 7, mark.Usage.InputTokens, "model 首次出现在 completed 事件时审计 usage 应取该事件的值")
				require.Equal(t, 3, mark.Usage.OutputTokens)
			} else {
				require.Equal(t, OpenAIUsage{}, mark.Usage)
			}
		})
	}
}

// ProxyResponsesWebSocketFromClient（WS 入站 → WS 上游）turn 循环
func TestUpstreamModelMismatch_WSIngressProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name          string
		responseModel string
		completedOnly bool
		// turn2Mismatch：首轮模型一致，第二轮上游换模型——只打标不拦截，事件照常下发。
		turn2Mismatch bool
		wantBlock     bool
	}{
		{name: "mismatch_blocks", responseModel: "gpt-4o-mini", wantBlock: true},
		{name: "match_passes", responseModel: "gpt-5.1", wantBlock: false},
		{name: "completed_only_mismatch_carries_usage", responseModel: "gpt-4o-mini", completedOnly: true, wantBlock: true},
		{name: "turn2_mismatch_only_marks", responseModel: "gpt-5.1", turn2Mismatch: true, wantBlock: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newUpstreamModelMismatchWSConfig()
			events := upstreamModelMismatchWSEvents(tc.responseModel)
			if tc.completedOnly {
				events = upstreamModelMismatchWSCompletedOnlyEvents(tc.responseModel)
			}
			if tc.turn2Mismatch {
				events = append(events, upstreamModelMismatchWSEvents("gpt-4o-mini")...)
			}
			captureConn := &openAIWSCaptureConn{events: events}
			captureDialer := &openAIWSCaptureDialer{conn: captureConn}
			pool := newOpenAIWSConnPool(cfg)
			pool.setClientDialerForTest(captureDialer)
			defer pool.Close()

			svc := &OpenAIGatewayService{
				cfg:              cfg,
				httpUpstream:     &httpUpstreamRecorder{},
				cache:            &stubGatewayCache{},
				openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
				toolCorrector:    NewCodexToolCorrector(),
				openaiWSPool:     pool,
			}
			account := &Account{
				ID: 1602, Name: "openai-ingress-model-check", Platform: PlatformOpenAI,
				Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"api_key": "sk-test"},
				Extra:       map[string]any{"responses_websockets_v2_enabled": true},
			}

			serverErrCh := make(chan error, 1)
			markCh := make(chan *UpstreamModelMismatchMark, 1)
			wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					serverErrCh <- err
					return
				}
				defer func() { _ = conn.CloseNow() }()

				rec := httptest.NewRecorder()
				ginCtx, _ := gin.CreateTestContext(rec)
				req := r.Clone(r.Context())
				req.Header = req.Header.Clone()
				req.Header.Set("User-Agent", "unit-test-agent/1.0")
				ginCtx.Request = req

				readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
				_, firstMessage, readErr := conn.Read(readCtx)
				cancel()
				if readErr != nil {
					serverErrCh <- readErr
					return
				}
				proxyErr := svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
				markCh <- GetOpsUpstreamModelMismatch(ginCtx)
				serverErrCh <- proxyErr
			}))
			defer wsServer.Close()

			dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
			clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
			cancelDial()
			require.NoError(t, err)
			defer func() { _ = clientConn.CloseNow() }()

			writeMessage := func(payload string) {
				writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
			}
			writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"hello"}`)

			readMessage := func() ([]byte, error) {
				readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_, message, readErr := clientConn.Read(readCtx)
				return message, readErr
			}
			readTurn := func(wantModel string) {
				created, readErr := readMessage()
				require.NoError(t, readErr)
				require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
				require.Equal(t, wantModel, gjson.GetBytes(created, "response.model").String())
				delta, readErr := readMessage()
				require.NoError(t, readErr)
				require.Equal(t, "hello", gjson.GetBytes(delta, "delta").String())
				completed, readErr := readMessage()
				require.NoError(t, readErr)
				require.Equal(t, "response.completed", gjson.GetBytes(completed, "type").String())
			}

			if !tc.wantBlock {
				readTurn("gpt-5.1")
				if tc.turn2Mismatch {
					writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":true,"previous_response_id":"resp_model_check_1","input":"again"}`)
					readTurn("gpt-4o-mini")
				}
				_ = clientConn.Close(coderws.StatusNormalClosure, "done")
				select {
				case serverErr := <-serverErrCh:
					require.NoError(t, serverErr)
				case <-time.After(5 * time.Second):
					t.Fatal("等待 ingress websocket 结束超时")
				}
				mark := <-markCh
				if !tc.turn2Mismatch {
					require.Nil(t, mark)
					return
				}
				require.NotNil(t, mark, "turn>=2 命中应只打标")
				require.Equal(t, "gpt-5.1", mark.SentModel)
				require.Equal(t, "gpt-4o-mini", mark.ResponseModel)
				require.True(t, mark.Stream)
				return
			}

			var serverErr error
			select {
			case serverErr = <-serverErrCh:
			case <-time.After(5 * time.Second):
				t.Fatal("等待 ingress websocket 结束超时")
			}
			require.Error(t, serverErr)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, serverErr, &failoverErr)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			require.Contains(t, string(failoverErr.ResponseBody), "upstream_model_mismatch")

			message, readErr := readMessage()
			require.Error(t, readErr, "拦截后客户端不应收到任何事件，只应看到连接关闭: got=%s", string(message))
			require.Empty(t, message)
			require.True(t, captureConn.closed, "命中后上游 lease 必须 MarkBroken")
			mark := <-markCh
			require.NotNil(t, mark)
			require.Equal(t, "gpt-5.1", mark.SentModel)
			require.Equal(t, "gpt-4o-mini", mark.ResponseModel)
			require.Equal(t, int64(1602), mark.AccountID)
			require.True(t, mark.Stream)
			if tc.completedOnly {
				require.Equal(t, 7, mark.Usage.InputTokens, "model 首次出现在 completed 事件时审计 usage 应取该事件的值")
				require.Equal(t, 3, mark.Usage.OutputTokens)
			} else {
				require.Equal(t, OpenAIUsage{}, mark.Usage)
			}
		})
	}
}

// proxyOpenAIWSHTTPBridgeTurn（WS 入站 → HTTP 上游 SSE）
func TestUpstreamModelMismatch_WSHTTPBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name          string
		turn          int
		responseModel string
		wantBlock     bool
		wantMark      bool
	}{
		{name: "mismatch_blocks", turn: 1, responseModel: "gpt-4o-mini", wantBlock: true, wantMark: true},
		{name: "match_passes", turn: 1, responseModel: "gpt-5.1", wantBlock: false},
		// turn>=2 命中只打标：handler 换号会用首包重放第 1 轮，后续轮不能触发 failover。
		{name: "turn2_mismatch_only_marks", turn: 2, responseModel: "gpt-4o-mini", wantBlock: false, wantMark: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_bridge_model_1\",\"model\":\"" + tc.responseModel + "\",\"status\":\"in_progress\"}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\",\"sequence_number\":1}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_bridge_model_1\",\"model\":\"" + tc.responseModel + "\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"X-Request-Id": []string{"req_bridge_1"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, toolCorrector: NewCodexToolCorrector()}
			account := &Account{ID: 1603, Name: "bridge-model-check", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"hi"}`)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload),
				"gpt-5.1", "", "", "", tc.turn,
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			if !tc.wantBlock {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, "resp_bridge_model_1", result.RequestID)
				require.Len(t, writes, 3)
				require.Equal(t, "hello", gjson.GetBytes(writes[1], "delta").String())
				mark := GetOpsUpstreamModelMismatch(c)
				if !tc.wantMark {
					require.Nil(t, mark)
					return
				}
				require.NotNil(t, mark, "turn>=2 命中应只打标")
				require.Equal(t, "gpt-5.1", mark.SentModel)
				require.Equal(t, "gpt-4o-mini", mark.ResponseModel)
				require.True(t, mark.Stream)
				return
			}

			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			require.Contains(t, string(failoverErr.ResponseBody), "upstream_model_mismatch")
			require.Empty(t, writes, "拦截后客户端 WS 不应收到任何事件")
			mark := GetOpsUpstreamModelMismatch(c)
			require.NotNil(t, mark)
			require.Equal(t, "gpt-5.1", mark.SentModel)
			require.Equal(t, "gpt-4o-mini", mark.ResponseModel)
			require.Equal(t, int64(1603), mark.AccountID)
			require.True(t, mark.Stream)
		})
	}
}
