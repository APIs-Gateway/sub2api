package service

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/tidwall/gjson"
)

// BillingInflightRequest uses the selected attempt's effective billing group and
// model fields. Estimates mitigate shared concurrent admission; provider token
// accounting and completion-time pricing remain authoritative at settlement.
type BillingInflightRequest struct {
	APIKey            *APIKey
	Account           *Account
	Model             string
	Body              []byte
	Images            *OpenAIImagesRequest
	Embeddings        bool
	GeminiLongContext bool
	ChannelUsageFields
	StableDecision *OpenAIAccountScheduleDecision
	// V2 passthrough returns the frozen first wire model, no UpstreamModel or
	// ImageCount. Estimate that existing settlement shape while forwarding each
	// later frame unchanged.
	PassthroughBillingModel string
}

func inflightEstimateTokens(body []byte, defaultOutput int, embeddings bool) UsageTokens {
	// A UTF-8 byte estimate also counts tools/system and structured input. It is
	// deliberately approximate, like upstream; it is not a maximum cost cap.
	input := int(math.Ceil(float64(len(body)) / 4))
	output := defaultOutput
	if output <= 0 {
		output = 8192
	}
	for _, path := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens", "generationConfig.maxOutputTokens", "generation_config.max_output_tokens"} {
		if v := gjson.GetBytes(body, path); v.Exists() && v.Int() > 0 {
			output = int(v.Int())
			break
		}
	}
	if embeddings {
		output = 0
	}
	return UsageTokens{InputTokens: input, OutputTokens: output}
}

func inflightPreferredModel(request BillingInflightRequest, upstream string) string {
	switch request.BillingModelSource {
	case BillingModelSourceUpstream:
		return upstream
	case BillingModelSourceRequested:
		if request.OriginalModel != "" {
			return request.OriginalModel
		}
	case BillingModelSourceChannelMapped:
		if request.ChannelMappedModel != "" {
			return request.ChannelMappedModel
		}
	}
	return request.Model
}

func inflightAttemptModel(request BillingInflightRequest) string {
	model := strings.TrimSpace(gjson.GetBytes(request.Body, "model").String())
	if model == "" {
		model = request.Model
	}
	if request.Account != nil {
		model = request.Account.GetMappedModel(model)
	}
	return model
}

func applyInflightEstimate(ctx context.Context, repo UsageBillingRepository, cfg *config.Config, request BillingInflightRequest, amount float64, exclusive bool) (*BillingInflightLease, error) {
	if request.APIKey == nil || request.APIKey.User == nil {
		return nil, nil
	}
	if !finiteBillingInflightAmount(amount) {
		amount = 0
		exclusive = true
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if lease := BillingInflightLeaseFromContext(ctx); lease != nil {
		return lease, lease.Resize(ctx, amount, exclusive)
	}
	if amount == 0 && !exclusive {
		return nil, nil
	}
	return newBillingInflightLease(ctx, repo, cfg, request.APIKey.User.ID, amount, exclusive)
}

func (s *GatewayService) ReserveBillingInflight(ctx context.Context, request BillingInflightRequest) (*BillingInflightLease, error) {
	if s == nil || s.cfg == nil || !s.cfg.Billing.InflightReservation.Enabled {
		return nil, nil
	}
	if request.APIKey == nil || request.APIKey.User == nil {
		return nil, nil
	}
	key := request.APIKey
	multiplier := s.cfg.Default.RateMultiplier
	if key.GroupID != nil && key.Group != nil {
		multiplier = s.getUserGroupRateMultiplier(ctx, key.User.ID, *key.GroupID, key.Group.RateMultiplier)
	}
	upstream := inflightAttemptModel(request)
	preferred := inflightPreferredModel(request, upstream)
	model := s.billableModelWithFallback(ctx, key, preferred, request.Model, request.ChannelMappedModel, upstream)
	tokens := inflightEstimateTokens(request.Body, s.cfg.Billing.InflightReservation.DefaultMaxOutputTokens, request.Embeddings)
	result := &ForwardResult{Model: request.Model, UpstreamModel: upstream, Usage: ClaudeUsage{InputTokens: tokens.InputTokens, OutputTokens: tokens.OutputTokens}}
	if request.Images != nil {
		result.ImageCount = request.Images.N
		result.ImageSize = request.Images.SizeTier
	}
	opts := &recordUsageOpts{}
	if request.GeminiLongContext {
		opts.LongContextThreshold = 200000
		opts.LongContextMultiplier = 2
	}
	known := s.hasResolvableTokenPricing(ctx, model, key)
	pricingAt := deepseekNowFunc()
	cost := s.calculateRecordUsageCost(ctx, result, key, model, multiplier, resolveImageRateMultiplier(key, multiplier), opts, pricingAt)
	if result.ImageCount > 0 && cost.TotalCost > 0 {
		known = true
	}
	amount := cost.ActualCost
	// Cache creation can cost more than ordinary input. Hold the largest priced
	// estimate while leaving the actual cache classification untouched.
	if result.ImageCount == 0 {
		result.Usage.InputTokens = 0
		result.Usage.CacheCreationInputTokens = tokens.InputTokens
		result.Usage.CacheCreation5mTokens = tokens.InputTokens
		amount = math.Max(amount, s.calculateRecordUsageCost(ctx, result, key, model, multiplier, resolveImageRateMultiplier(key, multiplier), opts, pricingAt).ActualCost)
		result.Usage.CacheCreation5mTokens = 0
		result.Usage.CacheCreation1hTokens = tokens.InputTokens
		amount = math.Max(amount, s.calculateRecordUsageCost(ctx, result, key, model, multiplier, resolveImageRateMultiplier(key, multiplier), opts, pricingAt).ActualCost)
	}
	return applyInflightEstimate(ctx, s.usageBillingRepo, s.cfg, request, amount, !known)
}

func (s *OpenAIGatewayService) ReserveBillingInflight(ctx context.Context, request BillingInflightRequest) (*BillingInflightLease, error) {
	if s == nil || s.cfg == nil || !s.cfg.Billing.InflightReservation.Enabled {
		return nil, nil
	}
	if request.APIKey == nil || request.APIKey.User == nil {
		return nil, nil
	}
	key := request.APIKey
	if d := request.StableDecision; d != nil && key.GroupID != nil && key.Group != nil && d.StableServedGroupID > 0 && d.StableServedGroupID != *key.GroupID && d.StableServedRateMultiplier > 0 {
		group := *key.Group
		group.ID = d.StableServedGroupID
		group.RateMultiplier = d.StableServedRateMultiplier
		group.ImageRateIndependent = d.StableServedImageRateIndependent
		group.ImageRateMultiplier = d.StableServedImageRateMultiplier
		group.ImagePrice1K = d.StableServedImagePrice1K
		group.ImagePrice2K = d.StableServedImagePrice2K
		group.ImagePrice4K = d.StableServedImagePrice4K
		copyKey := *key
		copyKey.Group = &group
		copyKey.GroupID = &group.ID
		key = &copyKey
	}
	multiplier := s.cfg.Default.RateMultiplier
	if key.GroupID != nil && key.Group != nil {
		multiplier = s.ResolveUserGroupRateMultiplier(ctx, key.User.ID, *key.GroupID, key.Group.RateMultiplier)
	}
	upstream := inflightAttemptModel(request)
	models := usageBillingModelCandidates(inflightPreferredModel(request, upstream), upstream, request.ChannelMappedModel, request.OriginalModel, request.Model)
	if request.PassthroughBillingModel != "" {
		upstream = ""
		billingModel := request.PassthroughBillingModel
		if request.BillingModelSource == BillingModelSourceChannelMapped && request.ChannelMappedModel != "" && request.ChannelMappedModel != request.OriginalModel {
			billingModel = request.ChannelMappedModel
		}
		if request.BillingModelSource == BillingModelSourceRequested && request.OriginalModel != "" {
			billingModel = request.OriginalModel
		}
		models = usageBillingModelCandidates(billingModel, request.ChannelMappedModel, request.OriginalModel, request.PassthroughBillingModel)
	}
	tokens := inflightEstimateTokens(request.Body, s.cfg.Billing.InflightReservation.DefaultMaxOutputTokens, request.Embeddings)
	result := &OpenAIForwardResult{Model: request.Model, UpstreamModel: upstream}
	if request.Images != nil {
		result.ImageCount = request.Images.N
		result.ImageSize = request.Images.SizeTier
	}
	if request.Images == nil && request.PassthroughBillingModel == "" && IsImageGenerationIntent(openAIResponsesEndpoint, upstream, request.Body) {
		imageCfg, err := resolveOpenAIResponsesImageBillingConfigDetailedFromBody(request.Body, upstream)
		if err != nil {
			return applyInflightEstimate(ctx, s.usageBillingRepo, s.cfg, request, 0, true)
		}
		result.ImageCount = 1
		result.ImageSize = imageCfg.SizeTier
		result.BillingModel = imageCfg.Model
		// Match RecordUsage: a generated image's billing model takes precedence
		// over the chat model unless the explicit channel/request policy overrides it.
		billingModel := imageCfg.Model
		if request.BillingModelSource == BillingModelSourceChannelMapped && request.ChannelMappedModel != "" && request.ChannelMappedModel != request.OriginalModel {
			billingModel = request.ChannelMappedModel
		}
		if request.BillingModelSource == BillingModelSourceRequested && request.OriginalModel != "" {
			billingModel = request.OriginalModel
		}
		models = usageBillingModelCandidates(billingModel, imageCfg.Model, request.ChannelMappedModel, request.OriginalModel, upstream, request.Model)
		tokens.ImageOutputTokens = tokens.OutputTokens
	}
	tier := gjson.GetBytes(request.Body, "service_tier").String()
	pricingAt := deepseekNowFunc()
	cost, err := s.calculateOpenAIRecordUsageCost(ctx, result, key, models, multiplier, resolveImageRateMultiplier(key, multiplier), tokens, tier, pricingAt)
	if err != nil {
		return applyInflightEstimate(ctx, s.usageBillingRepo, s.cfg, request, 0, true)
	}
	amount := cost.ActualCost
	if result.ImageCount == 0 {
		tokens.CacheCreationTokens = tokens.InputTokens
		tokens.InputTokens = 0
		if cacheCost, err := s.calculateOpenAIRecordUsageCost(ctx, result, key, models, multiplier, resolveImageRateMultiplier(key, multiplier), tokens, tier, pricingAt); err == nil {
			amount = math.Max(amount, cacheCost.ActualCost)
		}
	}
	return applyInflightEstimate(ctx, s.usageBillingRepo, s.cfg, request, amount, false)
}
