package service

// PricingWriteServices W6 PR4b-2b-2：价格写入路径的服务束，由依赖注入装配一次，交给管理端 handler。
// 这是写入路径第一次有生产代码构造：所有分组默认 pricing_stage = legacy，写入器只写 v2 分组，
// 所以装配之后线上没有任何行为变化；接口都挂在管理端，涉价的提交只允许交互式管理员会话。
type PricingWriteServices struct {
	Cells     *InterimPriceWriteGate
	GroupCfg  *GroupConfigService
	KnownFree *KnownFreeListService
	Catalog   *ModelCatalogTransitionService
	CostRules *CostRuleService
	Precheck  *OpenPrechecker
}

// ProvidePricingWriteServices 装配价格写入路径。quoter 同时被接上矩阵来源（只读），QuoteWith 因此可用，
// 估算器用它回答「这次写入让用户实付涨还是跌」。
func ProvidePricingWriteServices(
	store PriceWriteStore,
	cells CellWriter,
	groups GroupConfigWriter,
	reader ExposureReader,
	free KnownFreeListStore,
	catalogStore ModelCatalogStatusStore,
	costWriter CostRuleWriter,
	matrix PricingMatrixRepository,
	quoter *PriceQuoter,
	billing *BillingService,
	settings SettingRepository,
	policy *StagedGroupPolicy,
	catalog *ModelCatalogService,
) *PricingWriteServices {
	var invalidator MatrixSnapshotInvalidator
	if policy != nil { // 不能直接传 nil 的 *StagedGroupPolicy：那会得到一个非 nil 的接口值
		invalidator = policy
	}
	var prices OfficialPriceStateSource // 同样不能直接传 nil 的 *BillingService
	if billing != nil {
		prices = billing
	}
	var estimator PriceDeltaEstimator
	if quoter != nil {
		quoter.SetMatrixSource(matrix)
		estimator = NewPriceEstimator(quoter)
	}
	validator := NewExposureValidator(prices, settings)
	txw := NewMatrixTxWriter(cells, groups, NewExposureGuard(reader, validator))
	precheck := NewOpenPrechecker(matrix, validator, prices)
	return &PricingWriteServices{
		Cells:     NewInterimPriceWriteGate(store, txw, estimator, invalidator).WithOpenPrecheck(precheck),
		GroupCfg:  NewGroupConfigService(store, txw, estimator, invalidator).WithOpenPrecheck(precheck),
		KnownFree: NewKnownFreeListService(store, free, prices),
		Catalog:   NewModelCatalogTransitionService(catalogStore, catalog),
		CostRules: NewCostRuleService(store, costWriter, invalidator),
		Precheck:  precheck,
	}
}
