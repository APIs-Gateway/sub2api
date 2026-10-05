package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const structuredInputFirstLegDelay = 120 * time.Millisecond

type structuredInputDelayedReader struct {
	io.Reader
	delayed bool
}

func (r *structuredInputDelayedReader) Read(p []byte) (int, error) {
	if !r.delayed {
		r.delayed = true
		time.Sleep(structuredInputFirstLegDelay)
	}
	return r.Reader.Read(p)
}

func TestStructuredInputRecovery_FirstLegLatency(t *testing.T) {
	for _, kind := range []string{"buffered", "stream", "partial"} {
		t.Run(kind, func(t *testing.T) {
			stream := kind != "buffered"
			body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
			first := io.NopCloser(&structuredInputDelayedReader{Reader: strings.NewReader(structuredInputRejection)})
			payload := `{"id":"latency_raw","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":15,"total_tokens":25}}`
			var reader io.Reader = strings.NewReader(payload)
			contentType := "application/json"
			if stream {
				contentType = "text/event-stream"
				payload = `data: {"id":"latency_raw","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}],"usage":{"prompt_tokens":10,"completion_tokens":15,"total_tokens":25}}` + "\n\n"
				reader = strings.NewReader(payload + "data: [DONE]\n\n")
				if kind == "partial" {
					reader = io.MultiReader(strings.NewReader(payload+structuredInputProviderErrorFrame), structuredInputFailReader{errors.New("metered read failed")})
				}
			}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}}, Body: first},
				{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(reader)},
			}}
			svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: upstream}
			result, err := svc.ForwardAsChatCompletions(context.Background(), c, structuredInputRetryTestAccount(), body, "", "")
			if kind == "partial" {
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover))
				require.Equal(t, 1, strings.Count(rec.Body.String(), `"error":`))
				require.NotContains(t, rec.Body.String(), "data: [DONE]")
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, result)
			require.Len(t, upstream.requests, 2)
			require.Equal(t, 10, result.Usage.InputTokens)
			require.Equal(t, 15, result.Usage.OutputTokens)
			require.GreaterOrEqual(t, result.Duration, structuredInputFirstLegDelay, "whole attempt must include the first rejection body read")
			if stream {
				require.NotNil(t, result.FirstTokenMs)
				require.GreaterOrEqual(t, *result.FirstTokenMs, int(structuredInputFirstLegDelay.Milliseconds()))
			}
		})
	}
}
