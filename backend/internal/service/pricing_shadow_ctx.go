package service

import (
	"context"
	"sync"
)

// W6 PR5：影子比对用的 ctx 标记与「一次计算固定一份快照」。
//
// 影子重算与结算计算走同一个成本函数（设计 4.4）。区别只靠 ctx 带过去的两个标记：
//   - 影子标记（shadowRecomputeCtxKey）：这次计算不是结算，不写用量行、不计无价指标、不打 falling back 日志；
//   - forceStage（forceStageCtxKey）：让 stagedPolicy 不看分组的实际阶段，改用指定阶段的实现。
//
// 两个键都是未导出类型，包外构造不出来；forceStage 只有在 ctx 同时带影子标记时才生效（forcedStageFromCtx），
// 所以任何不经过 withShadowRecompute 的调用方都不可能靠它改变真实请求的路由（S-2）。

type forceStageCtxKey struct{}

type shadowRecomputeCtxKey struct{}

// withShadowRecompute 返回一个影子重算用的 ctx：带影子标记、非结算标记，并强制 stagedPolicy 使用 stage 对应的实现。
// 非结算标记复用 WithBillingNonSettlement（billing_non_settlement.go 约定的那一对），PR1 的无价计数因此自动跳过。
func withShadowRecompute(ctx context.Context, stage PricingStage) context.Context {
	ctx = WithBillingNonSettlement(ctx)
	ctx = context.WithValue(ctx, shadowRecomputeCtxKey{}, true)
	return context.WithValue(ctx, forceStageCtxKey{}, stage)
}

// isShadowRecompute 判断 ctx 是否带影子标记。
func isShadowRecompute(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(shadowRecomputeCtxKey{}).(bool)
	return marked
}

// forcedStageFromCtx 读取 forceStage。ctx 没有同时带影子标记时一律视为没有。
func forcedStageFromCtx(ctx context.Context) (PricingStage, bool) {
	if !isShadowRecompute(ctx) {
		return "", false
	}
	stage, ok := ctx.Value(forceStageCtxKey{}).(PricingStage)
	return stage, ok && stage != ""
}

// ---------------------------------------------------------------------------
// 快照固定（PR4-1 审查「PR5 前必须修」第 2 条、PR4-2 审查「给后续」）
// ---------------------------------------------------------------------------

// 一次计算里，额外倍率、价格覆盖、准入、成本模式、阶段、「是否兜底」都要从同一份分组快照读：
// 否则快照在两次读取之间被替换时，会读到新旧混合的状态（例如单元格从 extra x2 改成 custom X，
// 先读到旧的 x2、再读到新的 X，就按 X x 2 收费）。做法：计算入口在 ctx 里放一个固定器，
// matrixPolicy 读快照时先查固定器，没有再去缓存取，取到后记进固定器，这次计算后面的读取都用同一份。

type pricingSnapshotPinKey struct{}

type pinSlot struct {
	policy  *matrixPolicy
	groupID int64
}

// pinnedEntry 固定器里的一项。snap 为 nil 表示这次计算开始时缓存里还没有这个分组（冷启动），
// 同样要固定下来：后面的读取不能因为缓存刚好加载完成而改变这次计算看到的状态。
type pinnedEntry struct {
	snap *matrixSnapshot
}

type pricingSnapshotPin struct {
	mu    sync.Mutex
	slots map[pinSlot]pinnedEntry
}

func (p *pricingSnapshotPin) get(policy *matrixPolicy, groupID int64) (pinnedEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.slots[pinSlot{policy: policy, groupID: groupID}]
	return e, ok
}

// put 固定一项并返回最终生效的那一项：并发固定同一个槽位时先到者生效，保证同一次计算只有一个答案。
func (p *pricingSnapshotPin) put(policy *matrixPolicy, groupID int64, e pinnedEntry) pinnedEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	slot := pinSlot{policy: policy, groupID: groupID}
	if existing, ok := p.slots[slot]; ok {
		return existing
	}
	if p.slots == nil {
		p.slots = make(map[pinSlot]pinnedEntry, 2)
	}
	p.slots[slot] = e
	return e
}

// putSnapshot 把真快照固定进槽位，并返回最终生效的快照。槽位里已经有真快照时保持不变（先到者生效）；
// 槽位里是冷启动的空位时换成真快照。
func (p *pricingSnapshotPin) putSnapshot(policy *matrixPolicy, groupID int64, snap *matrixSnapshot) *matrixSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	slot := pinSlot{policy: policy, groupID: groupID}
	if existing, ok := p.slots[slot]; ok && existing.snap != nil {
		return existing.snap
	}
	if p.slots == nil {
		p.slots = make(map[pinSlot]pinnedEntry, 2)
	}
	p.slots[slot] = pinnedEntry{snap: snap}
	return snap
}

func pinFromContext(ctx context.Context) *pricingSnapshotPin {
	if ctx == nil {
		return nil
	}
	pin, _ := ctx.Value(pricingSnapshotPinKey{}).(*pricingSnapshotPin)
	return pin
}

// pinGroupPolicySnapshots 在计算入口调用：只有策略会读分组快照（stagedPolicy、matrixPolicy）时才放固定器，
// 其余策略（legacyPolicy、测试替身、nil）原样返回 ctx，所以 legacy 路径没有任何额外分配。
// ctx 里已经有固定器时原样返回：嵌套的计算入口共用外层那一个。
func pinGroupPolicySnapshots(ctx context.Context, policy GroupPolicy) context.Context {
	switch policy.(type) {
	case *stagedPolicy, *matrixPolicy:
	default:
		return ctx
	}
	if pinFromContext(ctx) != nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, pricingSnapshotPinKey{}, &pricingSnapshotPin{})
}

// withPinnedSnapshot 返回一个新的 ctx，固定器里只有这一项：影子比对用它保证 v2 一侧读到的正是
// 「准入判断时看过状态」的那一份快照。
func withPinnedSnapshot(ctx context.Context, policy *matrixPolicy, groupID int64, snap *matrixSnapshot) context.Context {
	pin := &pricingSnapshotPin{}
	pin.put(policy, groupID, pinnedEntry{snap: snap})
	return context.WithValue(ctx, pricingSnapshotPinKey{}, pin)
}
