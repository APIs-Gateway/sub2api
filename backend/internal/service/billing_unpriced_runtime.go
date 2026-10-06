package service

import (
	"context"
	"errors"
	"strings"
)

// W6 PR7a：billing_unpriced_policy 开关与网关调度阶段的无价检查入口（设计 5.2「运行时」、Q3）。
//
// 开关是全局 settings 键，只有两个值：
//   - observe（默认；键不存在、为空、不认识的值都按它）：白名单 v2 分组里无价的请求照旧放行，只记日志与计数；
//   - block_allowlist：白名单 v2 分组里无价的请求在调度阶段被拦，与受限模型走同一条路径（同一个通用错误文案，
//     不含上游厂商、上游模型名或内部路由，原因只写管理员侧日志）。开放分组、legacy、shadow 分组始终不拦。
//
// Q3：PR7 上线后观测满 2 周、白名单分组排除已知免费名单后 unpriced_billing_rows 为 0，才把它设成 block_allowlist；
// 通用 PUT /settings 之外没有专门的写入口，W5 落地后登记为 C 档、AI 不可写。

const (
	// SettingKeyBillingUnpricedPolicy 是运行时无价拦截开关的存储键。
	SettingKeyBillingUnpricedPolicy = "billing_unpriced_policy"

	BillingUnpricedPolicyObserve        = "observe"
	BillingUnpricedPolicyBlockAllowlist = "block_allowlist"
)

// BillingUnpricedPolicy 读取 billing_unpriced_policy；读取失败按 observe（失败放行，拦截只在显式开启时发生）。
// 调用方自己缓存（stagedPolicy 缓存 15 秒），这里每次都读库。
func (s *SettingService) BillingUnpricedPolicy(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return BillingUnpricedPolicyObserve
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyBillingUnpricedPolicy)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return BillingUnpricedPolicyObserve
	}
	if strings.TrimSpace(value) == BillingUnpricedPolicyBlockAllowlist {
		return BillingUnpricedPolicyBlockAllowlist
	}
	return BillingUnpricedPolicyObserve
}

// runtimeAccessPolicy 是会做运行时无价检查的策略（stagedPolicy）。legacyPolicy 与测试替身没有它，调用方直接放行。
type runtimeAccessPolicy interface {
	RuntimeAccess(ctx context.Context, groupID int64, candidates []string, in RuntimePriceInputs) QuoteAccess
}

// runtimeUnpricedBlocked 在调度阶段判断白名单 v2 分组里的这次请求是不是因为无价要被拦。
// candidates 按「请求模型、计费模型、映射后的模型」去空白去重（空串丢弃）。
// 返回 true 只发生在 billing_unpriced_policy = block_allowlist 时；observe 时无价只记日志与计数。
func runtimeUnpricedBlocked(ctx context.Context, policy GroupPolicy, billing *BillingService, settings *SettingService, groupID int64, requested, billingModel, mapped string) bool {
	rp, ok := policy.(runtimeAccessPolicy)
	if !ok {
		return false
	}
	candidates := runtimeCandidates(requested, billingModel, mapped)
	if len(candidates) == 0 {
		return false
	}
	in := RuntimePriceInputs{ReadPolicy: settings.BillingUnpricedPolicy}
	if billing != nil {
		in.OfficialPriced = func(model string) bool {
			_, err := billing.GetModelPricing(model)
			return err == nil
		}
		in.PricingSnapshotID = billing.pricingService.ActiveSnapshotID()
	}
	access := rp.RuntimeAccess(ctx, groupID, candidates, in)
	return !access.OK
}

// runtimeCandidates 构造无价检查的候选链：顺序是请求模型、计费模型、映射后的模型，去首尾空白、去重、丢空串。
func runtimeCandidates(models ...string) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		dup := false
		for _, existing := range out {
			if existing == m {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, m)
		}
	}
	return out
}
