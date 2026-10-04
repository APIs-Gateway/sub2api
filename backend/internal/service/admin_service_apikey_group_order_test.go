//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 组装审查 S-2 / 4a 复审 BK-1：管理员改 Key 主分组时，回退链联动必须发生在鉴权缓存失效之前，
// 否则缓存重算出来的 HasGroupRoutes 还是联动之前的旧链。既有测试分别只看「联动被调用」和「缓存被失效」，
// 看不出两者的先后；这里把两个动作写进同一条事件序列，直接断言顺序。

// groupOrderEvents 是联动与缓存失效共用的事件日志。
type groupOrderEvents struct{ log []string }

type groupOrderHooksStub struct{ events *groupOrderEvents }

func (h *groupOrderHooksStub) OnPrimaryGroupChanged(context.Context, *APIKey, *Group) error {
	h.events.log = append(h.events.log, "hook")
	return nil
}

func (h *groupOrderHooksStub) OnKeyDeleted(context.Context, int64) error { return nil }

type groupOrderInvalidatorStub struct{ events *groupOrderEvents }

func (s *groupOrderInvalidatorStub) InvalidateAuthCacheByKey(_ context.Context, key string) {
	s.events.log = append(s.events.log, "invalidate:"+key)
}
func (s *groupOrderInvalidatorStub) InvalidateAuthCacheByUserID(context.Context, int64)  {}
func (s *groupOrderInvalidatorStub) InvalidateAuthCacheByGroupID(context.Context, int64) {}

func TestAdminUpdateAPIKeyGroupID_NotifiesFallbackHookBeforeInvalidatingAuthCache(t *testing.T) {
	cases := []struct {
		name      string
		exclusive bool
	}{
		{name: "non_exclusive_group", exclusive: false},
		{name: "exclusive_group", exclusive: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := &groupOrderEvents{}
			repo := &apiKeyRepoStubForGroupUpdate{key: &APIKey{ID: 7, UserID: 11, Key: "sk-order", GroupID: int64Ptr(1)}}
			groups := &groupRepoStubForGroupUpdate{group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive, IsExclusive: tc.exclusive}}
			svc := &adminServiceImpl{
				apiKeyRepo:           repo,
				groupRepo:            groups,
				userRepo:             &userRepoStubForGroupUpdate{},
				groupRouteHooks:      &groupOrderHooksStub{events: events},
				authCacheInvalidator: &groupOrderInvalidatorStub{events: events},
			}

			_, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 7, int64Ptr(2))
			require.NoError(t, err)
			require.Equal(t, []string{"hook", "invalidate:sk-order"}, events.log,
				"先联动回退链、再失效鉴权缓存；顺序反了缓存会固化联动之前的 HasGroupRoutes")
		})
	}
}

func TestAdminUpdateAPIKeyGroupID_SamePrimaryGroupSkipsHookButStillInvalidatesAuthCache(t *testing.T) {
	events := &groupOrderEvents{}
	repo := &apiKeyRepoStubForGroupUpdate{key: &APIKey{ID: 7, UserID: 11, Key: "sk-order", GroupID: int64Ptr(2)}}
	groups := &groupRepoStubForGroupUpdate{group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}}
	svc := &adminServiceImpl{
		apiKeyRepo:           repo,
		groupRepo:            groups,
		groupRouteHooks:      &groupOrderHooksStub{events: events},
		authCacheInvalidator: &groupOrderInvalidatorStub{events: events},
	}

	_, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 7, int64Ptr(2))
	require.NoError(t, err)
	require.Equal(t, []string{"invalidate:sk-order"}, events.log, "主分组没变：不联动，缓存照旧失效")
}
