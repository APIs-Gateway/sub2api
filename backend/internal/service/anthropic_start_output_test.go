//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAnthropicStartOutput_ParserCumulativeAndControls(t *testing.T) {
	svc := &GatewayService{}
	for name, parse := range map[string]func(string, *ClaudeUsage){"ordinary": svc.parseSSEUsage, "passthrough": svc.parseSSEUsagePassthrough} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name, field string
				expected    int
			}{
				{"positive", `,"output_tokens":8`, 8},
				{"missing", "", 3},
				{"zero", `,"output_tokens":0`, 3},
				{"negative", `,"output_tokens":-8`, 3},
				{"null", `,"output_tokens":null`, 3},
			} {
				t.Run(tc.name, func(t *testing.T) {
					usage := &ClaudeUsage{OutputTokens: 3}
					parse(`{"type":"message_start","message":{"usage":{"input_tokens":2,"cache_read_input_tokens":7,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}`+tc.field+`}}}`, usage)
					require.Equal(t, tc.expected, usage.OutputTokens)
					require.Equal(t, 2, usage.InputTokens)
					require.Equal(t, 7, usage.CacheReadInputTokens)
					require.Equal(t, 5, usage.CacheCreationInputTokens)
					require.Equal(t, 2, usage.CacheCreation5mTokens)
					require.Equal(t, 3, usage.CacheCreation1hTokens)
					parse(`{"type":"message_delta","usage":{"output_tokens":0}}`, usage)
					require.Equal(t, tc.expected, usage.OutputTokens)
					parse(`{"type":"message_delta","usage":{"output_tokens":20}}`, usage)
					require.Equal(t, 20, usage.OutputTokens, "cumulative usage replaces start; never adds it")
				})
			}
			usage := &ClaudeUsage{}
			parse(`{"type":"message_start","message":{"usage":{"output_tokens":8,"cached_tokens":9}}}`, usage)
			expectedAlias := 0
			if name == "passthrough" {
				expectedAlias = 9
			}
			require.Equal(t, expectedAlias, usage.CacheReadInputTokens, "preserve each parser's existing alias support")
		})
	}
}

type startOutputBody struct {
	reader  *strings.Reader
	cancel  context.CancelFunc
	readErr error
	block   bool
	closed  chan struct{}
	once    sync.Once
}

func (b *startOutputBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if err != io.EOF {
		return n, err
	}
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	if b.block {
		<-b.closed
		return 0, io.ErrClosedPipe
	}
	if b.readErr != nil {
		return 0, b.readErr
	}
	return n, err
}
func (b *startOutputBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

func TestAnthropicStartOutput_ActualForwardKeepsPartialAndNoReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const start = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_start_only\",\"usage\":{\"output_tokens\":8}}}\n\n"
	const text = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"
	const stop = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, passthrough := range []bool{false, true} {
		for _, mode := range []string{"output_only_eof", "read_error", "idle_timeout", "cancel", "write_failure", "cumulative_complete"} {
			t.Run(fmt.Sprintf("passthrough_%t/%s", passthrough, mode), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
				payload := start
				if mode == "write_failure" {
					payload += text + stop
					c.Writer = &failWriteResponseWriter{ResponseWriter: c.Writer}
				}
				if mode == "cumulative_complete" {
					payload += text + "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":20}}\n\n" + stop
				}
				body := &startOutputBody{reader: strings.NewReader(payload), closed: make(chan struct{})}
				if mode == "read_error" {
					body.readErr = errors.New("fixture upstream read failure")
				}
				if mode == "idle_timeout" {
					body.block = true
				}
				if mode == "cancel" {
					body.cancel = cancel
					body.readErr = context.Canceled
				}
				upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"msg_start_only"}}, Body: body}}
				svc := newForwardPartialUsageServiceForTest(upstream)
				svc.rateLimitService = NewRateLimitService(&compatProviderPolicyRepo{}, nil, svc.cfg, nil, nil)
				svc.cfg.Gateway.StreamDataIntervalTimeout = 1
				account := newAnthropicAPIKeyAccountForPartialUsageTest()
				account.Extra = map[string]any{"anthropic_passthrough": passthrough}
				request := []byte(`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`)
				done := make(chan struct {
					result *ForwardResult
					err    error
				}, 1)
				go func() {
					result, err := svc.Forward(ctx, c, account, &ParsedRequest{Body: NewRequestBodyRef(request), Model: "claude-sonnet-4-5", Stream: true})
					done <- struct {
						result *ForwardResult
						err    error
					}{result, err}
				}()
				var result *ForwardResult
				var err error
				select {
				case got := <-done:
					result, err = got.result, got.err
				case <-time.After(4 * time.Second):
					_ = body.Close()
					cancel()
					<-done
					t.Fatal("forward did not exit within bounded fixture timeout")
				}
				if mode == "cumulative_complete" || mode == "write_failure" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				var retry *UpstreamFailoverError
				require.False(t, errors.As(err, &retry), "already observed output cannot be replayed")
				require.NotNil(t, result)
				expected := 8
				if mode == "cumulative_complete" {
					expected = 20
				}
				require.Equal(t, expected, result.Usage.OutputTokens)
				require.Zero(t, result.Usage.InputTokens)
				if mode == "cancel" || mode == "write_failure" {
					require.True(t, result.ClientDisconnect)
				}
				if !passthrough && strings.HasPrefix(mode, "output_only") {
					require.Nil(t, result.FirstTokenMs)
					require.Empty(t, rec.Body.String(), "staged metadata must not leak")
				}
				if passthrough && mode == "cumulative_complete" {
					require.Equal(t, payload, rec.Body.String(), "passthrough wire remains byte exact")
				}
				select {
				case <-body.closed:
				default:
					t.Fatal("upstream body was not reclaimed")
				}
			})
		}
	}
}
