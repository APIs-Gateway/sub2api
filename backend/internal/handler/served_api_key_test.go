//go:build unit

package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func servedTestKey() *service.APIKey {
	homeID := int64(10)
	override := 7
	return &service.APIKey{
		ID:      1,
		UserID:  2,
		Key:     "sk-test",
		GroupID: &homeID,
		Group:   &service.Group{ID: 10, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true},
		Quota:   5,
		User: &service.User{
			ID:                   2,
			Balance:              3.5,
			UserGroupRPMOverride: &override,
			Subscriptions:        []service.UserSubscription{{ID: 99}},
		},
	}
}

func servedTestHop(id int64, source string) service.ChainHop {
	return service.ChainHop{
		GroupID:     id,
		Group:       &service.Group{ID: id, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true},
		RouteSource: source,
	}
}

func TestNewServedAPIKey_SwapsGroupAndRecordsHome(t *testing.T) {
	src := servedTestKey()
	hop := servedTestHop(20, service.RouteSourceUser)

	served := NewServedAPIKey(src, hop)

	require.NotSame(t, src, served)
	require.Equal(t, hop.Group, served.Group)
	require.NotNil(t, served.GroupID)
	require.EqualValues(t, 20, *served.GroupID)
	require.NotNil(t, served.HomeGroupID)
	require.EqualValues(t, 10, *served.HomeGroupID)
	require.Equal(t, service.RouteSourceUser, served.RouteSource)
	// 其它字段原样保留
	require.Equal(t, src.ID, served.ID)
	require.Equal(t, src.Key, served.Key)
	require.Equal(t, src.Quota, served.Quota)
}

func TestNewServedAPIKey_DoesNotMutateSource(t *testing.T) {
	src := servedTestKey()
	_ = NewServedAPIKey(src, servedTestHop(20, service.RouteSourceAdmin))

	require.EqualValues(t, 10, *src.GroupID)
	require.EqualValues(t, 10, src.Group.ID)
	require.Nil(t, src.HomeGroupID)
	require.Empty(t, src.RouteSource)
	require.NotNil(t, src.User.UserGroupRPMOverride, "原 Key 的用户快照不能被清掉覆盖值")
	require.Equal(t, 7, *src.User.UserGroupRPMOverride)
}

func TestNewServedAPIKey_ClearsRPMOverrideKeepsSubscriptions(t *testing.T) {
	src := servedTestKey()
	served := NewServedAPIKey(src, servedTestHop(20, service.RouteSourceUser))

	require.NotSame(t, src.User, served.User)
	require.Nil(t, served.User.UserGroupRPMOverride)
	require.Equal(t, src.User.ID, served.User.ID)
	require.Equal(t, src.User.Balance, served.User.Balance)
	require.Len(t, served.User.Subscriptions, 1)
	require.EqualValues(t, 99, served.User.Subscriptions[0].ID)
}

func TestNewServedAPIKey_HomeGroupIsStableAcrossNesting(t *testing.T) {
	src := servedTestKey()
	first := NewServedAPIKey(src, servedTestHop(20, service.RouteSourceUser))
	second := NewServedAPIKey(first, servedTestHop(30, service.RouteSourceAdmin))

	require.EqualValues(t, 30, *second.GroupID)
	require.EqualValues(t, 10, *second.HomeGroupID)
	require.Equal(t, service.RouteSourceAdmin, second.RouteSource)

	// HomeGroupID 不与 src 共享指针
	*second.HomeGroupID = 999
	require.EqualValues(t, 10, *first.HomeGroupID)
}

func TestNewServedAPIKey_PrimaryHopKeepsGroup(t *testing.T) {
	src := servedTestKey()
	hop := servedTestHop(10, service.RouteSourcePrimary)
	served := NewServedAPIKey(src, hop)

	require.EqualValues(t, 10, *served.GroupID)
	require.EqualValues(t, 10, *served.HomeGroupID)
	require.Equal(t, service.RouteSourcePrimary, served.RouteSource)
}

func TestNewServedAPIKey_NilSafety(t *testing.T) {
	require.Nil(t, NewServedAPIKey(nil, servedTestHop(20, service.RouteSourceUser)))

	src := servedTestKey()
	require.Same(t, src, NewServedAPIKey(src, service.ChainHop{GroupID: 20}))

	noUser := servedTestKey()
	noUser.User = nil
	served := NewServedAPIKey(noUser, servedTestHop(20, service.RouteSourceUser))
	require.Nil(t, served.User)
	require.EqualValues(t, 20, *served.GroupID)

	noGroup := servedTestKey()
	noGroup.GroupID = nil
	served = NewServedAPIKey(noGroup, servedTestHop(20, service.RouteSourceUser))
	require.Nil(t, served.HomeGroupID)
}

func TestApplyServedGroupContext_ReplacesGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)

	home := servedTestKey().Group
	ApplyServedGroupContext(c, home)
	got, ok := c.Request.Context().Value(ctxkey.Group).(*service.Group)
	require.True(t, ok)
	require.EqualValues(t, 10, got.ID)

	hop := servedTestHop(20, service.RouteSourceUser)
	served := ServeHop(c, servedTestKey(), hop)
	got, ok = c.Request.Context().Value(ctxkey.Group).(*service.Group)
	require.True(t, ok)
	require.EqualValues(t, 20, got.ID)
	require.EqualValues(t, 20, *served.GroupID)
}

func TestApplyServedGroupContext_IgnoresInvalidGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)

	ApplyServedGroupContext(c, servedTestKey().Group)
	// 未 Hydrated 的分组不写入 ctx
	ApplyServedGroupContext(c, &service.Group{ID: 20, Platform: service.PlatformOpenAI, Status: service.StatusActive})
	got, ok := c.Request.Context().Value(ctxkey.Group).(*service.Group)
	require.True(t, ok)
	require.EqualValues(t, 10, got.ID)

	// nil 安全
	ApplyServedGroupContext(nil, nil)
}
