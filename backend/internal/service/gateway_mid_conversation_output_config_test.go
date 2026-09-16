//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSanitizeAnthropicBodyForBetaTokens_MessageOutputConfigStrippedOnlyWhenBetaMissing(t *testing.T) {
	body := []byte(`{
		"output_config":{"effort":"high"},
		"messages":[
			{"role":"system","content":[],"output_config":{"effort":"high"}},
			{"role":"system","content":"retain this","output_config":{"effort":"medium"}},
			{"role":"user","content":"hello","output_config":{"effort":"low"}},
			{"role":"assistant","content":[{"type":"text","text":"reply"}],"output_config":{"effort":"low"}},
			{"role":"user","content":"untouched"}
		]
	}`)

	out, changed := sanitizeAnthropicBodyForBetaTokens(body, claude.BetaContextManagement)
	require.True(t, changed)
	require.Equal(t, "high", gjson.GetBytes(out, "output_config.effort").String(),
		"top-level output_config is not guarded by the message beta")

	messages := gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 4, "empty system control message must be removed")
	require.Equal(t, "system", messages[0].Get("role").String())
	require.Equal(t, "retain this", messages[0].Get("content").String())
	for _, message := range messages {
		require.False(t, message.Get("output_config").Exists())
	}
	require.Equal(t, "hello", messages[1].Get("content").String())
	require.Equal(t, "reply", messages[2].Get("content.0.text").String())
	require.Equal(t, "untouched", messages[3].Get("content").String())
}

func TestSanitizeAnthropicBodyForBetaTokens_MessageOutputConfigKeptWhenBetaPresent(t *testing.T) {
	body := []byte(`{"output_config":{"effort":"high"},"messages":[{"role":"system","content":[],"output_config":{"effort":"high"}},{"role":"user","content":"hello","output_config":{"effort":"low"}}]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, claude.BetaMidConversationOutputConfig)
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
	require.True(t, gjson.GetBytes(out, "messages.0.output_config").Exists())
	require.Equal(t, "high", gjson.GetBytes(out, "output_config.effort").String())
}

func TestSanitizeAnthropicBodyForBetaTokens_TopLevelOutputConfigIsByteNoop(t *testing.T) {
	body := []byte(`{"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hello"}]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

func TestBuildUpstreamRequestOAuthMimic_PreservesMessageOutputConfigWithInjectedBeta(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	account := &Account{ID: 701, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-tok"}, Status: StatusActive, Schedulable: true}
	body := []byte(`{"model":"claude-opus-5","output_config":{"effort":"high"},"messages":[{"role":"system","content":[],"output_config":{"effort":"high"}},{"role":"user","content":"hello"}]}`)

	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body, "oauth-tok", "oauth", "claude-opus-5", false, true,
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	outBeta := getHeaderRaw(req.Header, "anthropic-beta")
	require.True(t, anthropicBetaTokensContains(outBeta, claude.BetaMidConversationOutputConfig))
	require.True(t, gjson.GetBytes(outBody, "messages.0.output_config").Exists())
	require.Equal(t, "high", gjson.GetBytes(outBody, "output_config.effort").String())
}

func TestBuildUpstreamRequestAnthropicAPIKeyPassthrough_StripsMessageOutputConfigWithoutClientBeta(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
	body := []byte(`{"output_config":{"effort":"high"},"messages":[{"role":"system","content":[],"output_config":{"effort":"high"}},{"role":"user","content":"hello"}]}`)

	svc := &GatewayService{cfg: &config.Config{}}
	req, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(
		context.Background(), c, newAnthropicAPIKeyPassthroughAccountForBetaTest(), body, "token",
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	require.Len(t, gjson.GetBytes(outBody, "messages").Array(), 1)
	require.Equal(t, "hello", gjson.GetBytes(outBody, "messages.0.content").String())
	require.Equal(t, "high", gjson.GetBytes(outBody, "output_config.effort").String())
}
