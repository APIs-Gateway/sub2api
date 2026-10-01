package handler

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 回退链的分组层 RPM（设计 3.4 第 8 项，审查 B2 / N2，Q17）。
//
// 入口流程：
//  1. 有链：CheckBillingEligibilityForChain（用户层 RPM 只做一次），无链仍走 CheckBillingEligibility，行为不变；
//  2. 每一跳进入 attempt 之前调用 CheckHopGroupRPM，按返回的 verdict 处理；
//  3. 该跳结束后调用 ReleaseHopGroupRPMIfNotServed，把没有真正服务的那一跳的分组层计数退回一次。

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

// CheckHopGroupRPM 对 hop 分组计数并检查。hopIndex 是这一跳在有效链里的下标（首跳为 0）。
// 返回的 ticket 在 Proceed 时用于该跳结束后的退回，Reject / Skip 时调用方不需要再处理（Skip 已退回）。
// billing 或 user 为空、hop 没有分组对象时视为通过，与无限额一致。
func CheckHopGroupRPM(ctx context.Context, billing *service.BillingCacheService, user *service.User, hop service.ChainHop, hopIndex int) (GroupRPMVerdict, *service.GroupRPMTicket) {
	if billing == nil || user == nil || hop.Group == nil {
		return GroupRPMProceed, nil
	}
	ticket, err := billing.CheckGroupRPMForHop(ctx, user, hop.Group)
	if err == nil {
		return GroupRPMProceed, ticket
	}
	if !errors.Is(err, service.ErrGroupRPMExceeded) {
		// CheckGroupRPMForHop 只会返回 ErrGroupRPMExceeded；防御性按通过处理（RPM 一贯 fail-open）。
		return GroupRPMProceed, ticket
	}
	if hopIndex == 0 {
		return GroupRPMReject, ticket
	}
	ticket.Release(ctx)
	return GroupRPMSkip, nil
}

// ReleaseHopGroupRPMIfNotServed 在一跳结束后调用：以「回退」结束（FallbackWorthy）或被跳过（Skipped）时，
// 把它的分组层计数退回一次，避免繁忙的主分组被从未成功服务的尝试耗掉额度；
// 成功（Done）和不可回退（Terminal）保持计数不变，与无链时一致。
func ReleaseHopGroupRPMIfNotServed(ctx context.Context, ticket *service.GroupRPMTicket, outcome service.HopOutcome) {
	switch outcome {
	case service.HopOutcomeFallbackWorthy, service.HopOutcomeSkipped:
		ticket.Release(ctx)
	}
}
