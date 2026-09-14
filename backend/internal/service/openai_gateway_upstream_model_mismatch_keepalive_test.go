//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 上游模型不一致拦截 × SSE 心跳：等待上游首事件期间网关已按 stream_keepalive_interval
// 写过心跳（Responses / Chat 是注释行 ":\n\n"，Anthropic 入站是 ping 事件），随后上游才给
// 带错误 model 的首事件。心跳不算内容交付，必须仍能拦截并以 SafeToFailoverAfterWrite 切号；
// 客户端只收到心跳字节，不能收到任何业务事件。
//
// 上游 body 用 keepaliveGatedBody 门控：直到网关第一次 Flush（即心跳真正写出）才放出数据，
// 不依赖固定 sleep。stream_keepalive_interval 只能配整秒，且首个 tick 距 lastDownstreamWriteAt
// 略小于 1s 会被跳过，所以每个用例约等 2s。

const (
	upstreamModelMismatchKeepaliveComment = ":\n\n"
	upstreamModelMismatchKeepalivePing    = "event: ping\ndata: {\"type\":\"ping\"}\n\n"
)

// keepaliveGatedBody 在首次 Read 时阻塞，直到 gate 关闭（网关首次 Flush）或超时兜底。
type keepaliveGatedBody struct {
	gate   <-chan struct{}
	reader io.Reader
	opened bool
}

func (b *keepaliveGatedBody) Read(p []byte) (int, error) {
	if !b.opened {
		select {
		case <-b.gate:
		case <-time.After(10 * time.Second):
		}
		b.opened = true
	}
	return b.reader.Read(p)
}

func (b *keepaliveGatedBody) Close() error { return nil }

// installKeepaliveGate 把 c.Writer 换成首次 Flush 即发信号的包装，并返回门控后的上游 body。
func installKeepaliveGate(c *gin.Context, body string) io.ReadCloser {
	flushed := make(chan struct{})
	c.Writer = &compactKeepaliveSignalWriter{ResponseWriter: c.Writer, flushed: flushed}
	return &keepaliveGatedBody{gate: flushed, reader: strings.NewReader(body)}
}

func requireUpstreamModelMismatchKeepaliveFailover(t *testing.T, c *gin.Context, err error, gotBody, wantKeepalive string) {
	t.Helper()
	failoverErr := requireUpstreamModelMismatchFailover(t, err)
	require.True(t, failoverErr.SafeToFailoverAfterWrite, "只写过心跳的拦截必须允许 handler 在已写后切号")
	require.True(t, c.Writer.Written(), "心跳已提交响应头")
	require.Equal(t, wantKeepalive, gotBody, "客户端只能收到心跳字节，不能有任何业务事件")
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c), "扣除心跳后应视为未写")

	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.True(t, mark.Blocked, "心跳后仍应拦截而不是只打标")
	require.Equal(t, upstreamModelMismatchSentModel, mark.SentModel)
	require.Equal(t, upstreamModelMismatchGotModel, mark.ResponseModel)
	require.True(t, mark.Stream)
}

func newUpstreamModelMismatchKeepaliveResponsesTest(t *testing.T, firstOutputGuard bool) (*OpenAIGatewayService, *gin.Context, func() string, *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	body := installKeepaliveGate(c, upstreamModelMismatchSSEBody(upstreamModelMismatchGotModel, upstreamModelMismatchGotModel, "leak"))
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
	}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	svc.cfg.Gateway.StreamKeepaliveInterval = 1
	if firstOutputGuard {
		svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 30
	}
	return svc, c, func() string { return recorder.Body.String() }, newOpenAIImageGenerationControlTestAccount()
}

// Responses 主路径（普通模式，心跳经 bufferedWriter flush）：心跳后 response.created 带错模型 → 仍拦截
func TestUpstreamModelMismatch_KeepaliveBeforeFirstEventStillBlocks_Responses(t *testing.T) {
	svc, c, body, account := newUpstreamModelMismatchKeepaliveResponsesTest(t, false)

	_, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestRequestBody))

	requireUpstreamModelMismatchKeepaliveFailover(t, c, err, body(), upstreamModelMismatchKeepaliveComment)
}

// Responses 主路径（首输出守卫模式，心跳直写 w）：同样拦截，staging 里的事件零泄漏
func TestUpstreamModelMismatch_KeepaliveBeforeFirstEventStillBlocks_ResponsesFirstOutputGuard(t *testing.T) {
	svc, c, body, account := newUpstreamModelMismatchKeepaliveResponsesTest(t, true)

	_, err := svc.Forward(context.Background(), c, account, []byte(upstreamModelMismatchTestRequestBody))

	requireUpstreamModelMismatchKeepaliveFailover(t, c, err, body(), upstreamModelMismatchKeepaliveComment)
}

func upstreamModelMismatchKeepaliveConfig() *config.Config {
	cfg := rawChatCompletionsTestConfig()
	cfg.Gateway.StreamKeepaliveInterval = 1
	return cfg
}

// Anthropic 入站：心跳是 ping 事件，不置 clientOutputStarted → 仍拦截
func TestUpstreamModelMismatch_KeepaliveBeforeFirstEventStillBlocks_Messages(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", nil)
	body := installKeepaliveGate(c, upstreamModelMismatchResponsesSSE(upstreamModelMismatchGotModel))
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_msg_keepalive", "")
	resp.Body = body
	svc := &OpenAIGatewayService{cfg: upstreamModelMismatchKeepaliveConfig()}

	_, err := svc.handleAnthropicStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now())

	requireUpstreamModelMismatchKeepaliveFailover(t, c, err, rec.Body.String(), upstreamModelMismatchKeepalivePing)
	require.NotContains(t, rec.Body.String(), "message_start")
}

// Chat 入站：心跳注释行后 response.created 带错模型 → 仍拦截
func TestUpstreamModelMismatch_KeepaliveBeforeFirstEventStillBlocks_Chat(t *testing.T) {
	c, rec := newUpstreamModelMismatchPathContext(t, "/v1/chat/completions", nil)
	body := installKeepaliveGate(c, upstreamModelMismatchResponsesSSE(upstreamModelMismatchGotModel))
	resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_chat_keepalive", "")
	resp.Body = body
	svc := &OpenAIGatewayService{cfg: upstreamModelMismatchKeepaliveConfig()}

	result, err := svc.handleChatStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
		upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, upstreamModelMismatchSentModel, time.Now(), 0)

	require.NotNil(t, result, "已写过心跳时 finalizeStream 返回带 usage 的结果")
	requireUpstreamModelMismatchKeepaliveFailover(t, c, err, rec.Body.String(), upstreamModelMismatchKeepaliveComment)
	require.NotContains(t, rec.Body.String(), "chat.completion.chunk")
}

// 没写过心跳时 SafeToFailoverAfterWrite 保持 false（普通切号语义，不占首输出超时的单次切号额度）
func TestUpstreamModelMismatch_NoKeepaliveKeepsSafeToFailoverAfterWriteFalse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newUpstreamModelMismatchSSEUpstream(upstreamModelMismatchSSEBody("gpt-6-sol", "gpt-6-sol", "leak"))
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")

	_, err := svc.Forward(context.Background(), c, newOpenAIImageGenerationControlTestAccount(), []byte(upstreamModelMismatchTestRequestBody))

	failoverErr := requireUpstreamModelMismatchFailover(t, err)
	require.False(t, failoverErr.SafeToFailoverAfterWrite)
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())
}
