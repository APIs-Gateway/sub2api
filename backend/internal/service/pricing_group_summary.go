package service

import (
	"context"
	"sort"
	"strconv"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 价格矩阵页的批量只读摘要：一次拿全各分组的阶段、配置 revision 与成本核算规则概况，避免前端对每个分组各请求一次（N+1）。

// MaxGroupPricingSummaries 一次最多返回（或最多接受 ids）的分组数。
const MaxGroupPricingSummaries = 500

// ReasonGroupSummaryTooMany 请求的 ids 超过上限。
const ReasonGroupSummaryTooMany = "GROUP_SUMMARY_TOO_MANY_IDS"

// CostRuleSummary 一个分组的成本核算规则概况（按 scope_group_id 聚合）。
type CostRuleSummary struct {
	Total         int `json:"total"`
	Enabled       int `json:"enabled"`
	LegacyDerived int `json:"legacy_derived"`
	LegacyFrozen  int `json:"legacy_frozen"`
	Manual        int `json:"manual"`
}

// GroupPricingSummary 一个分组的阶段、配置 revision 与成本核算规则摘要。
type GroupPricingSummary struct {
	GroupID  int64  `json:"group_id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	// HasConfig 为 false 表示 group_model_config 行不存在：按默认值（legacy、open、account_rate）处理，Revision 为 0。
	HasConfig bool             `json:"has_config"`
	Stage     PricingStage     `json:"pricing_stage"`
	Revision  int64            `json:"config_revision"`
	Access    MatrixAccessMode `json:"access_mode"`
	CostMode  MatrixCostMode   `json:"cost_mode"`
	// StageChangedAt、ConfigUpdatedAt 没有配置行或从未切换时为空。
	StageChangedAt  *time.Time      `json:"stage_changed_at"`
	ConfigUpdatedAt *time.Time      `json:"config_updated_at"`
	CostRules       CostRuleSummary `json:"cost_rules"`
}

// GroupPricingSummaryResult 批量摘要的结果。
type GroupPricingSummaryResult struct {
	Items []GroupPricingSummary `json:"items"`
	// MissingIDs 是请求里给了、但不存在或已删除的分组 id（没传 ids 时为空数组）。
	MissingIDs []int64 `json:"missing_ids"`
	// Truncated 为 true 表示没传 ids 时可见分组多于上限，只返回了前 MaxGroupPricingSummaries 个（按 id 升序）。
	Truncated bool `json:"truncated"`
}

// GroupSummaries 批量读取分组的价格阶段、配置 revision 与成本核算规则摘要。ids 为空表示全部可见（未删除）分组，
// 最多 MaxGroupPricingSummaries 个。只读，不加锁。
func (s *PricingDerivationService) GroupSummaries(ctx context.Context, ids []int64) (*GroupPricingSummaryResult, error) {
	ids = dedupeSortedIDs(ids)
	if len(ids) > MaxGroupPricingSummaries {
		return nil, infraerrors.BadRequest(ReasonGroupSummaryTooMany, "too many group ids").
			WithMetadata(map[string]string{"max": strconv.Itoa(MaxGroupPricingSummaries)})
	}
	items, err := s.repo.LoadGroupSummaries(ctx, ids, MaxGroupPricingSummaries+1)
	if err != nil {
		return nil, err
	}
	out := &GroupPricingSummaryResult{Items: items, MissingIDs: []int64{}}
	if out.Items == nil {
		out.Items = []GroupPricingSummary{}
	}
	if len(ids) == 0 && len(out.Items) > MaxGroupPricingSummaries {
		out.Items = out.Items[:MaxGroupPricingSummaries]
		out.Truncated = true
	}
	if len(ids) > 0 {
		found := make(map[int64]struct{}, len(out.Items))
		for _, it := range out.Items {
			found[it.GroupID] = struct{}{}
		}
		for _, id := range ids {
			if _, ok := found[id]; !ok {
				out.MissingIDs = append(out.MissingIDs, id)
			}
		}
		sort.Slice(out.MissingIDs, func(i, j int) bool { return out.MissingIDs[i] < out.MissingIDs[j] })
	}
	return out, nil
}
