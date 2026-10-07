package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// W6 PR4b-2b-1：价格方向的服务端估算器（设计 6.3、8 与 REVIEW_OPUS_2 给 4b-2b 的要求 ②③）。
//
// 价格写入的方向（up / down / none / unknown）只能来自这里：请求体里没有、也不该有这个字段，
// 否则请求方自己报一个 none，机器令牌就能做涉价写入。估算器用 QuoteWith 对「写入前」与「写入后」各报一次价，再用 PriceDiff 比较：
//   - 每一档 service tier 各报一次（标准、priority、flex）：inherit 改成 custom 会让 priority 档的独立价变成和标准档一样，
//     只报标准档会漏；
//   - DeepSeek 的模型在峰时与非峰时各报一次：峰价只作用于默认价卡，改成 custom 以后就没有峰价了；
//   - 任何一份报价失败、任一比较是 unknown，整体就是 unknown（最严）。

// PriceDeltaEstimator 价格方向的唯一来源。实现必须是确定的、只读的；出错时调用方按 unknown 处理。
type PriceDeltaEstimator interface {
	// EstimateCellWrites 估算一批规划好的单元格写入的价格方向。没有涉价的写入返回 none。
	EstimateCellWrites(ctx context.Context, planned []PlannedCellWrite) (PriceDelta, error)
	// EstimateGroupConfig 估算分组配置改动的价格方向：改计费来源或模型映射会改变请求按哪个模型计费。
	EstimateGroupConfig(ctx context.Context, groupID int64, before, after MatrixGroupConfig) (PriceDelta, error)
}

// estimatorServiceTiers 每个模型报价时覆盖的 service tier：标准档、priority 档、flex 档。
// fast 归为 priority，auto、default、scale 按标准档计价，所以这三档已经覆盖全部不同的计价方式。
var estimatorServiceTiers = []string{"", "priority", "flex"}

// estimatorDeepSeekHorizon 找 DeepSeek 峰时与非峰时参考时刻时最多向后看几个小时。
const estimatorDeepSeekHorizon = 24 * 8

// PriceEstimator 用 PriceQuoter 的 QuoteWith 实现 PriceDeltaEstimator。报价器必须已经接上 SetMatrixSource。
type PriceEstimator struct {
	quoter *PriceQuoter
	now    func() time.Time
}

var _ PriceDeltaEstimator = (*PriceEstimator)(nil)

// NewPriceEstimator 创建估算器。
func NewPriceEstimator(quoter *PriceQuoter) *PriceEstimator {
	return &PriceEstimator{quoter: quoter, now: time.Now}
}

// estimateVariant 一次报价的参数：service tier 与计费时刻。
type estimateVariant struct {
	tier string
	at   time.Time
}

// estimateVariants 一个模型要报价的全部组合。写入前后两次报价必须用同一组时刻，所以这里固定下来：
// 普通模型用「现在」，DeepSeek 用下一个峰时与下一个非峰时。
func estimateVariants(model string, now time.Time) []estimateVariant {
	times := []time.Time{now}
	if isDeepSeekModel(model) {
		peak, off := deepseekReferenceTimes(now)
		times = []time.Time{peak, off}
	}
	out := make([]estimateVariant, 0, len(estimatorServiceTiers)*len(times))
	for _, tier := range estimatorServiceTiers {
		for _, at := range times {
			out = append(out, estimateVariant{tier: tier, at: at})
		}
	}
	return out
}

// deepseekReferenceTimes 从 now 起向后找一个 DeepSeek 峰时时刻和一个非峰时时刻（按整点取）。
// 找不到（不可能，峰时每个工作日都有）时用 now 兜底。
func deepseekReferenceTimes(now time.Time) (peak, offPeak time.Time) {
	t := now.UTC().Truncate(time.Hour)
	for i := 0; i < estimatorDeepSeekHorizon && (peak.IsZero() || offPeak.IsZero()); i++ {
		if deepseekPeakMultiplierAt(t) > 1 {
			if peak.IsZero() {
				peak = t
			}
		} else if offPeak.IsZero() {
			offPeak = t
		}
		t = t.Add(time.Hour)
	}
	if peak.IsZero() {
		peak = now
	}
	if offPeak.IsZero() {
		offPeak = now
	}
	return peak, offPeak
}

// EstimateCellWrites 实现 PriceDeltaEstimator：对每个涉价的写入，在叠加前后各报一次价（每个组合一次），再逐对比较。
func (e *PriceEstimator) EstimateCellWrites(ctx context.Context, planned []PlannedCellWrite) (PriceDelta, error) {
	if e == nil || e.quoter == nil {
		return PriceDeltaUnknown, ErrPriceQuoterUnavailable
	}
	now := e.now()
	var reqs []QuoteRequest
	for _, p := range planned {
		if p.Action == CellWriteNoop || !p.TouchesPrice {
			continue
		}
		for _, v := range estimateVariants(p.Op.ModelKey, now) {
			reqs = append(reqs, QuoteRequest{Model: p.Op.ModelKey, GroupID: p.Op.GroupID, ServiceTier: v.tier, At: v.at})
		}
	}
	if len(reqs) == 0 {
		return PriceDeltaNone, nil
	}
	before, err := e.quoter.BatchQuoteWith(ctx, reqs, CellOverlay{})
	if err != nil {
		return PriceDeltaUnknown, err
	}
	after, err := e.quoter.BatchQuoteWith(ctx, reqs, OverlayFromPlanned(planned))
	if err != nil {
		return PriceDeltaUnknown, err
	}
	return diffQuotes(before, after), nil
}

// diffQuotes 逐对比较，合成一个方向；长度对不上按 unknown。
func diffQuotes(before, after []*Quote) PriceDelta {
	if len(before) != len(after) {
		return PriceDeltaUnknown
	}
	deltas := make([]PriceDelta, len(before))
	for i := range before {
		deltas[i] = PriceDiff(before[i], after[i])
	}
	return CombinePriceDeltas(deltas...)
}

// EstimateGroupConfig 实现 PriceDeltaEstimator。
//
// 改计费来源或模型映射，改变的是「一个请求模型按哪个模型计费」。原则：证明不了「没变」就给 unknown。
// 对要比较的每个请求模型，算出改前、改后的计费模型，两者不同就各自报价（单元格不变，叠加层为空），再比较。规则：
//   - 计费来源是 upstream（改前或改后）：计费模型取决于账号的上游模型，这里算不出来，unknown；
//   - 被改动的映射里有通配符来源：受影响的请求模型数不封顶，unknown；
//   - 计费来源在 requested 与非 requested 之间切换：全部映射来源（改前、改后两份映射里的精确来源）都要比较，
//     不只是 open 的单元格（开放分组里的映射来源通常是别名、不是单元格，照样可以被请求）；
//     任何一份映射里有通配符来源也是 unknown，因为它命中的请求模型数不封顶；
//   - 计费来源是 requested、映射目标变了：计费模型不变，但映射目标仍在运行时的候选链里
//     （源模型没有价时回落到映射后的模型，OpenAI 的图片请求还会在候选里挑按次或图片模式的模型），
//     算不出来，unknown；
//   - 其余按上面的办法逐个比较；没有任何模型的计费模型变了，就是 none。
//
// 要比较的模型是：分组里 open 的精确单元格，加上被改动的映射来源（以及计费来源切换时的全部映射来源）。
func (e *PriceEstimator) EstimateGroupConfig(ctx context.Context, groupID int64, before, after MatrixGroupConfig) (PriceDelta, error) {
	if !GroupConfigTouchesPrice(before, after) {
		return PriceDeltaNone, nil
	}
	if e == nil || e.quoter == nil || e.quoter.matrixSource == nil {
		return PriceDeltaUnknown, ErrPriceQuoterUnavailable
	}
	if isUpstreamBillingSource(before) || isUpstreamBillingSource(after) {
		return PriceDeltaUnknown, nil
	}
	changed, wildcard := changedMappingSources(before.ModelMapping, after.ModelMapping)
	if wildcard {
		return PriceDeltaUnknown, nil
	}
	reqBefore, reqAfter := isRequestedBillingSource(before), isRequestedBillingSource(after)
	if reqBefore && reqAfter && len(changed) > 0 {
		return PriceDeltaUnknown, nil
	}
	sources := changed
	if reqBefore != reqAfter {
		for _, m := range [][]MatrixMappingEntry{before.ModelMapping, after.ModelMapping} {
			exact, wc := mappingSourceNames(m)
			if wc {
				return PriceDeltaUnknown, nil
			}
			sources = append(sources, exact...)
		}
	}

	models, err := e.groupConfigModels(ctx, groupID, sources)
	if err != nil {
		return PriceDeltaUnknown, err
	}
	return e.compareBillingModels(ctx, groupID, models, before, after, e.now())
}

// compareBillingModels 对每个计费模型发生变化的请求模型，在同一组「service tier x 时刻」上报改前、改后的计费模型的价并比较。
// 所有模型的请求收齐以后，改前、改后各做一次批量报价（每次只读一次分组快照）。
// 时刻取并集：只要任一侧是 DeepSeek，两侧都按峰时与非峰时各报一次，保证两侧的组合一一对应。
func (e *PriceEstimator) compareBillingModels(ctx context.Context, groupID int64, models []string, before, after MatrixGroupConfig, now time.Time) (PriceDelta, error) {
	var reqsB, reqsA []QuoteRequest
	for _, m := range models {
		bb, ba := estimatorBillingModel(groupID, before, m), estimatorBillingModel(groupID, after, m)
		if strings.EqualFold(strings.TrimSpace(bb), strings.TrimSpace(ba)) {
			continue
		}
		times := []time.Time{now}
		if isDeepSeekModel(bb) || isDeepSeekModel(ba) {
			peak, off := deepseekReferenceTimes(now)
			times = []time.Time{peak, off}
		}
		for _, tier := range estimatorServiceTiers {
			for _, at := range times {
				reqsB = append(reqsB, QuoteRequest{Model: bb, GroupID: groupID, ServiceTier: tier, At: at})
				reqsA = append(reqsA, QuoteRequest{Model: ba, GroupID: groupID, ServiceTier: tier, At: at})
			}
		}
	}
	if len(reqsB) == 0 {
		return PriceDeltaNone, nil
	}
	qb, err := e.quoter.BatchQuoteWith(ctx, reqsB, CellOverlay{})
	if err != nil {
		return PriceDeltaUnknown, err
	}
	qa, err := e.quoter.BatchQuoteWith(ctx, reqsA, CellOverlay{})
	if err != nil {
		return PriceDeltaUnknown, err
	}
	return diffQuotes(qb, qa), nil
}

// groupConfigModels 要比较的请求模型：分组里 open 的精确单元格，加上映射里被改动的精确来源名。去重、排序。
func (e *PriceEstimator) groupConfigModels(ctx context.Context, groupID int64, changedSources []string) ([]string, error) {
	snaps, err := e.quoter.matrixSource.LoadGroupSnapshots(ctx, []int64{groupID})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m == "" {
			return
		}
		key := normalizeChannelPricingModelName(m)
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, m)
	}
	for _, c := range snaps[groupID].Cells {
		if c.Open && !c.IsPattern {
			add(c.ModelKey)
		}
	}
	for _, src := range changedSources {
		add(src)
	}
	sort.Strings(out)
	return out, nil
}

func isRequestedBillingSource(cfg MatrixGroupConfig) bool {
	return cfg.BillingModelSource != nil && *cfg.BillingModelSource == BillingModelSourceRequested
}

// mappingSourceNames 映射里的精确来源名（小写）；有通配符来源时 wildcard 为 true。
func mappingSourceNames(entries []MatrixMappingEntry) (exact []string, wildcard bool) {
	for _, e := range entries {
		src := strings.ToLower(strings.TrimSpace(e.Src))
		if strings.HasSuffix(src, "*") {
			wildcard = true
			continue
		}
		exact = append(exact, src)
	}
	return exact, wildcard
}

func isUpstreamBillingSource(cfg MatrixGroupConfig) bool {
	return cfg.BillingModelSource != nil && *cfg.BillingModelSource == BillingModelSourceUpstream
}

// changedMappingSources 比较前后两份映射，返回目标发生变化（新增、删除、改目标）的精确来源名；
// 有通配符来源发生变化时 wildcard 为 true。来源名小写比较。
func changedMappingSources(before, after []MatrixMappingEntry) (exact []string, wildcard bool) {
	toMap := func(entries []MatrixMappingEntry) map[string]string {
		m := make(map[string]string, len(entries))
		for _, e := range entries {
			m[strings.ToLower(strings.TrimSpace(e.Src))] = strings.TrimSpace(e.Dst)
		}
		return m
	}
	b, a := toMap(before), toMap(after)
	note := func(src string) {
		if strings.HasSuffix(src, "*") {
			wildcard = true
			return
		}
		exact = append(exact, src)
	}
	for src, dst := range b {
		if adst, ok := a[src]; !ok || adst != dst {
			note(src)
		}
	}
	for src := range a {
		if _, ok := b[src]; !ok {
			note(src)
		}
	}
	sort.Strings(exact)
	return exact, wildcard
}

// estimatorBillingModel 请求模型 model 在配置 cfg 下按哪个模型计费（计费来源不是 upstream）：
// requested 就是请求模型；channel_mapped 与没有渠道（空）都是分组映射之后的模型，没有映射则还是请求模型。
func estimatorBillingModel(groupID int64, cfg MatrixGroupConfig, model string) string {
	if isRequestedBillingSource(cfg) {
		return model
	}
	snap := buildMatrixSnapshot(groupID, "", GroupStateSnapshot{
		Config: &StoredGroupConfig{GroupID: groupID, MatrixGroupConfig: cfg, PricingStage: PricingStageV2},
	})
	if mapped := snap.lookupMapping(strings.ToLower(strings.TrimSpace(model))); mapped != "" {
		return mapped
	}
	return model
}

// errPriceEstimateInvalid 估算器返回了不认识的方向。
var errPriceEstimateInvalid = errors.New("price estimator returned an unknown direction")

// validPriceDelta 校验估算器返回的方向是四个取值之一。
func validPriceDelta(d PriceDelta) error {
	switch d {
	case PriceDeltaUp, PriceDeltaDown, PriceDeltaNone, PriceDeltaUnknown:
		return nil
	}
	return errPriceEstimateInvalid
}
