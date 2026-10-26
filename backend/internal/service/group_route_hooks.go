package service

import (
	"context"
	"log/slog"
)

// GroupRouteRef 是「某分组被哪条链用到」的反查结果。
type GroupRouteRef struct {
	KeyID     int64
	UserID    int64
	Source    string
	Placement string
}

// APIKeyGroupRouteExtras 是回退链表在 PR1 仓储接口之外的两个查询/清理操作。
// 单独成接口是为了不改动 PR1 已提交的仓储接口（以及依赖它的测试替身）。
type APIKeyGroupRouteExtras interface {
	// ListByGroup 返回把 groupID 放进链里的条目（只含未软删除的 Key）。最多 limit 条。
	ListByGroup(ctx context.Context, groupID int64, limit int) ([]GroupRouteRef, error)
	// DeleteByKey 物理删除某把 Key 的全部链项（Key 被软删除之后调用）。
	DeleteByKey(ctx context.Context, keyID int64) error
}

// groupRouteKeyHooks 是 APIKeyService / AdminService 对回退链的依赖：
// 改主分组与删除 Key 时联动链表。
type groupRouteKeyHooks interface {
	OnPrimaryGroupChanged(ctx context.Context, key *APIKey, newGroup *Group) error
	OnKeyDeleted(ctx context.Context, keyID int64) error
}

// GroupRouteKeyHooks 把 GroupRouteService 与清理操作组合成 Key 生命周期的联动入口。
type GroupRouteKeyHooks struct {
	routes GroupRouteService
	extras APIKeyGroupRouteExtras
}

// NewGroupRouteKeyHooks 创建联动入口。任一依赖为 nil 时对应联动是空操作。
func NewGroupRouteKeyHooks(routes GroupRouteService, extras APIKeyGroupRouteExtras) *GroupRouteKeyHooks {
	return &GroupRouteKeyHooks{routes: routes, extras: extras}
}

// OnPrimaryGroupChanged 在 Key 主分组变更落库之后调用：新主分组已在链里则删掉该项，
// 换了平台则清空用户链（管理员链保留，运行时判为 invalid_platform）。
// 与总开关无关：开关关闭时链仍要保持一致，否则开启后会出现含主分组的脏链。
func (h *GroupRouteKeyHooks) OnPrimaryGroupChanged(ctx context.Context, key *APIKey, newGroup *Group) error {
	if h == nil || h.routes == nil {
		return nil
	}
	return h.routes.OnPrimaryGroupChanged(ctx, key, newGroup)
}

// OnKeyDeleted 在 Key 软删除之后清理它的链项。读取已用 JOIN 过滤软删除的 Key，
// 所以这一步只是回收空间，失败不影响正确性。
func (h *GroupRouteKeyHooks) OnKeyDeleted(ctx context.Context, keyID int64) error {
	if h == nil || h.extras == nil || keyID <= 0 {
		return nil
	}
	return h.extras.DeleteByKey(ctx, keyID)
}

// notifyPrimaryGroupChanged 调用联动；失败只记录日志，不让「改分组」本身失败：
// 分组已经落库，运行时会在资格过滤之后按 group_id 去重，主分组不会重复尝试。
func notifyPrimaryGroupChanged(ctx context.Context, hooks groupRouteKeyHooks, key *APIKey, newGroup *Group) {
	if hooks == nil || key == nil || newGroup == nil {
		return
	}
	if err := hooks.OnPrimaryGroupChanged(ctx, key, newGroup); err != nil {
		slog.Warn("group fallback: apply primary group change failed",
			"api_key_id", key.ID, "group_id", newGroup.ID, "error", err)
	}
}
