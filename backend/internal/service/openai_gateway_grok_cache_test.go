//go:build unit

package service

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newGrokCacheTestContext(apiKeyID int64, path string, headers map[string]string) *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	for name, value := range headers {
		c.Request.Header.Set(name, value)
	}
	if apiKeyID > 0 {
		c.Set("api_key", &APIKey{ID: apiKeyID})
	}
	return c
}

func TestResolveGrokCacheIdentityUsesTenantAndStablePrefix(t *testing.T) {
	first := []byte(`{"model":"grok-4.3","instructions":"Be concise","tools":[{"type":"function","name":"lookup"}],"messages":[{"role":"user","content":"Question A"}]}`)
	second := []byte(`{"model":"grok-4.3","instructions":"Be concise","tools":[{"name":"lookup","type":"function"}],"messages":[{"role":"user","content":"Question B"}]}`)

	firstIdentity := resolveGrokCacheIdentity(newGrokCacheTestContext(101, "/v1/responses", nil), first, "", "grok-4.3")
	secondIdentity := resolveGrokCacheIdentity(newGrokCacheTestContext(101, "/v1/responses", nil), second, "", "grok-4.3")
	otherTenantIdentity := resolveGrokCacheIdentity(newGrokCacheTestContext(102, "/v1/responses", nil), first, "", "grok-4.3")
	otherModelIdentity := resolveGrokCacheIdentity(newGrokCacheTestContext(101, "/v1/responses", nil), first, "", "grok-4.5")

	require.NotEmpty(t, firstIdentity)
	require.Equal(t, firstIdentity, secondIdentity)
	require.NotEqual(t, firstIdentity, otherTenantIdentity)
	require.NotEqual(t, firstIdentity, otherModelIdentity)
	require.NotContains(t, firstIdentity, "Question A")
}

func TestResolveGrokCacheIdentityUsesExplicitIDEAndClaudeSessions(t *testing.T) {
	body := []byte(`{"model":"grok-4.3","messages":[{"role":"user","content":"hello"}]}`)

	opencode := resolveGrokCacheIdentity(newGrokCacheTestContext(201, "/v1/responses", map[string]string{
		"X-Session-Affinity": "opencode-session",
	}), body, "", "grok-4.3")
	claude := resolveGrokCacheIdentity(newGrokCacheTestContext(201, "/v1/messages", map[string]string{
		"X-Claude-Code-Session-Id": "claude-session",
	}), body, "", "grok-4.3")

	require.NotEmpty(t, opencode)
	require.NotEmpty(t, claude)
	require.NotEqual(t, opencode, claude)
	require.NotContains(t, opencode, "opencode-session")
	require.NotContains(t, claude, "claude-session")
}

func TestResolveGrokCacheIdentityFailsClosedForModelOnlyAndCompact(t *testing.T) {
	require.Empty(t, resolveGrokCacheIdentity(newGrokCacheTestContext(301, "/v1/responses", nil), []byte(`{"model":"grok-4.3"}`), "", "grok-4.3"))
	require.Empty(t, resolveGrokCacheIdentity(newGrokCacheTestContext(301, "/v1/responses/compact", nil), []byte(`{"model":"grok-4.3","input":"compact"}`), "", "grok-4.3"))
	require.Empty(t, resolveGrokCacheIdentity(newGrokCacheTestContext(0, "/v1/responses", nil), []byte(`{"model":"grok-4.3","input":"hello"}`), "", "grok-4.3"))
}

func TestApplyGrokResponsesCacheIdentityReplacesAndRemovesClientKey(t *testing.T) {
	body, err := applyGrokResponsesCacheIdentity([]byte(`{"model":"grok-4.3","prompt_cache_key":"client-key"}`), "isolated-key")
	require.NoError(t, err)
	require.Equal(t, "isolated-key", gjson.GetBytes(body, "prompt_cache_key").String())

	body, err = applyGrokResponsesCacheIdentity(body, "")
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "prompt_cache_key").Exists())
}

func TestApplyGrokCacheHeadersAndCLIHeaders(t *testing.T) {
	headers := make(http.Header)
	applyGrokCLIHeaders(headers)
	applyGrokCacheHeaders(headers, "isolated-key")
	require.Equal(t, grokUpstreamUserAgent, headers.Get("User-Agent"))
	require.Equal(t, grokCLIVersion, headers.Get("X-Grok-Client-Version"))
	require.Equal(t, "isolated-key", headers.Get(grokConversationIDHeader))

	applyGrokCacheHeaders(headers, "")
	require.Empty(t, headers.Get(grokConversationIDHeader))
}

func TestStripGrokChatPromptCacheKey(t *testing.T) {
	body, err := stripGrokChatPromptCacheKey(bytes.TrimSpace([]byte(`{"model":"grok-4.3","prompt_cache_key":"client-key"}`)))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "prompt_cache_key").Exists())
}
