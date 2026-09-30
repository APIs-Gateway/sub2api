//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGrokOAuthResponsesEmptyCompletedFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)

	account := &Account{
		ID:       7774,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "test-token",
			"base_url":     "https://xai.test/v1",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
	provider := NewGrokTokenProvider(nil, &grokUnauthorizedCacheStub{cacheMiss: true}, nil)
	created := `data: {"type":"response.created","response":{"id":"resp-grok","status":"in_progress"}}`

	for _, tc := range []struct {
		name      string
		events    []string
		failover  bool
		wantUsage int
		wantText  string
	}{
		{
			name:     "empty completion switches account before client output",
			events:   []string{created, `data: {"type":"response.completed","response":{"id":"resp-grok","status":"completed","output":[]}}`},
			failover: true,
		},
		{
			name: "text output is successful",
			events: []string{
				created,
				`data: {"type":"response.output_text.delta","delta":"hello"}`,
				`data: {"type":"response.completed","response":{"id":"resp-grok","status":"completed","usage":{"input_tokens":3,"output_tokens":1}}}`,
			},
			wantUsage: 3,
			wantText:  "hello",
		},
		{
			name:      "usage-only completion remains billable",
			events:    []string{created, `data: {"type":"response.completed","response":{"id":"resp-grok","status":"completed","usage":{"input_tokens":3,"output_tokens":0}}}`},
			wantUsage: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := strings.Join(tc.events, "\n\n") + "\n\n"
			upstream := &httpUpstreamStub{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(stream)),
			}}
			svc := &OpenAIGatewayService{httpUpstream: upstream, grokTokenProvider: provider}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

			result, err := svc.forwardGrokResponses(context.Background(), c, account,
				[]byte(`{"model":"grok-4.3","stream":true}`), "grok-4.3", true, time.Now())
			if tc.failover {
				var failoverErr *UpstreamFailoverError
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
				require.Nil(t, result)
				require.Empty(t, recorder.Body.String(), "an empty success must not reach the client")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tc.wantUsage, result.Usage.InputTokens)
			if tc.wantText != "" {
				require.Contains(t, recorder.Body.String(), tc.wantText)
			}
		})
	}
}
