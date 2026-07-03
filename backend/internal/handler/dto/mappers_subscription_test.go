package dto

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserSubscriptionFromService_SeparatesCalendarDayFromConsumptionDay(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	now := time.Now().In(loc)
	activatedAt := now.AddDate(0, 0, -2)

	sub := &service.UserSubscription{
		ID:              10,
		UserID:          20,
		GroupID:         30,
		StartsAt:        activatedAt,
		ActivatedAt:     &activatedAt,
		ExpiresAt:       activatedAt.AddDate(0, 0, 30),
		Status:          service.SubscriptionStatusActive,
		GrantedTotalUSD: 300,
		DailyAmountUSD:  10,
		ConsumedUSD:     50,
	}

	got := UserSubscriptionFromService(sub)
	require.NotNil(t, got)
	require.InDelta(t, 5, got.ConsumptionDay, 1e-9)
	require.Equal(t, 2, got.CalendarDay)
}
