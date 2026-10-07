//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// This fixture uses only APIs present on origin/main, so OLD failures must be
// observable contract differences rather than new-helper compilation failures.
func TestAstraUltrafast_PriceAndQuote(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6", "openai/gpt-6-astra", "gpt-6-astra-high"} {
		for _, contextSize := range []int{272000, 272001} {
			t.Run(fmt.Sprintf("%s/%d", model, contextSize), func(t *testing.T) {
				f := newQuoteTestFixture(nil, nil, []*Group{quoteTestGroup(1.25)}, nil)
				tokens := UsageTokens{InputTokens: contextSize - 300, OutputTokens: 4, CacheReadTokens: 200, CacheCreationTokens: 100}
				cost, err := f.billing.CalculateCostWithServiceTier(model, tokens, 1.25, " UltraFast ")
				require.NoError(t, err)
				in, out := 1.0, 1.0
				if contextSize > 272000 {
					in, out = 2, 1.5
				}
				require.InDelta(t, float64(tokens.InputTokens)*60e-6*in, cost.InputCost, 1e-10)
				require.InDelta(t, float64(tokens.OutputTokens)*300e-6*out, cost.OutputCost, 1e-10)
				require.InDelta(t, float64(tokens.CacheReadTokens)*6e-6*in, cost.CacheReadCost, 1e-10)
				require.InDelta(t, float64(tokens.CacheCreationTokens)*75e-6*in, cost.CacheCreationCost, 1e-10)
				require.InDelta(t, cost.TotalCost*1.25, cost.ActualCost, 1e-10)
				q, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: model, GroupID: quoteTestGroupID, ServiceTier: "ultrafast"})
				require.NoError(t, err)
				require.True(t, q.Priced)
				require.Equal(t, "multiplier", q.ServiceTier.Mode)
				require.Equal(t, 6.0, q.ServiceTier.Multiplier)
				quoted, err := q.Cost(context.Background(), QuoteUsage{Tokens: tokens})
				require.NoError(t, err)
				require.InDelta(t, cost.ActualCost, quoted.ActualCost, 1e-10)
			})
		}
	}
}

func TestAstraUltrafast_CustomAndOtherTiers(t *testing.T) {
	for _, inputPrice := range []float64{0, 1e-6} {
		t.Run(fmt.Sprint(inputPrice), func(t *testing.T) {
			f := newQuoteTestFixture(nil, quoteTestChannel(ChannelModelPricing{Platform: PlatformOpenAI, Models: []string{"gpt-6-astra"}, BillingMode: BillingModeToken, InputPrice: float64Ptr(inputPrice), OutputPrice: float64Ptr(0), CacheReadPrice: float64Ptr(0), CacheWritePrice: float64Ptr(0)}), []*Group{quoteTestGroup(1)}, nil)
			q, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "gpt-6-astra", GroupID: quoteTestGroupID, ServiceTier: "ultrafast"})
			require.NoError(t, err)
			cost, err := q.Cost(context.Background(), QuoteUsage{Tokens: UsageTokens{InputTokens: 100, OutputTokens: 100, CacheReadTokens: 100, CacheCreationTokens: 100}})
			require.NoError(t, err)
			require.InDelta(t, 100*inputPrice*6, cost.ActualCost, 1e-12)
			require.Zero(t, cost.OutputCost)
			require.Zero(t, cost.CacheReadCost)
			require.Zero(t, cost.CacheCreationCost)
		})
	}
	f := newQuoteTestFixture(nil, quoteTestChannel(ChannelModelPricing{Platform: PlatformOpenAI, Models: []string{"gpt-6-astra"}, BillingMode: BillingModeToken, Intervals: []PricingInterval{{MinTokens: 0, MaxTokens: testPtrInt(10), InputPrice: float64Ptr(1e-6)}, {MinTokens: 10, InputPrice: float64Ptr(2e-6)}}}), []*Group{quoteTestGroup(1)}, nil)
	q, err := f.quoter.Quote(context.Background(), QuoteRequest{Model: "gpt-6-astra", GroupID: quoteTestGroupID, ServiceTier: "ultrafast"})
	require.NoError(t, err)
	for _, n := range []int{10, 11, 300000} {
		cost, err := q.Cost(context.Background(), QuoteUsage{Tokens: UsageTokens{InputTokens: n}})
		require.NoError(t, err)
		price := 1e-6
		if n > 10 {
			price = 2e-6
		}
		require.InDelta(t, float64(n)*price*6, cost.ActualCost, 1e-10)
	}
	svc := newTestBillingService()
	for _, tc := range []struct {
		model, tier string
		price       float64
	}{{"gpt-6-astra", "priority", 20e-6}, {"gpt-6-astra", "flex", 5e-6}, {"gpt-6-sol", "ultrafast", 2e-6}, {"gpt-6.1-sol", "ultrafast", 2e-6}} {
		cost, err := svc.CalculateCostWithServiceTier(tc.model, UsageTokens{InputTokens: 1}, 1, tc.tier)
		require.NoError(t, err)
		require.InDelta(t, tc.price, cost.ActualCost, 1e-12)
	}
}

func TestAstraUltrafast_HTTPForwardAndPolicy(t *testing.T) {
	for _, action := range []string{"pass", "filter", "force_priority", "block"} {
		t.Run(action, func(t *testing.T) {
			svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{{ServiceTier: "ultrafast", Action: action, Scope: BetaPolicyScopeAll}}})
			svc.cfg = &config.Config{}
			u := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_ultra","model":"gpt-6-astra","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}`))}}
			svc.httpUpstream = u
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-only", "base_url": "https://api.openai.com"}, Extra: map[string]any{"openai_passthrough": true}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-6-astra","service_tier":"ultrafast","input":"hello"}`))
			if action == "block" {
				require.Error(t, err)
				require.Nil(t, u.lastReq)
				return
			}
			require.NoError(t, err)
			want := "ultrafast"
			if action == "filter" {
				want = ""
			}
			if action == "force_priority" {
				want = "priority"
			}
			require.Equal(t, want, gjson.GetBytes(u.lastBody, "service_tier").String())
			if want == "" {
				require.Nil(t, result.ServiceTier)
			} else {
				require.NotNil(t, result.ServiceTier)
				require.Equal(t, want, *result.ServiceTier)
			}
		})
	}
}

func TestAstraUltrafast_WSAndSettingsPolicy(t *testing.T) {
	for _, tier := range []string{"all", "ultrafast"} {
		for _, action := range []string{"pass", "filter", "block", "force_priority"} {
			t.Run(tier+"/"+action, func(t *testing.T) {
				settings := &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{{ServiceTier: tier, Action: action, Scope: BetaPolicyScopeAll}}}
				svc := newOpenAIGatewayServiceWithSettings(t, settings)
				account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
				frame := []byte(`{"type":"response.create","model":"gpt-6-astra","service_tier":" UltraFast "}`)
				out, blocked, err := svc.applyOpenAIFastPolicyToWSResponseCreate(context.Background(), account, "gpt-6-astra", frame)
				require.NoError(t, err)
				if action == "block" {
					require.NotNil(t, blocked)
					return
				}
				require.Nil(t, blocked)
				want := "ultrafast"
				if action == "filter" {
					want = ""
				}
				if action == "force_priority" {
					want = "priority"
				}
				require.Equal(t, want, gjson.GetBytes(out, "service_tier").String())
			})
		}
	}
	repo := &openAIFastPolicyRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})
	require.NoError(t, svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{{ServiceTier: " UltraFast ", Action: "filter", Scope: BetaPolicyScopeAll}}}))
}
