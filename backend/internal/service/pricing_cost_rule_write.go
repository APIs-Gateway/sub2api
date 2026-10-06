package service

import (
	"context"
	"sort"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 PR4b-2b-2：成本核算规则的写接口（设计 2.5，第 9 节 PR4b：「成本核算规则写接口」）。
//
// 规则按分组各存一行（scope_group_id），与单元格对称：只写 pricing_stage = 'v2' 的分组；
// legacy_derived 行由渠道派生、只读，只能改删 manual 与 legacy_frozen 行，每次只动被选中分组的那一行。
// 写入与单元格写入共用同一把锁（group_model_config 行 FOR UPDATE）和同一个基线（分组配置 revision），
// 写完把分组配置 revision 加一，提交后失效分组快照缓存。
// 成本核算只影响 usage_logs.account_stats_cost（账号成本），不改变用户实付，所以不走价格方向审批。

// 成本核算规则写入的错误原因。
const (
	ReasonCostRuleInvalid  = "COST_RULE_INVALID"
	ReasonCostRuleNotFound = "COST_RULE_NOT_FOUND"
	// ReasonCostRuleReadonly 规则是由渠道派生的（legacy_derived），要在渠道页修改。
	ReasonCostRuleReadonly = "COST_RULE_READONLY"
)

const (
	maxCostRuleNameLen   = 100
	maxCostRulePrices    = 200
	maxCostRuleModels    = 200
	maxCostRuleIDs       = 1000
	maxCostRuleModelName = 200
)

// CostRuleSpec 一条规则的目标态（整个替换）。
type CostRuleSpec struct {
	Name       string                `json:"name"`
	GroupIDs   []int64               `json:"group_ids"`
	AccountIDs []int64               `json:"account_ids"`
	SortOrder  int                   `json:"sort_order"`
	Enabled    bool                  `json:"enabled"`
	Prices     []MatrixCostRulePrice `json:"prices"`
}

// CostRuleWriteResult 一次规则写入的结果。
type CostRuleWriteResult struct {
	GroupID int64 `json:"group_id"`
	// RuleID 创建与更新的规则 id；删除时是被删的规则 id。
	RuleID int64 `json:"rule_id"`
	// Revision 写入之后的分组配置 revision。
	Revision int64 `json:"revision"`
}

// CostRuleWriter 成本核算规则的 tx-aware 写入器。三个方法都必须在事务里调用：先锁分组配置行，确认分组是 v2 且
// 基线 revision 一致，再写规则，最后把分组配置 revision 加一。
type CostRuleWriter interface {
	CreateTx(ctx context.Context, tx MatrixTx, groupID, baseline int64, spec CostRuleSpec) (*CostRuleWriteResult, error)
	UpdateTx(ctx context.Context, tx MatrixTx, groupID, baseline, ruleID int64, spec CostRuleSpec) (*CostRuleWriteResult, error)
	DeleteTx(ctx context.Context, tx MatrixTx, groupID, baseline, ruleID int64) (*CostRuleWriteResult, error)
}

// CostRuleService 成本核算规则写入的服务层。
type CostRuleService struct {
	store       PriceWriteStore
	writer      CostRuleWriter
	invalidator MatrixSnapshotInvalidator
}

// NewCostRuleService 创建服务；invalidator 可为 nil（没有读取方）。
func NewCostRuleService(store PriceWriteStore, writer CostRuleWriter, invalidator MatrixSnapshotInvalidator) *CostRuleService {
	return &CostRuleService{store: store, writer: writer, invalidator: invalidator}
}

func costRuleInvalid(msg string) error {
	return infraerrors.BadRequest(ReasonCostRuleInvalid, msg)
}

func normalizeCostRuleIDs(field string, in []int64) ([]int64, error) {
	if len(in) > maxCostRuleIDs {
		return nil, costRuleInvalid(field + " has too many entries")
	}
	out := dedupeSortedIDs(in)
	if len(out) != len(in) {
		// dedupeSortedIDs 丢掉非正数与重复项；数量对不上说明请求里有不合法的 id。
		return nil, costRuleInvalid(field + " must contain distinct positive ids")
	}
	return out, nil
}

// NormalizeCostRuleSpec 校验并规范一条规则：名字、命中条件、价格行（与渠道保存同一套价格校验）。
func NormalizeCostRuleSpec(in CostRuleSpec) (CostRuleSpec, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > maxCostRuleNameLen {
		return CostRuleSpec{}, costRuleInvalid("name must be 1 to " + strconv.Itoa(maxCostRuleNameLen) + " characters")
	}
	var err error
	if in.GroupIDs, err = normalizeCostRuleIDs("group_ids", in.GroupIDs); err != nil {
		return CostRuleSpec{}, err
	}
	if in.AccountIDs, err = normalizeCostRuleIDs("account_ids", in.AccountIDs); err != nil {
		return CostRuleSpec{}, err
	}
	if len(in.Prices) == 0 || len(in.Prices) > maxCostRulePrices {
		return CostRuleSpec{}, costRuleInvalid("prices must have 1 to " + strconv.Itoa(maxCostRulePrices) + " rows")
	}
	prices := make([]MatrixCostRulePrice, 0, len(in.Prices))
	for _, p := range in.Prices {
		np, err := normalizeCostRulePrice(p)
		if err != nil {
			return CostRuleSpec{}, err
		}
		prices = append(prices, np)
	}
	in.Prices = prices
	return in, nil
}

func normalizeCostRulePrice(p MatrixCostRulePrice) (MatrixCostRulePrice, error) {
	p.Platform = strings.TrimSpace(p.Platform)
	if len(p.Platform) > 32 {
		return p, costRuleInvalid("platform is longer than 32 characters")
	}
	if len(p.Models) == 0 || len(p.Models) > maxCostRuleModels {
		return p, costRuleInvalid("each price row needs 1 to " + strconv.Itoa(maxCostRuleModels) + " models")
	}
	models := make([]string, 0, len(p.Models))
	for _, m := range p.Models {
		m = strings.TrimSpace(m)
		if m == "" || len(m) > maxCostRuleModelName {
			return p, costRuleInvalid("model names must be non-empty and at most " + strconv.Itoa(maxCostRuleModelName) + " characters")
		}
		models = append(models, m)
	}
	sort.Strings(models)
	p.Models = models
	if p.Price.BillingMode == "" {
		p.Price.BillingMode = BillingModeToken
	}
	switch p.Price.BillingMode {
	case BillingModeToken, BillingModePerRequest, BillingModeImage:
	default:
		return p, costRuleInvalid("unknown billing_mode in price")
	}
	pricing := []ChannelModelPricing{p.Price.ToChannelModelPricing(p.Platform, models)}
	if err := validatePricingBillingMode(pricing); err != nil {
		return p, costRuleInvalid(infraerrors.Message(err))
	}
	if err := validatePricingIntervals(pricing); err != nil {
		return p, costRuleInvalid(infraerrors.Message(err))
	}
	return p, nil
}

func (s *CostRuleService) check(actor, groupID, baseline int64) error {
	if actor <= 0 {
		return infraerrors.Forbidden(ReasonPriceWriteActorRequired, "an administrator is required")
	}
	if groupID <= 0 || baseline <= 0 {
		return costRuleInvalid("group_id and baseline_revision must be positive")
	}
	if s == nil || s.writer == nil || s.store == nil {
		return infraerrors.InternalServer(ReasonPriceWriterMissing, "cost rule writer is not configured")
	}
	return nil
}

func (s *CostRuleService) run(ctx context.Context, fn func(ctx context.Context, tx MatrixTx) (*CostRuleWriteResult, error)) (*CostRuleWriteResult, error) {
	var res *CostRuleWriteResult
	err := s.store.WithTx(ctx, func(ctx context.Context, tx MatrixTx) error {
		r, err := fn(ctx, tx)
		if err != nil {
			return err
		}
		res = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	if s.invalidator != nil {
		s.invalidator.InvalidateGroups(res.GroupID)
	}
	return res, nil
}

// Create 在 v2 分组上新建一条 manual 规则。
func (s *CostRuleService) Create(ctx context.Context, actor, groupID, baseline int64, spec CostRuleSpec) (*CostRuleWriteResult, error) {
	if err := s.check(actor, groupID, baseline); err != nil {
		return nil, err
	}
	norm, err := NormalizeCostRuleSpec(spec)
	if err != nil {
		return nil, err
	}
	return s.run(ctx, func(ctx context.Context, tx MatrixTx) (*CostRuleWriteResult, error) {
		return s.writer.CreateTx(ctx, tx, groupID, baseline, norm)
	})
}

// Update 整个替换一条 manual 或 legacy_frozen 规则。
func (s *CostRuleService) Update(ctx context.Context, actor, groupID, baseline, ruleID int64, spec CostRuleSpec) (*CostRuleWriteResult, error) {
	if err := s.check(actor, groupID, baseline); err != nil {
		return nil, err
	}
	if ruleID <= 0 {
		return nil, costRuleInvalid("rule_id must be positive")
	}
	norm, err := NormalizeCostRuleSpec(spec)
	if err != nil {
		return nil, err
	}
	return s.run(ctx, func(ctx context.Context, tx MatrixTx) (*CostRuleWriteResult, error) {
		return s.writer.UpdateTx(ctx, tx, groupID, baseline, ruleID, norm)
	})
}

// Delete 删除一条 manual 或 legacy_frozen 规则。
func (s *CostRuleService) Delete(ctx context.Context, actor, groupID, baseline, ruleID int64) (*CostRuleWriteResult, error) {
	if err := s.check(actor, groupID, baseline); err != nil {
		return nil, err
	}
	if ruleID <= 0 {
		return nil, costRuleInvalid("rule_id must be positive")
	}
	return s.run(ctx, func(ctx context.Context, tx MatrixTx) (*CostRuleWriteResult, error) {
		return s.writer.DeleteTx(ctx, tx, groupID, baseline, ruleID)
	})
}
