package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// NewServedAPIKey 为回退链的某一跳生成「影子 Key」（设计 5.3）。
//
// 与旧的 cloneAPIKeyWithGroup 的区别：
//   - 记录 HomeGroupID：主分组的唯一来源，计费据此写 usage_logs.group_id，不再从 GroupID 反推；
//   - 记录 RouteSource：primary / user / admin，计费据此写 served_route_source；
//   - 清掉 User.UserGroupRPMOverride：快照里的覆盖值属于原分组，不能套用到 served 分组；
//   - 保留订阅信息：影子 User 是浅拷贝，Subscriptions 等字段原样保留。订阅仍按「用户唯一生效卡」结算，
//     调用方继续使用原来的 subscription 变量，不得像旧先例那样置 nil，否则 billing_type 会被记成 balance。
//
// 不修改 src（含 src.User），可以并发调用。hop.Group 为 nil 时原样返回 src。
func NewServedAPIKey(src *service.APIKey, hop service.ChainHop) *service.APIKey {
	if src == nil || hop.Group == nil {
		return src
	}
	cloned := *src
	groupID := hop.Group.ID
	cloned.Group = hop.Group
	cloned.GroupID = &groupID

	// 主分组始终是最原始那一个：对已经是影子的 Key 再套一层时保持不变。
	switch {
	case src.HomeGroupID != nil:
		home := *src.HomeGroupID
		cloned.HomeGroupID = &home
	case src.GroupID != nil:
		home := *src.GroupID
		cloned.HomeGroupID = &home
	default:
		cloned.HomeGroupID = nil
	}
	cloned.RouteSource = hop.RouteSource

	if src.User != nil {
		user := *src.User
		user.UserGroupRPMOverride = nil
		cloned.User = &user
	}
	return &cloned
}

// ApplyServedGroupContext 把 gin 请求 ctx 里的分组换成 served 分组（对应中间件 setGroupContext，但无条件替换）。
// 读取方都会校验 ctx 分组 ID 与目标 groupID 是否一致，不一致就回源查询，所以这是保险而非必需；
// 分组未经仓储加载（!Hydrated 等）时不写入，避免把不完整的分组塞进 ctx。
func ApplyServedGroupContext(c *gin.Context, group *service.Group) {
	if c == nil || c.Request == nil || !service.IsGroupContextValid(group) {
		return
	}
	if existing, ok := c.Request.Context().Value(ctxkey.Group).(*service.Group); ok && existing != nil && existing.ID == group.ID {
		return
	}
	ctx := context.WithValue(c.Request.Context(), ctxkey.Group, group)
	c.Request = c.Request.WithContext(ctx)
}

// ServeHop 是 NewServedAPIKey + ApplyServedGroupContext 的组合：入口每进入一跳调用一次。
func ServeHop(c *gin.Context, src *service.APIKey, hop service.ChainHop) *service.APIKey {
	served := NewServedAPIKey(src, hop)
	if hop.Group != nil {
		ApplyServedGroupContext(c, hop.Group)
	}
	return served
}
