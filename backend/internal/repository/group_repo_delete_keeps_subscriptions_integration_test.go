//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 回归：后台删除分组曾把该分组下所有订阅卡一并软删（用户剩余天数直接丢失）。
// 订阅卡归用户所有、可在任意 group 下使用，group_id 只是历史来源快照，
// 删除分组后卡必须原样保留，读取路径要能处理「分组已删、Group 边为 nil」。
func TestGroupRepository_DeleteCascade_KeepsSubscriptionCards(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	entClient := tx.Client()

	target, err := entClient.Group.Create().
		SetName(uniqueTestValue(t, "delete-keeps-cards-target")).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	other, err := entClient.Group.Create().
		SetName(uniqueTestValue(t, "delete-keeps-cards-other")).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	user, err := entClient.User.Create().
		SetEmail(uniqueTestValue(t, "keeps-cards") + "@example.com").
		SetPasswordHash("test-password-hash").
		SetStatus(service.StatusActive).
		SetRole(service.RoleUser).
		Save(ctx)
	require.NoError(t, err)

	now := time.Now()
	newCard := func(groupID *int64, status string, expiresAt time.Time) *dbent.UserSubscription {
		t.Helper()
		create := entClient.UserSubscription.Create().
			SetUserID(user.ID).
			SetStartsAt(now.Add(-time.Hour)).
			SetExpiresAt(expiresAt).
			SetStatus(status).
			SetAssignedAt(now).
			SetNotes("")
		if groupID != nil {
			create.SetGroupID(*groupID)
		}
		card, createErr := create.Save(ctx)
		require.NoError(t, createErr)
		return card
	}

	// 目标分组下：一张生效卡 + 一张已过期卡；另有无分组的自定义卡和别的分组的卡。
	activeCard := newCard(&target.ID, service.SubscriptionStatusActive, now.Add(30*24*time.Hour))
	expiredCard := newCard(&target.ID, service.SubscriptionStatusExpired, now.Add(-24*time.Hour))
	customCard := newCard(nil, service.SubscriptionStatusActive, now.Add(10*24*time.Hour))
	otherCard := newCard(&other.ID, service.SubscriptionStatusActive, now.Add(5*24*time.Hour))
	allIDs := []int64{activeCard.ID, expiredCard.ID, customCard.ID, otherCard.ID}

	groupRepo := newGroupRepositoryWithSQL(entClient, tx)
	subRepo := NewUserSubscriptionRepository(entClient)

	affectedUserIDs, err := groupRepo.DeleteCascade(ctx, target.ID)
	require.NoError(t, err)
	require.Empty(t, affectedUserIDs, "订阅卡未被改动，不应有需要失效订阅缓存的用户")

	// 分组本身已软删。
	_, err = groupRepo.GetByID(ctx, target.ID)
	require.ErrorIs(t, err, service.ErrGroupNotFound)

	// 四张卡都还在：默认查询会过滤 deleted_at，数量不变即没有被软删。
	remaining, err := entClient.UserSubscription.Query().Where(usersubscription.IDIn(allIDs...)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, len(allIDs), remaining)
	var deletedRows int
	require.NoError(t, scanSingleRow(ctx, entClient,
		"SELECT COUNT(*) FROM user_subscriptions WHERE user_id = $1 AND deleted_at IS NOT NULL",
		[]any{user.ID}, &deletedRows))
	require.Zero(t, deletedRows)

	// 卡按 ID 读取：group_id 保持原值，Group 边为 nil。
	got, err := subRepo.GetByID(ctx, activeCard.ID)
	require.NoError(t, err)
	require.Equal(t, target.ID, got.GroupID)
	require.Nil(t, got.Group)
	require.Equal(t, service.SubscriptionStatusActive, got.Status)
	require.WithinDuration(t, activeCard.ExpiresAt, got.ExpiresAt, time.Second, "剩余天数不应变化")

	// 认证 / 计费入口取用户唯一生效卡：不按分组匹配，分组已删也能取到。
	active, err := subRepo.GetActiveByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, activeCard.ID, active.ID)
	require.Nil(t, active.Group)

	// 续费入口取最近一张 status=active 的卡。
	latest, err := subRepo.GetLatestActiveStatusByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, activeCard.ID, latest.ID)
	require.Equal(t, target.ID, latest.GroupID)
	require.Nil(t, latest.Group)

	// 用户端订阅列表：生效卡都在，目标分组的卡 Group 为 nil，其他分组的卡不受影响。
	activeList, err := subRepo.ListActiveByUserID(ctx, user.ID)
	require.NoError(t, err)
	byID := make(map[int64]service.UserSubscription, len(activeList))
	for _, sub := range activeList {
		byID[sub.ID] = sub
	}
	require.Len(t, byID, 3)
	require.Contains(t, byID, activeCard.ID)
	require.Contains(t, byID, customCard.ID)
	require.Contains(t, byID, otherCard.ID)
	require.Nil(t, byID[activeCard.ID].Group)
	require.NotNil(t, byID[otherCard.ID].Group)
	require.Equal(t, other.ID, byID[otherCard.ID].Group.ID)

	// 后台订阅列表（按用户）：四张卡都在，包括已过期的那张。
	adminList, _, err := subRepo.List(ctx, pagination.PaginationParams{Page: 1, PageSize: 20}, &user.ID, nil, "", "", "", "")
	require.NoError(t, err)
	require.Len(t, adminList, len(allIDs))
	for _, sub := range adminList {
		if sub.ID == activeCard.ID || sub.ID == expiredCard.ID {
			require.Equal(t, target.ID, sub.GroupID)
			require.Nil(t, sub.Group)
		}
	}

	// 记账不依赖来源分组是否还在。
	require.NoError(t, subRepo.IncrementUsage(ctx, activeCard.ID, 1.5))
	billed, err := subRepo.GetByID(ctx, activeCard.ID)
	require.NoError(t, err)
	require.InDelta(t, 1.5, billed.DailyUsageUSD, 1e-6)
}
