package service

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// fuzzyMatchRepeat 是「同一输入反复匹配」的次数。修复前每次调用都重新随机遍历 map，
// 候选 2 个以上时重复这么多次几乎一定会撞出不同结果，所以这些用例在旧实现上必然失败。
const fuzzyMatchRepeat = 100

func fuzzyPricing(input, output float64) *LiteLLMModelPricing {
	return &LiteLLMModelPricing{InputCostPerToken: input, OutputCostPerToken: output, LiteLLMProvider: "anthropic", Mode: "chat"}
}

func TestIsScopedPricingKey(t *testing.T) {
	for _, key := range []string{
		"bedrock/claude-3-5-sonnet",
		"vertex_ai/claude-3-5-sonnet@20240620",
		"openrouter/anthropic/claude-3.5-sonnet",
		"us.anthropic.claude-3-5-sonnet-20241022-v2:0",
		"eu.anthropic.claude-sonnet-4-5-20250929-v1:0",
		"anthropic.claude-3-5-sonnet-20240620-v1:0",
		"meta.llama3-70b-instruct-v1:0",
		"US.Anthropic.Claude-3-5-Sonnet",
	} {
		require.True(t, isScopedPricingKey(key), key)
	}
	// 版本号里的小数点、bedrock 风格的 -v1:0 后缀、vertex 风格的 @日期 都不算前缀。
	for _, key := range []string{
		"gpt-5.2",
		"claude-opus-4.5",
		"claude-3.5-sonnet",
		"kimi-k2.5",
		"qwen3.5-plus",
		"grok-4.20-0309-reasoning",
		"gemini-2.5-pro",
		"o3-mini",
		"claude-sonnet-4-5-20250929-v1:0",
		"claude-3-5-sonnet@20240620",
	} {
		require.False(t, isScopedPricingKey(key), key)
	}
}

func TestSortedPricingKeys_UnscopedFirstThenShortestThenLexicographic(t *testing.T) {
	catalog := map[string]*LiteLLMModelPricing{}
	for _, key := range []string{
		"us.anthropic.claude-3-5-sonnet-20241022-v2:0",
		"claude-3-5-sonnet-20241022",
		"vertex_ai/claude-3-5-sonnet@20240620",
		"gpt-5.2",
		"anthropic.claude-3-5-sonnet-20241022-v2:0",
		"claude-3-5-sonnet",
		"gpt-5.1",
		"bedrock/claude-3-5-sonnet",
		"kimi-k2.5",
		"claude-opus-4.5",
	} {
		catalog[key] = fuzzyPricing(1e-6, 2e-6)
	}

	want := []string{
		// 无前缀：先短后长，同长按字典序（gpt-5.1 < gpt-5.2）。
		"gpt-5.1",
		"gpt-5.2",
		"kimi-k2.5",
		"claude-opus-4.5",
		"claude-3-5-sonnet",
		"claude-3-5-sonnet-20241022",
		// 带前缀的排在所有无前缀键之后，即使它（bedrock/…，25 字节）比无前缀的 claude-3-5-sonnet-20241022（26 字节）更短。
		"bedrock/claude-3-5-sonnet",
		"vertex_ai/claude-3-5-sonnet@20240620",
		"anthropic.claude-3-5-sonnet-20241022-v2:0",
		"us.anthropic.claude-3-5-sonnet-20241022-v2:0",
	}
	// map 构造本身没有顺序，多算几次确认结果不依赖遍历顺序。
	for i := 0; i < fuzzyMatchRepeat; i++ {
		require.Equal(t, want, sortedPricingKeys(catalog))
	}
	require.Empty(t, sortedPricingKeys(nil))
}

func TestGetModelPricing_FuzzyBaseNameKeyEqualToBaseNameWins(t *testing.T) {
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"claude-sonnet-4-5":               fuzzyPricing(3e-6, 15e-6), // 键名恰好等于基名
		"claude-sonnet-4-5-20250929":      fuzzyPricing(4e-6, 16e-6),
		"claude-sonnet-4-5-20250929-v1:0": fuzzyPricing(5e-6, 17e-6),
	}}

	for i := 0; i < fuzzyMatchRepeat; i++ {
		got := svc.GetModelPricing("claude-sonnet-4-5-20251231")
		require.NotNil(t, got)
		require.InDelta(t, 3e-6, got.InputCostPerToken, 1e-12)
	}
}

func TestGetModelPricing_FuzzyBaseNameFallsBackToShortestThenLexicographic(t *testing.T) {
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"acme-model-20250202":      fuzzyPricing(2e-6, 2e-6),
		"acme-model-20250101-v1:0": fuzzyPricing(3e-6, 3e-6),
		"acme-model-20250101":      fuzzyPricing(1e-6, 1e-6),
	}}

	// 三个键基名都是 acme-model，且没有键名恰好等于基名：取最短里字典序最小的 acme-model-20250101。
	for i := 0; i < fuzzyMatchRepeat; i++ {
		got := svc.GetModelPricing("acme-model-20991231")
		require.NotNil(t, got)
		require.InDelta(t, 1e-6, got.InputCostPerToken, 1e-12)
	}
}

func TestGetModelPricing_FamilyMatchPrefersUnscopedKey(t *testing.T) {
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"bedrock/claude-sonnet-4-5":                    fuzzyPricing(9e-6, 9e-6), // 区域价，且比无前缀的键更短
		"us.anthropic.claude-sonnet-4-5-20250929-v1:0": fuzzyPricing(8e-6, 8e-6),
		"claude-sonnet-4-5-20250929":                   fuzzyPricing(3e-6, 15e-6),
	}}

	// claude-sonnet-4-5-latest 精确、变体、基名都查不到，走系列匹配，三个键都含 claude-sonnet-4-5。
	for i := 0; i < fuzzyMatchRepeat; i++ {
		got := svc.GetModelPricing("claude-sonnet-4-5-latest")
		require.NotNil(t, got)
		require.InDelta(t, 3e-6, got.InputCostPerToken, 1e-12)
	}
}

func TestGetModelPricing_FamilyMatchOpus4PicksShortestThenLexicographic(t *testing.T) {
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"claude-opus-4-1":          fuzzyPricing(15e-6, 75e-6),
		"claude-opus-4-1-20250805": fuzzyPricing(15e-6, 75e-6),
		"claude-opus-4-20250514":   fuzzyPricing(15e-6, 75e-6),
		"claude-opus-4-5":          fuzzyPricing(5e-6, 25e-6),
		"claude-opus-4-6":          fuzzyPricing(5e-6, 25e-6),
		"claude-opus-4-7":          fuzzyPricing(5e-6, 25e-6),
		"claude-opus-4-8":          fuzzyPricing(5e-6, 25e-6),
	}}

	// 这几个名字都落在 opus-4 系列，pattern claude-opus-4 同时命中 15/75 与 5/25 两档价格的键。
	// 最短的有 5 个（长度都是 15），字典序最小的是 claude-opus-4-1，即 Opus 4 / 4.1 / 3 Opus 的 15/75。
	for _, model := range []string{
		"claude-opus-4-0",
		"claude-3-opus-latest",
		"claude-opus-latest",
		"claude-opus-4-1-20250805[1m]",
	} {
		for i := 0; i < fuzzyMatchRepeat; i++ {
			got := svc.GetModelPricing(model)
			require.NotNil(t, got, model)
			require.InDelta(t, 15e-6, got.InputCostPerToken, 1e-12, model)
			require.InDelta(t, 75e-6, got.OutputCostPerToken, 1e-12, model)
		}
	}
}

func TestGetModelPricing_FuzzyMatchBundledCatalogPicksExpectedKeys(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)

	svc := &PricingService{}
	pricingData, err := svc.parsePricingData(data)
	require.NoError(t, err)
	svc.mu.Lock()
	svc.setPricingDataLocked(pricingData)
	svc.mu.Unlock()

	for model, wantKey := range map[string]string{
		// 第 3 步，同基名多键：键名恰好等于基名的优先。
		"claude-sonnet-4-5-20251231": "claude-sonnet-4-5",
		"claude-haiku-4-5-20260101":  "claude-haiku-4-5",
		"claude-opus-4-5-20260101":   "claude-opus-4-5",
		"claude-opus-4-1-20251231":   "claude-opus-4-1",
		// 第 3 步，同基名只有一个键。
		"claude-opus-4-20251231": "claude-opus-4-20250514",
		"gpt-5.2-20251222":       "gpt-5.2",
		"gpt-5.4-20260301":       "gpt-5.4",
		// 系列匹配，候选价格相同：取最短里字典序最小的。
		"claude-3-5-sonnet-20241022": "claude-sonnet-4-5",
		"claude-opus-4-6[1m]":        "claude-opus-4-6",
	} {
		want := pricingData[wantKey]
		require.NotNil(t, want, wantKey)
		for i := 0; i < fuzzyMatchRepeat; i++ {
			require.Same(t, want, svc.GetModelPricing(model), model)
		}
	}
}

func TestPricingService_ReloadReplacesCatalogAndKeyOrderTogether(t *testing.T) {
	bodyA := []byte(`{"acme-model-20250101":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000001}}`)
	bodyB := []byte(`{
		"acme-model-20250303":{"input_cost_per_token":0.000003,"output_cost_per_token":0.000003},
		"acme-model-20250202":{"input_cost_per_token":0.000002,"output_cost_per_token":0.000002}
	}`)
	body := bodyA
	svc := retryPricingService(t, pricingRemoteClientStub{
		hash: func(context.Context) (string, error) { return "anchor", nil },
		json: func(context.Context) ([]byte, error) { return body, nil },
	})

	// 远端下载（热更新）路径。
	require.NoError(t, svc.downloadPricingDataWithContext(context.Background(), noPricingRetryDelay))
	require.Equal(t, []string{"acme-model-20250101"}, svc.pricingKeys)
	require.InDelta(t, 1e-6, svc.GetModelPricing("acme-model-20991231").InputCostPerToken, 1e-12)

	body = bodyB
	require.NoError(t, svc.downloadPricingDataWithContext(context.Background(), noPricingRetryDelay))
	require.Equal(t, sortedPricingKeys(svc.pricingData), svc.pricingKeys)
	require.Equal(t, []string{"acme-model-20250202", "acme-model-20250303"}, svc.pricingKeys)
	require.InDelta(t, 2e-6, svc.GetModelPricing("acme-model-20991231").InputCostPerToken, 1e-12)

	// 本地文件加载路径。
	path := filepath.Join(t.TempDir(), "pricing.json")
	require.NoError(t, os.WriteFile(path, bodyA, 0644))
	require.NoError(t, svc.loadPricingData(path))
	require.Equal(t, []string{"acme-model-20250101"}, svc.pricingKeys)
	require.InDelta(t, 1e-6, svc.GetModelPricing("acme-model-20991231").InputCostPerToken, 1e-12)
}

func TestPricingService_FuzzyMatchStaysConsistentWhileCatalogIsSwapped(t *testing.T) {
	catalogs := []map[string]*LiteLLMModelPricing{
		{"swap-model-20250101": fuzzyPricing(1e-6, 1e-6)},
		{
			"swap-model-20250202": fuzzyPricing(2e-6, 2e-6),
			"swap-model-20250303": fuzzyPricing(3e-6, 3e-6),
		},
	}
	svc := &PricingService{}
	svc.mu.Lock()
	svc.setPricingDataLocked(catalogs[0])
	svc.mu.Unlock()

	stop := make(chan struct{})
	var swapper sync.WaitGroup
	swapper.Add(1)
	go func() {
		defer swapper.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			svc.mu.Lock()
			svc.setPricingDataLocked(catalogs[i%2])
			svc.mu.Unlock()
		}
	}()

	var readers sync.WaitGroup
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for i := 0; i < 2000; i++ {
				// 新 map 配旧切片时，按旧键去新 map 里取会得到 nil。
				got := svc.GetModelPricing("swap-model-20991231")
				if got == nil {
					t.Error("fuzzy match returned nil while the catalog was being swapped")
					return
				}
				if in := got.InputCostPerToken; in != 1e-6 && in != 2e-6 {
					t.Errorf("unexpected price %v while the catalog was being swapped", in)
					return
				}
			}
		}()
	}
	readers.Wait()
	close(stop)
	swapper.Wait()
}

func TestPricingKeyOrderLocked_RecomputesWhenCatalogWasAssignedDirectly(t *testing.T) {
	svc := &PricingService{}
	svc.mu.Lock()
	svc.setPricingDataLocked(map[string]*LiteLLMModelPricing{
		"a-12": fuzzyPricing(1e-6, 1e-6),
		"b-1":  fuzzyPricing(1e-6, 1e-6),
	})
	svc.mu.Unlock()
	require.Equal(t, []string{"b-1", "a-12"}, svc.pricingKeyOrderLocked())

	// 测试里常见的写法：绕过加载函数直接改 pricingData，键数对不上时现算，顺序规则不变。
	svc.pricingData["c"] = fuzzyPricing(1e-6, 1e-6)
	require.Equal(t, []string{"c", "b-1", "a-12"}, svc.pricingKeyOrderLocked())

	literal := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"a-12": fuzzyPricing(1e-6, 1e-6),
		"b-1":  fuzzyPricing(1e-6, 1e-6),
	}}
	require.Equal(t, []string{"b-1", "a-12"}, literal.pricingKeyOrderLocked())
}
