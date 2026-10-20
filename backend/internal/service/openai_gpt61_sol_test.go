//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGPT61SolModelNormalization(t *testing.T) {
	for _, model := range []string{"gpt-6.1-sol", "openai/gpt-6.1-sol-max", "GPT_6.1_SOL", "gpt-6.1-sol-openai-compact", "gpt-6.1-sol-2026-09-29"} {
		require.Equal(t, "gpt-6.1-sol", normalizeKnownOpenAICodexModel(model), model)
		require.True(t, isOpenAIGPT6Model(model), model)
		require.True(t, shouldAutoInjectPromptCacheKeyForCompat(model), model)
	}
	require.Equal(t, "gpt-6-sol", normalizeKnownOpenAICodexModel("gpt-6-sol-max"), "GPT-6 Sol must not be folded into 6.1")
	require.False(t, shouldAutoInjectPromptCacheKeyForCompat("gpt-6.1-solitude"))
	require.Equal(t, "max", normalizeOpenAIReasoningEffortForModel("max", "gpt-6.1-sol"))
	require.Equal(t, "gpt-6.1-sol", NormalizeOpenAICompatRequestedModel("openai/gpt-6.1-sol-xhigh"))
}

func TestGPT61SolFallbackPricing(t *testing.T) {
	svc := newTestBillingService()
	for _, model := range []string{"gpt-6.1-sol", "openai/gpt-6.1-sol-max"} {
		pricing, err := svc.GetModelPricing(model)
		require.NoError(t, err, model)
		require.InDelta(t, 2e-6, pricing.InputPricePerToken, 1e-15, model)
		require.InDelta(t, 10e-6, pricing.OutputPricePerToken, 1e-15, model)
		require.InDelta(t, 2.5e-6, pricing.CacheCreationPricePerToken, 1e-15, model)
		require.InDelta(t, 0.1e-6, pricing.CacheReadPricePerToken, 1e-15, model)
		require.InDelta(t, 0.2e-6, pricing.CacheReadPricePerTokenPriority, 1e-15, model)
	}
	sol, err := svc.GetModelPricing("gpt-6-sol")
	require.NoError(t, err)
	require.InDelta(t, 0.2e-6, sol.CacheReadPricePerToken, 1e-15, "GPT-6 Sol keeps its own cache read price")
}

func TestGPT61SolDynamicPricingLookup(t *testing.T) {
	entry := &LiteLLMModelPricing{InputCostPerToken: 1}
	sol := &LiteLLMModelPricing{InputCostPerToken: 2}
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{"gpt-6.1-sol": entry, "gpt-6-sol": sol}}
	for _, model := range []string{"gpt-6.1-sol", "gpt-6.1-sol-max", "openai/gpt-6.1-sol-openai-compact"} {
		require.Same(t, entry, svc.GetModelPricing(model), model)
	}
	require.Same(t, sol, svc.GetModelPricing("gpt-6-sol-max"))

	empty := &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}
	require.Same(t, openAIGPT61SolFallbackPricing, empty.matchOpenAIModel("gpt-6.1-sol-high"))
}

func TestGPT61SolResponsesSamplingRemoved(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-sol","temperature":0.5,"top_p":0.9,"logprobs":true,"top_logprobs":2,"reasoning":{"effort":"max"}}`)
	out, changed, err := normalizeGPT6ResponsesSampling(body, "gpt-6.1-sol")
	require.NoError(t, err)
	require.True(t, changed)
	for _, field := range []string{"temperature", "top_p", "logprobs", "top_logprobs"} {
		require.False(t, gjson.GetBytes(out, field).Exists(), field)
	}
	require.Equal(t, "max", gjson.GetBytes(out, "reasoning.effort").String())
}

func TestGPT61SolCompatValidation(t *testing.T) {
	for _, body := range []string{
		`{"model":"public","reasoning_effort":"none"}`,
		`{"model":"public","reasoning":{"effort":"minimal"}}`,
		`{"model":"public","output_config":{"effort":"none"}}`,
		`{"model":"public","thinking":{"type":"disabled"}}`,
		`{"model":"gpt-6.1-sol-minimal"}`,
	} {
		require.Error(t, validateGPT61SolCompatRequest([]byte(body), "gpt-6.1-sol"), body)
		require.NoError(t, validateGPT61SolCompatRequest([]byte(body), "gpt-6-sol"), body)
	}
	require.NoError(t, validateGPT61SolCompatRequest([]byte(`{"model":"public","reasoning_effort":"max"}`), "gpt-6.1-sol"))
}

func TestGPT61SolAnthropicEffortSuffixPreserved(t *testing.T) {
	for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		req := &apicompat.AnthropicRequest{Model: "openai/gpt-6.1-sol-" + effort}
		applyOpenAICompatModelNormalization(req)
		require.Equal(t, "gpt-6.1-sol", req.Model, effort)
		require.Equal(t, effort, req.OutputConfig.Effort, effort)
		_, err := apicompat.AnthropicToResponses(req)
		if effort == "none" || effort == "minimal" {
			require.Error(t, err, effort)
		} else {
			require.NoError(t, err, effort)
		}
	}

	explicit := &apicompat.AnthropicRequest{Model: "gpt-6.1-sol-max", OutputConfig: &apicompat.AnthropicOutputConfig{Effort: "low"}}
	applyOpenAICompatModelNormalization(explicit)
	require.Equal(t, "low", explicit.OutputConfig.Effort, "explicit output_config.effort wins over the suffix")

	compact := &apicompat.AnthropicRequest{Model: "gpt-6.1-sol-openai-compact"}
	applyOpenAICompatModelNormalization(compact)
	require.Nil(t, compact.OutputConfig, "compact suffix carries no effort")
}

func TestGPT61SolChatOnlyPathsRejectBeforeForwarding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"public": "gpt-6.1-sol"}}}
	newCtx := func(path, body string) (*gin.Context, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		return c, rec
	}
	svc := &OpenAIGatewayService{cfg: &config.Config{}}

	for _, body := range []string{
		`{"model":"public","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"public","tools":[{"type":"function","function":{"name":"lookup"}}],"messages":[{"role":"user","content":"hi"}]}`,
	} {
		c, rec := newCtx("/v1/chat/completions", body)
		_, err := svc.forwardAsRawChatCompletions(context.Background(), c, account, []byte(body), "")
		require.Error(t, err, body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
		require.Contains(t, rec.Body.String(), "gpt-6.1-sol", body)
	}

	for _, body := range []string{
		`{"model":"public","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`,
		`{"model":"public","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`,
	} {
		c, rec := newCtx("/v1/messages", body)
		_, err := svc.forwardAnthropicViaRawChatCompletions(context.Background(), c, account, []byte(body), "")
		require.Error(t, err, body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
		require.Contains(t, rec.Body.String(), "gpt-6.1-sol", body)
	}

	for _, body := range []string{
		`{"model":"public","input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`,
		`{"model":"public","input":"hi","reasoning":{"effort":"none"}}`,
	} {
		c, rec := newCtx("/v1/responses", body)
		_, err := svc.forwardResponsesViaRawChatCompletions(context.Background(), c, account, []byte(body))
		require.Error(t, err, body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
		require.Contains(t, rec.Body.String(), "gpt-6.1-sol", body)
	}
}
