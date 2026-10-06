package service

import (
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 PR4b-2b-2：W5 注册表里 W6 要登记的设置键与动作（设计第 8 节「W6 新增的设置键」「阶段切换动作」）。
//
// W5 的注册表与 change-set 还没有落地，这里先把 W6 一侧的定义放进代码，W5 落地后直接引用：
//   - 设置键：存储键用下划线形式，点分写法只作别名；都是 C 档、AI 不可写（W5 审查 B7 的失败关闭）。
//     通用的设置写入（SettingRepository.Set / SetMultiple / Delete）必须拒绝这些键，只有各自专用的写入口可以写：
//     已知免费名单走 KnownFreeListService（保存时校验、交互式会话、二次确认），其余几个键由各自的 PR（PR7、PR9）接入。
//   - 动作：pricing.stage_switch，Category 不是 price，touches_price 为真，档位由 price_delta 经 W5 的 TierEngine 得出，
//     W6 不硬编码档位。

// W6 新增的设置键（存储键）。SettingKeyPricingSnapshotMode 在 pricing_snapshot_types.go，
// SettingKeyBillingKnownFreeList 在 billing_unpriced_observe.go。
const (
	SettingKeyBillingUnpricedPolicy = "billing_unpriced_policy"
	SettingKeyPricingDefaultStage   = "pricing_default_stage"
)

// W5Tier W5 注册表里的档位。
type W5Tier string

// W5TierC C 档：最高一档，需要交互式管理员确认。
const W5TierC W5Tier = "C"

// W6SettingRegistration 一个 W6 设置键在 W5 注册表里的登记项。
type W6SettingRegistration struct {
	Key        string   `json:"key"`
	Aliases    []string `json:"aliases,omitempty"`
	Tier       W5Tier   `json:"tier"`
	AIWritable bool     `json:"ai_writable"`
}

// W6SettingRegistrations 返回 W6 的全部设置键登记项。
func W6SettingRegistrations() []W6SettingRegistration {
	return []W6SettingRegistration{
		{Key: SettingKeyPricingSnapshotMode, Aliases: []string{"pricing.snapshot_mode"}, Tier: W5TierC},
		{Key: SettingKeyBillingUnpricedPolicy, Aliases: []string{"billing.unpriced_policy"}, Tier: W5TierC},
		{Key: SettingKeyPricingDefaultStage, Aliases: []string{"pricing.default_stage"}, Tier: W5TierC},
		{Key: SettingKeyBillingKnownFreeList, Aliases: []string{"billing.known_free_list"}, Tier: W5TierC},
	}
}

// IsW6ProtectedSettingKey key 是不是 W6 的受保护设置键（存储键或点分别名，忽略大小写与首尾空白）。
// 通用的设置写入必须拒绝这些键。
func IsW6ProtectedSettingKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	for _, r := range W6SettingRegistrations() {
		if k == r.Key {
			return true
		}
		for _, a := range r.Aliases {
			if k == a {
				return true
			}
		}
	}
	return false
}

// W5ActionPricingStageSwitch 阶段切换动作的名称（W5 两份文档都写着「名称由 W6 定」）。
const W5ActionPricingStageSwitch = "pricing.stage_switch"

// W5ActionRegistration 一个 W6 动作在 W5 注册表里的登记项。
type W5ActionRegistration struct {
	Name string `json:"name"`
	// Category 动作类别；阶段切换不是 price 类（它不直接改价），但 touches_price 为真。
	Category     string `json:"category"`
	TouchesPrice bool   `json:"touches_price"`
	// TierFromPriceDelta 档位不在 W6 里写死：由 price_delta 经 W5 TierEngine 的 D2 得出。
	TierFromPriceDelta bool `json:"tier_from_price_delta"`
}

// W6ActionRegistrations 返回 W6 的动作登记项。
func W6ActionRegistrations() []W5ActionRegistration {
	return []W5ActionRegistration{
		{Name: W5ActionPricingStageSwitch, Category: "pricing_stage", TouchesPrice: true, TierFromPriceDelta: true},
	}
}

// StageSwitchPriceDelta 阶段切换动作的 price_delta：影子比对与回放里翻译差异为 0 且没有被接受的差异时是 none，
// 否则取 PriceDiff 给出的方向（涨、跌或未知）；给不出方向时按 unknown（最严）。
func StageSwitchPriceDelta(translationDiffs, acceptedDiffs int, diff PriceDelta) PriceDelta {
	if translationDiffs == 0 && acceptedDiffs == 0 {
		return PriceDeltaNone
	}
	if validPriceDelta(diff) != nil || diff == PriceDeltaNone {
		// 有差异却给出 none 或不认识的值：不可信，按 unknown。
		return PriceDeltaUnknown
	}
	return diff
}

// ReasonSettingKeyProtected 通用设置写入拒绝受保护的键。
const ReasonSettingKeyProtected = "SETTING_KEY_PROTECTED"

// IsW6GenericWriteBlocked 通用的设置写入（SettingRepository.Set / SetMultiple / Delete）是否必须拒绝这个键。
// 受保护的键只能走各自专用的写入口；pricing_snapshot_mode 的专用入口是「固定当前价格」（交互式管理员会话，
// PR9），它经通用仓储写这个键，所以这里放行，由那个入口自己守 C 档。
func IsW6GenericWriteBlocked(key string) bool {
	if strings.ToLower(strings.TrimSpace(key)) == SettingKeyPricingSnapshotMode {
		return false
	}
	return IsW6ProtectedSettingKey(key)
}

// W6GenericWriteGuard 检查一组键，返回第一个被拒绝的键对应的错误；都放行返回 nil。
func W6GenericWriteGuard(keys ...string) error {
	for _, k := range keys {
		if IsW6GenericWriteBlocked(k) {
			return infraerrors.Forbidden(ReasonSettingKeyProtected, "this setting can only be changed through its dedicated, confirmed entry point").
				WithMetadata(map[string]string{"key": k})
		}
	}
	return nil
}
