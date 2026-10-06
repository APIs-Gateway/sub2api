package service

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

// W6 PR5：stagedPolicy，按分组的价格体系阶段选择 GroupPolicy 的实现（设计 3.3、4.3、4.4）。
//
//   - legacy：转发给 legacyPolicy，与没有 stagedPolicy 时逐位相同；
//   - shadow：legacyPolicy 的结果照常返回给调用方；同一个调用上 v2（matrixPolicy）同步算一遍并比较，
//     只在不一致时写指标与采样。比对永不影响请求：v2 一侧的 panic 被吞掉并计数；
//   - v2：准入、映射、功能、定价、账号成本都读矩阵（matrixPolicy）。W6 PR7a 起路由是活的（v2Live 为 true），
//     但阶段 API 仍然只允许 legacy 与 shadow（pricingStageAllowed），所以线上不会有分组处于 v2；
//     v2 一侧运行时额外叠加模型目录状态（draft、retired 不放行）与白名单分组的无价检查（RuntimeAccess）。
//
// 阶段读取：用 matrixPolicy.cachedSnapshot，缓存里有快照（哪怕过期，后台刷新）就不阻塞；缓存里没有（进程刚启动之后
// 第一次出现的分组）同步加载一次，因为它可能已经是 v2，不能按 legacy 处理（W6 PR7b，PR7a 审查 1）。加载失败又没有旧快照时，
// 分组可能是 v2 就拒绝它的请求（snapshotUnavailablePolicy），不是 v2（启动时没有配置行）就按 legacy。
//
// 一次计算固定同一份快照：阶段判断、v2 一侧的全部读取都从 ctx 里的固定器（pinGroupPolicySnapshots）取。
//
// 成本比对不在这里：成本由网关在一处调用点用 ctx 带 forceStage 把同一个成本函数再算一遍
// （pricing_shadow_session.go、gateway_pricing_shadow.go、openai_pricing_shadow.go）。
type stagedPolicy struct {
	legacy GroupPolicy
	matrix *matrixPolicy
	hub    *pricingShadowHub
	// v2Live 为 true 时阶段为 v2 的分组才真正读矩阵。生产构造恒为 true；测试可以关掉它来验证 legacy 路径。
	v2Live bool

	// catalog 是运行时目录状态读取方（带缓存），由 SetModelCatalog 在装配阶段接上；为 nil 时 v2 准入不看目录。
	catalog *runtimeCatalog
	// runtime 是白名单分组无价检查的缓存与计数（runtime_pricing.go）。
	runtime runtimePricingState
	// retryDelay 是启动预加载的重试间隔，测试里缩短；零值取默认。
	retryDelay time.Duration
}

var _ GroupPolicy = (*stagedPolicy)(nil)

// StagedGroupPolicy 是 stagedPolicy 的导出别名，供依赖注入与管理接口引用。
type StagedGroupPolicy = stagedPolicy

// newStagedGroupPolicy 创建 stagedPolicy。matrix 为 nil 时永远走 legacy。sink 可为 nil（只计数、不写样本）。
func newStagedGroupPolicy(legacy GroupPolicy, matrix *matrixPolicy, sink PricingShadowSink) *stagedPolicy {
	return &stagedPolicy{legacy: legacy, matrix: matrix, hub: newPricingShadowHub(sink), v2Live: true}
}

// Stats 返回影子比对的进程内计数。
func (s *stagedPolicy) Stats() PricingShadowStats { return s.hub.Stats() }

// InvalidateGroups 让矩阵快照失效，供阶段切换在写库之后调用。
func (s *stagedPolicy) InvalidateGroups(groupIDs ...int64) {
	if s.matrix != nil {
		s.matrix.InvalidateGroups(groupIDs...)
	}
}

// MatrixSnapshotStats 返回矩阵快照缓存的进程内计数；没有矩阵策略时返回零值。
func (s *stagedPolicy) MatrixSnapshotStats() MatrixSnapshotStats {
	if s.matrix == nil {
		return MatrixSnapshotStats{}
	}
	return s.matrix.Stats()
}

// route 决定这次调用用哪个实现。返回的 shadow 非空表示要做影子比对，并带着「判断时看到的那份快照」。
func (s *stagedPolicy) route(ctx context.Context, groupID int64) (active GroupPolicy, shadow *matrixSnapshot) {
	if s.matrix == nil {
		return s.legacy, nil
	}
	if forced, ok := forcedStageFromCtx(ctx); ok {
		if forced == PricingStageV2 {
			return s.matrix, nil
		}
		return s.legacy, nil
	}
	snap := s.matrix.cachedSnapshot(ctx, groupID)
	if snap == nil {
		// 缓存里没有（进程刚启动之后第一次出现的分组，或预加载之后才有配置行的分组）：同步加载，同一个分组只会等这一次。
		// 不能再按 legacy 处理：它可能已经是 v2，退回 legacy 会丢掉额外倍率、把只有 custom 价的模型按 0 元计费（PR7a 审查 1(b)）。
		snap = s.matrix.snapshot(ctx, groupID)
	}
	if snap.loadErr != nil && s.v2Live && s.matrix.mayBeConfigured(groupID) {
		// 快照加载不出来、也没有旧快照可用，而分组可能是 v2：不知道它的阶段，不能按 legacy 或默认状态计费。
		return s.snapshotUnavailable(groupID, snap.loadErr), nil
	}
	switch snap.stage {
	case PricingStageV2:
		if s.v2Live && snap.loadErr == nil {
			return s.matrix, nil
		}
	case PricingStageShadow:
		return s.legacy, snap
	}
	return s.legacy, nil
}

// snapshotUnavailable 返回「快照不可用」策略，并计数、限速记 Error 日志。
func (s *stagedPolicy) snapshotUnavailable(groupID int64, err error) GroupPolicy {
	s.matrix.unavailable.Add(1)
	s.runtime.logLimited(s.hub.now(), "snapshot_unavailable:"+strconv.FormatInt(groupID, 10), func() {
		slog.Error("pricing matrix snapshot unavailable for a group that may be on v2, rejecting its requests",
			"group_id", groupID, "error", err)
	})
	return snapshotUnavailablePolicy{err: err}
}

// EnsureGroupsLoaded 同步把这些分组的快照重新加载进本实例的缓存。加载失败，或只拿到沿用的旧数据，都返回错误。
// 阶段切换提交之后调用，确认本实例已经读到新阶段。
func (s *stagedPolicy) EnsureGroupsLoaded(ctx context.Context, groupIDs ...int64) error {
	if s == nil || s.matrix == nil {
		return nil
	}
	for _, id := range groupIDs {
		snap := s.matrix.loadSnapshot(ctx, id)
		if snap.loadErr != nil {
			return snap.loadErr
		}
		if snap.stale {
			return fmt.Errorf("group %d: only a stale snapshot is available", id)
		}
	}
	return nil
}

// snapshotUnavailablePolicy 是快照不可用时使用的策略：准入一律拒绝，两个返回错误的读口把错误交给调用方，其余是中性值。
// 请求在调度阶段的准入（ModelAccess）就被挡下，不会走到计费；拒绝只发生在「快照加载失败且没有旧快照」这种矩阵表读取故障里。
type snapshotUnavailablePolicy struct{ err error }

var _ GroupPolicy = snapshotUnavailablePolicy{}

// QuoteAccessReasonSnapshotUnavailable 分组的价格配置读不出来，请求被拒绝（原因只写日志，不出现在用户可见的文案里）。
const QuoteAccessReasonSnapshotUnavailable = "snapshot_unavailable"

func (snapshotUnavailablePolicy) Mapping(_ context.Context, _ int64, model string) ChannelMappingResult {
	return ChannelMappingResult{MappedModel: model}
}

func (snapshotUnavailablePolicy) ModelAccess(context.Context, int64, string) QuoteAccess {
	return QuoteAccess{OK: false, Reason: QuoteAccessReasonSnapshotUnavailable}
}

func (snapshotUnavailablePolicy) UpstreamAccess(context.Context, int64, string) QuoteAccess {
	return QuoteAccess{OK: false, Reason: QuoteAccessReasonSnapshotUnavailable}
}

func (p snapshotUnavailablePolicy) UpstreamCheck(context.Context, int64) (bool, error) {
	return false, p.err
}

func (p snapshotUnavailablePolicy) Feature(context.Context, int64, string, GroupFeature) (*bool, error) {
	return nil, p.err
}

func (snapshotUnavailablePolicy) PriceOverride(context.Context, int64, string, time.Time) *ChannelModelPricing {
	return nil
}

func (snapshotUnavailablePolicy) ExtraMultiplier(context.Context, int64, string, time.Time) float64 {
	return 1
}

func (snapshotUnavailablePolicy) CostMode(context.Context, int64) MatrixCostMode {
	return MatrixCostAccountRate
}

func (snapshotUnavailablePolicy) CostRules(context.Context, int64) ([]AccountStatsPricingRule, string) {
	return nil, ""
}

func (snapshotUnavailablePolicy) Stage(context.Context, int64) PricingStage {
	return PricingStageLegacy
}

// shadowReady 判断这次调用能不能比对：快照不能是兜底或沿用的旧数据，渠道与矩阵快照最近没有失效过，
// 并且没有超过限速。不能比对时记录原因。
func (s *stagedPolicy) shadowReady(snap *matrixSnapshot, gate *shadowRateGate) bool {
	switch {
	case snap.loadErr != nil:
		s.hub.noteSkipped(ShadowSkipDegraded)
		return false
	case snap.stale:
		s.hub.noteSkipped(ShadowSkipStale)
		return false
	}
	now := s.hub.now()
	if last := s.matrix.lastInvalidation(); !last.IsZero() && now.Sub(last) < shadowRecentChangeGrace {
		s.hub.noteSkipped(ShadowSkipRecentChange)
		return false
	}
	if !gate.allow(now) {
		s.hub.noteSkipped(ShadowSkipRateLimited)
		return false
	}
	return true
}

// compareCall 在 shadow 阶段对一次逐次调用做比对：run 在固定了快照的 ctx 上调用 v2 一侧并把差异登记下来。
// 整个过程吞掉 panic，永不影响调用方。
func (s *stagedPolicy) compareCall(ctx context.Context, groupID int64, snap *matrixSnapshot, run func(v2ctx context.Context)) {
	if !s.shadowReady(snap, &s.hub.callGate) {
		return
	}
	s.hub.guard("call", func() {
		run(withPinnedSnapshot(ctx, s.matrix, groupID, snap))
		s.hub.noteCompared(groupID)
	})
}

func (s *stagedPolicy) Mapping(ctx context.Context, groupID int64, model string) ChannelMappingResult {
	active, shadow := s.route(ctx, groupID)
	got := active.Mapping(ctx, groupID, model)
	if shadow != nil {
		s.compareCall(ctx, groupID, shadow, func(v2ctx context.Context) {
			// ChannelID 不比：v2 分组不再写 channel_id（设计 2.3、附录 A 第 11 条）。
			v2 := s.matrix.Mapping(v2ctx, groupID, model)
			if got.MappedModel != v2.MappedModel || got.Mapped != v2.Mapped || got.BillingModelSource != v2.BillingModelSource {
				s.hub.noteDiff(groupID, ShadowKindMapping, ShadowClassTranslation, model, "",
					shadowMappingView(got), shadowMappingView(v2))
			}
		})
	}
	return got
}

func (s *stagedPolicy) ModelAccess(ctx context.Context, groupID int64, model string) QuoteAccess {
	active, shadow := s.route(ctx, groupID)
	got := active.ModelAccess(ctx, groupID, model)
	if got.OK {
		got = s.catalogAccess(ctx, active, groupID, model)
	}
	if shadow != nil {
		s.compareAccess(ctx, groupID, shadow, model, got, func(v2ctx context.Context) QuoteAccess {
			return s.matrix.ModelAccess(v2ctx, groupID, model)
		})
	}
	return got
}

func (s *stagedPolicy) UpstreamAccess(ctx context.Context, groupID int64, upstreamModel string) QuoteAccess {
	active, shadow := s.route(ctx, groupID)
	got := active.UpstreamAccess(ctx, groupID, upstreamModel)
	if shadow != nil {
		s.compareAccess(ctx, groupID, shadow, upstreamModel, got, func(v2ctx context.Context) QuoteAccess {
			return s.matrix.UpstreamAccess(v2ctx, groupID, upstreamModel)
		})
	}
	return got
}

// compareAccess 比较准入结果，只比 OK，不比原因（原因只用于展示）。
// legacy 放行而 v2 因单元格 open=false 关闭，是 v2 新增的例外语义，记为预期差异；其余都是翻译差异。
func (s *stagedPolicy) compareAccess(ctx context.Context, groupID int64, snap *matrixSnapshot, model string, got QuoteAccess, v2Access func(context.Context) QuoteAccess) {
	s.compareCall(ctx, groupID, snap, func(v2ctx context.Context) {
		v2 := v2Access(v2ctx)
		if got.OK == v2.OK {
			return
		}
		class := ShadowClassTranslation
		if got.OK && !v2.OK && v2.Reason == QuoteAccessReasonClosedInGroup {
			class = ShadowClassExpected
		}
		s.hub.noteDiff(groupID, ShadowKindAccess, class, model, "", got, v2)
	})
}

func (s *stagedPolicy) UpstreamCheck(ctx context.Context, groupID int64) (bool, error) {
	active, shadow := s.route(ctx, groupID)
	got, err := active.UpstreamCheck(ctx, groupID)
	if shadow != nil {
		s.compareCall(ctx, groupID, shadow, func(v2ctx context.Context) {
			v2, v2err := s.matrix.UpstreamCheck(v2ctx, groupID)
			if (err == nil) != (v2err == nil) || got != v2 {
				s.hub.noteDiff(groupID, ShadowKindFeature, ShadowClassTranslation, "upstream_check", "",
					map[string]any{"required": got, "failed": err != nil}, map[string]any{"required": v2, "failed": v2err != nil})
			}
		})
	}
	return got, err
}

func (s *stagedPolicy) Feature(ctx context.Context, groupID int64, platform string, f GroupFeature) (*bool, error) {
	active, shadow := s.route(ctx, groupID)
	got, err := active.Feature(ctx, groupID, platform, f)
	if shadow != nil {
		s.compareCall(ctx, groupID, shadow, func(v2ctx context.Context) {
			v2, v2err := s.matrix.Feature(v2ctx, groupID, platform, f)
			if (err == nil) != (v2err == nil) || !groupFeatureEquivalent(f, got, v2) {
				s.hub.noteDiff(groupID, ShadowKindFeature, ShadowClassTranslation, string(f), "",
					shadowBoolPtrView(got), shadowBoolPtrView(v2))
			}
		})
	}
	return got, err
}

// PriceOverride、ExtraMultiplier、CostMode、CostRules 不在这里逐次比对：它们的结果只有放进成本计算才有意义，
// 由网关的成本比对点用同一个成本函数重算，一次覆盖（设计 4.4）。shadow 阶段它们走 legacy。

func (s *stagedPolicy) PriceOverride(ctx context.Context, groupID int64, model string, at time.Time) *ChannelModelPricing {
	active, _ := s.route(ctx, groupID)
	return active.PriceOverride(ctx, groupID, model, at)
}

func (s *stagedPolicy) ExtraMultiplier(ctx context.Context, groupID int64, model string, at time.Time) float64 {
	active, _ := s.route(ctx, groupID)
	return active.ExtraMultiplier(ctx, groupID, model, at)
}

func (s *stagedPolicy) CostMode(ctx context.Context, groupID int64) MatrixCostMode {
	active, _ := s.route(ctx, groupID)
	return active.CostMode(ctx, groupID)
}

func (s *stagedPolicy) CostRules(ctx context.Context, groupID int64) ([]AccountStatsPricingRule, string) {
	active, _ := s.route(ctx, groupID)
	return active.CostRules(ctx, groupID)
}

// Stage 返回分组配置的阶段（不是路由结果）：报价器据此决定要不要叠加目录状态。
// 影子重算里按强制的阶段返回。缓存里还没有这个分组时是 legacy。
func (s *stagedPolicy) Stage(ctx context.Context, groupID int64) PricingStage {
	if forced, ok := forcedStageFromCtx(ctx); ok {
		return forced
	}
	if s.matrix == nil {
		return s.legacy.Stage(ctx, groupID)
	}
	if snap := s.matrix.cachedSnapshot(ctx, groupID); snap != nil && snap.loadErr == nil {
		return snap.stage
	}
	return PricingStageLegacy
}

func shadowMappingView(r ChannelMappingResult) map[string]any {
	return map[string]any{"mapped_model": r.MappedModel, "mapped": r.Mapped, "billing_model_source": r.BillingModelSource}
}

// groupFeatureEquivalent 按运行时读取方的语义比较开关值（与 PR4 等价测试的 mqEffective 一致）：
// web_search_emulation、bedrock_cc_compat 的读取方把 nil 当 false（gateway_websearch_emulation.go、gateway_service.go 的
// isBedrockCCCompatEnabled），而派生按设计 2.3 在开关不存在时不写键，所以「未配置」与 false 等价；
// codex_image_generation_bridge 的 nil 表示跟随全局开关（openai_gateway_service.go），与 false 效果不同，严格比较。
func groupFeatureEquivalent(f GroupFeature, a, b *bool) bool {
	switch f {
	case GroupFeatureWebSearchEmulation, GroupFeatureBedrockCCCompat:
		return (a != nil && *a) == (b != nil && *b)
	default:
		return shadowBoolPtrEqual(a, b)
	}
}

func shadowBoolPtrEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func shadowBoolPtrView(v *bool) any {
	if v == nil {
		return nil
	}
	return *v
}
