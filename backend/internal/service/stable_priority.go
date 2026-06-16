package service

import (
	"context"
	"time"
)

// 稳定优先（Stable Priority）防抖参数。首版用包级常量，后续可升级为 group 字段。
const (
	// StablePriorityRevertStableDuration 廉价(home)组需持续健康多久才允许切回，迟滞防抖。
	StablePriorityRevertStableDuration = 90 * time.Second
	// StablePriorityFallbackMinDwell 进入兜底后最短驻留时长，避免刚兜底就被切回。
	StablePriorityFallbackMinDwell = 30 * time.Second
	// StablePriorityMaxChainDepth 兜底链最大档数（防御环/超长链）。
	StablePriorityMaxChainDepth = 5
	// StablePriorityStateTTL 状态 Hash 的 TTL（活跃刷新）。
	StablePriorityStateTTL = time.Hour
)

// 稳定优先运行时状态机的两个模式。normal = 用 home 组；fallback = 已跨分组兜底。
const (
	StablePriorityModeNormal   = "normal"
	StablePriorityModeFallback = "fallback"
)

// StablePriorityState 是某个 home(廉价)组的 per-group 运行时状态。
// 约定：normal 态不落 Redis（无 key），仅 fallback 态有 key，最小化存储占用。
type StablePriorityState struct {
	Mode                string // normal | fallback
	SinceUnixNano       int64  // 进入当前 mode 的时间
	HomeHealthyUnixNano int64  // home 组连续健康起点；0 = 当前不健康
}

// InFallback 是否处于兜底态。
func (s StablePriorityState) InFallback() bool { return s.Mode == StablePriorityModeFallback }

// StablePriorityStateStore 维护每个 home 组的兜底状态机，所有翻转用 Redis Lua CAS
// 保证并发安全（同一时刻只有一个请求能完成 enter/revert，其余幂等）。
type StablePriorityStateStore interface {
	// Get 读取 home 组当前状态；无 key 时返回 normal 零值。
	Get(ctx context.Context, homeGroupID int64) (StablePriorityState, error)
	// TryEnterFallback 仅当当前非 fallback 时 CAS 置 fallback(since=now, healthy_since=0)。
	// 返回 true 表示本次由该调用完成切换；并发下其余调用返回 false（幂等）。
	TryEnterFallback(ctx context.Context, homeGroupID int64, nowUnixNano int64) (bool, error)
	// ObserveHomeHealth 仅在 fallback 态有效：healthy 且原 healthy_since==0 → 置 now；
	// 不 healthy → 清 0。返回更新后的 healthy_since（normal 态/无 key 时返回 0）。
	ObserveHomeHealth(ctx context.Context, homeGroupID int64, healthy bool, nowUnixNano int64) (int64, error)
	// TryRevert 仅当 fallback 且 now-since>=minDwell 且 home 连续健康>=revertStable 时
	// CAS 切回 normal（删 key）。返回 true 表示本次完成切回。条件判定在 Lua 内原子完成。
	TryRevert(ctx context.Context, homeGroupID int64, nowUnixNano, minDwellNanos, revertStableNanos int64) (bool, error)
}
