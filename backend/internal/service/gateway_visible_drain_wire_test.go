//go:build unit

package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"testing"
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
		{"explicit_error_after_stop", start + text + delta + stop + providerErr, false, true, 27, "answer"},
		{"explicit_error_before_visible", start + providerErr, true, true, 0, ""},
		{"buffer_limit", start + visibleDrainEvent("ping", `{"type":"ping","opaque":"`+strings.Repeat("x", 9*1024*1024)+`"}`), true, true, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newPartialUsageTestContext(t)
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"attempt-private"}, "Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(tc.body))}}
			svc := newForwardPartialUsageServiceForTest(upstream)
			svc.cfg.Gateway.MaxLineSize = 16 * 1024 * 1024
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
			}
		})
	}
}
