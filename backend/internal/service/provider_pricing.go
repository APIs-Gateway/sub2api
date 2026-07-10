package service

import (
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	HvoyProviderPricingSchemaVersion = "1.1"
	HvoyProviderPricingCurrency      = "CNY"
	HvoyProviderPricingUnitTokens    = "per_1m_tokens"
	HvoyProviderPricingGroupName     = "codex plus"
)

var hvoyProviderPricingModels = []hvoyProviderPricingModelRef{
	{modelName: "gpt-5.5", groupName: HvoyProviderPricingGroupName},
	{modelName: "gpt-5.4", groupName: HvoyProviderPricingGroupName},
	{modelName: "gpt-5.6-sol", groupName: HvoyProviderPricingGroupName},
	{modelName: "gpt-5.6-terra", groupName: HvoyProviderPricingGroupName},
}

type hvoyProviderPricingModelRef struct {
	modelName string
	groupName string
}

type HvoyProviderPricingResponse struct {
	SchemaVersion string                  `json:"schema_version"`
	Success       bool                    `json:"success"`
	Message       string                  `json:"message"`
	Data          HvoyProviderPricingData `json:"data"`
}

type HvoyProviderPricingData struct {
	Currency   string                     `json:"currency"`
	PriceUnit  string                     `json:"price_unit"`
	SiteName   string                     `json:"site_name,omitempty"`
	SiteDomain string                     `json:"site_domain,omitempty"`
	UpdatedAt  string                     `json:"updated_at"`
	Models     []HvoyProviderPricingModel `json:"models"`
}

type HvoyProviderPricingModel struct {
	ModelName          string   `json:"model_name"`
	GroupName          string   `json:"group_name"`
	PriceUnit          string   `json:"price_unit,omitempty"`
	InputPrice         float64  `json:"input_price"`
	OutputPrice        *float64 `json:"output_price"`
	CacheInputPrice    *float64 `json:"cache_input_price"`
	CacheCreatePrice   *float64 `json:"cache_create_price"`
	CacheCreatePrice1H *float64 `json:"cache_create_price_1h"`
	Enabled            bool     `json:"enabled"`
	Note               string   `json:"note"`
}

func (s *PricingService) BuildHvoyProviderPricing(paymentMultiplier float64, siteName, frontendURL string, now time.Time) HvoyProviderPricingResponse {
	multiplier := normalizeBalanceRechargeMultiplier(paymentMultiplier)
	updatedAt := s.LastUpdated()
	if updatedAt.IsZero() {
		updatedAt = now
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	models := make([]HvoyProviderPricingModel, 0, len(hvoyProviderPricingModels))
	for _, model := range hvoyProviderPricingModels {
		pricing := s.GetModelPricing(model.modelName)
		if pricing == nil {
			models = append(models, HvoyProviderPricingModel{
				ModelName: model.modelName,
				GroupName: model.groupName,
				Enabled:   false,
				Note:      "pricing unavailable",
			})
			continue
		}

		models = append(models, HvoyProviderPricingModel{
			ModelName:          model.modelName,
			GroupName:          model.groupName,
			InputPrice:         usdPerTokenToCNYPerMTok(pricing.InputCostPerToken, multiplier),
			OutputPrice:        optionalUSDPerTokenToCNYPerMTok(pricing.OutputCostPerToken, multiplier),
			CacheInputPrice:    optionalUSDPerTokenToCNYPerMTok(pricing.CacheReadInputTokenCost, multiplier),
			CacheCreatePrice:   optionalUSDPerTokenToCNYPerMTok(pricing.CacheCreationInputTokenCost, multiplier),
			CacheCreatePrice1H: optionalUSDPerTokenToCNYPerMTok(pricing.CacheCreationInputTokenCostAbove1hr, multiplier),
			Enabled:            true,
			Note:               "",
		})
	}

	return HvoyProviderPricingResponse{
		SchemaVersion: HvoyProviderPricingSchemaVersion,
		Success:       true,
		Message:       "",
		Data: HvoyProviderPricingData{
			Currency:   HvoyProviderPricingCurrency,
			PriceUnit:  HvoyProviderPricingUnitTokens,
			SiteName:   strings.TrimSpace(siteName),
			SiteDomain: frontendURLDomain(frontendURL),
			UpdatedAt:  updatedAt.UTC().Format(time.RFC3339),
			Models:     models,
		},
	}
}

func (s *PricingService) LastUpdated() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastUpdated
}

func usdPerTokenToCNYPerMTok(usdPerToken, paymentMultiplier float64) float64 {
	if math.IsNaN(usdPerToken) || math.IsInf(usdPerToken, 0) || usdPerToken <= 0 {
		return 0
	}
	return decimal.NewFromFloat(usdPerToken).
		Mul(decimal.NewFromInt(1_000_000)).
		Div(decimal.NewFromFloat(normalizeBalanceRechargeMultiplier(paymentMultiplier))).
		Round(6).
		InexactFloat64()
}

func optionalUSDPerTokenToCNYPerMTok(usdPerToken, paymentMultiplier float64) *float64 {
	if math.IsNaN(usdPerToken) || math.IsInf(usdPerToken, 0) || usdPerToken <= 0 {
		return nil
	}
	value := usdPerTokenToCNYPerMTok(usdPerToken, paymentMultiplier)
	return &value
}

func frontendURLDomain(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Hostname())
}
