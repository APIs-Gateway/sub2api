//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type recordingInvalidator struct {
	keys []string
}

func (r *recordingInvalidator) InvalidateAuthCacheByKey(_ context.Context, key string) {
	r.keys = append(r.keys, key)
}

// fakeGroupRouteService 嵌入接口，只实现写路径；其余方法被调用会因 nil 接口 panic，说明透传发生了。
type fakeGroupRouteService struct {
	GroupRouteService
	err   error
	calls []string
}

func (f *fakeGroupRouteService) ReplaceUserChain(context.Context, *User, *APIKey, []int64) error {
	f.calls = append(f.calls, "user")
	return f.err
}

func (f *fakeGroupRouteService) ReplaceHiddenChain(context.Context, int64, *APIKey, []int64, []int64, string) error {
	f.calls = append(f.calls, "hidden")
	return f.err
}

func (f *fakeGroupRouteService) OnPrimaryGroupChanged(context.Context, *APIKey, *Group) error {
	f.calls = append(f.calls, "primary")
	return f.err
}

// BK-4：保存链（用户端 / 管理端隐藏链）与主分组变更成功后，必须失效这把 Key 的鉴权缓存，且参数是这把 Key。
func TestAuthCacheInvalidatingGroupRouteService_InvalidatesAfterWrites(t *testing.T) {
	inner := &fakeGroupRouteService{}
	inv := &recordingInvalidator{}
	svc := NewAuthCacheInvalidatingGroupRouteService(inner, inv)
	key := &APIKey{ID: 5, Key: "sk-abc"}

	require.NoError(t, svc.ReplaceUserChain(context.Background(), &User{ID: 1}, key, []int64{2}))
	require.NoError(t, svc.ReplaceHiddenChain(context.Background(), 1, key, []int64{3}, nil, "note"))
	require.NoError(t, svc.OnPrimaryGroupChanged(context.Background(), key, &Group{ID: 9}))

	require.Equal(t, []string{"user", "hidden", "primary"}, inner.calls)
	require.Equal(t, []string{"sk-abc", "sk-abc", "sk-abc"}, inv.keys)
}

// 写入失败时不失效（没有任何变更）；错误原样返回。
func TestAuthCacheInvalidatingGroupRouteService_NoInvalidateOnError(t *testing.T) {
	inner := &fakeGroupRouteService{err: ErrFallbackChainTooLong}
	inv := &recordingInvalidator{}
	svc := NewAuthCacheInvalidatingGroupRouteService(inner, inv)

	err := svc.ReplaceUserChain(context.Background(), &User{ID: 1}, &APIKey{ID: 5, Key: "sk-abc"}, []int64{2})
	require.True(t, errors.Is(err, ErrFallbackChainTooLong))
	require.Empty(t, inv.keys)
}

func TestInvalidateAuthCacheForGroupRoutes_NilSafe(t *testing.T) {
	inv := &recordingInvalidator{}
	require.NotPanics(t, func() {
		InvalidateAuthCacheForGroupRoutes(context.Background(), nil, &APIKey{Key: "k"})
		InvalidateAuthCacheForGroupRoutes(context.Background(), inv, nil)
		InvalidateAuthCacheForGroupRoutes(context.Background(), inv, &APIKey{ID: 1})
	})
	require.Empty(t, inv.keys, "没有 Key 明文时无法失效，空操作")

	// invalidator 为空时不包装。
	inner := &fakeGroupRouteService{}
	require.Same(t, GroupRouteService(inner), NewAuthCacheInvalidatingGroupRouteService(inner, nil))
}

// BK-A：KeyFallbackService（PR4a）写链成功后，鉴权缓存失效收到的就是这把 Key。
// 装饰器由 NewKeyFallbackService 套上，所以这里走真实构造函数，再把依赖换成测试替身；
// 失效器是真实的 *APIKeyService，用缓存替身记录被删掉的缓存 key（Key 明文的 sha256）。
func TestKeyFallbackService_WriteChainInvalidatesAuthCacheOfThatKey(t *testing.T) {
	ctx := context.Background()
	env := newFBEnv(true)
	env.keys.keys[100].Key = "sk-key-100"
	env.keys.keys[101].Key = "sk-key-101"

	cache := &authCacheStub{}
	apiKeySvc := NewAPIKeyService(&authRepoStub{}, nil, nil, nil, nil, cache, &config.Config{
		APIKeyAuth: config.APIKeyAuthCacheConfig{L2TTLSeconds: 60},
	})
	svc := NewKeyFallbackService(apiKeySvc, nil, nil, env.route.svc, env.extras, nil, nil)
	svc.keys, svc.users, svc.groups = env.keys, env.users, env.route.groups
	svc.pricer, svc.settings = env.pricer, routeTestSettings{enabled: true}

	_, isWrapped := svc.routes.(*authCacheInvalidatingGroupRouteService)
	require.True(t, isWrapped, "NewKeyFallbackService 必须给 routes 套上失效装饰器")

	// 用户端保存：失效的是 Key 100。
	_, err := svc.ReplaceUserChain(ctx, 1, 100, []int64{2}, "")
	require.NoError(t, err)
	require.Equal(t, []string{apiKeySvc.authCacheKey("sk-key-100")}, cache.deleteAuthKeys)

	// 管理端隐藏链：失效的是 Key 101，不会碰别的 Key。
	_, err = svc.AdminReplaceHiddenChain(ctx, 9, 101, []int64{5}, nil, "note")
	require.NoError(t, err)
	require.Equal(t, []string{apiKeySvc.authCacheKey("sk-key-100"), apiKeySvc.authCacheKey("sk-key-101")}, cache.deleteAuthKeys)

	// 清空隐藏链同样失效。
	_, err = svc.AdminClearHiddenChain(ctx, 9, 101)
	require.NoError(t, err)
	require.Len(t, cache.deleteAuthKeys, 3)
	require.Equal(t, apiKeySvc.authCacheKey("sk-key-101"), cache.deleteAuthKeys[2])

	// 校验失败（没有任何写入）不失效。
	_, err = svc.ReplaceUserChain(ctx, 1, 100, []int64{1, 1}, "")
	require.Error(t, err)
	require.Len(t, cache.deleteAuthKeys, 3)
}

// keys 为 nil 时不能把 nil 指针装进非 nil 接口再包装（否则写链成功后会在 nil 接收者上失效）。
func TestNewKeyFallbackService_NilKeysDoesNotWrapRoutes(t *testing.T) {
	env := newFBEnv(true)
	svc := NewKeyFallbackService(nil, nil, nil, env.route.svc, nil, nil, nil)
	require.Same(t, GroupRouteService(env.route.svc), svc.routes)
}

// authCacheOrderHooks 在联动被调用的那一刻记下鉴权缓存已经被删了几次。
type authCacheOrderHooks struct {
	cache               *authCacheStub
	deletedBeforeNotify int
	notified            bool
}

func (h *authCacheOrderHooks) OnPrimaryGroupChanged(context.Context, *APIKey, *Group) error {
	h.notified = true
	h.deletedBeforeNotify = len(h.cache.deleteAuthKeys)
	return nil
}

func (h *authCacheOrderHooks) OnKeyDeleted(context.Context, int64) error { return nil }

// BK-A / S-1：APIKeyService.Update 改主分组时，鉴权缓存失效必须发生在回退链联动之后，
// 这样重算出来的 HasGroupRoutes 已经反映联动后的链。
func TestAPIKeyServiceUpdate_InvalidatesAuthCacheAfterPrimaryGroupHook(t *testing.T) {
	cache := &authCacheStub{}
	repo := &apiKeyUpdateRepoStub{apiKeyRepoStub: apiKeyRepoStub{apiKey: &APIKey{ID: 7, UserID: 11, Key: "sk-test", Status: StatusActive, GroupID: int64Ptr(1)}}}
	svc := NewAPIKeyService(repo, &fbUserRepoStub{user: &User{ID: 11}}, &fbGroupRepoStub{group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}}, nil, nil, cache, &config.Config{
		APIKeyAuth: config.APIKeyAuthCacheConfig{L2TTLSeconds: 60},
	})
	hooks := &authCacheOrderHooks{cache: cache}
	svc.groupRouteHooks = hooks

	_, err := svc.Update(context.Background(), 7, 11, UpdateAPIKeyRequest{GroupID: int64Ptr(2)})
	require.NoError(t, err)
	require.True(t, hooks.notified)
	require.Zero(t, hooks.deletedBeforeNotify, "联动被调用时鉴权缓存还不能被失效")
	require.Equal(t, []string{svc.authCacheKey("sk-test")}, cache.deleteAuthKeys, "联动之后必须失效这把 Key 的鉴权缓存")
}
