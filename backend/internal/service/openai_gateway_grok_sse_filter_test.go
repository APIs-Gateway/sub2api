package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func filterGrokPingTestInput(t *testing.T, input string) string {
	t.Helper()
	body := newGrokResponsesBillingPingFilterBody(
		io.NopCloser(strings.NewReader(input)),
		&Account{Platform: PlatformGrok},
		defaultMaxLineSize,
	)
	output, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	return string(output)
}

func TestGrokResponsesBillingPingFilter(t *testing.T) {
	input := strings.Join([]string{
		": upstream keepalive",
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		"",
		"event: future.vendor_event",
		`data: {"type":"future.vendor_event","value":1}`,
		"",
		"event: ping",
		`data: {"type":"ping","x-opencode-type":"inference-cost","cost":2.75,"input-tokens":42}`,
		"",
		"event: ping",
		`data: {"type":"ping","cost":"0"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":3,"output_tokens":5}}}`,
		"",
	}, "\n")

	result := filterGrokPingTestInput(t, input)
	require.NotContains(t, result, "event: ping")
	require.NotContains(t, result, `"x-opencode-type":"inference-cost"`)
	require.NotContains(t, result, `{"type":"ping","cost":"0"}`)
	require.Equal(t, 2, strings.Count(result, ": ping\n\n"))
	require.Contains(t, result, ": upstream keepalive\n\n")
	require.Contains(t, result, "event: response.output_text.delta")
	require.Contains(t, result, `{"type":"response.output_text.delta","delta":"hello"}`)
	require.Contains(t, result, "event: future.vendor_event")
	require.Contains(t, result, `{"type":"future.vendor_event","value":1}`)
	require.Contains(t, result, "event: response.completed")
	require.Contains(t, result, `"usage":{"input_tokens":3,"output_tokens":5}`)
}

// Every `event: ping` frame is outside the Responses closed event enum and
// breaks strict clients regardless of its payload shape, so all variants are
// rewritten into an SSE comment (issue #5105).
func TestGrokResponsesBillingPingFilterConvertsPingVariants(t *testing.T) {
	frames := []string{
		"event: ping\ndata: {\"type\":\"ping\",\"x-opencode-type\":\"inference-cost\",\"cost\":\"0.06029240\"}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"cost\":\"0\"}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"cost\":\"0.06029240\"}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"kind\":\"keepalive\"}\n\n",
		"event: ping\ndata: {\"type\":\"ping\"}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"cost\":2}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"cost\":0.0001}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"cost\":\" 0 \"}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"x-opencode-type\":\"keepalive\",\"cost\":0}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"x-opencode-type\":null,\"cost\":0}\n\n",
		"event: ping\ndata: {\"cost\":\"0\"}\n\n",
		"event: ping\ndata: {not-json}\n\n",
		"event: ping\n\n",
		"event: ping\n: vendor note\ndata: {\"type\":\"ping\"}\n\n",
	}
	result := filterGrokPingTestInput(t, strings.Join(frames, ""))
	require.Equal(t, strings.Repeat(": ping\n\n", len(frames)), result)
}

func TestGrokResponsesBillingPingFilterPreservesNonPingFrames(t *testing.T) {
	input := strings.Join([]string{
		"event: ping",
		`data: {"type":" ping ","cost":0}`,
		"",
		"event: ping",
		`data: {"type":"response.completed"}`,
		"",
		"event: custom",
		`data: {"type":"ping","x-opencode-type":"inference-cost"}`,
		"",
		`data: {"type":"ping","cost":"0"}`,
		"",
		": keepalive comment",
		"",
		"retry: 1000",
		"",
	}, "\n")

	require.Equal(t, input, filterGrokPingTestInput(t, input))
}

// A ping candidate that turns out to carry an unexpected SSE field is not a
// vendor billing/keepalive frame; it must be replayed byte for byte.
func TestGrokResponsesBillingPingFilterPassesThroughPingFrameWithUnknownField(t *testing.T) {
	input := "event: ping\nid: 7\ndata: {\"type\":\"ping\",\"cost\":\"0\"}\n\n"
	require.Equal(t, input, filterGrokPingTestInput(t, input))
}

// Buffering caps: a ping candidate that grows past the line or byte limit is
// streamed through unchanged instead of accumulating unbounded memory.
func TestGrokResponsesBillingPingFilterPassesThroughOversizedPingFrame(t *testing.T) {
	lines := []string{"event: ping"}
	for i := 0; i < grokResponsesPingFrameMaxLines; i++ {
		lines = append(lines, ": filler comment")
	}
	lines = append(lines, `data: {"type":"ping","cost":"0"}`, "")
	byLines := strings.Join(lines, "\n")
	require.Equal(t, byLines, filterGrokPingTestInput(t, byLines))

	byBytes := "event: ping\ndata: {\"type\":\"ping\",\"pad\":\"" +
		strings.Repeat("x", grokResponsesPingFrameMaxBytes) + "\"}\n\n"
	require.Equal(t, byBytes, filterGrokPingTestInput(t, byBytes))
}

func TestGrokResponsesBillingPingFilterConvertsMalformedPingFrames(t *testing.T) {
	input := "event: ping\r\ndata: {not-json}\r\n\r\n" +
		"event: ping\r\ndata: {\"type\":\"ping\",\"cost\":\"0\"} trailing\r\n\r\n" +
		"event: future.response.event\r\ndata: {\"type\":\"future.response.event\"}"
	want := ": ping\n\n" + ": ping\n\n" +
		"event: future.response.event\r\ndata: {\"type\":\"future.response.event\"}"
	require.Equal(t, want, filterGrokPingTestInput(t, input))
}

func TestGrokResponsesBillingPingFilterHandlesBareCRFrames(t *testing.T) {
	input := "event: ping\rdata: {\"type\":\"ping\",\"cost\":\"0\"}\r\r" +
		"event: future.event\rdata: {\"type\":\"future.event\"}\r\r"
	want := ": ping\n\n" + "event: future.event\rdata: {\"type\":\"future.event\"}\r\r"
	require.Equal(t, want, filterGrokPingTestInput(t, input))
}

func TestGrokResponsesBillingPingFilterConvertsPartialPingFrameAtEOF(t *testing.T) {
	input := "event: ping\ndata: {\"type\":\"ping\",\"cost\":\"0\"}"
	require.Equal(t, ": ping\n\n", filterGrokPingTestInput(t, input))
}

func TestGrokResponsesBillingPingFilterDoesNotFilterNonGrokAccounts(t *testing.T) {
	input := "event: ping\ndata: {\"type\":\"ping\",\"cost\":\"0\"}\n\n"
	source := io.NopCloser(strings.NewReader(input))
	body := newGrokResponsesBillingPingFilterBody(source, &Account{Platform: PlatformOpenAI}, defaultMaxLineSize)

	output, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, input, string(output))
}

func TestGrokResponsesBillingPingFilterPreservesUsageAndTerminalEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	input := strings.Join([]string{
		"event: ping",
		`data: {"type":"ping","x-opencode-type":"inference-cost","cost":"0"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":3,"output_tokens":5}}}`,
		"",
	}, "\n")
	account := &Account{ID: 1, Platform: PlatformGrok}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body: newGrokResponsesBillingPingFilterBody(
			io.NopCloser(strings.NewReader(input)), account, defaultMaxLineSize,
		),
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	svc := &OpenAIGatewayService{
		cfg:           &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		toolCorrector: NewCodexToolCorrector(),
	}

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "grok-4.5", "grok-4.5")
	require.NoError(t, err)
	require.Equal(t, 3, result.usage.InputTokens)
	require.Equal(t, 5, result.usage.OutputTokens)
	require.Equal(t, "resp_1", result.responseID)
	require.Contains(t, recorder.Body.String(), "response.completed")
	require.NotContains(t, recorder.Body.String(), "inference-cost")
	require.NotContains(t, recorder.Body.String(), "event: ping")
}

type grokPingFilterTestReadCloser struct {
	reader     io.ReadCloser
	closeCount atomic.Int32
}

func (r *grokPingFilterTestReadCloser) Read(p []byte) (int, error) { return r.reader.Read(p) }
func (r *grokPingFilterTestReadCloser) Close() error {
	r.closeCount.Add(1)
	return r.reader.Close()
}

func TestGrokResponsesBillingPingFilterCloseCancelsSourceOnce(t *testing.T) {
	upstreamReader, upstreamWriter := io.Pipe()
	source := &grokPingFilterTestReadCloser{reader: upstreamReader}
	body := newGrokResponsesBillingPingFilterBody(source, &Account{Platform: PlatformGrok}, defaultMaxLineSize)

	require.NoError(t, body.Close())
	require.Eventually(t, func() bool { return source.closeCount.Load() == 1 }, time.Second, time.Millisecond)
	_, err := upstreamWriter.Write([]byte("blocked"))
	require.Error(t, err)
	require.NoError(t, upstreamWriter.Close())
}

func TestGrokResponsesBillingPingFilterFlushesCompletedFrames(t *testing.T) {
	upstreamReader, upstreamWriter := io.Pipe()
	body := newGrokResponsesBillingPingFilterBody(upstreamReader, &Account{Platform: PlatformGrok}, defaultMaxLineSize)
	t.Cleanup(func() { require.NoError(t, body.Close()) })

	go func() {
		_, _ = io.WriteString(upstreamWriter, "event: future.event\ndata: {\"type\":\"future.event\"}\n\n")
	}()

	result := make(chan error, 1)
	go func() {
		buffer := make([]byte, 64)
		n, err := body.Read(buffer)
		if err == nil && !strings.Contains(string(buffer[:n]), "future.event") {
			err = errors.New("completed frame was not forwarded")
		}
		result <- err
	}()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("completed frame was buffered until upstream EOF")
	}
}

func TestGrokResponsesBillingPingFilterReportsOversizedLine(t *testing.T) {
	body := newGrokResponsesBillingPingFilterBody(
		io.NopCloser(strings.NewReader("data: 123456789\n\n")),
		&Account{Platform: PlatformGrok},
		8,
	)
	_, err := io.ReadAll(body)
	require.ErrorContains(t, err, "filter Grok Responses billing ping")
	require.NoError(t, body.Close())
}

// maxLineSize<=0（调用方没传或传了非法值）应回退到 defaultMaxLineSize，而不是
// 直接把 0/负数交给 bufio.Scanner.Buffer（那会 panic）。
func TestGrokResponsesBillingPingFilterFallsBackToDefaultMaxLineSize(t *testing.T) {
	for _, size := range []int{0, -1} {
		body := newGrokResponsesBillingPingFilterBody(
			io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")),
			&Account{Platform: PlatformGrok},
			size,
		)
		output, err := io.ReadAll(body)
		require.NoError(t, err)
		require.Contains(t, string(output), "response.completed")
		require.NoError(t, body.Close())
	}
}

// 一个以 `event: ping` 开头、但候选帧最终被判定"不是真的 ping"（payload type
// 不是 ping）的帧，如果流恰好在这个候选帧内部结束（没有收到收尾的空行），
// endPingFrame 用 blankLine=nil 调用：原样重放已缓冲的行，但不追加任何空行
// （109-111 行）——这与既有的
// TestGrokResponsesBillingPingFilterPreservesNonPingFrames（收到了收尾空行）
// 是两条不同的代码路径。
func TestGrokResponsesBillingPingFilterReplaysNonPingCandidateAtEOFWithoutTrailingBlankLine(t *testing.T) {
	input := "event: ping\ndata: {\"type\":\"response.completed\"}"
	require.Equal(t, input, filterGrokPingTestInput(t, input))
}

// 以下四个测试覆盖 filterGrokResponsesBillingPings 里"下游消费者提前断开读取
// （destination.Write 返回错误）"这条防御性路径在四个不同触发点的表现：调用方
// 停止读取 body（典型场景是客户端断开连接、http.ResponseWriter 不再消费）时，
// 过滤器必须 abort 并把错误经由 destination 传播回 body.Read()，而不是死循环
// 或吞掉错误。用 io.Pipe 精确控制"读了多少字节就关闭"来确定性地让某一次特定的
// destination.Write 调用失败，而不是用真实网络断开这种无法确定性复现的手段。

// 场景一：候选帧确实是 ping，且在main 循环遇到收尾空行时触发（134-137 行）。
// 提前整体关闭（一次都不读），保证 endPingFrame 里唯一的一次 write 必然失败。
func TestGrokResponsesBillingPingFilterAbortsWhenBlankLineTriggeredWriteFails(t *testing.T) {
	t.Parallel()
	input := "event: ping\ndata: {\"type\":\"ping\"}\n\n"
	body := newGrokResponsesBillingPingFilterBody(
		io.NopCloser(strings.NewReader(input)),
		&Account{Platform: PlatformGrok},
		defaultMaxLineSize,
	)
	typed, ok := body.(*grokResponsesBillingPingFilterBody)
	require.True(t, ok)
	require.NoError(t, typed.PipeReader.CloseWithError(errors.New("consumer stopped reading")))
	require.Eventually(t, func() bool {
		_, err := typed.PipeReader.Read(make([]byte, 1))
		return err != nil
	}, time.Second, time.Millisecond)
}

// 场景二：候选帧不是真的 ping（收尾空行触发），endPingFrame 走 replayPingFrame()
// 重放分支，重放本身的第一次 write 就失败（88-90 行的 write 失败 + 106-108 行
// endPingFrame 对这次失败的错误检查）。
func TestGrokResponsesBillingPingFilterAbortsWhenReplayFromEndPingFrameFails(t *testing.T) {
	t.Parallel()
	input := "event: ping\ndata: {\"type\":\"response.completed\"}\n\n"
	body := newGrokResponsesBillingPingFilterBody(
		io.NopCloser(strings.NewReader(input)),
		&Account{Platform: PlatformGrok},
		defaultMaxLineSize,
	)
	typed, ok := body.(*grokResponsesBillingPingFilterBody)
	require.True(t, ok)
	require.NoError(t, typed.PipeReader.CloseWithError(errors.New("consumer stopped reading")))
	require.Eventually(t, func() bool {
		_, err := typed.PipeReader.Read(make([]byte, 1))
		return err != nil
	}, time.Second, time.Millisecond)
}

// 场景三：候选帧在缓冲期间遇到一个既不能延续、又不是收尾空行的字段（例如 "id: 7"），
// 这里精确放行第一次 write（重放已缓冲的 "event: ping\n"，12 字节）成功，再让
// 紧接着的第二次 write（当前行 "id: 7\n"）失败——分别覆盖 150-153 行（重放调用点）
// 与 154-157 行（重放之后写当前行）。
func TestGrokResponsesBillingPingFilterAbortsWhenWriteAfterReplaySucceedsFails(t *testing.T) {
	t.Parallel()
	input := "event: ping\nid: 7\ndata: {\"type\":\"ping\",\"cost\":\"0\"}\n\n"
	body := newGrokResponsesBillingPingFilterBody(
		io.NopCloser(strings.NewReader(input)),
		&Account{Platform: PlatformGrok},
		defaultMaxLineSize,
	)
	typed, ok := body.(*grokResponsesBillingPingFilterBody)
	require.True(t, ok)

	first := make([]byte, len("event: ping\n"))
	_, err := io.ReadFull(typed, first)
	require.NoError(t, err)
	require.Equal(t, "event: ping\n", string(first))

	require.NoError(t, typed.PipeReader.CloseWithError(errors.New("consumer stopped reading")))
	require.Eventually(t, func() bool {
		_, err := typed.PipeReader.Read(make([]byte, 1))
		return err != nil
	}, time.Second, time.Millisecond)
}

// 场景三 b：与场景三同样的"遇到不可延续字段"触发点，但这次不放行任何读取——
// replayPingFrame() 自己的唯一一次 write 就失败，覆盖 150-153 行本身（场景三
// 覆盖的是 replayPingFrame 成功之后、紧接着写当前行失败的 154-157 行，两者是
// 同一个 if 语句里不同的东西：150-153 是"重放本身失败"，154-157 是"重放成功、
// 写当前行失败"）。
func TestGrokResponsesBillingPingFilterAbortsWhenReplayFromNonExtendableFieldFails(t *testing.T) {
	t.Parallel()
	input := "event: ping\nid: 7\ndata: {\"type\":\"ping\",\"cost\":\"0\"}\n\n"
	body := newGrokResponsesBillingPingFilterBody(
		io.NopCloser(strings.NewReader(input)),
		&Account{Platform: PlatformGrok},
		defaultMaxLineSize,
	)
	typed, ok := body.(*grokResponsesBillingPingFilterBody)
	require.True(t, ok)
	require.NoError(t, typed.PipeReader.CloseWithError(errors.New("consumer stopped reading")))
	require.Eventually(t, func() bool {
		_, err := typed.PipeReader.Read(make([]byte, 1))
		return err != nil
	}, time.Second, time.Millisecond)
}

// 场景四：流在候选帧内部结束（没有收尾空行），走 176-180 行的 EOF flush 分支，
// 而不是 main 循环里的 blank-line 分支——虽然写的是同一条语句
// （grokResponsesPingComment），但调用点（连带它自己的错误检查）不同，是单独
// 一段覆盖率统计区间。
func TestGrokResponsesBillingPingFilterAbortsWhenEOFFlushWriteFails(t *testing.T) {
	t.Parallel()
	input := "event: ping\ndata: {\"type\":\"ping\"}"
	body := newGrokResponsesBillingPingFilterBody(
		io.NopCloser(strings.NewReader(input)),
		&Account{Platform: PlatformGrok},
		defaultMaxLineSize,
	)
	typed, ok := body.(*grokResponsesBillingPingFilterBody)
	require.True(t, ok)
	require.NoError(t, typed.PipeReader.CloseWithError(errors.New("consumer stopped reading")))
	require.Eventually(t, func() bool {
		_, err := typed.PipeReader.Read(make([]byte, 1))
		return err != nil
	}, time.Second, time.Millisecond)
}

// 场景五：直接进入主 passthrough 写路径（帧本身不是以 event: ping 开头），
// 覆盖 170-173 行。
func TestGrokResponsesBillingPingFilterAbortsWhenPlainPassthroughWriteFails(t *testing.T) {
	t.Parallel()
	input := "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"
	body := newGrokResponsesBillingPingFilterBody(
		io.NopCloser(strings.NewReader(input)),
		&Account{Platform: PlatformGrok},
		defaultMaxLineSize,
	)
	typed, ok := body.(*grokResponsesBillingPingFilterBody)
	require.True(t, ok)
	require.NoError(t, typed.PipeReader.CloseWithError(errors.New("consumer stopped reading")))
	require.Eventually(t, func() bool {
		_, err := typed.PipeReader.Read(make([]byte, 1))
		return err != nil
	}, time.Second, time.Millisecond)
}
