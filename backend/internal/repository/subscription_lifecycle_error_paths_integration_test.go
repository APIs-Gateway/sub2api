//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 订阅生命周期 service 方法的错误路径（目标卡不存在 / 无生效卡 / 用户不存在 / 天数夹上限）——
// 真实 DB 跑事务分支，补足 happy-path 集成测试未触达的防御性返回。

func TestApplyRenewFromOrder_ErrorPathsPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)

	// 目标卡不存在 → ErrSubscriptionNotFound（事务内 GetByID 落空）。
	_, err := svc.ApplyRenewFromOrder(ctx, 999999, 30)
	require.ErrorIs(t, err, service.ErrSubscriptionNotFound)

	// addDays 超 MaxValidityDays → 夹到上限后照常延长（不报错）。
	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("renew-clamp-%s@example.com", uuid.NewString())})
	group := mustCreateGroup(t, client, &service.Group{Name: "renew-clamp-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	d := 10.0
	w, m := service.DeriveWindowCaps(d, 30)
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  d,
		DailyLimitUSD:   &d,
		WeeklyLimitUSD:  &w,
		MonthlyLimitUSD: &m,
		TodayRemaining:  d,
		TodayDay:        today,
		StartDay:        today - 1,
		ExpireDay:       today + 2,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 2),
		Status:          service.SubscriptionStatusActive,
	})
	got, err := svc.ApplyRenewFromOrder(ctx, card.ID, service.MaxValidityDays+1)
	require.NoError(t, err)
	require.Equal(t, card.ID, got.ID)
}

func TestApplyChangePlanFromOrder_TargetNotFoundPostgres(t *testing.T) {
	ctx := context.Background()
	svc := makeSubscriptionService(t)
	_, err := svc.ApplyChangePlanFromOrder(ctx, 999999, 20, 30)
	require.ErrorIs(t, err, service.ErrSubscriptionNotFound)
}

func TestManualOverdraft_ErrorPathsPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	svc := makeSubscriptionService(t)

	// 用户不存在 → 事务内锁 user 行落空。
	_, err := svc.ManualOverdraft(ctx, 999999)
	require.Error(t, err)

	// 用户存在但无生效卡 → OVERDRAFT_NO_ACTIVE_CARD。
	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("ovd-nocard-%s@example.com", uuid.NewString())})
	_, err = svc.ManualOverdraft(ctx, user.ID)
	require.Equal(t, "OVERDRAFT_NO_ACTIVE_CARD", infraerrors.Reason(err))
}
