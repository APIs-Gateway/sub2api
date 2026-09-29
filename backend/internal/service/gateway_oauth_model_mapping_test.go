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

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func setupTokenModelMappingAccount() *Account {
	return &Account{
		ID:          7544,
		Name:        "setup-token-mapping",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeSetupToken,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "test-token",
			"model_mapping": map[string]any{
				"claude-opus-4-6":              "claude-opus-4-7",
				"claude-sonnet-4-5-20250929": "claude-opus-4-8",
				"claude-haiku-4-5-20251001":  "claude-haiku-4-5-20251001",
			},
		},
	}
}

func TestAnthropicOAuthModelMappingAdmissionAndNormalization(t *testing.T) {
	account := setupTokenModelMappingAccount()
	svc := &GatewayService{}
	for _, tc := range []struct {
		requested, upstream string
		mapped              bool
	}{
		{"claude-opus-4-6", "claude-opus-4-7", true},
		{"claude-sonnet-4-5", "claude-opus-4-8", true},
		{"claude-haiku-4-5", "claude-haiku-4-5-20251001", true},
	} {
		t.Run(tc.requested, func(t *testing.T) {
			require.True(t, svc.isModelSupportedByAccount(account, tc.requested))
			model, mapped := resolveAnthropicOAuthMappedModel(account, tc.requested)
			require.Equal(t, tc.upstream, model)
			require.Equal(t, tc.mapped, mapped)
		})
	}
	require.False(t, svc.isModelSupportedByAccount(account, "claude-sonnet-5"), "default mapping remains a whitelist")
	require.False(t, svc.isModelSupportedByAccount(account, "claude-opus-4-5"))
	account.Credentials[ModelMappingAllowUnlistedCredentialKey] = true
	require.False(t, svc.isModelSupportedByAccount(account, "claude-sonnet-5"), "identity entries still restrict the account")
}

func TestModelMappingAllowUnlistedPreservesLegacyAndIdentityWhitelist(t *testing.T) {
	account := setupTokenModelMappingAccount()
	account.Credentials["model_mapping"] = map[string]any{"claude-opus-4-6": "claude-opus-4-7"}
	require.False(t, account.ModelMappingAllowsUnlisted())
	require.False(t, account.IsModelSupported("claude-sonnet-5"))
	account.Credentials[ModelMappingAllowUnlistedCredentialKey] = "true"
	require.False(t, account.ModelMappingAllowsUnlisted(), "only a boolean true opts in")
	account.Credentials[ModelMappingAllowUnlistedCredentialKey] = true
	require.True(t, account.IsModelSupported("claude-sonnet-5"))
	require.Equal(t, "claude-sonnet-5", account.GetMappedModel("claude-sonnet-5"))
	account.Credentials["model_mapping"] = map[string]any{
		"claude-opus-4-6":   "claude-opus-4-7",
		"claude-sonnet-5":   "claude-sonnet-5",
	}
	require.False(t, account.IsModelSupported("claude-haiku-4-5"))
	require.True(t, account.IsModelSupported("claude-sonnet-5"))
	account.Credentials["model_mapping"] = map[string]any{"claude-*": "claude-*"}
	require.False(t, account.IsModelSupported("unlisted-model"), "legacy wildcard identities must fail closed")

	openAI := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"model_mapping": map[string]any{"my-alias": "gpt-6-sol"},
		ModelMappingAllowUnlistedCredentialKey: true,
	}}
	require.True(t, openAI.IsModelSupported("my-alias"))
	require.True(t, openAI.IsModelSupported("gpt-6-luna"))
	require.False(t, openAI.IsModelSupported("glm-5.3"), "OpenAI OAuth retains its platform admission rule")
}

func TestGatewayForwardSetupTokenUsesMappedWireModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct{ requested, upstream string }{
		{"claude-opus-4-6", "claude-opus-4-7"},
		{"claude-sonnet-4-5", "claude-opus-4-8"},
		{"claude-haiku-4-5", "claude-haiku-4-5-20251001"},
	} {
		t.Run(tc.requested, func(t *testing.T) {
			body := []byte(`{"model":"` + tc.requested + `","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{"id":"msg_1","type":"message","role":"assistant","model":"` + tc.upstream + `","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1}}`)),
			}}
			svc := newForwardPartialUsageServiceForTest(upstream)
			result, err := svc.Forward(context.Background(), c, setupTokenModelMappingAccount(), parsed)
			require.NoError(t, err)
			require.Equal(t, tc.upstream, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, tc.upstream, result.UpstreamModel, "usage must retain the final wire model")
		})
	}
}

func TestGatewayCountTokensSetupTokenUsesMappedWireModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"input_tokens":5}`)),
	}}
	svc := newForwardPartialUsageServiceForTest(upstream)
	require.NoError(t, svc.ForwardCountTokens(context.Background(), c, setupTokenModelMappingAccount(), parsed))
	require.Equal(t, "claude-opus-4-8", gjson.GetBytes(upstream.lastBody, "model").String())
}

func TestGatewayBridgeSetupTokenUsesMappedWireModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := setupTokenModelMappingAccount()
	for _, tc := range []struct {
		name, path, body string
		forward          func(*GatewayService, *gin.Context, *Account, []byte) (*ForwardResult, error)
	}{
		{"chat", "/v1/chat/completions", `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`,
			func(s *GatewayService, c *gin.Context, a *Account, body []byte) (*ForwardResult, error) {
				return s.ForwardAsChatCompletions(context.Background(), c, a, body, nil)
			}},
		{"responses", "/v1/responses", `{"model":"claude-sonnet-4-5","input":"hello"}`,
			func(s *GatewayService, c *gin.Context, a *Account, body []byte) (*ForwardResult, error) {
				return s.ForwardAsResponses(context.Background(), c, a, body, nil)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{},
				Body: io.NopCloser(strings.NewReader(namespaceToolAnthropicStream())),
			}}
			svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			result, err := tc.forward(svc, c, account, []byte(tc.body))
			require.NoError(t, err)
			require.Equal(t, "claude-opus-4-8", gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, "claude-opus-4-8", result.UpstreamModel)
		})
	}
}

func TestGetAvailableModelsMappingWithoutWhitelistIncludesDefaults(t *testing.T) {
	account := *setupTokenModelMappingAccount()
	account.Credentials = map[string]any{
		"model_mapping": map[string]any{"custom-opus": "claude-opus-4-8"},
		ModelMappingAllowUnlistedCredentialKey: true,
	}
	repo := &modelsListAccountRepoStub{all: []Account{account}}
	svc := &GatewayService{accountRepo: repo, modelsListCache: gocache.New(time.Minute, time.Minute), modelsListCacheTTL: time.Minute}
	models := svc.GetAvailableModels(context.Background(), nil, PlatformAnthropic)
	require.Contains(t, models, "custom-opus")
	for _, id := range claude.DefaultModelIDs() {
		require.Contains(t, models, id)
	}
	account.Credentials["model_mapping"] = map[string]any{"custom-opus": "claude-opus-4-8", "claude-sonnet-5": "claude-sonnet-5"}
	repo.all = []Account{account}
	svc.InvalidateAvailableModelsCache(nil, PlatformAnthropic)
	models = svc.GetAvailableModels(context.Background(), nil, PlatformAnthropic)
	require.Contains(t, models, "claude-sonnet-5")
	require.NotContains(t, models, "claude-opus-5")
	account.Credentials["model_mapping"] = map[string]any{"claude-*": "claude-*"}
	repo.all = []Account{account}
	svc.InvalidateAvailableModelsCache(nil, PlatformAnthropic)
	models = svc.GetAvailableModels(context.Background(), nil, PlatformAnthropic)
	require.Contains(t, models, "claude-*")
	require.NotContains(t, models, "claude-opus-4-7", "wildcard identity must not expose unrelated defaults")
}
