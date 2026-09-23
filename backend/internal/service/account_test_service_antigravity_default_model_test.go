//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAccountTestService_AntigravityEmptyModelUsesSonnet46(t *testing.T) {
	c, recorder := newTestContext()

	upstreamBody := []byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"pong\"}]},\"finishReason\":\"STOP\"}]}}\n\n")
	upstream := &queuedHTTPUpstreamStub{
		responses: []*http.Response{
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(bytes.NewReader(upstreamBody)),
			},
		},
	}
	gateway := &AntigravityGatewayService{
		settingService: NewSettingService(&antigravitySettingRepoStub{}, &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}),
		tokenProvider:  &AntigravityTokenProvider{},
		httpUpstream:   upstream,
	}
	svc := &AccountTestService{antigravityGatewayService: gateway}

	account := &Account{
		ID:          1108,
		Name:        "antigravity-default-model",
		Platform:    PlatformAntigravity,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "token",
			"project_id":   "test-project",
		},
	}

	require.NoError(t, svc.routeAntigravityTest(c, account, "", ""))

	require.Len(t, upstream.requestBodies, 1)
	var wrapped map[string]any
	require.NoError(t, json.Unmarshal(upstream.requestBodies[0], &wrapped))
	require.Equal(t, "claude-sonnet-4-6", wrapped["model"])

	out := recorder.Body.String()
	require.Contains(t, out, `"type":"test_start"`)
	require.Contains(t, out, `"model":"claude-sonnet-4-6"`)
	require.Contains(t, out, `"success":true`)
}
