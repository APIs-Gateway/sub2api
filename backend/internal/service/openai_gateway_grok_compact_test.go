//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHandleNonStreamingResponseConvertsGrokCompactResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_grok_1",
			"status":"completed",
			"output":[
				{"type":"reasoning","encrypted_content":"grok-encrypted-state"},
				{"type":"message","content":[{"type":"output_text","text":"summary text"}]}
			],
			"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}
		}`)),
	}

	result, err := (&OpenAIGatewayService{}).handleNonStreamingResponse(
		context.Background(),
		resp,
		c,
		&Account{Platform: PlatformGrok, Type: AccountTypeOAuth},
		"grok-4.5",
		"grok-4.5",
	)

	require.NoError(t, err)
	require.Equal(t, "resp_grok_1", result.responseID)
	require.Equal(t, "compaction", gjson.Get(recorder.Body.String(), "output.0.type").String())
	require.Equal(t, "grok-encrypted-state", gjson.Get(recorder.Body.String(), "output.0.encrypted_content").String())
	require.Equal(t, "summary text", gjson.Get(recorder.Body.String(), "output.0.summary.0.text").String())
}

func TestForwardGrokResponsesEmulatesCompactEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"grok","stream":true,"input":"hello"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"compact-grok"},
		},
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_compact_grok",
			"status":"completed",
			"output":[
				{"type":"reasoning","encrypted_content":"grok-encrypted-state"},
				{"type":"message","content":[{"type":"output_text","text":"summary text"}]}
			],
			"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}
		}`)),
	}}
	account := grokProtocolOAuthAccount(7105)
	repo := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil, nil),
		accountRepo:       repo,
	}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok", false, time.Now())

	require.NoError(t, err)
	require.Equal(t, "resp_compact_grok", result.ResponseID)
	require.False(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.Equal(t, "reasoning.encrypted_content", gjson.GetBytes(upstream.lastBody, "include.0").String())
	require.Equal(t, "hello", gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String())
	require.Contains(t, gjson.GetBytes(upstream.lastBody, "input.1.content.0.text").String(), "<summary>...</summary>")
	require.Equal(t, "compaction", gjson.Get(recorder.Body.String(), "output.0.type").String())
}
