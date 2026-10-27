//go:build unit

package service

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAnthropicParallelToolOverflowDrainsTerminalUsage(t *testing.T) {
	for _, keepalive := range []int{0, 1} {
		t.Run(map[int]string{0: "synchronous", 1: "keepalive"}[keepalive], func(t *testing.T) {
			body := strings.Join([]string{
				`data: {"type":"response.created","response":{"id":"resp_overflow","model":"gpt-5.6-sol"}}`,
				`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","call_id":"call_a","name":"first"}}`,
				`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"b","call_id":"call_b","name":"second"}}`,
				`data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"b","delta":"` + strings.Repeat("x", 1<<20) + `"}`,
				`data: {"type":"response.function_call_arguments.delta","output_index":0,"item_id":"a","delta":"must-not-leak-after-overflow"}`,
				`data: {"type":"response.completed","response":{"id":"resp_overflow","status":"completed","usage":{"input_tokens":13,"output_tokens":5,"input_tokens_details":{"cached_tokens":2}}}}`,
			}, "\n\n") + "\n\n"
			c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", nil)
			resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_overflow", body)
			cfg := rawChatCompletionsTestConfig()
			cfg.Gateway.StreamKeepaliveInterval = keepalive
			svc := &OpenAIGatewayService{cfg: cfg}

			result, err := svc.handleAnthropicStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
				"gpt-5.6-sol", "gpt-5.6-sol", "gpt-5.6-sol", time.Now())

			require.ErrorContains(t, err, "buffering limits")
			require.NotNil(t, result)
			require.Equal(t, "resp_overflow", result.ResponseID)
			require.Equal(t, 13, result.Usage.InputTokens)
			require.Equal(t, 5, result.Usage.OutputTokens)
			require.Equal(t, 2, result.Usage.CacheReadInputTokens)
			require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error"))
			require.NotContains(t, rec.Body.String(), "must-not-leak-after-overflow")
			require.NotContains(t, rec.Body.String(), "event: message_stop")
		})
	}
}

func TestAnthropicParallelToolOverflowDrainFailureKeepsOriginalError(t *testing.T) {
	prefix := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_overflow","model":"gpt-5.6-sol"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","name":"first"}}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"b","name":"second"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"b","delta":"` + strings.Repeat("x", 1<<20) + `"}`,
	}, "\n\n") + "\n\n"
	for _, keepalive := range []int{0, 1} {
		for _, tc := range []struct {
			name string
			body string
			err  error
		}{
			{name: "missing terminal", body: prefix},
			{name: "read error", body: prefix, err: io.ErrUnexpectedEOF},
			{name: "byte limit", body: prefix + `data: {"type":"response.output_text.delta","delta":"` + strings.Repeat("y", 5<<20) + `"}` + "\n\n"},
		} {
			t.Run(tc.name+map[int]string{0: " synchronous", 1: " keepalive"}[keepalive], func(t *testing.T) {
				c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", nil)
				resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_overflow", tc.body)
				if tc.err != nil {
					resp.Body = io.NopCloser(&chatFallbackReadError{reader: strings.NewReader(tc.body), err: tc.err})
				}
				cfg := rawChatCompletionsTestConfig()
				cfg.Gateway.StreamKeepaliveInterval = keepalive
				svc := &OpenAIGatewayService{cfg: cfg}
				result, err := svc.handleAnthropicStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
					"gpt-5.6-sol", "gpt-5.6-sol", "gpt-5.6-sol", time.Now())
				require.ErrorContains(t, err, "buffering limits")
				require.False(t, errors.Is(err, io.ErrUnexpectedEOF), "read failure must not replace the converter error")
				require.NotNil(t, result)
				require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error"))
				require.NotContains(t, rec.Body.String(), "event: message_stop")
			})
		}
	}
}

func TestAnthropicParallelToolOverflowStalledDrainHasDeadline(t *testing.T) {
	prefix := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_overflow","model":"gpt-5.6-sol"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","name":"first"}}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"b","name":"second"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"b","delta":"` + strings.Repeat("x", 1<<20) + `"}`,
	}, "\n\n") + "\n\n"
	for _, keepalive := range []int{0, 1} {
		t.Run(map[int]string{0: "synchronous", 1: "keepalive"}[keepalive], func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			go func() { _, _ = writer.Write([]byte(prefix)) }()
			c, rec := newUpstreamModelMismatchPathContext(t, "/v1/messages", nil)
			resp := upstreamModelMismatchHTTPResponse("text/event-stream", "rid_overflow", "")
			resp.Body = reader
			cfg := rawChatCompletionsTestConfig()
			cfg.Gateway.StreamKeepaliveInterval = keepalive
			svc := &OpenAIGatewayService{cfg: cfg}
			start := time.Now()
			result, err := svc.handleAnthropicStreamingResponse(resp, c, upstreamModelMismatchTestAccount(),
				"gpt-5.6-sol", "gpt-5.6-sol", "gpt-5.6-sol", start)
			require.ErrorContains(t, err, "buffering limits")
			require.NotNil(t, result)
			require.Less(t, time.Since(start), openAIChatErrorDrainMaxWait+3*time.Second)
			require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error"))
			require.NotContains(t, rec.Body.String(), "event: message_stop")
		})
	}
}
