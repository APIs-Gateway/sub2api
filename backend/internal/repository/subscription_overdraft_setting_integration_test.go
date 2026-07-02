//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestSubscriptionOverdraftSetting_ValueDomainAndGuardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	userRepo := NewUserRepository(client, integrationDB)
	userSubRepo := NewUserSubscriptionRepository(client)
	svc := service.NewSubscriptionService(nil, userSubRepo, userRepo, nil, nil, nil, nil, nil)

	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("overdraft-setting-%s@example.com", uuid.NewString()),
		Balance: 1000,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:             "overdraft-setting-" + uuid.NewString(),
		Platform:         service.PlatformAnthropic,
		SubscriptionType: service.SubscriptionTypeSubscription,
	})
	now := time.Now()
	cardA := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		GrantedTotalUSD: 500,
		DailyAmountUSD:  100,
		ActivatedAt:     &now,
		ExpiresAt:       now.AddDate(0, 0, 5),
	})
	cardB := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		GrantedTotalUSD: 500,
		DailyAmountUSD:  100,
		ActivatedAt:     &now,
		ExpiresAt:       now.AddDate(0, 0, 5),
	})

	readCardDays := func(id int64) sql.NullInt64 {
		var got sql.NullInt64
		require.NoError(t, integrationDB.QueryRowContext(ctx,
			`SELECT max_overdraft_days FROM user_subscriptions WHERE id=$1`, id).Scan(&got))
		return got
	}
	readGuard := func() bool {
		var got bool
		require.NoError(t, integrationDB.QueryRowContext(ctx,
			`SELECT subscription_overdraft_guard FROM users WHERE id=$1`, user.ID).Scan(&got))
		return got
	}

	aboveLimit := service.MaxSubscriptionOverdraftUses + 1
	require.ErrorIs(t,
		svc.SetSubscriptionOverdraftDays(ctx, user.ID, cardA.ID, &aboveLimit),
		service.ErrInvalidSubscriptionOverdraftDays,
	)
	require.False(t, readCardDays(cardA.ID).Valid, ">5 不应静默写入")
	require.False(t, readGuard(), "拒绝非法值不应开启 guard")

	zero := 0
	require.NoError(t, svc.SetSubscriptionOverdraftDays(ctx, user.ID, cardA.ID, &zero))
	require.False(t, readCardDays(cardA.ID).Valid, "0 与关闭语义一致，应存 NULL")
	require.False(t, readGuard(), "没有任何卡开启透支时 guard=false")

	one := 1
	require.NoError(t, svc.SetSubscriptionOverdraftDays(ctx, user.ID, cardA.ID, &one))
	cardADays := readCardDays(cardA.ID)
	require.True(t, cardADays.Valid)
	require.Equal(t, int64(1), cardADays.Int64)
	require.True(t, readGuard(), "任一卡开启透支时 guard=true")

	five := service.MaxSubscriptionOverdraftUses
	require.NoError(t, svc.SetSubscriptionOverdraftDays(ctx, user.ID, cardB.ID, &five))
	cardBDays := readCardDays(cardB.ID)
	require.True(t, cardBDays.Valid)
	require.Equal(t, int64(service.MaxSubscriptionOverdraftUses), cardBDays.Int64)
	require.True(t, readGuard())

	require.NoError(t, svc.SetSubscriptionOverdraftDays(ctx, user.ID, cardA.ID, &zero))
	require.False(t, readCardDays(cardA.ID).Valid)
	require.True(t, readGuard(), "关闭一张卡后仍有另一张开启，guard 保持 true")

	require.NoError(t, svc.SetSubscriptionOverdraftDays(ctx, user.ID, cardB.ID, nil))
	require.False(t, readCardDays(cardB.ID).Valid)
	require.False(t, readGuard(), "最后一张开启透支的卡关闭后 guard=false")
}
