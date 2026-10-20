package service

// LookupOfficialPriceFact 实现 OfficialPriceFactSource：查询官方价数据里关于某个模型的两条事实，
// 供派生函数作为显式输入使用（CHECK_OPUS_3 3.4）。
//   - HasPrice：动态目录或内置兜底里有这个模型的价格（GetModelPricing 不报「无价」）；
//   - ImageCapable：与 PriceQuoter 判定图片请求单价时用的是同一个函数（quoteModelImageCapable）。
func (s *BillingService) LookupOfficialPriceFact(model string) OfficialPriceFact {
	_, err := s.GetModelPricing(model)
	return OfficialPriceFact{
		HasPrice:     err == nil,
		ImageCapable: s.quoteModelImageCapable(model),
	}
}

// LookupOfficialPriceState 实现 OfficialPriceStateSource（W6 PR4b-2a，保存时校验用，设计 5.2 的 S-5）：
//   - Known：GetModelPricing 不报「无价」；
//   - TokenNonZero：有任一正的 token 单价（含图片 token 价）；
//   - ImageCapable：与 LookupOfficialPriceFact 同一个判定。
func (s *BillingService) LookupOfficialPriceState(model string) OfficialPriceState {
	pricing, err := s.GetModelPricing(model)
	if err != nil || pricing == nil {
		return OfficialPriceState{}
	}
	tokenNonZero := pricing.InputPricePerToken > 0 || pricing.OutputPricePerToken > 0 ||
		pricing.CacheCreationPricePerToken > 0 || pricing.CacheReadPricePerToken > 0 ||
		pricing.ImageInputPricePerToken > 0 || pricing.ImageOutputPricePerToken > 0
	return OfficialPriceState{Known: true, TokenNonZero: tokenNonZero, ImageCapable: s.quoteModelImageCapable(model)}
}
