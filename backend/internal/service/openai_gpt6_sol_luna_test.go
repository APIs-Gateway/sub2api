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

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// GPT-6 Sol / Luna 支持同步自上游 Wei-Shaw/sub2api#7509（仅 GPT-6 部分）。

func TestGPT6SolLunaModelNormalization(t *testing.T) {
	cases := map[string]string{
		"gpt-6-sol":                 "gpt-6-sol",
		"GPT-6-Sol":                 "gpt-6-sol",
		"openai/gpt-6-sol-max":      "gpt-6-sol",
		"gpt-6-sol-high":            "gpt-6-sol",
		"gpt-6-luna":                "gpt-6-luna",
		"gpt-6-luna-none":           "gpt-6-luna",
		"gpt-6-luna-openai-compact": "gpt-6-luna",
		"gpt-6-luna-2026-09-22":     "gpt-6-luna",
		"gpt-6":                     "gpt-6-astra",
		"gpt-6-astra":               "gpt-6-astra",
		"gpt-5.6-sol":               "gpt-5.6-sol",
		"gpt-5.6-luna":              "gpt-5.6-luna",
	}
	for input, want := range cases {
		require.Equal(t, want, normalizeKnownOpenAICodexModel(input), input)
		require.Equal(t, want, normalizeCodexModel(input), input)
	}
	for _, unknown := range []string{"gpt-6-solitude", "gpt-6-luna-preview"} {
		require.Empty(t, normalizeKnownOpenAICodexModel(unknown), unknown)
	}

	require.True(t, isOpenAIGPT6Model("gpt-6-sol"))
	require.True(t, isOpenAIGPT6Model("gpt-6-luna"))
	require.True(t, isOpenAIGPT6Model("gpt-6"))
	require.False(t, isOpenAIGPT6Model("gpt-5.6-sol"))
}

func TestGPT6SolLunaUpstreamModelMismatchStaysDistinct(t *testing.T) {
	require.True(t, upstreamModelMatches("gpt-6-sol", "gpt-6-sol"))
	require.True(t, upstreamModelMatches("gpt-6-sol-high", "gpt-6-sol"))
	require.True(t, upstreamModelMatches("gpt-6-luna", "gpt-6-luna-2026-09-22"))
	require.False(t, upstreamModelMatches("gpt-5.6-sol", "gpt-6-sol"))
	require.False(t, upstreamModelMatches("gpt-6-sol", "gpt-5.6-sol"))
	require.False(t, upstreamModelMatches("gpt-6-sol", "gpt-6-luna"))
	require.False(t, upstreamModelMatches("gpt-6-luna", "gpt-6-astra"))
}

func TestGPT6SolLunaReasoningEffort(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		require.Equal(t, "max", normalizeOpenAIReasoningEffortForModel("max", model))
		require.Equal(t, "none", normalizeOpenAIReasoningEffortForModel("none", model))
		require.Equal(t, "high", normalizeOpenAIReasoningEffortForModel("high", model))

		out, changed, err := normalizeOpenAICodexCompactReasoningEffort([]byte(`{"model":"`+model+`","reasoning":{"effort":"max"}}`), model)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, "xhigh", gjson.GetBytes(out, "reasoning.effort").String())
	}
	// 其它模型保持原有行为：none 不记录、max 折叠为 xhigh。
	require.Equal(t, "", normalizeOpenAIReasoningEffortForModel("none", "gpt-5.4"))
	require.Equal(t, "xhigh", normalizeOpenAIReasoningEffortForModel("max", "gpt-5.4"))
}

func TestGPT6SolLunaCompatPromptCacheKey(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "openai/gpt-6-luna", "gpt-6-sol-max"} {
		require.True(t, shouldAutoInjectPromptCacheKeyForCompat(model), model)
	}
	for _, model := range []string{"gpt-6-sol-preview", "gpt-6-solitude", "gpt-6-luna-preview"} {
		require.False(t, shouldAutoInjectPromptCacheKeyForCompat(model), model)
	}
}

func TestGPT6SolLunaCodexModelsSkipResponsesLite(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		_, ok := apiKeyCodexModelsWithoutResponsesLite[model]
		require.True(t, ok, model)
	}
}

func TestGPT6SolLunaDedicatedFallbacksUseOfficialRates(t *testing.T) {
	type rates struct {
		model                      string
		input, output, write, read float64
	}
	sources := map[string]*BillingService{
		"pricing_service": NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}),
		"billing_service": newTestBillingService(),
		// 目录里只有 gpt-6（Astra）时，gpt-6-sol 不能被基础版本号回退截成 gpt-6 命中 Astra 价。
		"catalog_with_astra_only": NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
			"gpt-6":       {InputCostPerToken: 10e-6, OutputCostPerToken: 50e-6},
			"gpt-6-astra": {InputCostPerToken: 10e-6, OutputCostPerToken: 50e-6},
		}}),
	}
	for source, svc := range sources {
		for _, tc := range []rates{
			{"gpt-6-sol", 2e-6, 10e-6, 2.5e-6, 0.2e-6},
			{"gpt-6-luna", 0.1e-6, 0.5e-6, 0.125e-6, 0.01e-6},
		} {
			t.Run(source+"/"+tc.model, func(t *testing.T) {
				for _, alias := range []string{tc.model, tc.model + "-max", "openai/" + tc.model, tc.model + "-2026-09-22"} {
					pricing, err := svc.GetModelPricing(alias)
					require.NoError(t, err)
					require.InDelta(t, tc.input, pricing.InputPricePerToken, 1e-15, alias)
					require.InDelta(t, tc.input*2, pricing.InputPricePerTokenPriority, 1e-15, alias)
					require.InDelta(t, tc.output, pricing.OutputPricePerToken, 1e-15, alias)
					require.InDelta(t, tc.output*2, pricing.OutputPricePerTokenPriority, 1e-15, alias)
					require.InDelta(t, tc.write, pricing.CacheCreationPricePerToken, 1e-15, alias)
					require.InDelta(t, tc.write*2, pricing.CacheCreationPricePerTokenPriority, 1e-15, alias)
					require.InDelta(t, tc.read, pricing.CacheReadPricePerToken, 1e-15, alias)
					require.InDelta(t, tc.read*2, pricing.CacheReadPricePerTokenPriority, 1e-15, alias)
					require.Equal(t, 272_000, pricing.LongContextInputThreshold, alias)
					require.InDelta(t, 2.0, pricing.LongContextInputMultiplier, 1e-12, alias)
					require.InDelta(t, 1.5, pricing.LongContextOutputMultiplier, 1e-12, alias)
				}
			})
		}
	}
}

func TestGPT6SolLunaCostStacksPriorityAndLongContext(t *testing.T) {
	svc := newTestBillingService()
	for _, tc := range []struct {
		model                      string
		input, output, write, read float64
	}{
		{"gpt-6-sol", 2e-6, 10e-6, 2.5e-6, 0.2e-6},
		{"gpt-6-luna", 0.1e-6, 0.5e-6, 0.125e-6, 0.01e-6},
	} {
		for _, long := range []bool{false, true} {
			for tier, mult := range map[string]float64{"": 1, "priority": 2} {
				inputTokens := 100_000
				if long {
					inputTokens = 300_000
				}
				tokens := UsageTokens{InputTokens: inputTokens, CacheReadTokens: 2000, CacheCreationTokens: 1000, OutputTokens: 500}
				cost, err := svc.CalculateCostWithServiceTier(tc.model, tokens, 1, tier)
				require.NoError(t, err)
				im, om := 1.0, 1.0
				if long {
					im, om = 2, 1.5
				}
				name := tc.model + "/" + tier
				require.InDelta(t, float64(inputTokens)*tc.input*im*mult, cost.InputCost, 1e-10, name)
				require.InDelta(t, 500*tc.output*om*mult, cost.OutputCost, 1e-10, name)
				require.InDelta(t, 2000*tc.read*im*mult, cost.CacheReadCost, 1e-10, name)
				require.InDelta(t, 1000*tc.write*im*mult, cost.CacheCreationCost, 1e-10, name)
			}
		}
	}
}

func TestGPT6ResponsesSamplingNormalization(t *testing.T) {
	body := []byte(`{"model":"gpt-6-sol","reasoning":{"effort":"max"},"temperature":0.7,"top_p":0.9,"top_logprobs":2,"include":["reasoning.encrypted_content","message.output_text.logprobs"],"prompt_cache_options":{"ttl":"30m"}}`)
	out, changed, err := normalizeGPT6ResponsesSampling(body, "gpt-6-sol")
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "temperature").Exists())
	require.False(t, gjson.GetBytes(out, "top_p").Exists())
	require.False(t, gjson.GetBytes(out, "top_logprobs").Exists())
	require.Equal(t, []any{"reasoning.encrypted_content"}, gjson.GetBytes(out, "include").Value())
	require.Equal(t, "30m", gjson.GetBytes(out, "prompt_cache_options.ttl").String())

	none := []byte(`{"model":"gpt-6-luna","reasoning":{"effort":"none"},"temperature":0.7}`)
	out, changed, err = normalizeGPT6ResponsesSampling(none, "gpt-6-luna")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(none), string(out))

	other := []byte(`{"model":"gpt-6-astra","temperature":0.7}`)
	out, changed, err = normalizeGPT6ResponsesSampling(other, "gpt-6-astra")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(other), string(out))
}

func TestGPT6MappedCompatibilityBridgesKeepReasoningToolsAndCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		for _, messages := range []bool{false, true} {
			body := []byte(`{"model":"public","reasoning_effort":"max","temperature":0.7,"top_p":0.9,"prompt_cache_options":{"ttl":"30m"},"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"hello"}]}`)
			if messages {
				body = []byte(`{"model":"public","max_tokens":1000,"output_config":{"effort":"max"},"temperature":0.7,"top_p":0.9,"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hello"}]}`)
			}
			response := `data: {"type":"response.completed","response":{"id":"resp_1","model":"` + model + `","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1000,"output_tokens":10,"input_tokens_details":{"cached_tokens":200}}}}` + "\n\n"
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(response))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "fixture-key", "base_url": "https://api.openai.com", "model_mapping": map[string]any{"public": model}}, Extra: map[string]any{"openai_responses_supported": true}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			var result *OpenAIForwardResult
			var err error
			if messages {
				result, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
			} else {
				result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
			}
			name := model
			if messages {
				name += "/messages"
			}
			require.NoError(t, err, name)
			require.NotNil(t, result, name)
			require.NotNil(t, upstream.lastReq, name)
			require.Equal(t, "/v1/responses", upstream.lastReq.URL.Path, name)
			require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String(), name)
			require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String(), name)
			require.Len(t, gjson.GetBytes(upstream.lastBody, "tools").Array(), 1, name)
			require.False(t, gjson.GetBytes(upstream.lastBody, "temperature").Exists(), name)
			require.False(t, gjson.GetBytes(upstream.lastBody, "top_p").Exists(), name)
			if !messages {
				require.Equal(t, "30m", gjson.GetBytes(upstream.lastBody, "prompt_cache_options.ttl").String(), name)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Claude Opus 5.5（同步自上游 #7509）
// ---------------------------------------------------------------------------

func TestOpus55RejectsUnsupportedParametersBeforeMimicry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, typ := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		for _, field := range []string{`"thinking":{"type":"disabled"}`, `"thinking":{"type":"enabled","budget_tokens":1024}`, `"tool_choice":{"type":"any"}`, `"tool_choice":{"type":"tool","name":"lookup"}`} {
			for _, count := range []bool{false, true} {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				model := "claude-opus-5-5"
				account := &Account{ID: 1, Platform: PlatformAnthropic, Type: typ}
				if typ == AccountTypeAPIKey {
					model = "public-opus"
					account.Credentials = map[string]any{"model_mapping": map[string]any{model: "claude-opus-5-5"}}
				}
				body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hello"}],` + field + `}`)
				parsed := &ParsedRequest{Model: model, Body: NewRequestBodyRef(body)}
				svc := &GatewayService{}
				var err error
				if count {
					err = svc.ForwardCountTokens(context.Background(), c, account, parsed)
				} else {
					_, err = svc.Forward(context.Background(), c, account, parsed)
				}
				require.Error(t, err)
				require.Equal(t, http.StatusBadRequest, rec.Code)
				require.Contains(t, rec.Body.String(), "invalid_request_error")
			}
		}
	}
	// 非 Opus 5.5 不受影响。
	require.NoError(t, validateClaudeOpus55Request([]byte(`{"thinking":{"type":"enabled","budget_tokens":1024},"tool_choice":{"type":"any"}}`), "claude-opus-5"))
	require.NoError(t, validateClaudeOpus55Request([]byte(`{"thinking":{"type":"adaptive"},"tool_choice":{"type":"auto"}}`), "claude-opus-5-5"))
}

func TestOpus55ThinkingDefaultPreservesSignedHistory(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"signed"},{"type":"redacted_thinking","data":"encrypted"},{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}],"tool_choice":{"type":"none"},"thinking":{"type":"adaptive","display":"omitted"}}`)
	require.Equal(t, string(body), string(FilterThinkingBlocks(body, "claude-opus-5-5")))
	withoutThinking, _ := deleteJSONPathBytes(body, "thinking")
	require.Equal(t, string(withoutThinking), string(FilterThinkingBlocks(withoutThinking, "claude-opus-5-5")))
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-opus-5-5", claudeOAuthNormalizeOptions{})
	require.Equal(t, "none", gjson.GetBytes(out, "tool_choice.type").String())
	require.False(t, gjson.GetBytes(out, "temperature").Exists())
	require.Equal(t, "omitted", gjson.GetBytes(out, "thinking.display").String())
	require.JSONEq(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(out, "messages").Raw)

	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)), PlatformAnthropic)
	require.NoError(t, err)
	require.True(t, parsed.ThinkingEnabled)
}

func TestOpus55ResponsesSignedThinkingBufferedAndStreamed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload := strings.Join([]string{
		"event: message_start\n" + `data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":10}}}`,
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed"}}`,
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}`,
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`,
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":1}`,
		"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		"event: message_stop\n" + `data: {"type":"message_stop"}`,
	}, "\n\n") + "\n\n"
	for _, stream := range []bool{false, true} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}
		svc := &GatewayService{}
		var err error
		if stream {
			_, err = svc.handleResponsesStreamingResponse(resp, c, "public-opus", "claude-opus-5-5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
		} else {
			_, err = svc.handleResponsesBufferedStreamingResponse(resp, c, "public-opus", "claude-opus-5-5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
		}
		require.NoError(t, err)
		require.Contains(t, rec.Body.String(), "anthropic-thinking-v1:")
		require.Contains(t, rec.Body.String(), "public-opus")
		require.Contains(t, rec.Body.String(), `"text":"ok"`)
	}
}

func TestOpus55BridgeUsesMappedModelBeforeThinkingConversion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, chat := range []bool{false, true} {
		for _, forced := range []bool{false, true} {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			body := `{"model":"public-opus","input":"hello","reasoning":{"effort":"xhigh"}}`
			if chat {
				body = `{"model":"public-opus","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"xhigh"}`
			}
			if forced {
				body = body[:len(body)-1] + `,"tool_choice":"required","tools":[{"type":"function","name":"lookup","function":{"name":"lookup"}}]}`
			}
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(namespaceToolAnthropicStream()))}}
			svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-key", "model_mapping": map[string]any{"public-opus": "claude-opus-5-5"}}}
			var err error
			var result *ForwardResult
			if chat {
				result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(body), nil)
			} else {
				result, err = svc.ForwardAsResponses(context.Background(), c, account, []byte(body), nil)
			}
			if forced {
				require.Error(t, err)
				require.Equal(t, 400, rec.Code)
				require.Nil(t, upstream.lastReq)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.NotNil(t, upstream.lastReq)
				require.Equal(t, "claude-opus-5-5", gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, "adaptive", gjson.GetBytes(upstream.lastBody, "thinking.type").String())
				require.Equal(t, "xhigh", gjson.GetBytes(upstream.lastBody, "output_config.effort").String())
				require.False(t, gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Exists())
			}
		}
	}
}

func TestOpus55PricingIsIndependentFromOpus5(t *testing.T) {
	sources := map[string]*BillingService{
		"billing_service": newTestBillingService(),
		"pricing_service": NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}),
		// 目录只有 claude-opus-5 时，Opus 5.5 不能被 "claude-opus-5" 子串匹配拿走。
		"catalog_with_opus5_only": NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
			"claude-opus-5": {InputCostPerToken: 5e-6, OutputCostPerToken: 25e-6},
		}}),
	}
	for name, svc := range sources {
		t.Run(name, func(t *testing.T) {
			for _, model := range []string{"claude-opus-5-5", "claude-opus-5-5-20260922", "anthropic/claude-opus-5-5"} {
				pricing, err := svc.GetModelPricing(model)
				require.NoError(t, err)
				require.InDelta(t, 4e-6, pricing.InputPricePerToken, 1e-15, model)
				require.InDelta(t, 20e-6, pricing.OutputPricePerToken, 1e-15, model)
				require.InDelta(t, 0.2e-6, pricing.CacheReadPricePerToken, 1e-15, model)
				require.InDelta(t, 5e-6, pricing.CacheCreation5mPrice, 1e-15, model)
				require.InDelta(t, 8e-6, pricing.CacheCreation1hPrice, 1e-15, model)
				require.True(t, pricing.SupportsCacheBreakdown, model)
			}
			tokens := UsageTokens{InputTokens: 300000, OutputTokens: 500, CacheReadTokens: 1000, CacheCreationTokens: 1000, CacheCreation5mTokens: 400, CacheCreation1hTokens: 600}
			for tier, mult := range map[string]float64{"": 1, "priority": 2} {
				cost, err := svc.CalculateCostWithServiceTier("claude-opus-5-5", tokens, 1, tier)
				require.NoError(t, err)
				require.InDelta(t, 1.2*mult, cost.InputCost, 1e-10, tier)
				require.InDelta(t, (400*5e-6+600*8e-6)*mult, cost.CacheCreationCost, 1e-10, tier)
				require.InDelta(t, 1000*0.2e-6*mult, cost.CacheReadCost, 1e-10, tier)
				require.InDelta(t, 500*20e-6*mult, cost.OutputCost, 1e-10, tier)
			}
			old, err := svc.GetModelPricing("claude-opus-5")
			require.NoError(t, err)
			require.InDelta(t, 5e-6, old.InputPricePerToken, 1e-15)
		})
	}
}
