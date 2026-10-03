//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func visibleDrainEvent(kind, data string) string {
	return "event: " + kind + "\ndata: " + data + "\n\n"
}
func TestGatewayVisibleDrain_ActualNativeAttempt(t *testing.T) {
	start := visibleDrainEvent("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":11,"cache_read_input_tokens":7}}}`)
	stop := visibleDrainEvent("message_stop", `{"type":"message_stop"}`)
	text := visibleDrainEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`)
	tool := visibleDrainEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_1","name":"probe","input":{}}}`)
	toolStop := visibleDrainEvent("content_block_stop", `{"type":"content_block_stop","index":0}`)
	thinking := visibleDrainEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"private"}}`)
	server := visibleDrainEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srv_1","name":"search","input":{}}}`)
	delta := visibleDrainEvent("message_delta", `{"type":"message_delta","usage":{"output_tokens":27}}`)
	providerErr := visibleDrainEvent("error", `{"type":"error","error":{"type":"overloaded_error","message":"fixture"}}`)
	for _, tc := range []struct {
		name, body     string
		retry, failure bool
		output         int
		visible        string
	}{
		{"empty_terminal", start + stop, true, true, 0, ""},
		{"thinking_only", start + thinking + stop, true, true, 0, ""},
		{"server_tool_only", start + server + toolStop + stop, true, true, 0, ""},
		{"incomplete_client_tool", start + tool + stop, true, true, 0, ""},
		{"complete_empty_client_tool", start + tool + toolStop + stop, false, false, 0, "probe"},
		{"visible_text_and_late_usage", start + text + stop + delta, false, false, 27, "answer"},
		{"text_in_block_start", start + visibleDrainEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"start answer"}}`) + toolStop + stop, false, false, 0, "start answer"},
		{"large_line_after_visible", start + text + visibleDrainEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+strings.Repeat("z", 9*1024*1024)+`"}}`) + stop, false, false, 0, "answer"},
		{"explicit_error_after_stop", start + text + delta + stop + providerErr, false, true, 27, "answer"},
		{"explicit_error_before_stop", start + text + delta + providerErr, false, true, 27, "answer"},
		{"explicit_error_before_visible", start + providerErr, true, true, 0, ""},
		{"buffer_limit", start + visibleDrainEvent("ping", `{"type":"ping","opaque":"`+strings.Repeat("x", 9*1024*1024)+`"}`), true, true, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newPartialUsageTestContext(t)
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"attempt-private"}, "Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(tc.body))}}
			svc := newForwardPartialUsageServiceForTest(upstream)
			svc.cfg.Gateway.MaxLineSize = 16 * 1024 * 1024
			policyRepo := &compatProviderPolicyRepo{}
			svc.rateLimitService = NewRateLimitService(policyRepo, nil, svc.cfg, nil, nil)
			account := newAnthropicAPIKeyAccountForPartialUsageTest()
			body := []byte(`{"model":"claude-3-5-sonnet-latest","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			result, err := svc.Forward(context.Background(), c, account, &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-3-5-sonnet-latest", Stream: true})
			if tc.failure {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			var retry *UpstreamFailoverError
			require.Equal(t, tc.retry, errors.As(err, &retry))
			if tc.name == "explicit_error_before_visible" {
				require.Equal(t, []int64{account.ID}, policyRepo.overloaded)
			} else {
				require.Empty(t, policyRepo.overloaded)
			}
			if tc.retry {
				require.Nil(t, result)
				require.Empty(t, rec.Body.String())
				require.Empty(t, rec.Header().Get("X-Request-Id"))
			} else {
				require.NotNil(t, result)
				require.Equal(t, 11, result.Usage.InputTokens)
				require.Equal(t, 7, result.Usage.CacheReadInputTokens)
				require.Equal(t, tc.output, result.Usage.OutputTokens)
				require.NotNil(t, result.FirstTokenMs)
				require.Contains(t, rec.Body.String(), tc.visible)
				if tc.failure {
					require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error\n"))
					require.Contains(t, rec.Body.String(), "overloaded_error")
				}
			}
		})
	}
}

type visibleDrainCancelWriter struct {
	*httptest.ResponseRecorder
	ctx          context.Context
	cancel       context.CancelFunc
	afterWrites  int
	afterFlushes int
}

func (w *visibleDrainCancelWriter) Write(p []byte) (int, error) {
	if w.ctx.Err() != nil {
		w.afterWrites++
	}
	return w.ResponseRecorder.Write(p)
}
func (w *visibleDrainCancelWriter) Flush() {
	if w.ctx.Err() != nil {
		w.afterFlushes++
	}
	w.ResponseRecorder.Flush()
	w.cancel()
}
func TestGatewayVisibleDrain_RealCancellationPreservesUsage(t *testing.T) {
	for _, visible := range []bool{false, true} {
		t.Run(fmt.Sprint(visible), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writer := &visibleDrainCancelWriter{ResponseRecorder: httptest.NewRecorder(), ctx: ctx, cancel: cancel}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
			reader, pipeWriter := io.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer pipeWriter.Close()
				prefix := visibleDrainEvent("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":11}}}`)
				if visible {
					prefix += visibleDrainEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`)
				}
				_, _ = io.WriteString(pipeWriter, prefix)
				if visible {
					<-ctx.Done()
				} else {
					cancel()
				}
				_, _ = io.WriteString(pipeWriter, visibleDrainEvent("error", `{"type":"error","error":{"type":"overloaded_error","message":"fixture"}}`))
			}()
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}}
			svc := newForwardPartialUsageServiceForTest(upstream)
			svc.rateLimitService = NewRateLimitService(&compatProviderPolicyRepo{}, nil, svc.cfg, nil, nil)
			result, err := svc.Forward(context.Background(), c, newAnthropicAPIKeyAccountForPartialUsageTest(), &ParsedRequest{Body: NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)), Model: "claude-sonnet-4-5", Stream: true})
			require.Error(t, err)
			require.NotNil(t, result)
			require.Equal(t, 11, result.Usage.InputTokens)
			require.True(t, result.ClientDisconnect)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			require.Zero(t, writer.afterWrites)
			require.Zero(t, writer.afterFlushes)
			require.NotContains(t, writer.Body.String(), "event: error")
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("upstream reader producer leaked")
			}
		})
	}
}

type visibleDrainCancelExitBody struct {
	payload *strings.Reader
	cancel  context.CancelFunc
	ending  string
	closed  chan struct{}
	once    sync.Once
}

func (b *visibleDrainCancelExitBody) Read(p []byte) (int, error) {
	n, _ := b.payload.Read(p)
	if n > 0 {
		return n, nil
	}
	// Native scanner acknowledges every complete SSE event before reading again.
	// Thus message_start usage is already merged when this cancellation happens.
	b.cancel()
	switch b.ending {
	case "read_error":
		return 0, io.ErrUnexpectedEOF
	case "terminal_tail":
		<-b.closed
		return 0, io.ErrClosedPipe
	default:
		return 0, io.EOF
	}
}
func (b *visibleDrainCancelExitBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }
func TestGatewayVisibleDrain_CanceledNonErrorEventExit(t *testing.T) {
	for _, visible := range []bool{false, true} {
		for _, ending := range []string{"eof", "read_error", "terminal_tail"} {
			t.Run(fmt.Sprintf("%t/%s", visible, ending), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				rec := httptest.NewRecorder()
				writer := &visibleDrainCancelWriter{ResponseRecorder: rec, ctx: ctx, cancel: func() {}}
				c, _ := gin.CreateTestContext(writer)
				c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
				payload := visibleDrainEvent("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":11,"cache_read_input_tokens":7}}}`)
				if visible {
					payload += visibleDrainEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`)
				}
				if ending == "terminal_tail" {
					payload += visibleDrainEvent("message_stop", `{"type":"message_stop"}`)
				}
				body := &visibleDrainCancelExitBody{payload: strings.NewReader(payload), cancel: cancel, ending: ending, closed: make(chan struct{})}
				upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}}
				svc := newForwardPartialUsageServiceForTest(upstream)
				defer body.Close()
				type outcome struct {
					result *ForwardResult
					err    error
				}
				finished := make(chan outcome, 1)
				go func() {
					result, err := svc.Forward(context.Background(), c, newAnthropicAPIKeyAccountForPartialUsageTest(), &ParsedRequest{Body: NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)), Model: "claude-sonnet-4-5", Stream: true})
					finished <- outcome{result, err}
				}()
				var got outcome
				select {
				case got = <-finished:
				case <-time.After(3 * time.Second):
					_ = body.Close()
					select {
					case <-finished:
					case <-time.After(time.Second):
						t.Fatal("forward and reader did not stop after body close")
					}
					t.Fatal("canceled terminal tail did not return within 3 seconds")
				}
				result, err := got.result, got.err
				if visible && ending == "terminal_tail" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				require.NotNil(t, result)
				require.Equal(t, 11, result.Usage.InputTokens)
				require.Equal(t, 7, result.Usage.CacheReadInputTokens)
				require.True(t, result.ClientDisconnect)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover))
				if visible {
					require.Contains(t, rec.Body.String(), "answer")
				} else {
					require.Empty(t, rec.Body.String())
				}
				require.Zero(t, writer.afterWrites)
				require.Zero(t, writer.afterFlushes)
				select {
				case <-body.closed:
				default:
					t.Fatal("reader was not closed on canceled exit")
				}
			})
		}
	}
}
