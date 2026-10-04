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
