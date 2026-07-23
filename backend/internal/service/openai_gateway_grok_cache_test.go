//go:build unit

package service

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
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
	intent := []byte(`{"model":"grok-4.3","prompt_cache_key":"client-key"}`)
	body, err := applyGrokResponsesCacheIdentity(intent, intent, "isolated-key", false)
	require.NoError(t, err)
	require.Equal(t, "isolated-key", gjson.GetBytes(body, "prompt_cache_key").String())

	body, err = applyGrokResponsesCacheIdentity(body, body, "", false)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "prompt_cache_key").Exists())
}

func TestApplyGrokResponsesCacheIdentityAddsDisabledNativeToolsForKnownFree(t *testing.T) {
	intent := []byte(`{"model":"grok-4.3","input":[{"role":"user","content":"hello"}]}`)
	body, err := applyGrokResponsesCacheIdentity(intent, intent, "isolated-key", true)
	require.NoError(t, err)
	require.Equal(t, "isolated-key", gjson.GetBytes(body, "prompt_cache_key").String())
	require.Equal(t, "none", gjson.GetBytes(body, "tool_choice").String())
	require.Equal(t, "web_search", gjson.GetBytes(body, "tools.0.type").String())
	require.Equal(t, "x_search", gjson.GetBytes(body, "tools.1.type").String())
}

func TestApplyGrokResponsesCacheIdentityPreservesExplicitToolIntent(t *testing.T) {
	intent := []byte(`{"model":"grok-4.3","tools":[{"type":"function","name":"lookup"}]}`)
	body, err := applyGrokResponsesCacheIdentity(intent, intent, "isolated-key", true)
	require.NoError(t, err)
	require.Equal(t, "isolated-key", gjson.GetBytes(body, "prompt_cache_key").String())
	require.Equal(t, "function", gjson.GetBytes(body, "tools.0.type").String())
	require.False(t, gjson.GetBytes(body, "tool_choice").Exists())
}

func TestApplyGrokResponsesCacheIdentityRespectsToolIntentShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "no input", body: `{"model":"grok-4.3"}`, want: false},
		{name: "additional tools without list", body: `{"input":[{"type":"additional_tools"}]}`, want: true},
		{name: "empty additional tools", body: `{"input":[{"type":"additional_tools","tools":[]}]}`, want: false},
		{name: "additional tools with entries", body: `{"input":[{"type":"additional_tools","tools":[{"type":"web_search"}]}]}`, want: true},
		{name: "explicit tools", body: `{"tools":[]}`, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, hasGrokResponsesToolIntent([]byte(tt.body)))
		})
	}
}

func TestGrokFreeFunctionToolCacheRouteIsSelective(t *testing.T) {
	freeAccount := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"subscription_tier": "free"},
	}
	paidAccount := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"subscription_tier": "SuperGrok"},
	}

	pureClientTools := []byte(`{"model":"grok-4.3","tools":[{"type":"function","name":"lookup"}]}`)
	unchanged, err := applyGrokFreeMessagesFunctionToolCacheRoute(pureClientTools, pureClientTools, freeAccount, "isolated-key")
	require.NoError(t, err)
	require.JSONEq(t, string(pureClientTools), string(unchanged))

	optInAccount := *freeAccount
	optInAccount.Extra = map[string]any{grokClientToolCacheOptInExtraKey: true}
	optIn, err := applyGrokFreeMessagesFunctionToolCacheRoute(pureClientTools, pureClientTools, &optInAccount, "isolated-key")
	require.NoError(t, err)
	require.Equal(t, 3, len(gjson.GetBytes(optIn, "tools").Array()))
	require.Equal(t, "lookup", gjson.GetBytes(optIn, "tools.0.name").String())
	require.Equal(t, "web_search", gjson.GetBytes(optIn, "tools.1.type").String())
	require.Equal(t, "x_search", gjson.GetBytes(optIn, "tools.2.type").String())

	mixedTools := []byte(`{"model":"grok-4.3","tools":[{"type":"function","name":"lookup"},{"type":"function","name":"web_search"}]}`)
	mixed, err := applyGrokFreeMessagesFunctionToolCacheRoute(mixedTools, mixedTools, freeAccount, "isolated-key")
	require.NoError(t, err)
	require.Equal(t, 3, len(gjson.GetBytes(mixed, "tools").Array()))
	require.Equal(t, "lookup", gjson.GetBytes(mixed, "tools.0.name").String())
	require.Equal(t, "web_search", gjson.GetBytes(mixed, "tools.1.type").String())
	require.Equal(t, "x_search", gjson.GetBytes(mixed, "tools.2.type").String())

	paid, err := applyGrokFreeMessagesFunctionToolCacheRoute(mixedTools, mixedTools, paidAccount, "isolated-key")
	require.NoError(t, err)
	require.JSONEq(t, string(mixedTools), string(paid))

	nativeOnly := []byte(`{"model":"grok-4.3","tools":[{"type":"web_search"}]}`)
	nativeResult, err := applyGrokFreeMessagesFunctionToolCacheRoute(nativeOnly, nativeOnly, freeAccount, "isolated-key")
	require.NoError(t, err)
	require.JSONEq(t, string(nativeOnly), string(nativeResult))
}

func TestIsKnownGrokFreeAccountUsesQuotaProbe(t *testing.T) {
	limit := xai.GrokFreeRolling24hTokenLimit
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{grokQuotaSnapshotExtraKey: map[string]any{
			"tokens": map[string]any{"limit": limit},
		}},
	}
	require.True(t, isKnownGrokFreeAccount(account))

	legacy := *account
	legacy.Extra = map[string]any{grokQuotaSnapshotExtraKey: map[string]any{
		"tokens": map[string]any{"limit": int64(2_000_000)},
	}}
	require.True(t, isKnownGrokFreeAccount(&legacy))
}

func TestIsKnownGrokFreeAccountFailsClosedForUnknownAndPaidSignals(t *testing.T) {
	require.False(t, isKnownGrokFreeAccount(nil))
	require.False(t, isKnownGrokFreeAccount(&Account{Platform: PlatformGrok, Type: AccountTypeAPIKey}))
	require.False(t, isKnownGrokFreeAccount(&Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"subscription_tier": "SuperGrok",
		},
	}))
	require.False(t, isKnownGrokFreeAccount(&Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{grokQuotaSnapshotExtraKey: map[string]any{
			"subscription_tier": "SuperGrok",
		}},
	}))
	require.True(t, isKnownGrokFreeAccount(&Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"subscription_tier": "grok-basic",
		},
	}))
	require.True(t, isKnownGrokFreeAccount(&Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{grokQuotaSnapshotExtraKey: map[string]any{
			"subscription_tier": "free",
		}},
	}))
}

func TestGrokFreeCacheFunctionToolIntentValidation(t *testing.T) {
	validTools := gjson.Parse(`[{"type":"function","name":"lookup"}]`)
	for name, body := range map[string]string{
		"missing tools":           `{}`,
		"empty tools":             `{"tools":[]}`,
		"unsupported native tool": `{"tools":[{"type":"computer_use"}]}`,
		"missing name":            `{"tools":[{"type":"function"}]}`,
		"nested function":         `{"tools":[{"type":"function","name":"lookup","function":{}}]}`,
		"object choice":           `{"tools":[{"type":"function","name":"lookup"}],"tool_choice":{}}`,
		"unsupported choice":      `{"tools":[{"type":"function","name":"lookup"}],"tool_choice":"required"}`,
	} {
		result := gjson.GetBytes([]byte(body), "tools")
		require.False(t, isGrokFreeCacheFunctionToolIntent(result, gjson.GetBytes([]byte(body), "tool_choice")), name)
	}
	require.True(t, isGrokFreeCacheFunctionToolIntent(validTools, gjson.Result{}))
	require.True(t, isGrokFreeCacheFunctionToolIntent(validTools, gjson.Parse(`"none"`)))
	require.True(t, isGrokFreeCacheFunctionToolIntent(gjson.Parse(`[{"type":"web_search"}]`), gjson.Result{}))
}

func TestAppendMissingGrokFreeCacheNativeToolsHandlesExistingAndInvalidTools(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "no tools", body: `{"model":"grok"}`, want: `{"model":"grok"}`},
		{name: "empty tools", body: `{"tools":[]}`, want: `{"tools":[]}`},
		{name: "native only", body: `{"tools":[{"type":"web_search"}]}`, want: `{"tools":[{"type":"web_search"}]}`},
		{name: "unknown tool", body: `{"tools":[{"type":"computer_use"}]}`, want: `{"tools":[{"type":"computer_use"}]}`},
		{name: "malformed function", body: `{"tools":[{"type":"function"}]}`, want: `{"tools":[{"type":"function"}]}`},
		{name: "nested function", body: `{"tools":[{"type":"function","name":"lookup","function":{}}]}`, want: `{"tools":[{"type":"function","name":"lookup","function":{}}]}`},
		{name: "native web plus function", body: `{"tools":[{"type":"web_search"},{"type":"function","name":"lookup"}]}`, want: `{"tools":[{"type":"web_search"},{"type":"function","name":"lookup"},{"type":"x_search"}]}`},
		{name: "both native plus function", body: `{"tools":[{"type":"web_search"},{"type":"x_search"},{"type":"function","name":"lookup"}]}`, want: `{"tools":[{"type":"web_search"},{"type":"x_search"},{"type":"function","name":"lookup"}]}`},
		{name: "duplicate native and function search", body: `{"tools":[{"type":"function","name":"lookup"},{"type":"function","name":"web_search"},{"type":"web_search"}]}`, want: `{"tools":[{"type":"function","name":"lookup"},{"type":"web_search"},{"type":"x_search"}]}`},
		{name: "duplicate function search", body: `{"tools":[{"type":"function","name":"lookup"},{"type":"function","name":"web_search"},{"type":"function","name":"web_search"}]}`, want: `{"tools":[{"type":"function","name":"lookup"},{"type":"web_search"},{"type":"x_search"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := appendMissingGrokFreeCacheNativeTools([]byte(tt.body))
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(body))
		})
	}
}

func TestApplyGrokCacheHeadersAndCLIHeaders(t *testing.T) {
	applyGrokCacheHeaders(nil, "ignored")
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
	unchanged, err := stripGrokChatPromptCacheKey([]byte(`{"model":"grok-4.3"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"grok-4.3"}`, string(unchanged))

	body, err := stripGrokChatPromptCacheKey(bytes.TrimSpace([]byte(`{"model":"grok-4.3","prompt_cache_key":"client-key"}`)))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "prompt_cache_key").Exists())
}
