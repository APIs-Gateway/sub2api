//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type wsEmptyTTFTHTTPWriter struct {
	*httptest.ResponseRecorder
	frames chan string
}

func (w *wsEmptyTTFTHTTPWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(b)
	w.frames <- string(b)
	return n, err
}

// Real WS transport into the public HTTP Forward API. Never use TTFT as the
// existing client commitment/fallback boundary: empty frames still commit.
func TestWSEmptyTTFTHTTPViaWS_PreservesCommittedFrames(t *testing.T) {
	for _, mode := range []string{"early_close", "error_429", "usage_only", "delayed_content"} {
		t.Run(mode, func(t *testing.T) {
			releaseEmpty, releaseMetadata := make(chan struct{}), make(chan struct{})
			var emptyOnce, metadataOnce sync.Once
			release := func() {
				emptyOnce.Do(func() { close(releaseEmpty) })
				metadataOnce.Do(func() { close(releaseMetadata) })
			}
			var calls atomic.Int64
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				_, _, err = conn.Read(r.Context())
				if err != nil {
					return
				}
				write := func(data string) bool { return conn.Write(r.Context(), coderws.MessageText, []byte(data)) == nil }
				if !write(`{"type":"response.created","response":{"id":"resp_buffered","model":"gpt-5.1"}}`) {
					return
				}
				if !write(`{"type":"response.output_text.delta","delta":""}`) {
					return
				}
				select {
				case <-releaseEmpty:
				case <-r.Context().Done():
					return
				}
				if !write(`{"type":"response.in_progress","response":{"id":"resp_buffered"}}`) {
					return
				}
				select {
				case <-releaseMetadata:
				case <-r.Context().Done():
					return
				}
				switch mode {
				case "early_close":
					return
				case "error_429":
					write(`{"type":"error","error":{"code":"rate_limit_exceeded","type":"rate_limit_error","message":"later rate limit"}}`)
					return
				case "delayed_content":
					time.Sleep(150 * time.Millisecond)
					if !write(`{"type":"response.output_text.delta","delta":" "}`) {
						return
					}
				}
				write(`{"type":"response.completed","response":{"id":"resp_buffered","model":"gpt-5.1","output":[],"usage":{"input_tokens":2,"output_tokens":1}}}`)
			}))
			cfg := newUpstreamModelMismatchWSConfig()
			pool := newOpenAIWSConnPool(cfg)
			defer func() { release(); pool.Close(); provider.Close() }()
			httpFallback := &httpUpstreamSequenceRecorder{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"unexpected_http_fallback","usage":{"input_tokens":1,"output_tokens":1}}`))}}}
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: httpFallback, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
			account := &Account{ID: 17001, Name: "ws-ttft-real-http", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "sk-test", "base_url": provider.URL}, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
			recorder := httptest.NewRecorder()
			writer := &wsEmptyTTFTHTTPWriter{ResponseRecorder: recorder, frames: make(chan string, 16)}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type outcome struct {
				result *OpenAIForwardResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				res, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`))
				done <- outcome{res, err}
			}()
			waitFrame := func(needle string) {
				for {
					select {
					case frame := <-writer.frames:
						if strings.Contains(frame, needle) {
							return
						}
					case got := <-done:
						release()
						t.Fatalf("Forward ended before frame %s: result=%v error=%v", needle, got.result, got.err)
					case <-time.After(2 * time.Second):
						release()
						t.Fatalf("committed frame %s was incorrectly buffered", needle)
					}
				}
			}
			waitFrame(`"delta":""`)
			emptyOnce.Do(func() { close(releaseEmpty) })
			waitFrame(`"type":"response.in_progress"`)
			metadataOnce.Do(func() { close(releaseMetadata) })
			var got outcome
			select {
			case got = <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("bounded Forward did not end")
			}
			require.EqualValues(t, 1, calls.Load(), "committed attempt must not open another upstream WS")
			require.Equal(t, 0, httpFallback.callCount, "committed attempt must not replay HTTP")
			require.Contains(t, recorder.Body.String(), `"delta":""`)
			require.Contains(t, recorder.Body.String(), `"type":"response.in_progress"`)
			if mode == "early_close" || mode == "error_429" {
				require.Error(t, got.err)
				var failover *UpstreamFailoverError
				require.NotErrorAs(t, got.err, &failover, "already committed frames prohibit account replay")
				var fallback *openAIWSFallbackError
				require.NotErrorAs(t, got.err, &fallback, "committed attempt must not return an HTTP fallback signal")
				require.NotContains(t, recorder.Body.String(), "unexpected_http_fallback")
			} else {
				require.NoError(t, got.err)
				require.NotNil(t, got.result)
				require.True(t, got.result.OpenAIWSMode)
				require.Equal(t, 2, got.result.Usage.InputTokens)
				require.Equal(t, 1, got.result.Usage.OutputTokens)
				if mode == "usage_only" {
					require.Nil(t, got.result.FirstTokenMs)
				} else {
					require.NotNil(t, got.result.FirstTokenMs)
					require.GreaterOrEqual(t, *got.result.FirstTokenMs, 100)
				}
			}
		})
	}
}
