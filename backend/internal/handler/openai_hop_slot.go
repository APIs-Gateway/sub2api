package handler

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// openAIHopSlotPolicy 是 Key 级回退链「繁忙短等 + 组内重选一次 + 兜底排队限额」在单跳内的状态。
//
// 三种形态：
//   - nil：无链请求，或链上第 0 跳就是最后一跳（理论上不会出现）。acquireResponsesAccountSlotForHop 的所有分支
//     与引入回退链之前完全一致（原等待时长、原错误响应、不重选、不缩减排队上限）；
//   - 完整策略（有链且非末跳）：繁忙短等、单账号等待后组内重选一次、排队已满 / 超时不写响应而交给入口回退；
//     以兜底身份（链上下标 > 0）进入时排队上限还要按份额缩减；
//   - LimitOnly（有链的末跳）：规则 4 要求末跳保持原等待与原错误，规则 7 要求兜底身份的排队有份额、等待有封顶。
//     两者在「末跳是兜底分组」时冲突，取舍是：末跳不做繁忙短等、不重选、不延迟写错误（原错误照常输出），
//     但排队上限取份额，等待时长按原值再被剩余总预算封顶——总预算是对客户端的硬承诺
//     （非流式 25 秒，留在 Cloudflare 100 秒之内，设计 3.7），末跳原等待 30 / 120 秒会把它击穿。
//
// 一个 hop 内的多次选号 / 等槽共用同一个指针：reselectUsed 保证每跳最多重选一次。
type openAIHopSlotPolicy struct {
	// BusyWait 整组繁忙（GroupSaturated）、非粘性单账号、以及重选之后的等待上限（busy_wait_ms）。
	BusyWait time.Duration
	// StickyWait 粘性单账号首次等待的上限（sticky_wait_ms）。
	StickyWait time.Duration
	// Deadline 为本次回退总时间预算的截止时刻。等待时长不得超过它（设计 3.5 / 复核）。
	// newOpenAIHopSlotPolicy 总会设置它（剩余预算 <= 0 时等于创建时刻，即「已用完」）；
	// 只有测试里手工构造的策略才会是零值，零值表示不限制。
	Deadline time.Time
	// LimitOnly 为 true 表示末跳：只做排队份额与等待封顶，不做繁忙短等、重选与延迟写错误。
	LimitOnly bool
	// QueueShare 在 (0,1) 之间时，这一跳以兜底身份排队：账号等待队列的 MaxWaiting 只取这个份额
	// （group_fallback_sticky_queue_share，默认 0.3），避免回退流量挤占以该分组为主分组的用户（设计 3.5 第 7 条）。
	// 0 或 >= 1 表示不缩减。
	QueueShare float64

	reselectUsed bool
	lastKind     service.HopFailureKind
	lastErr      error
}

// newOpenAIHopSlotPolicy 为回退链的一跳创建策略。HasChain=false 时返回 nil；末跳（IsLast 且下标 > 0）返回 LimitOnly 策略。
func newOpenAIHopSlotPolicy(info service.HopInfo, settings service.GroupFallbackSettings, now time.Time) *openAIHopSlotPolicy {
	if !info.HasChain {
		return nil
	}
	// 剩余预算 <= 0 表示总预算已用完：Deadline 取 now，waitFor 会算出 0 时长，不再排队等槽；
	// 不能当成「不限时」（S1）。runner 在 i>0 时已先检查过预算，这里只是兜住竞态和异常输入。
	remaining := info.TimeRemaining
	if remaining < 0 {
		remaining = 0
	}
	p := &openAIHopSlotPolicy{Deadline: now.Add(remaining)}
	if info.Index > 0 {
		// 下标 > 0 即兜底身份（主分组或 admin head 那一跳是首跳，不受份额限制）。
		p.QueueShare = settings.StickyQueueShare
	}
	if info.IsLast {
		if info.Index == 0 {
			return nil
		}
		p.LimitOnly = true
		return p
	}
	p.BusyWait = time.Duration(settings.BusyWaitMS) * time.Millisecond
	p.StickyWait = time.Duration(settings.StickyWaitMS) * time.Millisecond
	return p
}

// queueLimit 返回这一跳排队时使用的 MaxWaiting：兜底身份取份额（至少 1），否则原值不变。p 为 nil 时原样返回。
func (p *openAIHopSlotPolicy) queueLimit(maxWaiting int) int {
	if p == nil || maxWaiting <= 0 || p.QueueShare <= 0 || p.QueueShare >= 1 {
		return maxWaiting
	}
	limit := int(math.Floor(float64(maxWaiting) * p.QueueShare))
	if limit < 1 {
		limit = 1
	}
	return limit
}

// selectionContext 给选号用的 ctx 打上「补试整组饱和」标记；p 为 nil（无链 / 末跳）时原样返回 ctx。
func (p *openAIHopSlotPolicy) selectionContext(ctx context.Context) context.Context {
	if p == nil || p.LimitOnly {
		// 末跳不补试：末跳保持原选号行为（规则 2）。
		return ctx
	}
	return service.WithOpenAIGroupSaturationProbe(ctx)
}

// ReselectUsed 报告本跳是否已经用掉了唯一一次组内重选。
func (p *openAIHopSlotPolicy) ReselectUsed() bool {
	return p != nil && p.reselectUsed
}

// waitFor 计算本次等槽的时长。
//   - 命中 previous_response_id 层：保持原时长（规则 3）；
//   - 末跳（LimitOnly）：原时长，只被剩余总预算封顶；
//   - 其余：整组已满 / 已重选过 / 非粘性取 BusyWait，首次粘性单账号取 StickyWait，
//     且不超过 WaitPlan.Timeout 与剩余总预算。
func (p *openAIHopSlotPolicy) waitFor(plan *service.AccountWaitPlan, decision service.OpenAIAccountScheduleDecision) time.Duration {
	if plan == nil {
		return 0
	}
	if p == nil || decision.HitPreviousResponse() {
		return plan.Timeout
	}
	if p.LimitOnly {
		return p.capByDeadline(plan.Timeout)
	}
	limit := p.BusyWait
	if !plan.GroupSaturated && !p.reselectUsed && decision.StickySessionHit {
		limit = p.StickyWait
	}
	wait := plan.Timeout
	if limit < wait {
		wait = limit
	}
	return p.capByDeadline(wait)
}

// capByDeadline 把等待时长限制在剩余总预算之内；Deadline 为零值表示不限制。
func (p *openAIHopSlotPolicy) capByDeadline(wait time.Duration) time.Duration {
	if p == nil || p.Deadline.IsZero() {
		return wait
	}
	remaining := time.Until(p.Deadline)
	if remaining < 0 {
		remaining = 0
	}
	if remaining < wait {
		return remaining
	}
	return wait
}

// onCapacityFailure 在排队已满 / 等待超时时决定下一步。返回 deferred=false 表示本策略不接管，
// 调用方照原样写响应（命中 previous_response_id 层）。
//   - 整组已满，或本跳已经重选过一次：返回 accountSlotBusyDeferred（按「繁忙」回退）；
//   - 其余（单账号等待）：占用唯一一次重选机会，返回 accountSlotRetrySelection。
//
// 两种返回都不写响应，也都不修改 sessionHash、不占用 maxAccountSwitches。
func (p *openAIHopSlotPolicy) onCapacityFailure(
	plan *service.AccountWaitPlan,
	decision service.OpenAIAccountScheduleDecision,
	kind service.HopFailureKind,
	err error,
) (accountSlotAcquireStatus, bool) {
	if p == nil || p.LimitOnly || decision.HitPreviousResponse() {
		return accountSlotAcquireFailed, false
	}
	p.lastKind = kind
	p.lastErr = err
	if (plan != nil && plan.GroupSaturated) || p.reselectUsed {
		return accountSlotBusyDeferred, true
	}
	p.reselectUsed = true
	return accountSlotRetrySelection, true
}

// writeDeferredAccountSlotFailure 把最近一次被延迟的等槽失败按原样写给客户端。
// 回退链走完仍无人服务、需要输出「原始错误」时由入口（runner 的 WriteFinalError）调用，
// 响应内容与无链请求在同一情形下写出的完全相同。
func (h *OpenAIGatewayHandler) writeDeferredAccountSlotFailure(c *gin.Context, p *openAIHopSlotPolicy, streamStarted bool) {
	if p == nil {
		return
	}
	if p.lastKind == service.HopFailureQueueFull {
		h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later", streamStarted)
		return
	}
	h.handleConcurrencyError(c, p.lastErr, "account", streamStarted)
}
