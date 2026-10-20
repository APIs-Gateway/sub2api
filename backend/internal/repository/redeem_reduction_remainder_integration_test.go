//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 卡服务到明天（今天剩余不足一天 + 明天整天）：扣 1 天后应服务到今天结束，而不是整卡取消。
// 旧逻辑 int((expires_at−now)/24h) 在这里恒为 1（≤ 1）→ 取消。
func TestRedeemReductionPreservesPartialDay(t *testing.T) {
	today := service.EastDayNumber(time.Now())
	redeem, _, client, sub, userID, codes := newReductionLockFixture(t, today+1, 1, 1)

	_, err := redeem.Redeem(context.Background(), userID, codes[0])
	require.NoError(t, err)

	got, err := client.UserSubscription.Get(context.Background(), sub.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, got.Status)
	require.Equal(t, today, got.ExpireDay)
	require.True(t, service.ExpireDayToExpiresAt(today).Equal(got.ExpiresAt))
	require.True(t, got.ExpiresAt.After(time.Now()))
	require.NotNil(t, got.Notes)
	require.Equal(t, fmt.Sprintf("initial\n通过兑换码 %s 退款扣减 1 天", codes[0]), *got.Notes)

	used, err := NewRedeemCodeRepository(client).GetByCode(context.Background(), codes[0])
	require.NoError(t, err)
	require.Equal(t, service.StatusUsed, used.Status)
}

// 卡只服务到今天：扣 1 天后不再覆盖今天 → 整卡取消（status=expired、expire_day=today−1）。
func TestRedeemReductionCancelsWhenTodayIsNoLongerCovered(t *testing.T) {
	today := service.EastDayNumber(time.Now())
	redeem, _, client, sub, userID, codes := newReductionLockFixture(t, today, 1, 1)

	_, err := redeem.Redeem(context.Background(), userID, codes[0])
	require.NoError(t, err)

	got, err := client.UserSubscription.Get(context.Background(), sub.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusExpired, got.Status)
	require.Equal(t, today-1, got.ExpireDay)
	require.InDelta(t, 0, got.TodayRemaining, 1e-9)
	require.False(t, got.ExpiresAt.After(time.Now()))
}
