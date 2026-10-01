package service

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Key 级分组回退链（api_key_group_routes）的领域类型。
//
// 「主分组」仍是 api_keys.group_id，链里只存兜底项。本文件只定义类型、常量、错误与仓储接口；
// PR1 阶段没有任何 handler / 网关 / 计费路径引用这些类型。

// 链项来源。
const (
	// RouteSourceUser 用户自己配置的回退链。
	RouteSourceUser = "user"
	// RouteSourceAdmin 管理员配置的隐藏链，用户端接口一律不得返回。
	RouteSourceAdmin = "admin"
	// RouteSourcePrimary 仅用于有效链里的主分组那一跳，不会落库到 api_key_group_routes。
	RouteSourcePrimary = "primary"
)

// 链项位置。
const (
	// RoutePlacementTail 接在主分组之后。
	RoutePlacementTail = "tail"
	// RoutePlacementHead 排在主分组之前，仅 admin 允许。
	RoutePlacementHead = "head"
)

// usage_logs.served_route_source 的取值（NULL = 主分组/未回退）。
const (
	ServedRouteSourceUserChain  int16 = 1
	ServedRouteSourceAdminChain int16 = 2
)

// 链长上限。
const (
	// MaxUserFallbackItems 用户链最多 5 个兜底项（只统计 source='user' 的行）。
	MaxUserFallbackItems = 5
	// MaxAdminFallbackItemsPerPlacement 管理员 head / tail 各最多 5 项。
	MaxAdminFallbackItemsPerPlacement = 5
	// MaxEffectiveChainLen 有效链（含主分组）的运行时硬上限。
	MaxEffectiveChainLen = 6
	// MaxRouteNoteLen 对应 note VARCHAR(200)。
	MaxRouteNoteLen = 200
)

// 有效链里某一项被跳过的原因。
const (
	RouteSkipGroupMissing    = "group_missing"
	RouteSkipGroupInactive   = "group_inactive"
	RouteSkipInvalidPlatform = "invalid_platform"
	RouteSkipNotAllowed      = "not_allowed"
)

// 服务层错误。文案沿用产品术语（分组、兜底），不提上游 / 账号池。
var (
	ErrFallbackKeyNotGrouped    = infraerrors.BadRequest("FALLBACK_KEY_NOT_GROUPED", "api key is not bound to a group")
	ErrFallbackGroupDup         = infraerrors.BadRequest("FALLBACK_GROUP_DUPLICATE", "duplicate group in fallback chain")
	ErrFallbackGroupIsPrimary   = infraerrors.BadRequest("FALLBACK_GROUP_IS_PRIMARY", "the primary group cannot be a fallback group")
	ErrFallbackChainTooLong     = infraerrors.BadRequest("FALLBACK_CHAIN_TOO_LONG", "fallback chain is too long")
	ErrFallbackPlatformMismatch = infraerrors.BadRequest("FALLBACK_GROUP_PLATFORM_MISMATCH", "fallback group platform must match the primary group")
	ErrFallbackGroupNotAllowed  = infraerrors.Forbidden("FALLBACK_GROUP_NOT_ALLOWED", "user is not allowed to use this group")
	ErrFallbackGroupNotFound    = infraerrors.NotFound("FALLBACK_GROUP_NOT_FOUND", "fallback group not found")
	ErrFallbackGroupUnavailable = infraerrors.NotFound("FALLBACK_GROUP_UNAVAILABLE", "fallback group is unavailable")
	ErrFallbackNoteRequired     = infraerrors.BadRequest("FALLBACK_NOTE_REQUIRED", "note is required when a head route is configured")
	ErrFallbackNoteTooLong      = infraerrors.BadRequest("FALLBACK_NOTE_TOO_LONG", "note is too long")
	// ErrFallbackKeyChanged 保存链期间 Key 的主分组被并发修改，调用方应重新读取后重试。
	ErrFallbackKeyChanged = infraerrors.Conflict("FALLBACK_KEY_CHANGED", "api key group changed, please retry")
)

// RouteItem 是 api_key_group_routes 的一行。
type RouteItem struct {
	ID        int64
	APIKeyID  int64
	GroupID   int64
	Platform  string
	Source    string
	Placement string
	Position  int
	// Note 仅 admin 使用，不对用户展示。
	Note string
	// CreatedBy admin 写入时记录的管理员用户 ID。
	CreatedBy *int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ReplaceRoutesParams 描述「整条替换某个 source 的链」。
type ReplaceRoutesParams struct {
	APIKeyID int64
	// Source 只替换该 source 的行，另一个 source 的行不受影响。
	Source string
	// ExpectedPrimaryGroupID 非 0 时，在锁住 api_keys 行之后核对当前 group_id，
	// 不一致返回 ErrFallbackKeyChanged，避免与「改主分组」并发时写入含主分组的链。
	ExpectedPrimaryGroupID int64
	// Items 要写入的行（APIKeyID / Source 由仓储覆盖）。Position 需从 0 起连续（按 platform/source/placement 分区）。
	Items []RouteItem
}

// APIKeyGroupRouteRepository 是回退链表的持久化接口。
type APIKeyGroupRouteRepository interface {
	// ListByKey 读取某把 Key 的链项；source 为空表示全部。
	// 已软删除的 Key、已软删除的分组不会返回（外键 CASCADE 只对物理删除生效）。
	ListByKey(ctx context.Context, keyID int64, source string) ([]RouteItem, error)
	// ReplaceChain 在单个事务内先 SELECT ... FOR UPDATE 锁住 api_keys 行，再先删后插，
	// 避免两个并发替换互相撞位置唯一约束。Key 不存在或已软删除返回 ErrAPIKeyNotFound。
	ReplaceChain(ctx context.Context, params ReplaceRoutesParams) error
	// ApplyPrimaryGroupChange 在同一事务内处理「Key 主分组被改」：
	//  1. 新主分组已在链里（任意 source）则删除该项；
	//  2. 新主分组换了平台，则清空 user 链；admin 链保留（运行时判为 invalid_platform）。
	// 剩余项的 position 重新压实为从 0 起连续。
	ApplyPrimaryGroupChange(ctx context.Context, keyID, newGroupID int64, newPlatform string) error
}

// ChainHop 是有效链上的一跳。
type ChainHop struct {
	GroupID int64
	Group   *Group
	// RouteSource 为 RouteSourcePrimary / RouteSourceUser / RouteSourceAdmin，
	// 计费落库时据此写 usage_logs.served_route_source。
	RouteSource string
	// Placement 仅 admin 项有意义（head / tail）；主分组与用户项为空或 tail。
	Placement string
}

// ServedRouteSourceValue 返回写入 usage_logs.served_route_source 的取值；主分组返回 nil。
func (h ChainHop) ServedRouteSourceValue() *int16 {
	var v int16
	switch h.RouteSource {
	case RouteSourceUser:
		v = ServedRouteSourceUserChain
	case RouteSourceAdmin:
		v = ServedRouteSourceAdminChain
	default:
		return nil
	}
	return &v
}

// SkippedRoute 记录资格过滤时被跳过的链项，dry-run / 管理端 GET 可见。
type SkippedRoute struct {
	GroupID int64
	Source  string
	Reason  string
}

// Chain 是 ResolveEffectiveChain 的结果。
type Chain struct {
	// Hops 按尝试顺序排列，已完成资格过滤、去重与截断；开关关闭或 Key 未绑分组时为空。
	Hops []ChainHop
	// Skipped 资格过滤时被跳过的项（不含去重丢弃的项）。
	Skipped []SkippedRoute
	// Truncated 为 true 表示去重后超过 MaxEffectiveChainLen 被截断。
	Truncated bool
}

// ResolveOptions 控制 ResolveEffectiveChain。
type ResolveOptions struct {
	// IgnoreSwitch 为 true 时忽略全局开关（管理端 dry-run 用）；运行时路径不得设置。
	IgnoreSwitch bool
}
