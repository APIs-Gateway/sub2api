package handler

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 回退链的分组层 RPM（设计 3.4 第 8 项，审查 B2 / N2，Q17）。
//
// 入口流程：
//  1. 有链：CheckBillingEligibilityForChain(…, 首跳分组, …)，按无链时的顺序先首跳分组层、后用户层（用户层只做一次），
//     返回首跳的 ticket；无链仍走 CheckBillingEligibility，行为不变；
//  2. 首跳（有效链第 0 项）不再调用 CheckHopGroupRPM：它的计数已在入口完成，ticket 直接交给第 0 跳结束后的退回；
//     入口拿到 ErrGroupRPMExceeded 即首跳超限，直接写 429、不回退、不退回计数；
//  3. 第 1 跳起，每一跳进入 attempt 之前调用 CheckHopGroupRPM，按返回的 verdict 处理；
//  4. 该跳结束后调用 ReleaseHopGroupRPMIfNotServed，把没有真正服务的那一跳的分组层计数退回一次。

// GroupRPMVerdict 是分组层 RPM 对一跳的处理结论。
type GroupRPMVerdict int

const (
	// GroupRPMProceed 通过（或该分组没有限额 / Redis 故障 fail-open），可以执行这一跳。
	GroupRPMProceed GroupRPMVerdict = iota
	// GroupRPMReject 首跳（有效链第 0 项）超限：保持现状，入口直接写 429，不回退，计数保持不变。
	// 对应 attempt 返回 HopOutcomeTerminal 且已写出 ErrGroupRPMExceeded 对应的 429。
	GroupRPMReject
	// GroupRPMSkip 非首跳超限：这一跳被跳过（计数已退回）。对应 attempt 返回 HopOutcomeSkipped；
	// 如果这是最后一跳，应提供 WriteFinalError 写出 429，覆盖前面跳暂存的错误。
	GroupRPMSkip
)

// CheckHopGroupRPM 对 hop 分组计数并检查，供第 1 跳起使用（首跳在入口计数，见上）。
// hopIndex 是这一跳在有效链里的下标；传 0 时超限返回 GroupRPMReject（保持计数），仅为兼容，正常流程不会这样调用。
// 返回的 ticket 在 Proceed 时用于该跳结束后的退回，Reject / Skip 时调用方不需要再处理（Skip 已退回）。
// 超限（Reject / Skip）时同时返回 service.ErrGroupRPMExceeded，调用方可直接交给 billingErrorDetails 写 429
// （含 Retry-After），不必自己构造错误。
// billing 或 user 为空、hop 没有分组对象时视为通过，与无限额一致。
func CheckHopGroupRPM(ctx context.Context, billing *service.BillingCacheService, user *service.User, hop service.ChainHop, hopIndex int) (GroupRPMVerdict, *service.GroupRPMTicket, error) {
	if billing == nil || user == nil || hop.Group == nil {
		return GroupRPMProceed, nil, nil
	}
	ticket, err := billing.CheckGroupRPMForHop(ctx, user, hop.Group)
	if err == nil {
		return GroupRPMProceed, ticket, nil
	}
	if !errors.Is(err, service.ErrGroupRPMExceeded) {
		// CheckGroupRPMForHop 只会返回 ErrGroupRPMExceeded；防御性按通过处理（RPM 一贯 fail-open）。
		return GroupRPMProceed, ticket, nil
	}
	if hopIndex == 0 {
		return GroupRPMReject, ticket, err
	}
	ticket.Release(ctx)
	return GroupRPMSkip, nil, err
}

// ReleaseHopGroupRPMIfNotServed 在一跳结束后调用，决定是否把这一跳的分组层计数退回一次：
//   - Skipped（该跳没有执行）：退回；
//   - FallbackWorthy 且不是最后一跳、没有把错误写给客户端、并且本跳没有真正打到上游（Attempts == 0，
//     即没号 / 繁忙 / 等待超时）：退回，避免繁忙的主分组被从未服务的尝试耗掉额度；
//   - 其余一律保持计数，与无链时一致：成功（Done）、不可回退（Terminal）、最后一跳（它以写出最终错误结束，
//     不是「回退」）、已经打过上游的失败（Attempts > 0，上游失败风暴仍受分组 RPM 约束）。
//
// 边界：非末跳回退后，runner 因预算用尽而把这一跳的错误作为最终错误写出，入口在调用本函数时无法知道，
// 这种情况下计数已经退回，属于可接受的偏差。
func ReleaseHopGroupRPMIfNotServed(ctx context.Context, ticket *service.GroupRPMTicket, info service.HopInfo, result service.HopResult) {
	switch result.Outcome {
	case service.HopOutcomeSkipped:
		ticket.Release(ctx)
	case service.HopOutcomeFallbackWorthy:
		if !info.IsLast && !result.ErrorWritten && result.Attempts == 0 {
			ticket.Release(ctx)
		}
	}
}
