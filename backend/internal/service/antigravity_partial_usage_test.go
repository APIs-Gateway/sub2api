//go:build unit

package service

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const agMeteredSnapshot = `{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]}}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":3,"candidatesTokenCount":2,"thoughtsTokenCount":4,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":5}]}}}`
const agMeteredTerminal = `{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":3,"candidatesTokenCount":2,"thoughtsTokenCount":4,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":5}]}}}`

func agMeteredFrame(payload string) string { return "data: " + payload + "\n\n" }

type agMeteredReadFailure struct{ err error }

func (r agMeteredReadFailure) Read([]byte) (int, error) { return 0, r.err }

func agMeteredReader(t *testing.T, mode string, body io.ReadCloser, cfg *config.Config, cancel bool) (*antigravityStreamResult, error, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if cancel {
		ctx, stop := context.WithCancel(c.Request.Context())
		stop()
		c.Request = c.Request.WithContext(ctx)
	}
	svc := newAntigravityTestService(cfg)
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}
	var result *antigravityStreamResult
	var err error
	switch mode {
	case "claude_stream":
		result, err = svc.handleClaudeStreamingResponse(c, resp, time.Now(), "claude-sonnet-4-5")
	case "claude_buffered":
		result, err = svc.handleClaudeStreamToNonStreaming(c, resp, time.Now(), "claude-sonnet-4-5")
	case "gemini_stream":
		result, err = svc.handleGeminiStreamingResponse(c, resp, time.Now())
	case "gemini_buffered":
		result, err = svc.handleGeminiStreamToNonStreaming(c, resp, time.Now())
	}
	return result, err, rec
}

func assertAGMeteredSnapshot(t *testing.T, usage *ClaudeUsage) {
	t.Helper()
	require.NotNil(t, usage)
	require.Equal(t, 7, usage.InputTokens)
	require.Equal(t, 6, usage.OutputTokens)
	require.Equal(t, 3, usage.CacheReadInputTokens)
	require.Equal(t, 5, usage.ImageOutputTokens)
}

func TestAntigravityInterruptedUsage_AllReadersPreserveMeteredFailure(t *testing.T) {
	sentinel := errors.New("upstream read interrupted")
	for _, mode := range []string{"claude_stream", "claude_buffered", "gemini_stream", "gemini_buffered"} {
		for _, failure := range []string{"read_error", "oversize", "provider_error", "truncated", "zero_terminal_snapshot"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: 64 << 10}}
				payload := agMeteredFrame(agMeteredSnapshot)
				var body io.ReadCloser
				switch failure {
				case "read_error":
					body = io.NopCloser(io.MultiReader(strings.NewReader(payload), agMeteredReadFailure{sentinel}))
				case "oversize":
					body = io.NopCloser(strings.NewReader(payload + "data: " + strings.Repeat("x", 128<<10) + "\n\n"))
				case "provider_error":
					body = io.NopCloser(strings.NewReader(payload + agMeteredFrame(`{"response":{"error":{"code":403,"status":"PERMISSION_DENIED","message":"projects/private-project-123 pool-sa@internal.example.com"}}}`)))
				case "truncated":
					body = io.NopCloser(strings.NewReader(payload))
				case "zero_terminal_snapshot":
					body = io.NopCloser(strings.NewReader(payload + agMeteredFrame(`{"response":{"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0}}}`)))
				}
				result, err, rec := agMeteredReader(t, mode, body, cfg, false)
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover), "observed provider cost must not replay")
				require.NotNil(t, result)
				assertAGMeteredSnapshot(t, result.usage)
				require.NotContains(t, rec.Body.String(), "message_stop", "failure must not synthesize completion")
				assertAntigravityClientSafe(t, rec.Body.String())
				if failure == "read_error" {
					require.ErrorIs(t, err, sentinel)
				}
				if failure == "oversize" {
					require.ErrorIs(t, err, bufio.ErrTooLong)
				}
			})
		}
	}
}

func TestAntigravityInterruptedUsage_MeteredEmptyDoesNotReplay(t *testing.T) {
	for _, mode := range []string{"claude_stream", "claude_buffered"} {
		t.Run(mode, func(t *testing.T) {
			result, err, rec := agMeteredReader(t, mode, io.NopCloser(strings.NewReader(agMeteredFrame(agMeteredTerminal))), &config.Config{}, false)
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			require.NotNil(t, result)
			assertAGMeteredSnapshot(t, result.usage)
			require.NotContains(t, rec.Body.String(), "message_stop")
			require.Contains(t, rec.Body.String(), "error")
		})
	}
}

func TestAntigravityInterruptedUsage_UsageOnlyDoesNotReplay(t *testing.T) {
	for _, mode := range []string{"claude_stream", "claude_buffered"} {
		t.Run(mode, func(t *testing.T) {
			result, err, rec := agMeteredReader(t, mode, io.NopCloser(strings.NewReader(agMeteredFrame(`{"response":{"usageMetadata":{"promptTokenCount":10}}}`))), &config.Config{}, false)
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			require.NotNil(t, result)
			require.Equal(t, 10, result.usage.InputTokens)
			require.Zero(t, result.usage.OutputTokens)
			require.NotContains(t, rec.Body.String(), "message_stop")
			require.Contains(t, rec.Body.String(), "error")
		})
	}
}

func TestAntigravityInterruptedUsage_ZeroMeterEmptyRetainsRetry(t *testing.T) {
	for _, mode := range []string{"claude_stream", "claude_buffered"} {
		t.Run(mode, func(t *testing.T) {
			result, err, rec := agMeteredReader(t, mode, io.NopCloser(strings.NewReader(agMeteredFrame(`{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`))), &config.Config{}, false)
			require.Nil(t, result)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.False(t, failover.RetryableOnSameAccount)
			require.False(t, failover.BillingNoCharge)
			require.Empty(t, rec.Body.String())
		})
	}
}

func TestAntigravityInterruptedUsage_OuterForwardRetainsBillingMetadata(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(map[bool]string{false: "claude", true: "gemini"}[native]+map[bool]string{false: "_buffered", true: "_stream"}[stream], func(t *testing.T) {
				svc, account, c, _ := antigravityClientErrorFixture(t, 200, agMeteredFrame(agMeteredSnapshot))
				account.Credentials["model_mapping"] = map[string]any{"claude-sonnet-4-5": "claude-sonnet-4-5", "gemini-2.5-flash": "gemini-2.5-flash"}
				var result *ForwardResult
				var err error
				if native {
					result, err = svc.ForwardGemini(context.Background(), c, account, "gemini-2.5-flash", "streamGenerateContent", stream, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`), false)
				} else {
					body := `{"model":"claude-sonnet-4-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"stream":` + map[bool]string{false: "false", true: "true"}[stream] + `}`
					result, err = svc.Forward(context.Background(), c, account, []byte(body), false)
				}
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover))
				require.NotNil(t, result)
				assertAGMeteredSnapshot(t, &result.Usage)
				require.Equal(t, stream, result.Stream)
				require.NotEmpty(t, result.Model)
				require.NotEmpty(t, result.UpstreamModel)
				require.Equal(t, "private-upstream-request", result.RequestID)
				require.Positive(t, result.Duration)
			})
		}
	}
}

func TestAntigravityInterruptedUsage_ClientCancellationRetainsMeteredUsage(t *testing.T) {
	for _, mode := range []string{"claude_stream", "claude_buffered", "gemini_stream", "gemini_buffered"} {
		t.Run(mode, func(t *testing.T) {
			body := io.NopCloser(strings.NewReader(agMeteredFrame(agMeteredTerminal)))
			result, _, _ := agMeteredReader(t, mode, body, &config.Config{}, true)
			require.NotNil(t, result)
			assertAGMeteredSnapshot(t, result.usage)
			require.True(t, result.clientDisconnect)
		})
	}
}

func TestAntigravityInterruptedUsage_CanceledStallIsBounded(t *testing.T) {
	for _, mode := range []string{"claude_stream", "claude_buffered", "gemini_stream", "gemini_buffered"} {
		for _, setting := range []string{"nil", "disabled", "one_second"} {
			t.Run(mode+"/"+setting, func(t *testing.T) {
				t.Parallel()
				var cfg *config.Config
				if setting != "nil" {
					cfg = &config.Config{}
				}
				if setting == "one_second" {
					cfg.Gateway.StreamDataIntervalTimeout = 1
				}
				reader, writer := io.Pipe()
				t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
				written := make(chan struct{})
				go func() { _, _ = io.WriteString(writer, agMeteredFrame(agMeteredSnapshot)); close(written) }()
				done := make(chan *antigravityStreamResult, 1)
				started := time.Now()
				go func() { result, _, _ := agMeteredReader(t, mode, reader, cfg, true); done <- result }()
				select {
				case <-written:
				case <-time.After(5 * time.Second):
					t.Fatal("upstream metered prefix was not consumed")
				}
				select {
				case result := <-done:
					require.NotNil(t, result)
					assertAGMeteredSnapshot(t, result.usage)
					require.True(t, result.clientDisconnect)
					require.Less(t, time.Since(started), 35*time.Second)
				case <-time.After(35 * time.Second):
					t.Fatal("client cancellation leaked a blocked upstream reader")
				}
			})
		}
	}
}

func TestAntigravityInterruptedUsage_ImageOnlyPartialIsBillable(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, failureKind := range []string{"read_error", "cancel", "cancel_no_image"} {
			t.Run(map[bool]string{false: "buffered", true: "stream"}[stream]+"/"+failureKind, func(t *testing.T) {
				image := `{"response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}}]}}`
				if failureKind == "cancel_no_image" {
					image = agMeteredSnapshot
				}
				svc, account, c, _ := antigravityClientErrorFixture(t, 200, "")
				account.Credentials["model_mapping"] = map[string]any{"gemini-3.1-flash-image": "gemini-3.1-flash-image"}
				failure := errors.New("read failed after generated image")
				if failureKind != "read_error" {
					failure = context.Canceled
				}
				svc.httpUpstream = &httpUpstreamStub{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(io.MultiReader(strings.NewReader(agMeteredFrame(image)), agMeteredReadFailure{failure}))}}
				result, err := svc.ForwardGemini(context.Background(), c, account, "gemini-3.1-flash-image", "streamGenerateContent", stream, []byte(`{"contents":[{"role":"user","parts":[{"text":"draw"}]}]}`), false)
				if failureKind == "read_error" || !stream {
					require.ErrorIs(t, err, failure)
				} else {
					require.NoError(t, err)
				}
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover))
				require.NotNil(t, result)
				if failureKind == "cancel_no_image" {
					require.Zero(t, result.ImageCount)
					assertAGMeteredSnapshot(t, &result.Usage)
				} else {
					require.Equal(t, 1, result.ImageCount)
					require.False(t, result.Usage.hasObservedTokens())
				}
				if failureKind != "read_error" && stream {
					require.True(t, result.ClientDisconnect)
				}
			})
		}
	}
}

func TestAntigravityInterruptedUsage_ConvertedProviderFailureKeepsRawOps(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "buffered", true: "stream"}[stream], func(t *testing.T) {
			body := agMeteredFrame(agMeteredSnapshot) + agMeteredFrame(antigravityPrivateError)
			svc, account, c, rec := antigravityClientErrorFixture(t, 200, body)
			account.Credentials["model_mapping"] = map[string]any{"claude-sonnet-4-5": "claude-sonnet-4-5"}
			req := `{"model":"claude-sonnet-4-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"stream":` + map[bool]string{false: "false", true: "true"}[stream] + `}`
			result, err := svc.Forward(context.Background(), c, account, []byte(req), false)
			require.Error(t, err)
			require.NotNil(t, result)
			assertAGMeteredSnapshot(t, &result.Usage)
			marker, ok := GetOpsStreamError(c)
			require.True(t, ok)
			require.True(t, marker.UpstreamAttributed)
			require.True(t, marker.CountTowardsSLA)
			require.Equal(t, !stream, marker.NonStream)
			require.Equal(t, 403, marker.IntendedStatus)
			raw, ok := c.Get(OpsUpstreamErrorMessageKey)
			require.True(t, ok)
			require.Contains(t, raw, "private-project-123")
			assertAntigravityClientSafe(t, rec.Body.String())
		})
	}
}

// Record actual writes/flushes after request cancellation, including keepalives
// while a silent upstream has not provided another scan event.
type agMeteredCancelWriter struct {
	*httptest.ResponseRecorder
	ctx           context.Context
	firstFlush    chan struct{}
	firstSent     bool
	ioAfterCancel int
}

func (w *agMeteredCancelWriter) Write(body []byte) (int, error) {
	if w.ctx.Err() != nil {
		w.ioAfterCancel++
	}
	return w.ResponseRecorder.Write(body)
}

func (w *agMeteredCancelWriter) Flush() {
	if w.ctx.Err() != nil {
		w.ioAfterCancel++
	}
	w.ResponseRecorder.Flush()
	if !w.firstSent {
		w.firstSent = true
		close(w.firstFlush)
	}
}

func TestAntigravityInterruptedUsage_CanceledSilentStreamSkipsKeepaliveAndKeepsLateUsage(t *testing.T) {
	for _, mode := range []string{"claude_stream", "gemini_stream"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writer := &agMeteredCancelWriter{ResponseRecorder: httptest.NewRecorder(), ctx: ctx, firstFlush: make(chan struct{})}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
			svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize, StreamKeepaliveInterval: 1}})
			reader, upstream := io.Pipe()
			defer reader.Close()
			defer upstream.Close()
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}
			type completion struct {
				result *antigravityStreamResult
				err    error
			}
			done := make(chan completion, 1)
			go func() {
				var result *antigravityStreamResult
				var err error
				if mode == "claude_stream" {
					result, err = svc.handleClaudeStreamingResponse(c, resp, time.Now(), "claude-sonnet-4-5")
				} else {
					result, err = svc.handleGeminiStreamingResponse(c, resp, time.Now())
				}
				done <- completion{result, err}
			}()
			_, err := io.WriteString(upstream, agMeteredFrame(`{"response":{"candidates":[{"content":{"parts":[{"text":"first"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2}}}`))
			require.NoError(t, err)
			select {
			case <-writer.firstFlush:
			case <-time.After(5 * time.Second):
				t.Fatal("first provider content was not written")
			}
			cancel()
			time.Sleep(1200 * time.Millisecond) // A keepalive tick happens while the next provider read remains parked.
			_, err = io.WriteString(upstream, agMeteredFrame(`{"response":{"candidates":[{"content":{"parts":[{"text":"late"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":8}}}`))
			require.NoError(t, err)
			require.NoError(t, upstream.Close())
			var final completion
			select {
			case final = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("stream did not finish after the provider terminal")
			}
			require.NoError(t, final.err)
			require.NotNil(t, final.result)
			require.True(t, final.result.clientDisconnect)
			require.Equal(t, 20, final.result.usage.InputTokens)
			require.Equal(t, 8, final.result.usage.OutputTokens)
			require.Zero(t, writer.ioAfterCancel, "a canceled client must receive neither keepalive writes nor flushes")
			require.NotContains(t, writer.Body.String(), "message_stop")
		})
	}
}

func TestAntigravityInterruptedUsage_SparseCumulativeCountersRetainMetering(t *testing.T) {
	for _, mode := range []string{"claude_stream", "claude_buffered", "gemini_stream", "gemini_buffered"} {
		for _, tc := range []struct {
			name, metadata              string
			input, output, cache, image int
		}{
			{"prompt_only", `{"promptTokenCount":10}`, 7, 6, 3, 5},
			{"candidate_only", `{"candidatesTokenCount":7}`, 7, 11, 3, 5},
			{"cache_only", `{"cachedContentTokenCount":4}`, 6, 6, 4, 5},
			{"partial_zeros", `{"promptTokenCount":10,"cachedContentTokenCount":0,"candidatesTokenCount":0,"thoughtsTokenCount":0,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":0}]}`, 7, 6, 3, 5},
			{"positive_correction_not_max", `{"promptTokenCount":8,"cachedContentTokenCount":2,"candidatesTokenCount":1,"thoughtsTokenCount":1,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":2}]}`, 6, 2, 2, 2},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				sentinel := errors.New("read failed after sparse metering")
				prefix := agMeteredFrame(agMeteredSnapshot) + agMeteredFrame(`{"response":{"usageMetadata":`+tc.metadata+`}}`)
				result, err, rec := agMeteredReader(t, mode, io.NopCloser(io.MultiReader(strings.NewReader(prefix), agMeteredReadFailure{sentinel})), &config.Config{}, false)
				require.ErrorIs(t, err, sentinel)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover))
				require.NotNil(t, result)
				require.Equal(t, tc.input, result.usage.InputTokens)
				require.Equal(t, tc.output, result.usage.OutputTokens)
				require.Equal(t, tc.cache, result.usage.CacheReadInputTokens)
				require.Equal(t, tc.image, result.usage.ImageOutputTokens)
				require.NotContains(t, rec.Body.String(), "message_stop")
			})
		}
	}
}
