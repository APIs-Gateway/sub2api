package service

import (
	"context"
	"errors"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// W6 PR7a：billing_unpriced_policy 开关与网关调度阶段的无价检查入口（设计 5.2「运行时」、Q3）。
//
// 开关是全局 settings 键，只有两个值：
//   - observe（默认；键不存在、为空、不认识的值都按它）：白名单 v2 分组里无价的请求照旧放行，只记日志与计数；
//   - block_allowlist：白名单 v2 分组里无价的请求在调度阶段被拦，与受限模型走同一条路径（同一个通用错误文案，
//     不含上游厂商、上游模型名或内部路由，原因只写管理员侧日志）。开放分组、legacy、shadow 分组始终不拦。
//
// Q3：PR7 上线后观测满 2 周、白名单分组排除已知免费名单后 unpriced_billing_rows 为 0，才把它设成 block_allowlist。
// 写入口是管理端 PUT /admin/settings 的 billing_unpriced_policy 字段（只认这两个值，非法值 400；
// 要求交互式管理员会话，admin API key 不能写，因为它会改变用户请求是否被拒）。

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

// BillingKnownFreeList 读取已知免费名单。与 loadBillingKnownFreeList 的区别：读取失败或内容写坏时返回错误，
// 由调用方（stagedPolicy 的进程内缓存）决定保留上一次的名单。键不存在是空名单，不是错误。
func (s *SettingService) BillingKnownFreeList(ctx context.Context) ([]BillingKnownFreeEntry, error) {
	if s == nil || s.settingRepo == nil {
		return nil, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyBillingKnownFreeList)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return parseBillingKnownFreeList(raw)
}

// NormalizeBillingUnpricedPolicy 校验并规范化 billing_unpriced_policy 的写入值（去首尾空白）；只认两个取值，其余（含空串）不合法。
func NormalizeBillingUnpricedPolicy(value string) (string, bool) {
	switch v := strings.TrimSpace(value); v {
	case BillingUnpricedPolicyObserve, BillingUnpricedPolicyBlockAllowlist:
		return v, true
	}
	return "", false
}

// SetBillingUnpricedPolicy 写入 billing_unpriced_policy。非法值返回 400。权限（交互式管理员会话）由调用方的 handler 负责。
// 各实例的进程内缓存最多 15 秒后生效。
func (s *SettingService) SetBillingUnpricedPolicy(ctx context.Context, value string) error {
	v, ok := NormalizeBillingUnpricedPolicy(value)
	if !ok {
		return infraerrors.BadRequest("INVALID_BILLING_UNPRICED_POLICY", "billing_unpriced_policy must be observe or block_allowlist")
	}
	if s == nil || s.settingRepo == nil {
		return infraerrors.InternalServer("SETTING_REPO_UNAVAILABLE", "setting repository is not configured")
	}
	// 这个键是受保护键，通用的 settingRepo.Set 会 403（W6GenericWriteGuard）；只走仓储的专用写入。没有专用写入就失败关闭。
	writer, ok := s.settingRepo.(billingUnpricedPolicyWriter)
	if !ok {
		return infraerrors.InternalServer("SETTING_REPO_UNAVAILABLE", "setting repository has no billing_unpriced_policy writer")
	}
	return writer.SetBillingUnpricedPolicy(ctx, v)
}

// billingUnpricedPolicyWriter 是仓储的专用写入口，绕过通用写入守卫，只写 billing_unpriced_policy。
type billingUnpricedPolicyWriter interface {
	SetBillingUnpricedPolicy(ctx context.Context, value string) error
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
	in := RuntimePriceInputs{ReadPolicy: settings.BillingUnpricedPolicy, ReadKnownFree: settings.BillingKnownFreeList}
	if billing != nil {
		in.OfficialState = billing.LookupOfficialPriceState
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
