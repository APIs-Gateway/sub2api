package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 负数兑换码按自然日扣减：扣减后仍覆盖今天（expire_day−N ≥ today）就缩短并保留今天剩余时长，
// 否则整卡取消。旧逻辑按 int((expires_at−now)/24h) 取整，会把「今天剩余 + 整天」中不足一天的
// 部分截掉，例如 12:00 时剩 36h（服务到明天）扣 1 天被误判为取消。
func TestRedeemReductionPreservesRemainingTime(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	noon := time.Date(2026, 9, 22, 12, 0, 0, 0, loc)
	today := EastDayNumber(noon)
	for _, tc := range []struct {
		name          string
		now           time.Time
		expireDay     int // relative to today
		reduceDays    int
		wantCancelled bool
		wantExpireDay int // relative to today
	}{
		// 12:00，服务到明天：剩 36h，扣 1 天 → 服务到今天结束（旧逻辑：int(36/24)=1 ≤ 1 → 取消）。
		{name: "36h left minus 1 day keeps today", now: noon, expireDay: 1, reduceDays: 1, wantExpireDay: 0},
		// 23:59:59，服务到明天：剩 24h+1s，扣 1 天 → 仍保留今天最后 1 秒。
		{name: "24h+1s left minus 1 day keeps today", now: time.Date(2026, 9, 22, 23, 59, 59, 0, loc), expireDay: 1, reduceDays: 1, wantExpireDay: 0},
		// 00:00:01，服务到明天：剩 48h−1s，扣 1 天 → 服务到今天结束（旧逻辑：int(47.99/24)=1 → 取消）。
		{name: "48h-1s left minus 1 day keeps today", now: time.Date(2026, 9, 22, 0, 0, 1, 0, loc), expireDay: 1, reduceDays: 1, wantExpireDay: 0},
		// 12:00，服务到后天：剩 60h，扣 2 天 → 服务到今天结束（旧逻辑：int(60/24)=2 ≤ 2 → 取消）。
		{name: "60h left minus 2 days keeps today", now: noon, expireDay: 2, reduceDays: 2, wantExpireDay: 0},
		// 12:00，服务到 today+3：剩 84h，扣 2 天 → today+1。
		{name: "84h left minus 2 days", now: noon, expireDay: 3, reduceDays: 2, wantExpireDay: 1},
		// 12:00，只服务到今天：剩 12h，扣 1 天 → 取消（expire_day = today−1，expires_at = now）。
		{name: "12h left minus 1 day cancels", now: noon, expireDay: 0, reduceDays: 1, wantCancelled: true, wantExpireDay: -1},
		// 扣减远超剩余 → 取消。
		{name: "excess deduction cancels", now: noon, expireDay: 1, reduceDays: 5, wantCancelled: true, wantExpireDay: -1},
		// status 仍为 active 但已惰性过期（expire_day < today）→ 取消。
		{name: "lazily expired card cancels", now: noon, expireDay: -1, reduceDays: 1, wantCancelled: true, wantExpireDay: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := perDaySub(7, 11, today+tc.expireDay, SubscriptionStatusActive)
			repo := &lockingAdjustRepo{stale: sub, current: sub}
			subscriptions := NewSubscriptionService(nil, repo, nil, nil, nil, nil, nil, nil)
			now := tc.now
			subscriptions.now = func() time.Time { return now }
			svc := &RedeemService{subscriptionService: subscriptions}

			require.NoError(t, svc.reduceOrCancelSubscription(context.Background(), 11, tc.reduceDays, "deduct"))

			require.Equal(t, 1, repo.lockReads)
			require.Equal(t, today+tc.wantExpireDay, repo.current.ExpireDay)
			if tc.wantCancelled {
				require.Equal(t, 1, repo.closeCalls)
				require.Zero(t, repo.shortenCalls)
				require.Equal(t, SubscriptionStatusExpired, repo.current.Status)
				require.True(t, now.Equal(repo.current.ExpiresAt))
			} else {
				require.Zero(t, repo.closeCalls)
				require.Equal(t, 1, repo.shortenCalls)
				require.Equal(t, tc.reduceDays, repo.lastReduceDays)
				require.Equal(t, SubscriptionStatusActive, repo.current.Status)
				require.True(t, ExpireDayToExpiresAt(today+tc.wantExpireDay).Equal(repo.current.ExpiresAt))
				require.True(t, repo.current.ExpiresAt.After(now), "card must still serve the rest of today")
			}
			require.Equal(t, "original\n通过兑换码 deduct 退款扣减 "+itoaDays(tc.reduceDays)+" 天", repo.current.Notes)
		})
	}
}

func itoaDays(n int) string {
	return strconvFormatInt(int64(n))
}
