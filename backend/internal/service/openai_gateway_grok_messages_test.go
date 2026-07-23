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

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestForwardAsAnthropicForGrokUsesXAIResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 7109})

	account := grokProtocolOAuthAccount(7109)
	repo := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}
	upstream := &httpUpstreamRecorder{resp: grokMessagesSSECompletedResponse()}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil, nil),
		accountRepo:       repo,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "client-messages-key", "")

	require.NoError(t, err)
	require.Equal(t, "https://api.x.ai/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer oauth-protocol-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "sub2api-grok/1.0", upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "grok-4.3", gjson.GetBytes(upstream.lastBody, "model").String())
	cacheIdentity := gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String()
	require.NotEqual(t, "client-messages-key", cacheIdentity)
	require.NotEmpty(t, cacheIdentity)
	require.Equal(t, cacheIdentity, upstream.lastReq.Header.Get(grokConversationIDHeader))
	require.Empty(t, upstream.lastReq.Header.Get("session_id"))
	require.NotContains(t, string(upstream.lastBody), "chatgpt.com")
	require.Equal(t, "grok", result.Model)
	require.Equal(t, "grok-4.3", result.UpstreamModel)
	require.Contains(t, recorder.Body.String(), `"type":"text_delta"`)
	require.Contains(t, recorder.Body.String(), `"text":"ok"`)
}

func grokMessagesSSECompletedResponse() *http.Response {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_grok_messages","object":"response","model":"grok-4.3","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_grok_messages"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestShouldForwardAnthropicViaRawChatCompletionsKeepsGrokOnResponses(t *testing.T) {
	account := grokProtocolAPIKeyAccount(7110)
	require.False(t, shouldForwardAnthropicViaRawChatCompletions(account))
}

func TestForwardAsAnthropicForGrokRetriesInvalidEncryptedContentOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"first"},{"role":"assistant","content":[{"type":"thinking","thinking":"private reasoning","signature":"enc-rs-valid-signature"}]},{"role":"user","content":"next"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	account := grokProtocolOAuthAccount(7111)
	repo := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newJSONResponse(http.StatusBadRequest, `{"code":"invalid-argument","error":"Could not decrypt the provided encrypted_content."}`),
		grokMessagesSSECompletedResponse(),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil, nil),
		accountRepo:       repo,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	firstReasoning := gjson.GetBytes(upstream.bodies[0], `input.#(type=="reasoning")`)
	require.True(t, firstReasoning.Exists(), "first request body: %s", upstream.bodies[0])
	require.Equal(t, "enc-rs-valid-signature", firstReasoning.Get("encrypted_content").String())
	require.False(t, gjson.GetBytes(upstream.bodies[1], `input.#(type=="reasoning")`).Exists(), "second request body: %s", upstream.bodies[1])
	require.Contains(t, recorder.Body.String(), `"text":"ok"`)
}

func TestForwardAsAnthropicForGrokStripsThinkingSignaturesAfterRetryFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"first"},{"role":"assistant","content":[{"type":"thinking","thinking":"private reasoning","signature":"enc-rs-valid-signature"}]},{"role":"user","content":"next"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	account := grokProtocolOAuthAccount(7112)
	repo := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}
	invalid := func() *http.Response {
		return newJSONResponse(http.StatusBadRequest, `{"code":"invalid-argument","error":"Could not decrypt the provided encrypted_content."}`)
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{invalid(), invalid(), grokMessagesSSECompletedResponse()}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil, nil),
		accountRepo:       repo,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 3)
	require.True(t, gjson.GetBytes(upstream.bodies[0], `input.#(type=="reasoning")`).Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], `input.#(type=="reasoning")`).Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[2], `input.#(type=="reasoning")`).Exists())
}

func TestGrokInvalidEncryptedContentResponseClassification(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "flat invalid argument",
			status: http.StatusBadRequest,
			body:   `{"code":"invalid-argument","error":"Could not decrypt the provided encrypted_content."}`,
			want:   true,
		},
		{
			name:   "nested invalid encrypted content",
			status: http.StatusBadRequest,
			body:   `{"error":{"code":"invalid_encrypted_content","message":"content rejected"}}`,
			want:   true,
		},
		{
			name:   "wrong status",
			status: http.StatusInternalServerError,
			body:   `{"code":"invalid_encrypted_content","message":"content rejected"}`,
			want:   false,
		},
		{
			name:   "unrelated bad request",
			status: http.StatusBadRequest,
			body:   `{"code":"invalid-argument","error":"tool_choice is invalid"}`,
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isGrokInvalidEncryptedContentResponse(tt.status, []byte(tt.body)))
		})
	}
}

func TestTrimGrokInvalidEncryptedContentRetryBodyPreservesOtherInput(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"keep"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"metadata":{"keep":true}}`)

	retryBody, changed, err := trimGrokInvalidEncryptedContentRetryBody(body)

	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(retryBody, `input.#(type=="reasoning").encrypted_content`).Exists())
	require.Equal(t, "keep", gjson.GetBytes(retryBody, `input.#(type=="reasoning").summary.0.text`).String())
	require.Equal(t, "hello", gjson.GetBytes(retryBody, "input.1.content.0.text").String())
	require.True(t, gjson.GetBytes(retryBody, "metadata.keep").Bool())
}

func TestStripAnthropicThinkingSignaturesOnlyChangesThinkingBlocks(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reasoning","signature":"gAAAAAB"},{"type":"text","text":"visible","signature":"keep-me"}]}]}`)

	stripped, changed := stripAnthropicThinkingSignatures(body)

	require.True(t, changed)
	require.False(t, gjson.GetBytes(stripped, "messages.0.content.0.signature").Exists())
	require.Equal(t, "keep-me", gjson.GetBytes(stripped, "messages.0.content.1.signature").String())
}
