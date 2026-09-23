//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 选号热路径读取分组只需要隐私开关等分组行字段，不能走会聚合账号计数的 GetByID。
func TestSchedulerSnapshotService_GetGroupByIDLite_SkipsAccountCountAggregation(t *testing.T) {
	groupRepo := &mockGroupRepoForGateway{groups: map[int64]*Group{
		7: {ID: 7, Platform: PlatformOpenAI, Status: StatusActive, RequirePrivacySet: true},
	}}
	svc := &SchedulerSnapshotService{groupRepo: groupRepo}

	group, err := svc.GetGroupByIDLite(context.Background(), 7)
	require.NoError(t, err)
	require.NotNil(t, group)
	require.True(t, group.RequirePrivacySet)
	require.Equal(t, 0, groupRepo.getByIDCalls)
	require.Equal(t, 1, groupRepo.getByIDLiteCalls)

	_, err = svc.GetGroupByIDLite(context.Background(), 8)
	require.ErrorIs(t, err, ErrGroupNotFound)
}

func TestSchedulerSnapshotService_GetGroupByIDLite_NilGroupRepo(t *testing.T) {
	svc := &SchedulerSnapshotService{}
	group, err := svc.GetGroupByIDLite(context.Background(), 7)
	require.NoError(t, err)
	require.Nil(t, group)
}
