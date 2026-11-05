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
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// GPT-6.1 Terra 支持同步自上游 342366742（Wei-Shaw/sub2api#8364）。

func TestGPT61TerraIdentityAndEffort(t *testing.T) {
	for _, effort := range []string{"none", "low", "medium", "high", "xhigh", "max"} {
		model := "openai/GPT-6.1-Terra-" + effort
		require.True(t, openai.IsGPT61TerraModelSpelling(model))
		require.False(t, openai.IsGPT61SolModelSpelling(model))
		require.Equal(t, "gpt-6.1-terra", normalizeCodexModel(model))
		require.Equal(t, "gpt-6.1-terra", normalizeKnownOpenAICodexModel(model))
		require.Equal(t, "gpt-6.1-terra", NormalizeOpenAICompatRequestedModel(model))
		require.Equal(t, "gpt-6.1-terra", ModelFamily(model))
		require.True(t, isOpenAIGPT6Model(model))
		require.True(t, shouldAutoInjectPromptCacheKeyForCompat(model))
		require.Equal(t, effort, normalizeOpenAIReasoningEffortForModel(effort, model))

		request := &apicompat.AnthropicRequest{Model: model}
		applyOpenAICompatModelNormalization(request)
		require.Equal(t, "gpt-6.1-terra", request.Model)
		require.Equal(t, effort, request.OutputConfig.Effort)
		require.Equal(t, effort, openAICompatAnthropicReasoningEffort(request, request.Model, effort))
		if effort != "max" { // 直接桥接把 Anthropic max 映射为 xhigh，原值 max 由 openAICompatAnthropicReasoningEffort 恢复（与 Sol 一致）
			converted, err := apicompat.AnthropicToResponses(request)
			require.NoError(t, err)
			require.Equal(t, effort, converted.Reasoning.Effort)
		}

		chatConverted, err := apicompat.ChatCompletionsToResponses(&apicompat.ChatCompletionsRequest{
			Model: "gpt-6.1-terra", ReasoningEffort: effort,
			Tools: []apicompat.ChatTool{{Type: "function", Function: &apicompat.ChatFunction{Name: "lookup"}}},
		})
		require.NoError(t, err)
		require.Equal(t, effort, chatConverted.Reasoning.Effort)
		require.NoError(t, validateGPT61SolCompatRequest([]byte(`{"reasoning":{"effort":"`+effort+`"}}`), model))
	}
	for _, model := range []string{"gpt-6.1-terra", "gpt-6.1-terra-openai-compact", "openai/gpt_6.1_terra", "gpt-6.1-terra-minimal", "gpt-6.1-terra-2026-10-27"} {
		require.Equal(t, "gpt-6.1-terra", normalizeKnownOpenAICodexModel(model))
		require.Equal(t, "gpt-6.1-terra", normalizeCodexModel(model), "forwarded model and billed model must agree")
		require.Equal(t, "gpt-6.1-terra", ModelFamily(model))
	}
	// minimal / 日期后缀不当作 effort 写进 output_config，保持原有行为（走通用映射，默认 medium）。
	for _, model := range []string{"gpt-6.1-terra-minimal", "gpt-6.1-terra-2026-10-27"} {
		request := &apicompat.AnthropicRequest{Model: model}
		applyOpenAICompatModelNormalization(request)
		if request.OutputConfig != nil {
			require.Empty(t, request.OutputConfig.Effort, model)
		}
	}
	// 禁用 thinking 对 Terra 合法（Sol 会拒绝）。
	_, err := apicompat.AnthropicToResponses(&apicompat.AnthropicRequest{Model: "gpt-6.1-terra", Thinking: &apicompat.AnthropicThinking{Type: "disabled"}})
	require.NoError(t, err)
	require.NoError(t, validateGPT61SolCompatRequest([]byte(`{"thinking":{"type":"disabled"}}`), "gpt-6.1-terra"))
	converted, err := apicompat.AnthropicToResponses(&apicompat.AnthropicRequest{Model: "gpt-6.1-terra"})
	require.NoError(t, err)
	require.Equal(t, "medium", converted.Reasoning.Effort)

	compact := &apicompat.AnthropicRequest{Model: "gpt-6.1-terra-openai-compact"}
	applyOpenAICompatModelNormalization(compact)
	require.Nil(t, compact.OutputConfig)
	explicit := &apicompat.AnthropicRequest{Model: "gpt-6.1-terra-max", OutputConfig: &apicompat.AnthropicOutputConfig{Effort: "low"}}
	applyOpenAICompatModelNormalization(explicit)
	require.Equal(t, "low", explicit.OutputConfig.Effort)

	_, ok := apiKeyCodexModelsWithoutResponsesLite["gpt-6.1-terra"]
	require.True(t, ok)
}

func TestGPT61TerraDoesNotMatchOtherModels(t *testing.T) {
	for _, model := range []string{"gpt-6.1-sol", "gpt-5.6-terra", "gpt-6.1-terra-pro", "gpt-6.1-terra-ultra", "gpt-6.1-terrace"} {
		require.False(t, openai.IsGPT61TerraModelSpelling(model), model)
		require.NotEqual(t, "gpt-6.1-terra", ModelFamily(model), model)
		require.NotEqual(t, "gpt-6.1-terra", NormalizeOpenAICompatRequestedModel(model), model)
		require.NotEqual(t, "gpt-6.1-terra", normalizeKnownOpenAICodexModel(model), model)
	}
	require.Equal(t, "gpt-5.6-terra", normalizeKnownOpenAICodexModel("gpt-5.6-terra"))
	require.Equal(t, "gpt-6.1-sol", normalizeKnownOpenAICodexModel("gpt-6.1-sol-max"))
	// Sol 仍拒绝 none，Terra 不受影响。
	require.Error(t, validateGPT61SolCompatRequest([]byte(`{"reasoning_effort":"none"}`), "gpt-6.1-sol"))
	require.NoError(t, validateGPT61SolCompatRequest([]byte(`{"reasoning_effort":"none"}`), "gpt-6.1-terra"))
}

func TestGPT61TerraFallbackPricing(t *testing.T) {
	sources := map[string]*BillingService{
		"billing_fallback": newTestBillingService(),
		"pricing_fallback": NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}),
		// 目录只有 gpt-6.1 / gpt-6 / gpt-5.6-terra 时，不得被基础版本号或 5.6 Terra 截走。
		"catalog_with_neighbours": NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
			"gpt-6":         {InputCostPerToken: 10e-6, OutputCostPerToken: 50e-6},
			"gpt-6.1":       {InputCostPerToken: 9e-6, OutputCostPerToken: 49e-6},
			"gpt-5.6-terra": {InputCostPerToken: 2e-6, OutputCostPerToken: 12e-6},
		}}),
	}
	for name, svc := range sources {
		for _, model := range []string{"gpt-6.1-terra", "openai/gpt-6.1-terra-max", "gpt_6.1_terra", "gpt-6.1-terra-openai-compact"} {
			pricing, err := svc.GetModelPricing(model)
			require.NoError(t, err, name+"/"+model)
			require.InDelta(t, 1e-6, pricing.InputPricePerToken, 1e-15, name+"/"+model)
			require.InDelta(t, 2e-6, pricing.InputPricePerTokenPriority, 1e-15, name+"/"+model)
			require.InDelta(t, 5e-6, pricing.OutputPricePerToken, 1e-15, name+"/"+model)
			require.InDelta(t, 10e-6, pricing.OutputPricePerTokenPriority, 1e-15, name+"/"+model)
			require.InDelta(t, 1.25e-6, pricing.CacheCreationPricePerToken, 1e-15, name+"/"+model)
			require.InDelta(t, 2.5e-6, pricing.CacheCreationPricePerTokenPriority, 1e-15, name+"/"+model)
			require.InDelta(t, 0.05e-6, pricing.CacheReadPricePerToken, 1e-15, name+"/"+model)
			require.InDelta(t, 0.1e-6, pricing.CacheReadPricePerTokenPriority, 1e-15, name+"/"+model)
			require.Equal(t, 272_000, pricing.LongContextInputThreshold, name+"/"+model)
			require.InDelta(t, 2.0, pricing.LongContextInputMultiplier, 1e-12, name+"/"+model)
			require.InDelta(t, 1.5, pricing.LongContextOutputMultiplier, 1e-12, name+"/"+model)
		}
		// 价格不得等于 5.6 Terra、6.1 Sol。
		terra, err := svc.GetModelPricing("gpt-6.1-terra")
		require.NoError(t, err)
		for _, other := range []string{"gpt-5.6-terra", "gpt-6.1-sol", "gpt-6-sol"} {
			otherPricing, err := svc.GetModelPricing(other)
			require.NoError(t, err, other)
			require.NotEqual(t, otherPricing.InputPricePerToken, terra.InputPricePerToken, name+"/"+other)
			require.NotEqual(t, otherPricing.OutputPricePerToken, terra.OutputPricePerToken, name+"/"+other)
		}
		// 未知后缀不得按 Terra 计价。
		for _, model := range []string{"gpt-6.1-terra-pro", "gpt-6.1-terra-ultra"} {
			if pricing, err := svc.GetModelPricing(model); err == nil && pricing != nil {
				require.NotEqual(t, 5e-6, pricing.OutputPricePerToken, name+"/"+model)
			}
		}
	}
}

func TestGPT61TerraDynamicPricingLookup(t *testing.T) {
	entry := &LiteLLMModelPricing{InputCostPerToken: 1}
	sol := &LiteLLMModelPricing{InputCostPerToken: 2}
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{"gpt-6.1-terra": entry, "gpt-6.1-sol": sol}}
	for _, model := range []string{"gpt-6.1-terra", "gpt-6.1-terra-max", "openai/gpt-6.1-terra-openai-compact", "GPT_6.1_TERRA"} {
		require.Same(t, entry, svc.GetModelPricing(model), model)
	}
	require.Same(t, sol, svc.GetModelPricing("gpt-6.1-sol-max"))
	require.Equal(t, "gpt-6.1-terra", normalizeModelNameForPricing("openai/gpt-6.1-terra-high"))

	empty := &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}
	require.Same(t, openAIGPT61TerraFallbackPricing, empty.matchOpenAIModel("gpt-6.1-terra-high"))
	require.NotSame(t, openAIGPT61SolFallbackPricing, empty.matchOpenAIModel("gpt-6.1-terra"))
	require.NotSame(t, openAIGPT61TerraFallbackPricing, empty.matchOpenAIModel("gpt-6.1-sol"))
}

func TestGPT61TerraChatFallbackRejectsTools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"public": "gpt-6.1-terra"}}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	newCtx := func(path, body string) (*gin.Context, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		return c, rec
	}
	for _, body := range []string{
		`{"model":"public","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`,
		`{"model":"public","messages":[{"role":"user","content":"hi"}],"functions":[{"name":"lookup","parameters":{"type":"object"}}]}`,
	} {
		c, rec := newCtx("/v1/chat/completions", body)
		_, err := svc.forwardAsRawChatCompletions(context.Background(), c, account, []byte(body), "")
		require.ErrorContains(t, err, "gpt-6.1-terra requires Responses", body)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	}
	body := `{"model":"public","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`
	c, rec := newCtx("/v1/messages", body)
	_, err := svc.forwardAnthropicViaRawChatCompletions(context.Background(), c, account, []byte(body), "")
	require.ErrorContains(t, err, "gpt-6.1-terra requires Responses")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	body = `{"model":"public","input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`
	c, rec = newCtx("/v1/responses", body)
	_, err = svc.forwardResponsesViaRawChatCompletions(context.Background(), c, account, []byte(body))
	require.ErrorContains(t, err, "gpt-6.1-terra requires Responses")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// Terra 允许 none：无工具时不会被兼容校验拦截（此处只验证校验函数，不真正转发）。
	require.NoError(t, validateGPT61SolCompatRequest([]byte(`{"reasoning_effort":"none"}`), "gpt-6.1-terra"))
}

func TestResponsesRequiredForToolsMessageUsesCanonicalModel(t *testing.T) {
	require.EqualError(t, errResponsesRequiredForTools("openai/gpt-6.1-sol-max"), "gpt-6.1-sol requires Responses for tool calls; this account only supports Chat Completions")
	require.EqualError(t, errResponsesRequiredForTools("gpt-6.1-terra-high"), "gpt-6.1-terra requires Responses for tool calls; this account only supports Chat Completions")
}
