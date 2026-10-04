package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Key 级分组回退链的入口接线（PR2a 第 4 段）。
//
// 本文件是各入口共用的骨架：「解析一次链 → 审核用并集 → 逐跳进入 attempt → 收尾」。
// 入口（Responses、以后的 chat/completions）只需要提供自己的单跳逻辑 chainHopFuncs.Attempt，
// 以及（可选的）静态资格检查与错误写出函数；影子 Key、分组层 RPM、RPM 退回、输出状态、指标与日志都在这里。
//
// 无链请求（开关关闭、Key 没配链、链长 < 2、解析出错）一律不经过本文件：入口拿到的 plan 为 nil，
// 走与引入回退链之前完全一致的原路径。

// groupFallbackSettingsProvider 是读取回退链设置的最小接口（*service.SettingService 实现，读取带 60 秒进程内缓存）。
type groupFallbackSettingsProvider interface {
	GetGroupFallbackSettings(ctx context.Context) service.GroupFallbackSettings
}

// groupFallbackRuntime 聚合入口接线需要的依赖：链解析服务、设置、熔断器。
// nil 表示不启用回退链（入口的所有链分支都被短路，行为与引入回退链之前一致）。
type groupFallbackRuntime struct {
	routes   service.GroupRouteService
	settings groupFallbackSettingsProvider
	breaker  service.GroupChainBreakerGate
}

// newGroupFallbackRuntime 在缺少链解析服务或设置时返回 nil（视为不启用）。breaker 可为 nil（不做熔断）。
func newGroupFallbackRuntime(routes service.GroupRouteService, settings groupFallbackSettingsProvider, breaker service.GroupChainBreakerGate) *groupFallbackRuntime {
	if routes == nil || settings == nil {
		return nil
	}
	return &groupFallbackRuntime{routes: routes, settings: settings, breaker: breaker}
}

// groupChainPlan 是入口对一个请求解析出的有效链。解析只做一次：审核用链上全部分组的并集，
// 之后 runner 逐跳迭代的就是同一份切片，循环里不再解析、不再读缓存（B1 不变式：被审的链 ⊇ 实际服务的分组）。
type groupChainPlan struct {
	rt       *groupFallbackRuntime
	hops     []service.ChainHop
	settings service.GroupFallbackSettings
	output   *service.OutputTracker
}

// Hops 返回有效链（原样、按尝试顺序）。调用方不得修改。
func (p *groupChainPlan) Hops() []service.ChainHop {
	if p == nil {
		return nil
	}
	return p.hops
}

// FirstHop 返回链上第 0 跳（主分组，或管理员 head 分组）。
func (p *groupChainPlan) FirstHop() service.ChainHop {
	return p.hops[0]
}

// HasAdminHop 报告链上是否有管理员隐藏链的项。
func (p *groupChainPlan) HasAdminHop() bool {
	if p == nil {
		return false
	}
	for _, hop := range p.hops {
		if hop.RouteSource == service.RouteSourceAdmin {
			return true
		}
	}
	return false
}

// resolvePlan 解析请求的有效链。返回 nil 表示该请求走原路径，判定顺序从最便宜到最贵：
//  1. 没有启用回退链运行时，或 Key 没配链（apiKey.HasGroupRoutes 来自鉴权快照，热路径零额外查询）；
//  2. 全局开关关闭（设置带 60 秒缓存，热路径同样不读库）；
//  3. 解析出错：按无链处理，链配置的问题不能挡住主分组的请求；
//  4. 有效链长度 < 2：只有主分组一项，原路径（审核、RPM、调度都不变）。
func (rt *groupFallbackRuntime) resolvePlan(ctx context.Context, apiKey *service.APIKey, reqLog *zap.Logger) *groupChainPlan {
	if rt == nil || apiKey == nil || !apiKey.HasGroupRoutes || apiKey.GroupID == nil {
		return nil
	}
	settings := rt.settings.GetGroupFallbackSettings(ctx)
	if !settings.Enabled {
		return nil
	}
	chain, err := rt.routes.ResolveEffectiveChain(ctx, apiKey, apiKey.User, service.ResolveOptions{})
	if err != nil {
		if reqLog != nil {
			reqLog.Warn("group_fallback.resolve_chain_failed", zap.Error(err))
		}
		return nil
	}
	if len(chain.Hops) < 2 {
		return nil
	}
	return &groupChainPlan{rt: rt, hops: chain.Hops, settings: settings, output: &service.OutputTracker{}}
}

// chainHopFuncs 是入口提供给骨架的单跳逻辑。
type chainHopFuncs struct {
	// Eligible 是每跳的静态资格检查（不含容量，设计 3.4）：返回 false 则静默跳过本跳（不写响应、不计 RPM）。
	// 可为 nil（全部通过）。
	Eligible func(hopKey *service.APIKey, info service.HopInfo) bool
	// Attempt 是入口自己的单跳逻辑：选号 → 等槽 → Forward → failover。
	Attempt func(ctx context.Context, hopKey *service.APIKey, info service.HopInfo) service.HopResult
	// WriteRPMExceeded 写出分组层 RPM 超限的 429（含 Retry-After）。非首跳超限且这一跳是末跳时，
	// 骨架把它挂在 Skipped 结果的 WriteFinalError 上，覆盖前面跳暂存的错误。
	WriteRPMExceeded func(err error)
	// WriteUnresolved 在没有任何一跳运行、也没有暂存错误可写时调用（例如所有跳都被资格检查跳过），
	// 保证客户端总能收到一个响应。
	WriteUnresolved func()
}

// groupChainEntry 把一次请求的链、第 0 跳的 RPM ticket 和入口的单跳逻辑接起来。
type groupChainEntry struct {
	plan    *groupChainPlan
	billing *service.BillingCacheService
	apiKey  *service.APIKey // 鉴权中间件放进 ctx 的原始 Key，每一跳的影子 Key 都从它派生
	// firstTicket 是入口 CheckBillingEligibilityForChain 对第 0 跳分组层 RPM 的计数凭据。
	// 第 0 跳不得再调用 CheckHopGroupRPM，否则首跳会被重复计数（第 2 段契约）。
	firstTicket *service.GroupRPMTicket
	reqLog      *zap.Logger
	model       string
	stream      bool
	// hopModel 返回某一跳渠道映射之后的实际模型名，用于熔断的模型族判定；可为 nil（用请求模型）。
	hopModel func(hop service.ChainHop) string

	firstHopRan bool
}

func newGroupChainEntry(plan *groupChainPlan, billing *service.BillingCacheService, apiKey *service.APIKey, firstTicket *service.GroupRPMTicket, reqLog *zap.Logger, model string, stream bool, hopModel func(hop service.ChainHop) string) *groupChainEntry {
	return &groupChainEntry{
		plan:        plan,
		billing:     billing,
		apiKey:      apiKey,
		firstTicket: firstTicket,
		reqLog:      reqLog,
		model:       model,
		stream:      stream,
		hopModel:    hopModel,
	}
}

// run 用 runner 逐跳执行这条链，并完成收尾：RPM 退回、输出状态、指标与日志、Unresolved 兜底。
func (e *groupChainEntry) run(c *gin.Context, fns chainHopFuncs) service.ChainRunResult {
	runner := &service.GroupChainRunner{Settings: e.plan.settings, Breaker: e.plan.rt.breaker}
	in := service.ChainRunInput{
		Chain:    e.plan.hops,
		Model:    e.model,
		HopModel: e.hopModel,
		UserID:   e.apiKey.UserID,
		Stream:   e.stream,
		Output:   e.plan.output,
	}
	res := runner.Run(c.Request.Context(), in, func(ctx context.Context, info service.HopInfo) service.HopResult {
		result := e.attemptHop(ctx, c, info, fns)
		// 响应头已经提交（心跳 / ping / 已写内容）：之后的最终错误必须按流式格式收尾。
		// 真实内容是否已输出由 attempt 在 failover 判定处标记（MarkContentStarted），这里只标记「已提交」。
		if c.Writer != nil && c.Writer.Written() {
			e.plan.output.MarkStreamCommitted()
		}
		return result
	})

	// 第 0 跳被熔断跳过、整个请求从未进入过第 0 跳：入口为它计的分组层 RPM 没有对应的服务，退回。
	if !e.firstHopRan {
		e.firstTicket.Release(c.Request.Context())
	}

	if res.Status == service.ChainRunUnresolved && fns.WriteUnresolved != nil {
		fns.WriteUnresolved()
	}

	service.RecordGroupFallbackRun(res)
	e.logRun(res)
	return res
}

func (e *groupChainEntry) attemptHop(ctx context.Context, c *gin.Context, info service.HopInfo, fns chainHopFuncs) service.HopResult {
	// 每进入一跳都重新生成影子 Key 并把 ctx 里的分组换成这一跳的分组。
	hopKey := ServeHop(c, e.apiKey, info.Hop)
	service.SetOpsServedGroup(c, e.servedGroupIDForOps(info.Hop))

	var ticket *service.GroupRPMTicket
	if info.Index == 0 {
		ticket = e.firstTicket
		e.firstHopRan = true
	}

	// 静态资格先于分组层 RPM 计数：被跳过的跳不应占用 RPM 额度。
	if fns.Eligible != nil && !fns.Eligible(hopKey, info) {
		if e.reqLog != nil {
			e.reqLog.Debug("group_fallback.hop_ineligible", zap.Int("hop_index", info.Index), zap.Int64("group_id", info.Hop.GroupID))
		}
		skipped := service.HopResult{Outcome: service.HopOutcomeSkipped}
		ReleaseHopGroupRPMIfNotServed(ctx, ticket, info, skipped)
		return skipped
	}

	if info.Index > 0 {
		verdict, t, err := CheckHopGroupRPM(ctx, e.billing, e.apiKey.User, info.Hop, info.Index)
		switch verdict {
		case GroupRPMSkip:
			// 非首跳超限：跳过这一跳（计数已退回）。末跳要写 429，覆盖前面跳暂存的错误。
			skipped := service.HopResult{Outcome: service.HopOutcomeSkipped}
			if info.IsLast && fns.WriteRPMExceeded != nil {
				skipped.WriteFinalError = func() { fns.WriteRPMExceeded(err) }
			}
			return skipped
		case GroupRPMReject:
			// 下标 > 0 不会得到 Reject；防御：按首跳超限处理，写 429 并终止，不回退。
			if fns.WriteRPMExceeded != nil {
				fns.WriteRPMExceeded(err)
			}
			return service.HopResult{Outcome: service.HopOutcomeTerminal}
		}
		ticket = t
	}

	result := fns.Attempt(ctx, hopKey, info)
	ReleaseHopGroupRPMIfNotServed(ctx, ticket, info, result)
	return result
}

// servedGroupIDForOps 返回要写进 ops 事件的服务分组：就是主分组时为 0（不标记）。
func (e *groupChainEntry) servedGroupIDForOps(hop service.ChainHop) int64 {
	if e.apiKey.GroupID != nil && hop.GroupID == *e.apiKey.GroupID {
		return 0
	}
	return hop.GroupID
}

// servedHop 返回成功服务的那一跳；没有成功服务时 ok 为 false。
func (e *groupChainEntry) servedHop(res service.ChainRunResult) (service.ChainHop, bool) {
	if res.Status != service.ChainRunServed || res.ServedIndex < 0 || res.ServedIndex >= len(e.plan.hops) {
		return service.ChainHop{}, false
	}
	return e.plan.hops[res.ServedIndex], true
}

func (e *groupChainEntry) logRun(res service.ChainRunResult) {
	if e.reqLog == nil {
		return
	}
	servedGroup := int64(0)
	if hop, ok := e.servedHop(res); ok {
		servedGroup = hop.GroupID
	}
	fields := []zap.Field{
		zap.Int("status", int(res.Status)),
		zap.Int("served_index", res.ServedIndex),
		zap.Int64("served_group_id", servedGroup),
		zap.String("trace", formatChainTrace(res.Trace)),
		zap.String("stopped_by", res.StoppedBy),
		zap.Bool("error_flushed", res.ErrorFlushed),
		zap.Bool("breaker_bypass_retried", res.BreakerBypassRetried),
	}
	// 一帆风顺（第 0 跳直接服务）只打 debug，避免日志量随流量增长；出现回退、跳过、失败才打 info。
	if res.Status == service.ChainRunServed && res.ServedIndex == 0 && len(res.Trace) == 1 {
		e.reqLog.Debug("group_fallback.run_finished", fields...)
		return
	}
	e.reqLog.Info("group_fallback.run_finished", fields...)
}

func formatChainTrace(trace []service.HopTrace) string {
	parts := make([]string, 0, len(trace))
	for _, t := range trace {
		part := fmt.Sprintf("%d@%d:%d", t.Index, t.GroupID, int(t.Outcome))
		if t.Reason != "" {
			part += "/" + string(t.Reason)
		}
		if t.SkippedBy != "" {
			part += "/skip=" + t.SkippedBy
		}
		if t.BreakerBypassed {
			part += "/bypass"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ",")
}
