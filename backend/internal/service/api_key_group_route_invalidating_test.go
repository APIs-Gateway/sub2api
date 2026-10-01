//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

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

// BK-4：wire 的提供者返回的就是带失效钩子的包装，注入方拿到的 GroupRouteService 不会绕过失效。
func TestProvideGroupRouteService_ReturnsInvalidatingWrapper(t *testing.T) {
	svc := ProvideGroupRouteService(nil, nil, nil, &APIKeyService{})
	wrapped, ok := svc.(*authCacheInvalidatingGroupRouteService)
	require.True(t, ok, "wire 提供的 GroupRouteService 必须带失效钩子")
	require.NotNil(t, wrapped.GroupRouteService)
	require.NotNil(t, wrapped.invalidator)
}
