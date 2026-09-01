//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// groupPlatformRepoStub 只实现 UpdateGroup 走到的两个方法，其余靠内嵌接口占位。
type groupPlatformRepoStub struct {
	GroupRepository
	group   *Group
	updated *Group
}

func (r *groupPlatformRepoStub) GetByID(_ context.Context, _ int64) (*Group, error) {
	cloned := *r.group
	return &cloned, nil
}

func (r *groupPlatformRepoStub) Update(_ context.Context, group *Group) error {
	r.updated = group
	return nil
}

type channelCacheInvalidatorSpy struct {
	calls int
}

func (s *channelCacheInvalidatorSpy) InvalidateCache() { s.calls++ }

// 渠道缓存持有 groupID → platform，而渠道定价/模型映射/模型白名单都按平台严格隔离。
// 改了分组平台却不失效缓存，最长 10 分钟内这些查找仍按旧平台匹配（静默走错价）。
func TestUpdateGroupInvalidatesChannelCacheOnPlatformChange(t *testing.T) {
	tests := []struct {
		name          string
		fromPlatform  string
		inputPlatform string
		wantCalls     int
	}{
		{
			name:          "platform changed invalidates",
			fromPlatform:  PlatformAnthropic,
			inputPlatform: PlatformOpenAI,
			wantCalls:     1,
		},
		{
			name:          "same platform does not invalidate",
			fromPlatform:  PlatformAnthropic,
			inputPlatform: PlatformAnthropic,
			wantCalls:     0,
		},
		{
			// 请求里不带 platform 字段时不应该动缓存
			name:          "platform omitted does not invalidate",
			fromPlatform:  PlatformAnthropic,
			inputPlatform: "",
			wantCalls:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &groupPlatformRepoStub{group: &Group{ID: 7, Name: "g", Platform: tt.fromPlatform}}
			spy := &channelCacheInvalidatorSpy{}
			svc := &adminServiceImpl{groupRepo: repo, channelCacheInvalidator: spy}

			got, err := svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{Platform: tt.inputPlatform})
			require.NoError(t, err)
			require.NotNil(t, got)
			require.Equal(t, tt.wantCalls, spy.calls)
		})
	}
}

// 依赖可以不注入（例如测试或裁剪构建），此时不应 panic——缓存靠 TTL 自然重建。
func TestUpdateGroupWithoutChannelCacheInvalidator(t *testing.T) {
	repo := &groupPlatformRepoStub{group: &Group{ID: 7, Name: "g", Platform: PlatformAnthropic}}
	svc := &adminServiceImpl{groupRepo: repo}

	got, err := svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{Platform: PlatformOpenAI})
	require.NoError(t, err)
	require.Equal(t, PlatformOpenAI, got.Platform)
}

// AttachChannelCacheInvalidator 必须把依赖正确注入到已构造的 AdminService 实例上，
// 且不影响 NewAdminService 固定参数列表的调用点兼容性。
func TestAttachChannelCacheInvalidator(t *testing.T) {
	spy := &channelCacheInvalidatorSpy{}
	svc := NewAdminService(
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	AttachChannelCacheInvalidator(svc, spy)

	impl, ok := svc.(*adminServiceImpl)
	require.True(t, ok)
	require.NotNil(t, impl.channelCacheInvalidator)

	impl.channelCacheInvalidator.InvalidateCache()
	require.Equal(t, 1, spy.calls)
}

// 不调用 AttachChannelCacheInvalidator 时（现有调用点的形态）channelCacheInvalidator
// 必须保持 nil，不能意外拿到非 nil 默认值。
func TestNewAdminServiceLeavesChannelCacheInvalidatorNilByDefault(t *testing.T) {
	svc := NewAdminService(
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)

	impl, ok := svc.(*adminServiceImpl)
	require.True(t, ok)
	require.Nil(t, impl.channelCacheInvalidator)
}
