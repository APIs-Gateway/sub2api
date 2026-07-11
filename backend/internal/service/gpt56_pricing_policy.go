package service

const gpt56NoSurchargeTokenThreshold = 272000

// normalizeGPT56ChannelTokenPricing makes a channel's token intervals obey the
// GPT-5.6 no-surcharge boundary without mutating the stored channel config.
func normalizeGPT56ChannelTokenPricing(model string, pricing *ChannelModelPricing) *ChannelModelPricing {
	if pricing == nil || !isOpenAIGPT56Model(model) ||
		pricing.BillingMode == BillingModePerRequest || pricing.BillingMode == BillingModeImage ||
		len(pricing.Intervals) == 0 {
		return pricing
	}

	cloned := pricing.Clone()
	base := gpt56ChannelBaseInterval(&cloned)
	cloned.Intervals = clipGPT56ChannelIntervals(cloned.Intervals, base)
	return &cloned
}

func gpt56ChannelBaseInterval(pricing *ChannelModelPricing) PricingInterval {
	base := PricingInterval{
		InputPrice:      clonePricePtr(pricing.InputPrice),
		OutputPrice:     clonePricePtr(pricing.OutputPrice),
		CacheWritePrice: clonePricePtr(pricing.CacheWritePrice),
		CacheReadPrice:  clonePricePtr(pricing.CacheReadPrice),
	}

	// A common channel shape has a base interval ending at 272K and a more
	// expensive unbounded interval. Use that base interval when flat prices are
	// omitted.
	if iv := FindMatchingInterval(pricing.Intervals, gpt56NoSurchargeTokenThreshold); iv != nil {
		fillMissingIntervalPrices(&base, iv)
	}
	if !hasTokenIntervalPrice(&base) {
		for i := range pricing.Intervals {
			if hasTokenIntervalPrice(&pricing.Intervals[i]) {
				fillMissingIntervalPrices(&base, &pricing.Intervals[i])
				break
			}
		}
	}
	return base
}

func clipGPT56ChannelIntervals(intervals []PricingInterval, base PricingInterval) []PricingInterval {
	clipped := make([]PricingInterval, 0, len(intervals)+1)
	for _, iv := range intervals {
		if iv.MinTokens >= gpt56NoSurchargeTokenThreshold {
			continue
		}
		if iv.MaxTokens != nil && *iv.MaxTokens <= gpt56NoSurchargeTokenThreshold {
			clipped = append(clipped, iv)
			continue
		}

		iv.MaxTokens = gpt56IntPtr(gpt56NoSurchargeTokenThreshold)
		clipped = append(clipped, iv)
	}

	if hasTokenIntervalPrice(&base) {
		base.MinTokens = gpt56NoSurchargeTokenThreshold
		base.MaxTokens = nil
		base.SortOrder = len(clipped)
		clipped = append(clipped, base)
	}
	return clipped
}

func fillMissingIntervalPrices(dst *PricingInterval, src *PricingInterval) {
	if dst.InputPrice == nil {
		dst.InputPrice = clonePricePtr(src.InputPrice)
	}
	if dst.OutputPrice == nil {
		dst.OutputPrice = clonePricePtr(src.OutputPrice)
	}
	if dst.CacheWritePrice == nil {
		dst.CacheWritePrice = clonePricePtr(src.CacheWritePrice)
	}
	if dst.CacheReadPrice == nil {
		dst.CacheReadPrice = clonePricePtr(src.CacheReadPrice)
	}
}

func hasTokenIntervalPrice(iv *PricingInterval) bool {
	return iv != nil && (iv.InputPrice != nil || iv.OutputPrice != nil ||
		iv.CacheWritePrice != nil || iv.CacheReadPrice != nil)
}

func clonePricePtr(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func gpt56IntPtr(value int) *int {
	return &value
}
