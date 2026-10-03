package service

import (
	"context"
	"sync"
	"time"
)

// 回退链运行时（设计 3.1 / 3.3 / 3.6 / 3.7）。
//
// 本文件是纯库：不读设置、不访问数据库、不依赖 gin，也不被任何生产路径调用（PR2a 第 1 段）。
// 入口 handler 在后续分段接入：入口解析并审核一条链 → 把这条链交给 Run → Run 逐跳调用入口提供的 attempt 闭包。
//
// B1 不变式：Run 只迭代入口传入的 Chain（原样、按序），循环内不解析链、不读缓存；
// 熔断跳过、预算截止、attempt 返回 Skipped 都只会缩小实际服务的集合。

// HopOutcome 是 attempt 对一跳的结果分类。
type HopOutcome int

const (
	// HopOutcomeDone 该跳成功服务，响应已写出。
	HopOutcomeDone HopOutcome = iota + 1
	// HopOutcomeTerminal 不可回退：已按原逻辑写出响应（或必须原样结束），不再尝试后续跳。
	HopOutcomeTerminal
	// HopOutcomeFallbackWorthy 容量类问题，可以换下一跳。attempt 在 DeferFinalError=true 时不得写任何响应。
	HopOutcomeFallbackWorthy
	// HopOutcomeSkipped 该跳没有发生任何上游尝试（资格检查失败、分组层 RPM 超限等），直接看下一跳。
	HopOutcomeSkipped
)

// FallbackReason 触发回退的原因（用于日志与指标）。
type FallbackReason string

const (
	FallbackReasonNoAccount         FallbackReason = "no_account"
	FallbackReasonBusy              FallbackReason = "busy"
	FallbackReasonFailoverExhausted FallbackReason = "failover_exhausted"
)

// BreakerSignal 说明一次可回退的失败对熔断意味着什么（设计 3.6 / B3）。
type BreakerSignal int

const (
	// BreakerSignalNone 不计入熔断（繁忙、模型找不到、能力不支持、401/403、请求自身造成的失败……）。
	BreakerSignalNone BreakerSignal = iota
	// BreakerSignalCount 立即计入（没号但组内有支持该模型的账号、429/529 容量类）。
	BreakerSignalCount
	// BreakerSignalPending 暂记：只有同一请求的下一跳成功才计入（5xx，可能是载荷造成的）。
	BreakerSignalPending
)

// HopResult 是 attempt 闭包的返回值。
type HopResult struct {
	Outcome HopOutcome
	// Reason 仅 FallbackWorthy 有意义。
	Reason FallbackReason
	// Breaker 仅 FallbackWorthy 有意义。
	Breaker BreakerSignal
	// Attempts 本跳实际发往上游的 Forward 次数（等待与组内重选不计），计入每请求总尝试上限。
	Attempts int
	// UpstreamStatus 最后一个上游状态码，仅用于诊断日志。
	UpstreamStatus int
	// ErrorWritten 为 true 表示 attempt 已经把（原始）错误写给了客户端，runner 不会再写。
	// 只有 DeferFinalError=false 的那一跳（链上最后一跳）允许这样做。
	ErrorWritten bool
	// WriteFinalError 把本跳的原始错误按原样写给客户端（流式场景按流式格式收尾）。
	// 闭包必须在被调用时才判断用流式还是 JSON 格式（读取当时的 tracker 状态），不要用创建闭包时的状态。
	// FallbackWorthy 且 ErrorWritten=false 时必须提供：如果之后没有任何后续跳能运行（预算用尽、
	// 后续跳全被跳过、真实内容已输出），runner 会调用它，保证客户端总能收到原始错误。
	// Skipped 也可以提供，用来覆盖前面跳的错误（例如最后一跳因 RPM 超限被跳过，要写 429）。
	WriteFinalError func()
}

// HopInfo 是 runner 传给 attempt 的每跳上下文。
type HopInfo struct {
	Index int
	Hop   ChainHop
	// HasChain 为 true 表示有效链长度 >= 2，此时才启用短等、组内重选与 min(现有值, MaxPerHopSwitches)。
	HasChain bool
	// IsLast 为 true 表示链上最后一跳：原样输出原始错误、保持原等待时长、不受熔断影响。
	IsLast bool
	// DeferFinalError 为 true（非最后一跳）时，原来会直接写 503/429/failover 耗尽错误的出口必须改为
	// 返回 FallbackWorthy 且不写响应。
	DeferFinalError bool
	// HeartbeatOnly 为 true 表示此前只写过心跳/ping、没有真实内容；最终错误必须按流式格式收尾。
	HeartbeatOnly bool
	// MaxSwitches 本跳的换号预算上限（有链时为 MaxPerHopSwitches，无链为 0 表示沿用现有值）。
	MaxSwitches int
	// AttemptsRemaining 距离每请求上游尝试总上限还剩几次。
	AttemptsRemaining int
	// TimeRemaining 回退总时间预算还剩多久；等待时长应取 min(WaitPlan.Timeout, 短等设置, TimeRemaining)。
	TimeRemaining time.Duration
}

// ChainHopAttempt 是入口提供的单跳闭包：选号 → 等槽 → Forward → failover。
type ChainHopAttempt func(ctx context.Context, info HopInfo) HopResult

// ChainRunInput 是 Run 的输入。
type ChainRunInput struct {
	// Chain 是入口已经解析并完成审核的有效链，runner 原样迭代，不会修改或重新解析。
	Chain []ChainHop
	// Model 请求模型名，用于熔断的模型族判定；HopModel 非 nil 时按跳取映射后的实际模型名。
	Model    string
	HopModel func(hop ChainHop) string
	UserID   int64
	Stream   bool
	// Output 请求级输出状态，跨跳共享；可为 nil（视为什么都没写）。
	Output *OutputTracker
}

// ChainRunStatus 是 Run 的最终状态。
type ChainRunStatus int

const (
	// ChainRunServed 某一跳成功服务。
	ChainRunServed ChainRunStatus = iota + 1
	// ChainRunTerminal 不可回退，响应已由 attempt 写出（或 runner 写出了暂存的原始错误）。
	ChainRunTerminal
	// ChainRunExhausted 所有跳都未能服务，最后一个原始错误已写出。
	ChainRunExhausted
	// ChainRunClientGone 客户端已断开，不写任何响应。
	ChainRunClientGone
	// ChainRunUnresolved 没有任何跳运行且没有可写的错误，调用方必须自己写一个响应。
	ChainRunUnresolved
)

// HopTrace 记录每一跳发生了什么，供日志、指标与测试使用。
type HopTrace struct {
	Index   int
	GroupID int64
	Outcome HopOutcome
	Reason  FallbackReason
	// SkippedBy 非空表示该跳没有被尝试：breaker_open / breaker_probe_busy / attempt_skipped。
	SkippedBy string
	Attempts  int
	// BreakerBypassed 为 true 表示这是「兜底重试」：整条链没有任何一跳真正发出过上游请求，
	// runner 忽略熔断，对最后一个被熔断跳过的跳补跑了一次。
	BreakerBypassed bool
}

// ChainRunResult 是 Run 的结果。
type ChainRunResult struct {
	Status ChainRunStatus
	// ServedIndex 成功服务的跳在链中的下标，没有则为 -1。
	ServedIndex int
	Trace       []HopTrace
	// StoppedBy 非空表示回退被限额截断：total_attempts / time_budget。
	StoppedBy string
	// ErrorFlushed 为 true 表示 runner 调用了暂存的 WriteFinalError。
	ErrorFlushed bool
	// BreakerBypassRetried 为 true 表示触发了「兜底重试」（见 HopTrace.BreakerBypassed），便于排查日志。
	BreakerBypassRetried bool
}

// GroupChainRunner 逐跳执行回退链。Settings 通常取自 SettingService.GetGroupFallbackSettings；零值设置会回落到默认值。
type GroupChainRunner struct {
	Settings GroupFallbackSettings
	// Breaker 可为 nil，此时不做熔断。
	Breaker GroupChainBreakerGate
	// Now 可注入时钟，默认 time.Now。
	Now func() time.Time
}

func (r *GroupChainRunner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

type pendingBreakerFailure struct {
	key        BreakerKey
	probeToken string
}

// Run 按序执行 in.Chain。
func (r *GroupChainRunner) Run(ctx context.Context, in ChainRunInput, attempt ChainHopAttempt) ChainRunResult {
	res := ChainRunResult{ServedIndex: -1}
	chain := in.Chain
	if len(chain) > MaxEffectiveChainLen {
		// 只会缩小实际服务的集合，不破坏「被审核的链 ⊇ 实际服务的分组」。
		chain = chain[:MaxEffectiveChainLen]
	}
	if len(chain) == 0 || attempt == nil {
		res.Status = ChainRunUnresolved
		return res
	}

	settings := r.Settings
	def := DefaultGroupFallbackSettings()
	maxAttempts := settings.MaxTotalAttempts
	if maxAttempts <= 0 {
		maxAttempts = def.MaxTotalAttempts
	}
	budgetMS := settings.TotalBudgetNonStreamMS
	if in.Stream {
		budgetMS = settings.TotalBudgetStreamMS
	}
	if budgetMS <= 0 {
		if in.Stream {
			budgetMS = def.TotalBudgetStreamMS
		} else {
			budgetMS = def.TotalBudgetNonStreamMS
		}
	}
	budget := time.Duration(budgetMS) * time.Millisecond
	maxSwitches := settings.MaxPerHopSwitches
	if maxSwitches <= 0 {
		maxSwitches = def.MaxPerHopSwitches
	}
	breakerCfg := BreakerConfigFromSettings(settings)

	hasChain := len(chain) >= 2
	lastIdx := len(chain) - 1
	start := r.now()
	attemptsUsed := 0

	var pending *pendingBreakerFailure
	var flush func()
	// 兜底重试：没有任何一跳真正发出过上游请求时，对最后一个被熔断跳过的跳再跑一次。
	anyAttempted := false
	lastBreakerSkipped := -1

	// resolvePending 在「下一跳实际尝试」有了结果后调用：成功才把暂记的 5xx 正式计入熔断。
	resolvePending := func(confirm bool) {
		if pending == nil {
			return
		}
		p := pending
		pending = nil
		if r.Breaker == nil {
			return
		}
		if confirm {
			r.Breaker.RecordFailure(ctx, p.key, breakerCfg, in.UserID, p.probeToken)
		} else if p.probeToken != "" {
			r.Breaker.ReleaseProbe(ctx, p.key, p.probeToken)
		}
	}
	releaseProbe := func(key BreakerKey, token string) {
		if token != "" && r.Breaker != nil {
			r.Breaker.ReleaseProbe(ctx, key, token)
		}
	}
	flushError := func() bool {
		if flush == nil {
			return false
		}
		f := flush
		flush = nil
		f()
		res.ErrorFlushed = true
		return true
	}

	for i := range chain {
		hop := chain[i]

		if ctx.Err() != nil {
			resolvePending(false)
			res.Status = ChainRunClientGone
			return res
		}
		if i > 0 {
			if r.now().Sub(start) >= budget {
				res.StoppedBy = "time_budget"
				break
			}
			if attemptsUsed >= maxAttempts {
				res.StoppedBy = "total_attempts"
				break
			}
		}

		isLast := i == lastIdx
		trace := HopTrace{Index: i, GroupID: hop.GroupID}

		// 熔断：只作用于非最后一跳；族未命中不进熔断。
		var bkey BreakerKey
		var probeToken string
		if r.Breaker != nil && hasChain {
			if fam := ModelFamily(r.hopModel(in, hop)); fam != "" && hop.Group != nil {
				bkey = BreakerKey{Platform: hop.Group.Platform, GroupID: hop.GroupID, Family: fam}
			}
		}
		if bkey.Valid() && !isLast {
			adm := r.Breaker.Admit(ctx, bkey, breakerCfg)
			if !adm.Allowed {
				trace.Outcome = HopOutcomeSkipped
				trace.SkippedBy = "breaker_open"
				if adm.State == BreakerStateHalfOpen {
					trace.SkippedBy = "breaker_probe_busy"
				}
				res.Trace = append(res.Trace, trace)
				lastBreakerSkipped = i
				continue
			}
			probeToken = adm.ProbeToken
		}

		info := HopInfo{
			Index:             i,
			Hop:               hop,
			HasChain:          hasChain,
			IsLast:            isLast,
			DeferFinalError:   !isLast,
			HeartbeatOnly:     in.Output.HeartbeatOnly(),
			AttemptsRemaining: maxAttempts - attemptsUsed,
			TimeRemaining:     budget - r.now().Sub(start),
		}
		if hasChain {
			info.MaxSwitches = maxSwitches
		}

		result := attempt(ctx, info)
		if result.Attempts > 0 {
			attemptsUsed += result.Attempts
		}
		trace.Outcome = result.Outcome
		trace.Reason = result.Reason
		trace.Attempts = result.Attempts
		if result.Outcome != HopOutcomeSkipped {
			anyAttempted = true
		}

		switch result.Outcome {
		case HopOutcomeDone:
			res.Trace = append(res.Trace, trace)
			resolvePending(true)
			if probeToken != "" {
				r.Breaker.RecordProbeSuccess(ctx, bkey, breakerCfg, probeToken)
			}
			res.Status = ChainRunServed
			res.ServedIndex = i
			return res

		case HopOutcomeSkipped:
			trace.SkippedBy = "attempt_skipped"
			res.Trace = append(res.Trace, trace)
			releaseProbe(bkey, probeToken)
			if result.WriteFinalError != nil {
				flush = result.WriteFinalError
			}
			continue

		case HopOutcomeFallbackWorthy:
			res.Trace = append(res.Trace, trace)
			resolvePending(false) // 下一跳没有成功，上一跳暂记的 5xx 作废

			// 客户端已断开：不换组、不记熔断、不写响应。
			if ctx.Err() != nil {
				releaseProbe(bkey, probeToken)
				res.Status = ChainRunClientGone
				return res
			}

			// 防线：真实内容已经输出就不能换组，把暂存的原始错误写掉。
			if in.Output.Committed() {
				releaseProbe(bkey, probeToken)
				if !result.ErrorWritten {
					// 只用本跳的闭包：不能执行前面跳暂存的旧错误（本跳已写出真实内容）。
					flush = result.WriteFinalError
					flushError()
				}
				res.Status = ChainRunTerminal
				return res
			}

			switch {
			case !bkey.Valid() || result.Breaker == BreakerSignalNone:
				releaseProbe(bkey, probeToken)
			case result.Breaker == BreakerSignalCount:
				r.Breaker.RecordFailure(ctx, bkey, breakerCfg, in.UserID, probeToken)
			case result.Breaker == BreakerSignalPending:
				pending = &pendingBreakerFailure{key: bkey, probeToken: probeToken}
			}

			if result.ErrorWritten {
				// attempt 已写出错误（最后一跳的原样错误），不能再写第二次；没有「下一跳」可以确认暂记的 5xx。
				resolvePending(false)
				res.Status = ChainRunExhausted
				return res
			}
			if result.WriteFinalError != nil {
				flush = result.WriteFinalError // nil 不覆盖前面跳已暂存的错误
			}
			continue

		default: // HopOutcomeTerminal 及未知值一律按不可回退处理
			res.Trace = append(res.Trace, trace)
			resolvePending(false)
			releaseProbe(bkey, probeToken)
			res.Status = ChainRunTerminal
			return res
		}
	}

	// 兜底重试：整条链没有任何一跳真正发出过上游请求，而有跳是因为熔断被跳过的。
	// 熔断只是尽力而为的优化，不能让用户一次真实尝试都得不到：忽略熔断，对最后一个被熔断跳过的跳补跑一次。
	// 仍受总时间预算与总尝试次数约束；结果照常上报熔断器（相当于一次额外的探测）。
	if !anyAttempted && lastBreakerSkipped >= 0 && ctx.Err() == nil && !in.Output.Committed() &&
		r.now().Sub(start) < budget && attemptsUsed < maxAttempts {
		hop := chain[lastBreakerSkipped]
		bkey := BreakerKey{}
		if r.Breaker != nil {
			if fam := ModelFamily(r.hopModel(in, hop)); fam != "" && hop.Group != nil {
				bkey = BreakerKey{Platform: hop.Group.Platform, GroupID: hop.GroupID, Family: fam}
			}
		}
		info := HopInfo{
			Index:             lastBreakerSkipped,
			Hop:               hop,
			HasChain:          hasChain,
			DeferFinalError:   true,
			HeartbeatOnly:     in.Output.HeartbeatOnly(),
			AttemptsRemaining: maxAttempts - attemptsUsed,
			TimeRemaining:     budget - r.now().Sub(start),
			MaxSwitches:       maxSwitches,
		}
		// 兜底是最后一次尝试，之后不再读取 attemptsUsed，所以这里不累加。
		result := attempt(ctx, info)
		res.BreakerBypassRetried = true
		trace := HopTrace{Index: lastBreakerSkipped, GroupID: hop.GroupID, Outcome: result.Outcome,
			Reason: result.Reason, Attempts: result.Attempts, BreakerBypassed: true}
		if result.Outcome == HopOutcomeSkipped {
			trace.SkippedBy = "attempt_skipped"
		}
		res.Trace = append(res.Trace, trace)

		switch result.Outcome {
		case HopOutcomeDone:
			resolvePending(false)
			res.Status = ChainRunServed
			res.ServedIndex = lastBreakerSkipped
			return res
		case HopOutcomeFallbackWorthy:
			if ctx.Err() != nil {
				resolvePending(false)
				res.Status = ChainRunClientGone
				return res
			}
			if in.Output.Committed() {
				resolvePending(false)
				if !result.ErrorWritten {
					flush = result.WriteFinalError
					flushError()
				}
				res.Status = ChainRunTerminal
				return res
			}
			if bkey.Valid() && result.Breaker == BreakerSignalCount {
				r.Breaker.RecordFailure(ctx, bkey, breakerCfg, in.UserID, "")
			}
			if result.ErrorWritten {
				resolvePending(false)
				res.Status = ChainRunExhausted
				return res
			}
			// 前面跳已暂存的错误（例如最后一跳因 RPM 超限要写的 429）优先，没有才用本跳的。
			if flush == nil {
				flush = result.WriteFinalError
			}
		case HopOutcomeSkipped:
			if result.WriteFinalError != nil && flush == nil {
				flush = result.WriteFinalError
			}
		default: // Terminal 及未知值
			resolvePending(false)
			res.Status = ChainRunTerminal
			return res
		}
	}

	// 没有任何一跳服务成功：暂记的 5xx 作废，把最后一个原始错误写给客户端。
	resolvePending(false)
	if ctx.Err() != nil {
		res.Status = ChainRunClientGone
		return res
	}
	if flushError() {
		res.Status = ChainRunExhausted
		return res
	}
	res.Status = ChainRunUnresolved
	return res
}

func (r *GroupChainRunner) hopModel(in ChainRunInput, hop ChainHop) string {
	if in.HopModel != nil {
		if m := in.HopModel(hop); m != "" {
			return m
		}
	}
	return in.Model
}

// ---------------------------------------------------------------------------
// 结果分类（设计 3.3 逐条对应）
// ---------------------------------------------------------------------------

// HopFailureKind 是入口对「这一跳为什么没服务成功」的事实描述。
type HopFailureKind int

const (
	// HopFailureNoAccount 选号阶段没号（ErrNoAvailableAccounts，或无账号且无 WaitPlan）。
	HopFailureNoAccount HopFailureKind = iota + 1
	// HopFailureBusyTimeout 确认整组已满（或组内重选后仍满），短等超时。
	HopFailureBusyTimeout
	// HopFailureQueueFull 排队已满（IncrementAccountWaitCount 返回不可排队）。
	HopFailureQueueFull
	// HopFailureFailoverExhausted 本跳换号预算用完 / FailoverExhausted。
	HopFailureFailoverExhausted
	// HopFailureInvalidRequest 参数错误（4xx 请求形错误）。
	HopFailureInvalidRequest
	// HopFailureContextTooLong 上下文超长。
	HopFailureContextTooLong
	// HopFailureModerationBlocked 内容审核 / 提示词审计拦截。
	HopFailureModerationBlocked
	// HopFailureInsufficientBalance 余额 / 额度不足。
	HopFailureInsufficientBalance
	// HopFailureClientDisconnected 客户端已断开（ctx.Err() != nil）。
	HopFailureClientDisconnected
	// HopFailureOther 其它错误：Redis 错误、context.Canceled、非容量类。
	HopFailureOther
)

// HopFailure 是 ClassifyHopFailure 的输入，全部是入口能直接拿到的事实。
type HopFailure struct {
	Kind HopFailureKind

	// 仅 HopFailureNoAccount：与 classifyNoAccountError 同源的诊断。
	PoolHasAccounts   bool // 组内有账号
	ModelSupported    bool // 组内有支持该模型的账号
	CapabilityBlocked bool // 能力 / compact / images / WS transport 被阻断

	// 仅 HopFailureFailoverExhausted：最后一个上游错误。
	LastStatus             int
	RequestScopedTransient bool

	// OutputCommitted 为 true 表示真实内容已写给客户端（已扣除心跳）。
	OutputCommitted bool
}

// isFallbackCapacityStatus 对应设计 3.3 / 1.6 状态码表：账号级、上游容量类。
func isFallbackCapacityStatus(status int) bool {
	switch status {
	case 401, 402, 403, 429, 529:
		return true
	}
	return status >= 500 && status <= 599
}

// ClassifyHopFailure 把一次失败分类成 HopResult（Outcome / Reason / Breaker）。
// 调用方随后补上 Attempts、ErrorWritten、WriteFinalError。
//
// 逐条对应设计 3.3：
//
//	回退：没号；繁忙（整组已满，由调用方按 3.5 判定后传 BusyTimeout）；队满；
//	      failover 耗尽且最后错误属 401/402/403/429/529/5xx。
//	不回退：参数错误、上下文超长、审核拦截、余额不足、已输出真实内容、客户端断开、其它错误。
//
// 熔断信号对应 3.6：
//
//	没号：仅「组内有支持该模型的账号、且能力未被阻断」才计；模型找不到 / 能力不支持不计。
//	繁忙：不计。failover 耗尽：RequestScopedTransient 不计；401/402/403 不计；429/529 立即计；5xx 暂记。
func ClassifyHopFailure(f HopFailure) HopResult {
	terminal := HopResult{Outcome: HopOutcomeTerminal}
	if f.OutputCommitted {
		return terminal
	}
	worthy := func(reason FallbackReason, sig BreakerSignal) HopResult {
		return HopResult{Outcome: HopOutcomeFallbackWorthy, Reason: reason, Breaker: sig}
	}
	switch f.Kind {
	case HopFailureNoAccount:
		sig := BreakerSignalNone
		if f.PoolHasAccounts && f.ModelSupported && !f.CapabilityBlocked {
			sig = BreakerSignalCount
		}
		return worthy(FallbackReasonNoAccount, sig)
	case HopFailureBusyTimeout, HopFailureQueueFull:
		return worthy(FallbackReasonBusy, BreakerSignalNone)
	case HopFailureFailoverExhausted:
		if !isFallbackCapacityStatus(f.LastStatus) {
			return terminal
		}
		sig := BreakerSignalNone
		switch {
		case f.RequestScopedTransient:
		case f.LastStatus == 429 || f.LastStatus == 529:
			sig = BreakerSignalCount
		case f.LastStatus >= 500 && f.LastStatus <= 599:
			sig = BreakerSignalPending
		}
		r := worthy(FallbackReasonFailoverExhausted, sig)
		r.UpstreamStatus = f.LastStatus
		return r
	default:
		// 参数错误、上下文超长、审核拦截、余额不足、客户端断开、其它错误
		return terminal
	}
}

// ---------------------------------------------------------------------------
// 输出状态（设计 3.3「已开始输出」）
// ---------------------------------------------------------------------------

// OutputCommitted 是统一的判定函数：字节数一律扣除心跳后再判断。
// totalWritten 为响应已写出的总字节数，keepaliveWritten 为其中心跳 / ping / 保活的字节数（二者都是请求级累计值），
// contentStarted 为「真实模型内容已写出」。
//
// 注意：contentStarted 不是 handler 里现有的 streamStarted。现有的 streamStarted 表示「响应头已按 200 流式提交」，
// 只写过心跳时也会置 true；把它传进来会让只写过心跳的请求被误判为已输出、无法回退。
func OutputCommitted(totalWritten, keepaliveWritten int64, contentStarted bool) bool {
	if contentStarted {
		return true
	}
	return totalWritten-keepaliveWritten > 0
}

// OutputTracker 是请求级的输出状态，跨跳共享。nil 接收者视为什么都没写。
//
// 区分两个概念：
//   - 流式已提交（MarkStreamCommitted，或 total > 0）：响应头已发出，最终错误必须按流式格式收尾；只影响 HeartbeatOnly。
//   - 真实内容已输出（MarkContentStarted，或 total-keepalive > 0）：不能再换组；即 Committed()。
//
// 入口接入时机：handler 在把第一个真实模型输出（首个非心跳的数据事件 / 非流式的响应体）写给客户端之后，
// 调用 MarkContentStarted；写心跳或提交流式响应头时只调用 MarkStreamCommitted，绝不能标记真实内容。
type OutputTracker struct {
	mu              sync.Mutex
	total           int64
	keepalive       int64
	contentStarted  bool
	streamCommitted bool
}

// Observe 记录最新观测值（单调不减）。totalWritten 与 keepaliveWritten 必须是请求级累计值，
// 不能是每跳的计数：keepalive 传小了会把心跳误算成真实内容（偏保守），传大了会把真实内容误算成心跳（危险方向）。
// contentStarted 只在真实模型输出已写出时才传 true，不得传入 handler 现有的 streamStarted。
func (t *OutputTracker) Observe(totalWritten, keepaliveWritten int64, contentStarted bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if totalWritten > t.total {
		t.total = totalWritten
	}
	if keepaliveWritten > t.keepalive {
		t.keepalive = keepaliveWritten
	}
	if contentStarted {
		t.contentStarted = true
	}
}

// MarkContentStarted 标记真实模型内容已写出（之后不可回退）。
func (t *OutputTracker) MarkContentStarted() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.contentStarted = true
}

// MarkStreamCommitted 标记响应头已按流式提交（例如已写过心跳）。不影响 Committed()，只让 HeartbeatOnly() 为真。
func (t *OutputTracker) MarkStreamCommitted() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.streamCommitted = true
}

// Committed 即 outputCommitted：真实内容已输出，不能再换组。
func (t *OutputTracker) Committed() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return OutputCommitted(t.total, t.keepalive, t.contentStarted)
}

// HeartbeatOnly 表示响应头已按流式提交但没有真实内容（全是心跳）：允许换组，最终错误必须按流式格式收尾。
func (t *OutputTracker) HeartbeatOnly() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return !OutputCommitted(t.total, t.keepalive, t.contentStarted) && (t.total > 0 || t.streamCommitted)
}
