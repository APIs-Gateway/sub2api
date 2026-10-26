package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// W6 PR1：「无价计费」指标、告警与只读管理 API 的服务层。
//
// 指标口径（OpsUnpricedBillingRepository 的实现必须一致）：窗口内满足下列全部条件的 usage_logs 行——
//
//   - 有用量：token（含缓存、图片 token）或图片张数不为 0；
//   - 分组倍率大于 0（倍率为 0 的分组是有意免费，自然排除）；
//   - total_cost 与 actual_cost 都为 0；
//   - upstream_model_mismatch 为假。
//
// 最后一条是核对生产数据与代码后定的：近 30 天满足前三条的 15,665 行里，100% 是上游模型不一致拦截留下的
// 审计行（不计费是有意的，见 RecordUpstreamModelMismatchUsageLog），其中包括全部订阅计费行和全部
// 「只有输入、输出为 0」的行。订阅计费本身并不特殊：订阅行同样记录 total_cost 与 actual_cost
// （近 30 天 1,028,722 行里 1,014,693 行为正），所以指标既不排除 billing_type=1，也不把它做成维度。
//
// 局限：观察模式下放行并正常计费的不一致行同样带 upstream_model_mismatch=true，若那时缺价，行级指标看不到，
// 由网关的 reason 日志与进程内计数覆盖。另外只抓「零成本」，抓不到价格被错误匹配成别的模型的价。

// OpsUnpricedBillingFilter 是按（分组、模型）聚合无价用量行的查询条件。
type OpsUnpricedBillingFilter struct {
	StartTime time.Time
	EndTime   time.Time
	Platform  string
	GroupID   *int64
}

// OpsUnpricedBillingRow 是一个（分组、模型）组合在窗口内的无价用量汇总。
type OpsUnpricedBillingRow struct {
	// GroupID 为 nil 表示该用量没有分组。
	GroupID      *int64    `json:"group_id"`
	GroupName    string    `json:"group_name"`
	Platform     string    `json:"platform"`
	Model        string    `json:"model"`
	Rows         int64     `json:"rows"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CacheTokens  int64     `json:"cache_tokens"`
	ImageCount   int64     `json:"image_count"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	// KnownFree 为真表示该（分组、模型）在 billing_known_free_list 里，不计入告警指标。
	KnownFree bool `json:"known_free"`
}

func (r *OpsUnpricedBillingRow) groupIDValue() int64 {
	if r == nil || r.GroupID == nil {
		return 0
	}
	return *r.GroupID
}

// OpsUnpricedBillingRepository 由 repository.opsRepository 实现。像 OpsIngressRejectRepository 一样
// 作为可选接口断言使用，不并入 OpsRepository，避免所有 OpsRepository 的假实现都要补方法。
type OpsUnpricedBillingRepository interface {
	// ListUnpricedBillingUsage 返回窗口内按（分组、模型）聚合的无价用量行，按行数降序。
	// 必须以 created_at 范围条件打头，实现不得对 usage_logs 做全表扫描。
	ListUnpricedBillingUsage(ctx context.Context, filter *OpsUnpricedBillingFilter) ([]*OpsUnpricedBillingRow, error)
}

var errOpsUnpricedBillingUnsupported = errors.New("ops repository does not support unpriced billing queries")

const (
	// 管理 API 支持的窗口。
	OpsUnpricedBillingWindow24h = "24h"
	OpsUnpricedBillingWindow7d  = "7d"

	opsUnpricedBillingDefaultLimit = 200
	opsUnpricedBillingMaxLimit     = 500
	// 告警详情里列出的（分组、模型）个数。
	opsUnpricedBillingAlertTopN = 5
)

// OpsUnpricedBillingReport 是「未定价用量」管理 API 的返回。
type OpsUnpricedBillingReport struct {
	Window    string    `json:"window"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
	Platform  string    `json:"platform,omitempty"`
	GroupID   *int64    `json:"group_id,omitempty"`

	// TotalRows 是窗口内全部命中行数；KnownFreeRows 是其中在已知免费名单里的；
	// UnpricedRows 是两者之差，也就是告警指标 unpriced_billing_rows 在这个窗口上的值。
	TotalRows     int64 `json:"total_rows"`
	KnownFreeRows int64 `json:"known_free_rows"`
	UnpricedRows  int64 `json:"unpriced_rows"`

	// Items 先列不在已知免费名单里的，再列在名单里的；各自按行数降序。超过 limit 时 Truncated 为真。
	Items     []*OpsUnpricedBillingRow `json:"items"`
	Truncated bool                     `json:"truncated"`

	KnownFreeList []BillingKnownFreeEntry `json:"known_free_list"`
	// ProcessCounters 是当前这个进程里网关无价分支的计数（重启清零，多实例各算各的），
	// 用来和上面基于 usage_logs 的结果互相印证。
	ProcessCounters []UnpricedBillingCounter `json:"process_counters"`
}

// loadBillingKnownFreeList 读取已知免费名单。读取失败或内容写坏（JSON 不合法、字段名写错等，
// 解析规则见 parseBillingKnownFreeList）时整份作废，返回空名单并记一条警告：
// 名单只会让告警变少，空名单是安全的一侧。
func loadBillingKnownFreeList(ctx context.Context, settingRepo SettingRepository) []BillingKnownFreeEntry {
	if settingRepo == nil {
		return nil
	}
	raw, err := settingRepo.GetValue(ctx, SettingKeyBillingKnownFreeList)
	if err != nil {
		if !errors.Is(err, ErrSettingNotFound) {
			logger.FromContext(ctx).Warn("billing.known_free_list_read_failed", zap.Error(err))
		}
		return nil
	}
	list, err := parseBillingKnownFreeList(raw)
	if err != nil {
		logger.FromContext(ctx).Warn("billing.known_free_list_invalid", zap.Error(err))
		return nil
	}
	return list
}

// collectUnpricedBilling 查询无价用量行并按已知免费名单打标。返回的行已按
// （不在名单内优先、行数降序、分组、模型）排序。
func collectUnpricedBilling(
	ctx context.Context,
	repo OpsRepository,
	settingRepo SettingRepository,
	filter *OpsUnpricedBillingFilter,
) ([]*OpsUnpricedBillingRow, []BillingKnownFreeEntry, error) {
	reader, ok := repo.(OpsUnpricedBillingRepository)
	if !ok || reader == nil {
		return nil, nil, errOpsUnpricedBillingUnsupported
	}
	rows, err := reader.ListUnpricedBillingUsage(ctx, filter)
	if err != nil {
		return nil, nil, err
	}
	knownFree := loadBillingKnownFreeList(ctx, settingRepo)
	for _, row := range rows {
		if row == nil {
			continue
		}
		row.KnownFree = billingKnownFreeMatches(knownFree, row.groupIDValue(), row.Model)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a == nil || b == nil {
			return a != nil
		}
		if a.KnownFree != b.KnownFree {
			return !a.KnownFree
		}
		if a.Rows != b.Rows {
			return a.Rows > b.Rows
		}
		if a.groupIDValue() != b.groupIDValue() {
			return a.groupIDValue() < b.groupIDValue()
		}
		return a.Model < b.Model
	})
	return rows, knownFree, nil
}

// GetUnpricedBillingReport 汇总近 24 小时或 7 天按（分组、模型）划分的无价用量。只读。
func (s *OpsService) GetUnpricedBillingReport(
	ctx context.Context,
	window string,
	platform string,
	groupID *int64,
	limit int,
) (*OpsUnpricedBillingReport, error) {
	if s == nil {
		return nil, ErrOpsDisabled
	}
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}

	var span time.Duration
	switch window {
	case "", OpsUnpricedBillingWindow24h:
		window = OpsUnpricedBillingWindow24h
		span = 24 * time.Hour
	case OpsUnpricedBillingWindow7d:
		span = 7 * 24 * time.Hour
	default:
		return nil, fmt.Errorf("unsupported window %q", window)
	}
	if limit <= 0 {
		limit = opsUnpricedBillingDefaultLimit
	}
	if limit > opsUnpricedBillingMaxLimit {
		limit = opsUnpricedBillingMaxLimit
	}

	end := time.Now().UTC()
	filter := &OpsUnpricedBillingFilter{
		StartTime: end.Add(-span),
		EndTime:   end,
		Platform:  strings.TrimSpace(strings.ToLower(platform)),
		GroupID:   groupID,
	}
	report := &OpsUnpricedBillingReport{
		Window:          window,
		StartTime:       filter.StartTime,
		EndTime:         filter.EndTime,
		Platform:        filter.Platform,
		GroupID:         groupID,
		Items:           []*OpsUnpricedBillingRow{},
		KnownFreeList:   []BillingKnownFreeEntry{},
		ProcessCounters: UnpricedBillingCounterSnapshot(),
	}

	rows, knownFree, err := collectUnpricedBilling(ctx, s.opsRepo, s.settingRepo, filter)
	if err != nil {
		if errors.Is(err, errOpsUnpricedBillingUnsupported) {
			return report, nil
		}
		return nil, err
	}
	if knownFree != nil {
		report.KnownFreeList = knownFree
	}
	for _, row := range rows {
		if row == nil {
			continue
		}
		report.TotalRows += row.Rows
		if row.KnownFree {
			report.KnownFreeRows += row.Rows
		}
	}
	report.UnpricedRows = report.TotalRows - report.KnownFreeRows
	if len(rows) > limit {
		rows = rows[:limit]
		report.Truncated = true
	}
	for _, row := range rows {
		if row != nil {
			report.Items = append(report.Items, row)
		}
	}
	return report, nil
}

// computeUnpricedBillingRows 是告警指标 unpriced_billing_rows 的取值：窗口内不在已知免费名单里的无价用量行数。
// 告警规则的 platform、group_id 作用域沿用，没有按模型过滤的作用域（用已知免费名单排除）。
func (s *OpsAlertEvaluatorService) computeUnpricedBillingRows(
	ctx context.Context,
	start time.Time,
	end time.Time,
	platform string,
	groupID *int64,
) (float64, bool) {
	if s == nil || s.opsRepo == nil {
		return 0, false
	}
	rows, _, err := collectUnpricedBilling(ctx, s.opsRepo, s.settingRepoForUnpriced(), &OpsUnpricedBillingFilter{
		StartTime: start,
		EndTime:   end,
		Platform:  strings.TrimSpace(strings.ToLower(platform)),
		GroupID:   groupID,
	})
	if err != nil {
		logger.LegacyPrintf("service.ops_alert_evaluator", "[OpsAlertEvaluator] unpriced_billing_rows query failed: %v", err)
		return 0, false
	}
	var total int64
	for _, row := range rows {
		if row != nil && !row.KnownFree {
			total += row.Rows
		}
	}
	return float64(total), true
}

func (s *OpsAlertEvaluatorService) settingRepoForUnpriced() SettingRepository {
	if s == nil || s.opsService == nil {
		return nil
	}
	return s.opsService.settingRepo
}

// describeUnpricedBillingAlert 给 unpriced_billing_rows 的告警描述补上行数最多的几个（分组、模型），
// 让告警邮件和事件列表里直接看到是谁。查询失败时返回空串，不影响告警本身。
func (s *OpsAlertEvaluatorService) describeUnpricedBillingAlert(
	ctx context.Context,
	start time.Time,
	end time.Time,
	platform string,
	groupID *int64,
) string {
	if s == nil || s.opsRepo == nil {
		return ""
	}
	rows, _, err := collectUnpricedBilling(ctx, s.opsRepo, s.settingRepoForUnpriced(), &OpsUnpricedBillingFilter{
		StartTime: start,
		EndTime:   end,
		Platform:  strings.TrimSpace(strings.ToLower(platform)),
		GroupID:   groupID,
	})
	if err != nil {
		return ""
	}
	parts := make([]string, 0, opsUnpricedBillingAlertTopN)
	for _, row := range rows {
		if row == nil || row.KnownFree {
			continue
		}
		parts = append(parts, fmt.Sprintf("group %d / %s x%d", row.groupIDValue(), row.Model, row.Rows))
		if len(parts) >= opsUnpricedBillingAlertTopN {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "; top: " + strings.Join(parts, ", ")
}
