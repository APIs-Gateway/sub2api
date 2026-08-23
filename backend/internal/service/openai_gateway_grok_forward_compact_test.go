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

// forwardGrokResponses's compact branch (isOpenAIResponsesCompactPath ->
// buildGrokCompactRequestBody) is exercised end-to-end here using the same
// queuedHTTPUpstreamStub pattern already used by antigravity_gateway_service_test.go,
// asserting the upstream actually receives the rewritten compaction-turn body.
func TestOpenAIGatewayServiceForwardGrokResponses_CompactPathRewritesUpstreamBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID:       720,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token",
			"base_url":     "https://xai.test/v1",
			"expires_at":   time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	provider := NewGrokTokenProvider(nil, &grokUnauthorizedCacheStub{cacheMiss: true}, nil)
	upstream := &queuedHTTPUpstreamStub{
		responses: []*http.Response{{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"id":"resp-compact-1","model":"grok-4.5","output":[` +
				`{"type":"reasoning","encrypted_content":"enc-state"},` +
				`{"type":"message","content":[{"type":"output_text","text":"the summary"}]}` +
				`],"usage":{"input_tokens":5,"output_tokens":3}}`)),
		}},
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream, grokTokenProvider: provider}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)

	body := []byte(`{"model":"grok-4.5","input":"summarize this","stream":true}`)
	_, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok-4.5", false, time.Now())
	require.NoError(t, err)
	require.Len(t, upstream.requestBodies, 1)

	sent := upstream.requestBodies[0]
	require.NotContains(t, string(sent), `"stream":true`, "compact turn must force stream=false")
	require.Contains(t, string(sent), "produce a faithful, concise summary",
		"upstream body must carry the compaction summary prompt")
	require.Contains(t, string(sent), "reasoning.encrypted_content",
		"compact turn must request the encrypted reasoning state back")
}

func TestOpenAIGatewayServiceForwardGrokResponses_CompactPathRejectsInvalidInputShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID:       721,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token",
			"base_url":     "https://xai.test/v1",
			"expires_at":   time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	provider := NewGrokTokenProvider(nil, &grokUnauthorizedCacheStub{cacheMiss: true}, nil)
	upstream := &queuedHTTPUpstreamStub{}
	svc := &OpenAIGatewayService{httpUpstream: upstream, grokTokenProvider: provider}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)

	// input is a number -> normalizeGrokCompactInput errors before any upstream call.
	body := []byte(`{"model":"grok-4.5","input":42}`)
	_, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok-4.5", false, time.Now())
	require.Error(t, err)
	require.Equal(t, 0, upstream.callCount, "invalid compact body must never reach the upstream")
}
