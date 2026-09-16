//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 心跳写入失败（客户端在等待上游首事件期间断开）：三条流式路径都必须把 clientDisconnected
// 置位、不再向客户端写任何字节，但继续读完上游以拿到 usage 计费；心跳字节计数保持为 0
//（没有真正写出去的心跳不能扣减 Size）。
//
// keepaliveFailWriter 让所有 body 写入都失败（模拟 broken pipe），并在首次写入尝试时
// 放行门控的上游 body，不依赖固定 sleep。

type keepaliveFailWriter struct {
	gin.ResponseWriter
	attempted chan struct{}
	once      sync.Once
	mu        sync.Mutex
	writes    int
}

func newKeepaliveFailWriter(c *gin.Context) *keepaliveFailWriter {
	w := &keepaliveFailWriter{ResponseWriter: c.Writer, attempted: make(chan struct{})}
	c.Writer = w
	return w
}

func (w *keepaliveFailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	w.once.Do(func() { close(w.attempted) })
	return 0, errors.New("write tcp: broken pipe")
}

func (w *keepaliveFailWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *keepaliveFailWriter) writeAttempts() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}

func keepaliveGatedUpstreamBody(gate <-chan struct{}, body string) *keepaliveGatedBody {
	return &keepaliveGatedBody{gate: gate, reader: strings.NewReader(body)}
}

func requireKeepaliveWriteFailureDrainedUpstream(t *testing.T, c *gin.Context, w *keepaliveFailWriter, gotBody string, result *OpenAIForwardResult, err error, wantInput, wantOutput int) {
	t.Helper()
	require.NoError(t, err, "客户端断开不是上游错误：继续读完上游后正常返回")
	require.NotNil(t, result)
	require.Equal(t, wantInput, result.Usage.InputTokens, "断开后仍要读完上游拿到 usage 计费")
	require.Equal(t, wantOutput, result.Usage.OutputTokens)
	require.Empty(t, gotBody, "写入失败后不能再有任何字节到达客户端")
	require.GreaterOrEqual(t, w.writeAttempts(), 1, "至少尝试写过一次心跳")
	require.Zero(t, openAIStreamKeepaliveBytesWritten(c), "没有真正写出的心跳不计入心跳字节")
	require.Nil(t, GetOpsUpstreamModelMismatch(c), "模型一致：不打标")
}

// Chat 入站（Responses SSE 上游）：注释行心跳写失败 → 断开、继续读完上游。
func TestOpenAIStreamKeepalive_WriteFailureMarksClientDisconnected_Chat(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", nil)
	w := newKeepaliveFailWriter(c)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_chat_keepalive_fail", "")
	resp.Body = keepaliveGatedUpstreamBody(w.attempted, upstreamModelMismatchResponsesSSE(upstreamModelMismatchSentModel))
	svc := &OpenAIGatewayService{cfg: upstreamModelMismatchKeepaliveConfig()}

	result, err := svc.handleChatStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now(), 0)

	requireKeepaliveWriteFailureDrainedUpstream(t, c, w, rec.Body.String(), result, err, 5, 1)
}

// Anthropic 入站：ping 事件写失败 → 断开、继续读完上游。
func TestOpenAIStreamKeepalive_WriteFailureMarksClientDisconnected_Messages(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", nil)
	w := newKeepaliveFailWriter(c)
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_msg_keepalive_fail", "")
	resp.Body = keepaliveGatedUpstreamBody(w.attempted, upstreamModelMismatchResponsesSSE(upstreamModelMismatchSentModel))
	svc := &OpenAIGatewayService{cfg: upstreamModelMismatchKeepaliveConfig()}

	result, err := svc.handleAnthropicStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now())

	requireKeepaliveWriteFailureDrainedUpstream(t, c, w, rec.Body.String(), result, err, 5, 1)
}

// Responses 主路径（首输出守卫模式，心跳直写 w）：写失败 → 断开、staging 里的事件不再提交、继续读完上游。
func TestOpenAIStreamKeepalive_WriteFailureMarksClientDisconnected_ResponsesFirstOutputGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, rec := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	w := newKeepaliveFailWriter(c)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       keepaliveGatedUpstreamBody(w.attempted, upstreamModelMismatchSSEBody(upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, "hidden")),
	}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	svc.cfg.Gateway.StreamKeepaliveInterval = 1
	svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 30

	result, err := svc.Forward(context.Background(), c, newOpenAIImageGenerationControlTestAccount(), []byte(upstreamModelMismatchTestRequestBody))

	requireKeepaliveWriteFailureDrainedUpstream(t, c, w, rec.Body.String(), result, err, 596, 5)
	require.NotContains(t, rec.Body.String(), "hidden")
}
