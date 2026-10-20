package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexRequestBodyIdentityNamespaceIsStablePerOAuthAccount(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-codex","prompt_cache_key":"client-session","client_metadata":{"x-codex-installation-id":"client-installation","session_id":"client-session","thread_id":"client-thread","x-codex-window-id":"client-window","x-codex-turn-metadata":"{\"installation_id\":\"client-installation\",\"session_id\":\"client-session\",\"thread_id\":\"client-thread\",\"turn_id\":\"client-turn\",\"window_id\":\"client-window\"}"}}`)
	account11 := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-11"}}
	account19 := &Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-19"}}

	first, changed, err := applyCodexAccountIdentityClientMetadataRaw(body, account11, 77)
	require.NoError(t, err)
	require.True(t, changed)
	firstAgain, changed, err := applyCodexAccountIdentityClientMetadataRaw(body, account11, 77)
	require.NoError(t, err)
	require.True(t, changed)
	second, changed, err := applyCodexAccountIdentityClientMetadataRaw(body, account19, 77)
	require.NoError(t, err)
	require.True(t, changed)
	require.JSONEq(t, string(first), string(firstAgain))

	paths := []string{
		"prompt_cache_key",
		"client_metadata.x-codex-installation-id",
		"client_metadata.session_id",
		"client_metadata.thread_id",
		"client_metadata.x-codex-window-id",
	}
	for _, path := range paths {
		require.NotEqual(t, gjson.GetBytes(body, path).String(), gjson.GetBytes(first, path).String(), path)
		require.NotEqual(t, gjson.GetBytes(first, path).String(), gjson.GetBytes(second, path).String(), path)
	}
	require.Equal(t, gjson.GetBytes(first, "prompt_cache_key").String(), gjson.GetBytes(first, "client_metadata.session_id").String())

	var embeddedFirst map[string]any
	var embeddedSecond map[string]any
	require.NoError(t, json.Unmarshal([]byte(gjson.GetBytes(first, "client_metadata.x-codex-turn-metadata").String()), &embeddedFirst))
	require.NoError(t, json.Unmarshal([]byte(gjson.GetBytes(second, "client_metadata.x-codex-turn-metadata").String()), &embeddedSecond))
	for _, field := range []string{"installation_id", "session_id", "thread_id", "turn_id", "window_id"} {
		require.NotEqual(t, embeddedFirst[field], embeddedSecond[field], field)
	}
}

func TestCodexAccountIdentityNamespaceUsesStableCredentialSource(t *testing.T) {
	firstRow := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "shared-upstream-account"}}
	secondRow := &Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "shared-upstream-account"}}
	require.Equal(t, codexAccountIdentityNamespace(firstRow), codexAccountIdentityNamespace(secondRow))

	firstUser := &Account{ID: 20, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "team-account", "chatgpt_user_id": "user-1"}}
	sameUser := &Account{ID: 21, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "team-account", "chatgpt_user_id": "user-1"}}
	secondUser := &Account{ID: 22, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "team-account", "chatgpt_user_id": "user-2"}}
	require.Equal(t, codexAccountIdentityNamespace(firstUser), codexAccountIdentityNamespace(sameUser))
	require.NotEqual(t, codexAccountIdentityNamespace(firstUser), codexAccountIdentityNamespace(secondUser))

	// Local row IDs repeat across independent deployments, so they are not a
	// safe fallback for upstream identity.
	require.Empty(t, codexAccountIdentityNamespace(&Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth}))

	setupTokenA := &Account{ID: 30, Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Credentials: map[string]any{"access_token": "setup-token-a"}}
	setupTokenADuplicate := &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Credentials: map[string]any{"access_token": "setup-token-a"}}
	setupTokenB := &Account{ID: 32, Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Credentials: map[string]any{"access_token": "setup-token-b"}}
	setupNamespace := codexAccountIdentityNamespace(setupTokenA)
	require.NotEmpty(t, setupNamespace)
	require.NotContains(t, setupNamespace, "setup-token-a")
	require.Equal(t, setupNamespace, codexAccountIdentityNamespace(setupTokenADuplicate))
	require.NotEqual(t, setupNamespace, codexAccountIdentityNamespace(setupTokenB))
}

func TestCodexAccountIdentityHelpersPreserveUnscopedAndMalformedInputs(t *testing.T) {
	service := &OpenAIGatewayService{}
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "account-1"}}
	unscoped := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	resolved := service.prepareCodexAccountIdentitySource(nil, oauth)
	require.Same(t, oauth, resolved)
	require.Same(t, oauth, codexAccountIdentitySource(nil, oauth))
	require.Nil(t, codexAccountIdentitySource(nil, nil))

	require.Empty(t, codexAccountIdentityNamespace(nil))
	require.Empty(t, codexAccountIdentityNamespace(&Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}))
	require.Empty(t, codexAccountIdentityNamespace(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}))
	require.Empty(t, codexAccountIdentityNamespace(&Account{Platform: PlatformOpenAI, Type: AccountTypeSetupToken}))
	require.Empty(t, isolateOpenAIUpstreamSessionID(7, oauth, " "))
	require.Equal(t, isolateOpenAISessionID(7, "client-session"), isolateOpenAIUpstreamSessionID(7, unscoped, "client-session"))
	require.Equal(t, "", scopeCodexAccountIdentityValue(oauth, 7, "session", " "))
	require.Equal(t, "client-session", scopeCodexAccountIdentityValue(unscoped, 7, "session", "client-session"))

	require.False(t, applyCodexAccountIdentityFields(nil, oauth, 7))
	fields := map[string]any{"session_id": 7, "thread_id": " ", "turn_id": "client-turn"}
	require.True(t, applyCodexAccountIdentityFields(fields, oauth, 7))
	scopedTurnID, ok := fields["turn_id"].(string)
	require.True(t, ok)
	require.Equal(t, scopeCodexAccountIdentityValue(oauth, 7, "turn", "client-turn"), scopedTurnID)
	require.NotEqual(t, "client-turn", scopedTurnID)
	require.False(t, applyCodexAccountIdentityEmbeddedMetadata(map[string]any{}, oauth, 7))
	require.False(t, applyCodexAccountIdentityEmbeddedMetadata(map[string]any{openAIWSTurnMetadataHeader: "not-json"}, oauth, 7))
	require.False(t, applyCodexAccountIdentityEmbeddedMetadata(map[string]any{openAIWSTurnMetadataHeader: `{"unrelated":"value"}`}, oauth, 7))

	request := map[string]any{"client_metadata": map[string]any{"session_id": "client-session"}, "prompt_cache_key": "client-session"}
	require.True(t, applyCodexAccountIdentityClientMetadataMap(request, oauth, 7))
	clientMetadata, ok := request["client_metadata"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, request["prompt_cache_key"], clientMetadata["session_id"])
	requestWithTurnMetadata := map[string]any{"client_metadata": map[string]any{openAIWSTurnMetadataHeader: `{"turn_id":"client-turn"}`}}
	require.True(t, applyCodexAccountIdentityClientMetadataMap(requestWithTurnMetadata, oauth, 7))
	metadataRaw, ok := requestWithTurnMetadata["client_metadata"].(map[string]any)[openAIWSTurnMetadataHeader].(string)
	require.True(t, ok)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(metadataRaw), &metadata))
	require.Equal(t, scopeCodexAccountIdentityValue(oauth, 7, "turn", "client-turn"), metadata["turn_id"])
	require.False(t, applyCodexAccountIdentityClientMetadataMap(nil, oauth, 7))
	require.False(t, applyCodexAccountIdentityClientMetadataMap(map[string]any{"prompt_cache_key": "client-session"}, unscoped, 7))

	unchanged, changed, err := applyCodexAccountIdentityClientMetadataRaw([]byte("[]"), oauth, 7)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, "[]", string(unchanged))
	unchanged, changed, err = applyCodexAccountIdentityClientMetadataRaw([]byte(`{"prompt_cache_key":"client-session"}`), unscoped, 7)
	require.NoError(t, err)
	require.False(t, changed)
	require.JSONEq(t, `{"prompt_cache_key":"client-session"}`, string(unchanged))

	applyCodexAccountIdentityHeaders(nil, oauth, 7)
	headers := make(http.Header)
	headers.Set("session-id", "client-session")
	headers.Set(openAIWSTurnMetadataHeader, "not-json")
	applyCodexAccountIdentityHeaders(headers, oauth, 7)
	require.NotEqual(t, "client-session", headers.Get("session-id"))
	require.Equal(t, "not-json", headers.Get(openAIWSTurnMetadataHeader))
}

func TestCodexAccountIdentitySourceOverwritesFailoverContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	first := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"chatgpt_account_id": "team-account",
		"chatgpt_user_id":    "user-1",
	}}
	service := &OpenAIGatewayService{}

	resolved := service.prepareCodexAccountIdentitySource(c, first)
	require.Same(t, first, resolved)
	require.Same(t, first, codexAccountIdentitySource(c, nil))

	req, err := service.buildUpstreamRequest(
		context.Background(), c, first,
		[]byte(`{"model":"gpt-5.6-codex","stream":true,"prompt_cache_key":"client-session"}`),
		"token", true, "client-session", true,
	)
	require.NoError(t, err)
	require.Equal(t, isolateOpenAIUpstreamSessionID(0, first, "client-session"), req.Header.Get("session_id"))

	next := &Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"chatgpt_account_id": "other-account",
		"chatgpt_user_id":    "user-2",
	}}
	resolved = service.prepareCodexAccountIdentitySource(c, next)
	require.Same(t, next, resolved)
	require.Same(t, next, codexAccountIdentitySource(c, first))
}

func TestCodexAccountIdentityFailoverReprojectsWSV2PassthroughPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 77})

	service := &OpenAIGatewayService{}
	first := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"chatgpt_account_id": "primary-oauth-account",
	}}
	next := &Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"chatgpt_account_id": "failover-oauth-account",
	}}
	clientFrame := []byte(`{"type":"response.create","prompt_cache_key":"client-session","client_metadata":{"session_id":"client-session","thread_id":"client-thread","x-codex-turn-metadata":"{\"turn_id\":\"client-turn\"}"}}`)

	project := func(account *Account) []byte {
		t.Helper()
		service.prepareCodexAccountIdentitySource(c, account)
		projected, changed, err := applyCodexAccountIdentityClientMetadataRaw(clientFrame, codexAccountIdentitySource(c, nil), getAPIKeyIDFromContext(c))
		require.NoError(t, err)
		require.True(t, changed)
		return projected
	}

	primary := project(first)
	failover := project(next)
	require.Same(t, next, codexAccountIdentitySource(c, first), "the next selected OAuth account must replace the prior attempt source")
	for _, path := range []string{
		"prompt_cache_key",
		"client_metadata.session_id",
		"client_metadata.thread_id",
		"client_metadata.x-codex-turn-metadata",
	} {
		require.NotEqual(t, gjson.GetBytes(primary, path).String(), gjson.GetBytes(failover, path).String(), path)
	}
	require.Equal(t, scopeCodexAccountIdentityValue(next, 77, "session", "client-session"), gjson.GetBytes(failover, "prompt_cache_key").String())
	require.Equal(t, scopeCodexAccountIdentityValue(next, 77, "thread", "client-thread"), gjson.GetBytes(failover, "client_metadata.thread_id").String())
	turnMetadata := gjson.GetBytes(failover, "client_metadata.x-codex-turn-metadata").String()
	require.Equal(t, scopeCodexAccountIdentityValue(next, 77, "turn", "client-turn"), gjson.Get(turnMetadata, "turn_id").String())
}

func TestBuildOpenAIWSHeadersNamespacesCodexIdentityByOAuthAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Set("api_key_id", int64(77))
	c.Request.Header.Set("x-codex-installation-id", "client-installation")
	c.Request.Header.Set("thread-id", "client-thread")
	c.Request.Header.Set("x-codex-window-id", "client-window")
	c.Request.Header.Set("x-client-request-id", "client-request")

	account11 := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-11"}}
	account19 := &Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-19"}}
	service := &OpenAIGatewayService{}
	build := func(account *Account) http.Header {
		headers, _ := service.buildOpenAIWSHeaders(
			context.Background(), c, account, "token",
			OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
			true, "", "", "client-session", "", "",
		)
		return headers
	}

	first := build(account11)
	firstAgain := build(account11)
	second := build(account19)
	for _, header := range []string{"session_id", "x-codex-installation-id", "x-codex-window-id"} {
		require.NotEmpty(t, first.Get(header), header)
		require.Equal(t, first.Get(header), firstAgain.Get(header), header)
		require.NotEqual(t, first.Get(header), second.Get(header), header)
	}

	httpRequest, err := service.buildUpstreamRequest(
		context.Background(), c, account11,
		[]byte(`{"model":"gpt-5.6-codex","stream":true,"prompt_cache_key":"client-session"}`),
		"token", true, "client-session", true,
	)
	require.NoError(t, err)
	require.Equal(t, httpRequest.Header.Get("session_id"), first.Get("session_id"), "HTTP and WS must derive the same identity from the raw client key")
}

func TestBuildUpstreamRequestNamespacesCodexIdentityByOAuthAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}
	body := []byte(`{"model":"gpt-5.6-codex","stream":true,"prompt_cache_key":"client-session"}`)

	build := func(accountID int64, chatgptAccountID string) http.Header {
		t.Helper()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		c.Set("api_key", &APIKey{ID: 77})
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.0")
		c.Request.Header.Set("x-codex-installation-id", "client-installation")
		c.Request.Header.Set("x-codex-window-id", "client-window")
		c.Request.Header.Set("session-id", "client-session")
		c.Request.Header.Set("thread-id", "client-thread")
		c.Request.Header.Set("x-client-request-id", "client-request")
		c.Request.Header.Set("x-codex-turn-metadata", `{"installation_id":"client-installation","session_id":"client-session","thread_id":"client-thread","turn_id":"client-turn","window_id":"client-window"}`)

		account := &Account{
			ID:       accountID,
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"chatgpt_account_id": chatgptAccountID,
			},
		}
		req, err := svc.buildUpstreamRequest(
			context.Background(), c, account, body, "oauth-token", true, "client-session", true,
		)
		require.NoError(t, err)
		return req.Header
	}

	first := build(11, "chatgpt-account-11")
	firstAgain := build(11, "chatgpt-account-11")
	second := build(19, "chatgpt-account-19")

	identityHeaders := []string{
		"x-codex-installation-id",
		"x-codex-window-id",
		"session-id",
		"session_id",
		"conversation_id",
		"thread-id",
		"x-client-request-id",
		"x-codex-turn-metadata",
	}
	checked := 0
	for _, header := range identityHeaders {
		if first.Get(header) == "" && second.Get(header) == "" {
			continue
		}
		checked++
		require.NotEmpty(t, first.Get(header), header)
		require.Equal(t, first.Get(header), firstAgain.Get(header), "same account must retain stable identity: %s", header)
		require.NotEqual(t, first.Get(header), second.Get(header), "account failover must rotate upstream identity: %s", header)
	}
	require.GreaterOrEqual(t, checked, 5, "test must exercise the real outbound identity surface")
}
