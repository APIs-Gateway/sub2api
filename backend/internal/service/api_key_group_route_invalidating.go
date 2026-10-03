package service

import (
	"context"
	"log/slog"
)

// APIKeyAuthCacheKeyInvalidator 是 APIKeyAuthCacheInvalidator 里按 Key 失效的那一个方法
// （*APIKeyService 实现了它），回退链只需要这一项。
type APIKeyAuthCacheKeyInvalidator interface {
	InvalidateAuthCacheByKey(ctx context.Context, key string)
}

// InvalidateAuthCacheForGroupRoutes 在回退链变更后失效这把 Key 的鉴权缓存（L1 + L2 + 跨实例通知），
// 让快照里的 HasGroupRoutes 立刻重算（设计 2.5 / 审查 BK-4、N4）。
// 复用现有的「Key 变更后失效鉴权缓存」入口 InvalidateAuthCacheByKey
// （*APIKeyService 实现了它）。必须在链的写入提交之后调用。invalidator 或 key 为空时是空操作。
func InvalidateAuthCacheForGroupRoutes(ctx context.Context, invalidator APIKeyAuthCacheKeyInvalidator, key *APIKey) {
	if invalidator == nil || key == nil {
		return
	}
	if key.Key == "" {
		// 没有 Key 明文就算不出缓存 key，无法失效；调用方应传入完整加载的 Key。
		slog.Warn("group_route.auth_cache_invalidate_skipped_empty_key", "api_key_id", key.ID)
		return
	}
	invalidator.InvalidateAuthCacheByKey(ctx, key.Key)
}

// authCacheInvalidatingGroupRouteService 是 GroupRouteService 写路径上的失效钩子：
// 在 ReplaceUserChain / ReplaceHiddenChain / OnPrimaryGroupChanged 成功之后失效这把 Key 的鉴权缓存。
// 读路径与 ResolveEffectiveChain 原样透传。
//
// 注意：这里靠嵌入接口透传，GroupRouteService 以后新增的写方法如果不在下面显式覆盖，会被静默透传、
// 不触发失效（鉴权快照里的 HasGroupRoutes 就会陈旧）。**新增写方法必须在这里覆盖**（审查 S-2）。
type authCacheInvalidatingGroupRouteService struct {
	GroupRouteService
	invalidator APIKeyAuthCacheKeyInvalidator
}

// NewAuthCacheInvalidatingGroupRouteService 用失效钩子包装 GroupRouteService。invalidator 为 nil 时返回 inner 本身。
//
// 接线：由 NewKeyFallbackService 对自己持有的 routes 包装一次（用户端 PUT、管理端隐藏链的写入都经过它）。
// 不能放在 wire 的 GroupRouteService 提供者里：*APIKeyService 依赖 GroupRouteKeyHooks，后者依赖 GroupRouteService，
// 提供者再依赖 *APIKeyService 就成环（审查 BK-A）。主分组变更走的是 APIKeyService / AdminService 持有的
// 未包装实例，那两处自己在 notifyPrimaryGroupChanged 之后失效鉴权缓存。
// 不要在注入之外直接 NewGroupRouteService 去写链，否则会绕过失效。
// 调用方传入的 key 必须带 Key 明文（GetByID 加载的完整 Key），否则算不出缓存 key，无法失效。
func NewAuthCacheInvalidatingGroupRouteService(inner GroupRouteService, invalidator APIKeyAuthCacheKeyInvalidator) GroupRouteService {
	if inner == nil || invalidator == nil {
		return inner
	}
	return &authCacheInvalidatingGroupRouteService{GroupRouteService: inner, invalidator: invalidator}
}

func (s *authCacheInvalidatingGroupRouteService) ReplaceUserChain(ctx context.Context, user *User, key *APIKey, groupIDs []int64) error {
	err := s.GroupRouteService.ReplaceUserChain(ctx, user, key, groupIDs)
	if err == nil {
		InvalidateAuthCacheForGroupRoutes(ctx, s.invalidator, key)
	}
	return err
}

func (s *authCacheInvalidatingGroupRouteService) ReplaceHiddenChain(ctx context.Context, adminID int64, key *APIKey, head, tail []int64, note string) error {
	err := s.GroupRouteService.ReplaceHiddenChain(ctx, adminID, key, head, tail, note)
	if err == nil {
		InvalidateAuthCacheForGroupRoutes(ctx, s.invalidator, key)
	}
	return err
}

func (s *authCacheInvalidatingGroupRouteService) OnPrimaryGroupChanged(ctx context.Context, key *APIKey, newGroup *Group) error {
	err := s.GroupRouteService.OnPrimaryGroupChanged(ctx, key, newGroup)
	if err == nil {
		InvalidateAuthCacheForGroupRoutes(ctx, s.invalidator, key)
	}
	return err
}

var _ APIKeyAuthCacheKeyInvalidator = (*APIKeyService)(nil)
