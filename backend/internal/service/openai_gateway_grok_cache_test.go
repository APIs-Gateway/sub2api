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

func TestGrokFreeCacheRoutePreservesToolChoiceNoneAndSupportsNativeCompanions(t *testing.T) {
	freeAccount := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"subscription_tier": "free"},
	}

	noneBody := []byte(`{"model":"grok-4.3","tools":[{"type":"function","name":"lookup"}],"tool_choice":"none"}`)
	noneResult, err := applyGrokFreeMessagesFunctionToolCacheRoute(noneBody, noneBody, freeAccount, "isolated-key")
	require.NoError(t, err)
	require.Equal(t, 3, len(gjson.GetBytes(noneResult, "tools").Array()))
	require.Equal(t, "none", gjson.GetBytes(noneResult, "tool_choice").String())

	withNative := []byte(`{"model":"grok-4.3","tools":[{"type":"web_search"},{"type":"code_execution"}]}`)
	nativeResult, err := applyGrokFreeToolCacheRoute(withNative, withNative, freeAccount, "isolated-key", false, false)
	require.NoError(t, err)
	require.Equal(t, 3, len(gjson.GetBytes(nativeResult, "tools").Array()))
	require.Equal(t, "x_search", gjson.GetBytes(nativeResult, "tools.2.type").String())

	pureClient := []byte(`{"model":"grok-4.3","tools":[{"type":"function","name":"lookup"}]}`)
	pureResult, err := applyGrokFreeToolCacheRoute(pureClient, pureClient, freeAccount, "isolated-key", false, false)
	require.NoError(t, err)
	require.JSONEq(t, string(pureClient), string(pureResult))

	clientSearch, err := appendGrokFreeCacheNativeToolsWithPolicy(
		[]byte(`{"tools":[{"type":"function","name":"web_search"},{"type":"function","name":"lookup"}]}`),
		true,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, "function", gjson.GetBytes(clientSearch, "tools.0.type").String())
	require.Equal(t, "x_search", gjson.GetBytes(clientSearch, "tools.2.type").String())
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
			body, err := appendGrokFreeCacheNativeToolsWithPolicy([]byte(tt.body), false, true)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(body))
		})
	}
}

func TestGrokFreeRequestToolCacheRouteAutoOptsInClaudeDesktop(t *testing.T) {
	account := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"subscription_tier": "free"},
	}
	body := []byte(`{"model":"grok-4.3","tools":[{"type":"function","name":"Read"},{"type":"function","name":"Edit"}],"tool_choice":"auto"}`)

	for _, xApp := range []string{"cli", "cli-bg"} {
		t.Run(xApp, func(t *testing.T) {
			c := newGrokCacheTestContext(90141, "/v1/responses", map[string]string{
				"User-Agent":                "claude-cli/2.1.215 (external, future-desktop, agent-sdk/0.3.215)",
				"X-App":                     xApp,
				"anthropic-client-platform": "desktop_app",
				"X-Claude-Code-Session-Id":  "desktop-session-1",
			})

			patched, err := applyGrokFreeRequestToolCacheRoute(c, body, body, account, "isolated-id")

			require.NoError(t, err)
			tools := gjson.GetBytes(patched, "tools").Array()
			require.Len(t, tools, 4)
			require.Equal(t, "Read", tools[0].Get("name").String())
			require.Equal(t, "Edit", tools[1].Get("name").String())
			require.Equal(t, "web_search", tools[2].Get("type").String())
			require.Equal(t, "x_search", tools[3].Get("type").String())
		})
	}
}

func TestGrokFreeRequestToolCacheRouteRequiresClaudeDesktopFingerprint(t *testing.T) {
	account := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"subscription_tier": "free"},
	}
	body := []byte(`{"model":"grok-4.3","tools":[{"type":"function","name":"Read"}],"tool_choice":"auto"}`)
	baseHeaders := map[string]string{
		"User-Agent":                "claude-cli/2.1.215 (external, claude-desktop-3p, agent-sdk/0.3.215)",
		"X-App":                     "cli",
		"anthropic-client-platform": "desktop_app",
		"X-Claude-Code-Session-Id":  "desktop-session-1",
	}
	tests := []struct {
		name    string
		path    string
		remove  string
		replace map[string]string
	}{
		{name: "chat path", path: "/v1/chat/completions"},
		{name: "compact path", path: "/v1/responses/compact"},
		{name: "missing user agent", path: "/v1/responses", remove: "User-Agent"},
		{name: "wrong app", path: "/v1/responses", replace: map[string]string{"X-App": "desktop"}},
		{name: "missing platform", path: "/v1/responses", remove: "anthropic-client-platform"},
		{name: "wrong platform", path: "/v1/responses", replace: map[string]string{"anthropic-client-platform": "web"}},
		{name: "missing session", path: "/v1/responses", remove: "X-Claude-Code-Session-Id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(map[string]string, len(baseHeaders))
			for name, value := range baseHeaders {
				headers[name] = value
			}
			delete(headers, tt.remove)
			for name, value := range tt.replace {
				headers[name] = value
			}
			c := newGrokCacheTestContext(90142, tt.path, headers)

			patched, err := applyGrokFreeRequestToolCacheRoute(c, body, body, account, "isolated-id")

			require.NoError(t, err)
			require.JSONEq(t, string(body), string(patched))
		})
	}
}

func TestGrokFreeRequestToolCacheRouteHonorsExplicitRequestPolicy(t *testing.T) {
	freeAccount := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"subscription_tier": "free"},
	}
	body := []byte(`{"model":"grok-4.3","tools":[{"type":"function","name":"lookup"}],"tool_choice":"auto"}`)

	optIn := newGrokCacheTestContext(90143, "/v1/responses", map[string]string{
		grokClientToolCacheOptInHeader: "prefer-cache",
	})
	patched, err := applyGrokFreeRequestToolCacheRoute(optIn, body, body, freeAccount, "isolated-id")
	require.NoError(t, err)
	require.Len(t, gjson.GetBytes(patched, "tools").Array(), 3)

	accountOptIn := *freeAccount
	accountOptIn.Extra = map[string]any{grokClientToolCacheOptInExtraKey: true}
	requestOptOut := newGrokCacheTestContext(90144, "/v1/responses", map[string]string{
		"User-Agent":                   "claude-cli/2.1.215 (external, claude-desktop-3p, agent-sdk/0.3.215)",
		"X-App":                        "cli",
		"anthropic-client-platform":    "desktop_app",
		"X-Claude-Code-Session-Id":     "desktop-session-1",
		grokClientToolCacheOptInHeader: "off",
	})
	patched, err = applyGrokFreeRequestToolCacheRoute(requestOptOut, body, body, &accountOptIn, "isolated-id")
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(patched))

	paidAccount := *freeAccount
	paidAccount.Credentials = map[string]any{"subscription_tier": "supergrok"}
	claHeaders := map[string]string{
		"User-Agent":                "claude-cli/2.1.215 (external, claude-desktop-3p, agent-sdk/0.3.215)",
		"X-App":                     "cli",
		"anthropic-client-platform": "desktop_app",
		"X-Claude-Code-Session-Id":  "desktop-session-1",
	}
	paid := newGrokCacheTestContext(90145, "/v1/responses", claHeaders)
	patched, err = applyGrokFreeRequestToolCacheRoute(paid, body, body, &paidAccount, "isolated-id")
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(patched))

	unchanged, err := applyGrokFreeRequestToolCacheRoute(nil, body, body, freeAccount, "isolated-id")
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(unchanged))
}

func TestForwardGrokResponsesClaudeDesktopUsesRequestCacheRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{
		"model":"grok","stream":false,"instructions":"You are Claude Desktop.",
		"tools":[
			{"type":"function","name":"Read","parameters":{"type":"object"}},
			{"type":"function","name":"Edit","parameters":{"type":"object"}}
		],
		"input":[{"role":"user","content":[{"type":"input_text","text":"first turn"}]}]
	}`)
	c := newGrokCacheTestContext(4504, "/v1/responses", map[string]string{
		"User-Agent":                "claude-cli/2.1.215 (external, claude-desktop-3p, agent-sdk/0.3.215)",
		"X-App":                     "cli",
		"anthropic-client-platform": "desktop_app",
		"X-Claude-Code-Session-Id":  "claude-desktop-session",
	})
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.215 (external, claude-desktop-3p, agent-sdk/0.3.215)")
	c.Request.Header.Set("X-App", "cli")
	c.Request.Header.Set("anthropic-client-platform", "desktop_app")
	c.Request.Header.Set("X-Claude-Code-Session-Id", "claude-desktop-session")
	c.Set("api_key", &APIKey{ID: 4504})

	account := grokProtocolOAuthAccount(4504)
	account.Credentials["subscription_tier"] = "free"
	repo := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_claude_desktop_1","object":"response","model":"grok-4.3","status":"completed",
			"output":[],"usage":{"input_tokens":30000,"output_tokens":10,"input_tokens_details":{"cached_tokens":0}}
		}`)),
	}}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil, nil),
		accountRepo:       repo,
	}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok", false, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 1)
	tools := gjson.GetBytes(upstream.bodies[0], "tools").Array()
	require.Len(t, tools, 4)
	require.Equal(t, "Read", tools[0].Get("name").String())
	require.Equal(t, "Edit", tools[1].Get("name").String())
	require.Equal(t, "web_search", tools[2].Get("type").String())
	require.Equal(t, "x_search", tools[3].Get("type").String())
	require.Empty(t, upstream.requests[0].Header.Get("X-App"))
	require.Empty(t, upstream.requests[0].Header.Get("anthropic-client-platform"))
	require.Empty(t, upstream.requests[0].Header.Get("X-Claude-Code-Session-Id"))
	require.NotEmpty(t, gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").String())
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
