package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// W6 PR1：无价计费的可观测性（只加日志、进程内计数与告警指标，不改任何计费结果）。
//
// 背景：两个网关在「算不出价格」的分支上都是静默的。OpenAI 网关把成本置零并只打一条警告，
// Anthropic 网关（含 Gemini、Antigravity 共用的 recordUsageCore）算价出错时只打日志并返回
// ActualCost: 0。这里给这些分支补一行带 reason 的结构化日志和进程内计数器，让它们能被看见。

const (
	// OpsAlertMetricUnpricedBillingRows 是告警规则的指标类型：窗口内「有用量、倍率大于 0，
	// 但 total_cost 与 actual_cost 都是 0」的 usage_logs 行数。口径见 ops_unpriced_billing.go。
	OpsAlertMetricUnpricedBillingRows = "unpriced_billing_rows"

	// SettingKeyBillingKnownFreeList 是「已知免费」名单的 settings 键，值为 JSON 数组，
	// 每项是 BillingKnownFreeEntry。名单内的（分组、模型）不计入 unpriced_billing_rows。
	// W5 落地后登记为 C 档、AI 不可写；在那之前只能由管理员直接改 settings。
	SettingKeyBillingKnownFreeList = "billing_known_free_list"
)

// 无价计费的原因标签。写入日志的 reason 字段和进程内计数器的维度。
const (
	// UnpricedBillingReasonMissingPrice：所有候选模型都查不到价格（ErrModelPricingUnavailable 一类）。
	UnpricedBillingReasonMissingPrice = "missing_price"
	// UnpricedBillingReasonCalcError：算价返回了价格缺失以外的错误。
	UnpricedBillingReasonCalcError = "calc_error"
	// UnpricedBillingReasonNoBillingService：网关没有注入 BillingService，直接不计价。
	UnpricedBillingReasonNoBillingService = "no_billing_service"
	// UnpricedBillingReasonImageCalcError：图片计费算价出错。
	UnpricedBillingReasonImageCalcError = "image_calc_error"
)

const (
	// 计数器维度的基数上限。模型名来自请求，虽然走到这里的请求都已被上游接受，仍要防止异常名字撑爆内存；
	// 超出后新的维度并入 unpricedBillingOverflowModel。
	unpricedBillingCounterMaxKeys = 2048
	unpricedBillingOverflowModel  = "_other"
	// 写入计数器的模型名最长字节数；日志字段不截断。
	unpricedBillingModelMaxBytes = 200
)

type unpricedBillingCounterKey struct {
	Platform string
	GroupID  int64
	Model    string
	Reason   string
}

var (
	unpricedBillingCounters   sync.Map // unpricedBillingCounterKey -> *atomic.Int64
	unpricedBillingCounterLen atomic.Int64
)

// UnpricedBillingCounter 是进程内计数器的一项快照。进程重启后清零，多实例各算各的。
type UnpricedBillingCounter struct {
	Platform string `json:"platform"`
	GroupID  int64  `json:"group_id"`
	Model    string `json:"model"`
	Reason   string `json:"reason"`
	Count    int64  `json:"count"`
}

// noteUnpricedBilling 记录一次无价/算价出错：打一行结构化警告，并给（平台、分组、模型、原因）计数加一。
//
//   - 只观测，不返回任何东西，调用点的计费行为保持不变。
//   - model 取用量行的 Model（与 usage_logs.model 对得上），原样记录，不 trim、不改大小写，
//     这样带前导空格之类的脏名字能在维度里直接看见；实际参与算价的候选模型放在 billingModels，只进日志。
//   - err 可为 nil（例如 no_billing_service 没有错误对象）。
//   - outcome 说明这次调用点之后发生了什么：zero_cost（成本置零继续记账）、error_returned（把错误返回给调用方）、
//     group_image_price_fallback（改用分组图片价兜底）。
func noteUnpricedBilling(ctx context.Context, apiKey *APIKey, model, reason string, err error, outcome string, billingModels ...string) {
	platform, groupID := unpricedBillingDims(apiKey)
	incrementUnpricedBillingCounter(platform, groupID, model, reason)

	fields := make([]zap.Field, 0, 8)
	fields = append(fields,
		zap.String("component", "service.billing"),
		zap.String("reason", reason),
		zap.String("platform", platform),
		zap.Int64("group_id", groupID),
		zap.String("model", model),
		zap.String("outcome", outcome),
	)
	if len(billingModels) > 0 {
		fields = append(fields, zap.Strings("billing_models", billingModels))
	}
	if err != nil {
		fields = append(fields, zap.Error(err))
	}
	logger.FromContext(ctx).Warn("billing.unpriced_usage", fields...)
}

// unpricedBillingDims 取 API Key 所属分组的平台与 ID；缺失时为空串和 0。
func unpricedBillingDims(apiKey *APIKey) (platform string, groupID int64) {
	if apiKey == nil {
		return "", 0
	}
	if apiKey.GroupID != nil {
		groupID = *apiKey.GroupID
	}
	if apiKey.Group != nil {
		platform = apiKey.Group.Platform
		if groupID == 0 {
			groupID = apiKey.Group.ID
		}
	}
	return platform, groupID
}

func incrementUnpricedBillingCounter(platform string, groupID int64, model, reason string) {
	if len(model) > unpricedBillingModelMaxBytes {
		model = model[:unpricedBillingModelMaxBytes]
	}
	key := unpricedBillingCounterKey{Platform: platform, GroupID: groupID, Model: model, Reason: reason}
	if addToExistingUnpricedBillingCounter(key) {
		return
	}
	if unpricedBillingCounterLen.Load() >= unpricedBillingCounterMaxKeys {
		key.Model = unpricedBillingOverflowModel
		if addToExistingUnpricedBillingCounter(key) {
			return
		}
	}
	actual, loaded := unpricedBillingCounters.LoadOrStore(key, &atomic.Int64{})
	if !loaded {
		unpricedBillingCounterLen.Add(1)
	}
	if counter, ok := actual.(*atomic.Int64); ok {
		counter.Add(1)
	}
}

// addToExistingUnpricedBillingCounter 给已存在的维度加一，返回是否命中。
func addToExistingUnpricedBillingCounter(key unpricedBillingCounterKey) bool {
	existing, ok := unpricedBillingCounters.Load(key)
	if !ok {
		return false
	}
	if counter, ok := existing.(*atomic.Int64); ok {
		counter.Add(1)
	}
	return true
}

// UnpricedBillingCounterSnapshot 返回当前进程的计数器快照，按次数降序、再按维度排序。
func UnpricedBillingCounterSnapshot() []UnpricedBillingCounter {
	out := make([]UnpricedBillingCounter, 0, 16)
	unpricedBillingCounters.Range(func(k, v any) bool {
		key, ok := k.(unpricedBillingCounterKey)
		if !ok {
			return true
		}
		counter, ok := v.(*atomic.Int64)
		if !ok {
			return true
		}
		out = append(out, UnpricedBillingCounter{
			Platform: key.Platform,
			GroupID:  key.GroupID,
			Model:    key.Model,
			Reason:   key.Reason,
			Count:    counter.Load(),
		})
		return true
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Platform != out[j].Platform {
			return out[i].Platform < out[j].Platform
		}
		if out[i].GroupID != out[j].GroupID {
			return out[i].GroupID < out[j].GroupID
		}
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

// BillingKnownFreeEntry 是 billing_known_free_list 里的一项：这个（分组、模型）免费是有意为之，
// 零成本不算「无价」。
//
//	[{"group_id": 16, "model": "some-free-model", "note": "活动期免费"}]
//
// group_id 为 0 或省略表示任意分组。model 必填，按字面比较（忽略大小写，不 trim）：
// 带前导空格的名字不会被不带空格的名单项盖住，仍会出现在指标里。
type BillingKnownFreeEntry struct {
	GroupID int64  `json:"group_id"`
	Model   string `json:"model"`
	Note    string `json:"note,omitempty"`
}

// parseBillingKnownFreeList 解析 settings 里的名单。空串是空名单；JSON 无法解析时返回错误，
// 调用方应按空名单处理（宁可多告警，也不要因为配置写坏而漏掉）。无效项（model 为空、group_id 为负）会被丢弃。
func parseBillingKnownFreeList(raw string) ([]BillingKnownFreeEntry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var entries []BillingKnownFreeEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, err
	}
	valid := entries[:0]
	for _, entry := range entries {
		if entry.Model == "" || entry.GroupID < 0 {
			continue
		}
		valid = append(valid, entry)
	}
	return valid, nil
}

// billingKnownFreeMatches 判断（分组、模型）是否在已知免费名单里。
func billingKnownFreeMatches(list []BillingKnownFreeEntry, groupID int64, model string) bool {
	for _, entry := range list {
		if entry.GroupID != 0 && entry.GroupID != groupID {
			continue
		}
		if strings.EqualFold(entry.Model, model) {
			return true
		}
	}
	return false
}
