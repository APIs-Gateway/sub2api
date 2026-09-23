package service

import (
	"context"
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
	{modelName: "gpt-5.6-sol", groupName: HvoyProviderPricingGroupName},
	{modelName: "gpt-5.6-terra", groupName: HvoyProviderPricingGroupName},
	{modelName: "gpt-5.6-luna", groupName: HvoyProviderPricingGroupName},
	{modelName: "gpt-6-astra", groupName: HvoyProviderPricingGroupName},
	{modelName: "gpt-6-sol", groupName: HvoyProviderPricingGroupName},
	{modelName: "gpt-6-luna", groupName: HvoyProviderPricingGroupName},
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

// HvoyProviderGroupLister is the slice of GroupRepository the provider pricing
// endpoint needs to read each published group's rate multiplier.
type HvoyProviderGroupLister interface {
	ListActive(ctx context.Context) ([]Group, error)
}

// LoadHvoyProviderGroupMultipliers maps every published hvoy group name to the
// rate multiplier of the matching active group. Names are matched after
// collapsing whitespace and case so the public identifier stays stable even if
// the admin-side group name carries extra spaces.
func LoadHvoyProviderGroupMultipliers(ctx context.Context, lister HvoyProviderGroupLister) (map[string]float64, error) {
	if lister == nil {
		return map[string]float64{}, nil
	}
	groups, err := lister.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]float64, len(groups))
	for _, group := range groups {
		byName[normalizeHvoyGroupName(group.Name)] = group.RateMultiplier
	}
	out := make(map[string]float64, len(hvoyProviderPricingModels))
	for _, model := range hvoyProviderPricingModels {
		if multiplier, ok := byName[normalizeHvoyGroupName(model.groupName)]; ok {
			out[model.groupName] = multiplier
		}
	}
	return out, nil
}

func normalizeHvoyGroupName(name string) string {
	return strings.Join(strings.Fields(strings.ToLower(name)), " ")
}

// BuildHvoyProviderPricing renders the published price list. groupMultipliers
// holds each group's rate multiplier (the "Nx" usage rate billing applies on
// top of the official price); a missing or non-positive entry publishes the
// model at 1x with a note so the gap is visible to hvoy.
func (s *PricingService) BuildHvoyProviderPricing(paymentMultiplier float64, groupMultipliers map[string]float64, siteName, frontendURL string, now time.Time) HvoyProviderPricingResponse {
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

		rateMultiplier, note := hvoyGroupRateMultiplier(groupMultipliers, model.groupName)
		models = append(models, HvoyProviderPricingModel{
			ModelName:          model.modelName,
			GroupName:          model.groupName,
			InputPrice:         usdPerTokenToCNYPerMTok(pricing.InputCostPerToken*rateMultiplier, multiplier),
			OutputPrice:        optionalUSDPerTokenToCNYPerMTok(pricing.OutputCostPerToken*rateMultiplier, multiplier),
			CacheInputPrice:    optionalUSDPerTokenToCNYPerMTok(pricing.CacheReadInputTokenCost*rateMultiplier, multiplier),
			CacheCreatePrice:   optionalUSDPerTokenToCNYPerMTok(pricing.CacheCreationInputTokenCost*rateMultiplier, multiplier),
			CacheCreatePrice1H: optionalUSDPerTokenToCNYPerMTok(pricing.CacheCreationInputTokenCostAbove1hr*rateMultiplier, multiplier),
			Enabled:            true,
			Note:               note,
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

func hvoyGroupRateMultiplier(groupMultipliers map[string]float64, groupName string) (float64, string) {
	rate, ok := groupMultipliers[groupName]
	if !ok {
		return 1, "group rate multiplier unavailable"
	}
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 {
		return 1, "group rate multiplier invalid"
	}
	return rate, ""
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
