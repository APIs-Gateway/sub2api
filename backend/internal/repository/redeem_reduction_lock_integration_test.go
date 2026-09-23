//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// lockedReductionReadRepo lets a test act between the negative redemption's
// unlocked lookup of the user's active card and its locked re-read.
type lockedReductionReadRepo struct {
	service.UserSubscriptionRepository
	afterRead func(*service.UserSubscription)
	lockReads atomic.Int32
}

func (r *lockedReductionReadRepo) GetActiveByUserID(ctx context.Context, userID int64) (*service.UserSubscription, error) {
	sub, err := r.UserSubscriptionRepository.GetActiveByUserID(ctx, userID)
	if err == nil && r.afterRead != nil {
		r.afterRead(sub)
	}
	return sub, err
}

func (r *lockedReductionReadRepo) GetByIDForUpdate(ctx context.Context, id int64) (*service.UserSubscription, error) {
	r.lockReads.Add(1)
	return r.UserSubscriptionRepository.GetByIDForUpdate(ctx, id)
}

// newReductionLockFixture seeds one active per-day card for a fresh user and
// creates `codes` negative subscription redeem codes of -reduceDays each.
func newReductionLockFixture(t *testing.T, expireDay, reduceDays, codes int) (*service.RedeemService, *lockedReductionReadRepo, *dbent.Client, *dbent.UserSubscription, int64, []string) {
	t.Helper()
	ctx := context.Background()
	client := testEntClient(t)
	user, sub, codeIDs := seedAdjustLockSub(t, client, expireDay, "initial")

	repo := &lockedReductionReadRepo{UserSubscriptionRepository: NewUserSubscriptionRepository(client)}
	redeemRepo := NewRedeemCodeRepository(client)
	values := make([]string, codes)
	for i := range values {
		values[i] = fmt.Sprintf("rrl-%x-%d", time.Now().UnixNano(), i)
		code := &service.RedeemCode{
			Code:         values[i],
			Type:         service.RedeemTypeSubscription,
			Status:       service.StatusUnused,
			ValidityDays: -reduceDays,
		}
		require.NoError(t, redeemRepo.Create(ctx, code))
		*codeIDs = append(*codeIDs, code.ID)
	}
	subscriptions := service.NewSubscriptionService(nil, repo, nil, nil, nil, client, nil, nil)
	redeem := service.NewRedeemService(redeemRepo, newUserRepositoryWithSQL(client, integrationDB), subscriptions, nil, nil, client, nil, nil)
	return redeem, repo, client, sub, user.ID, values
}

// 卡只剩今天（expire_day=today）：旧逻辑按锁外读到的行判定「扣 1 天 → 整卡取消」。
// 在判定前另一连接续费 10 天并改备注；锁内重读后应缩短为 today+9，保留续费与备注。
func TestRedeemReductionPreservesConcurrentRenewal(t *testing.T) {
	today := service.EastDayNumber(time.Now())
	redeem, repo, client, sub, userID, codes := newReductionLockFixture(t, today, 1, 1)
	var once sync.Once
	repo.afterRead = func(stale *service.UserSubscription) {
		once.Do(func() {
			bg := context.Background()
			// No ambient tx: commits on another connection before the reduction locks the row.
			_, _, err := repo.UserSubscriptionRepository.GrantSubscriptionDays(bg, stale.ID, 10, time.Time{}, time.Now())
			require.NoError(t, err)
			require.NoError(t, repo.UserSubscriptionRepository.UpdateNotes(bg, stale.ID, "renewal"))
		})
	}

	_, err := redeem.Redeem(context.Background(), userID, codes[0])
	require.NoError(t, err)

	got, err := client.UserSubscription.Get(context.Background(), sub.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, got.Status)
	require.Equal(t, today+9, got.ExpireDay)
	require.True(t, service.ExpireDayToExpiresAt(today+9).Equal(got.ExpiresAt))
	require.NotNil(t, got.Notes)
	require.Equal(t, fmt.Sprintf("renewal\n通过兑换码 %s 退款扣减 1 天", codes[0]), *got.Notes)
	require.EqualValues(t, 1, repo.lockReads.Load())
}

// 两张不同的 -1 天码并发兑换同一张卡（today+10），且都在拿锁前读到同一旧行：
// 两次扣减都必须生效（today+8），两条备注都保留。
func TestRedeemConcurrentReductionsBothApply(t *testing.T) {
	today := service.EastDayNumber(time.Now())
	redeem, repo, client, sub, userID, codes := newReductionLockFixture(t, today+10, 1, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	repo.afterRead = func(*service.UserSubscription) {
		ready <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	results := make(chan error, 2)
	for _, code := range codes {
		go func(code string) {
			_, err := redeem.Redeem(ctx, userID, code)
			results <- err
		}(code)
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("both deductions did not reach the read barrier")
		}
	}
	close(release)
	for range 2 {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("deductions did not finish")
		}
	}

	got, err := client.UserSubscription.Get(context.Background(), sub.ID)
	require.NoError(t, err)
	require.Equal(t, service.SubscriptionStatusActive, got.Status)
	require.Equal(t, today+8, got.ExpireDay)
	require.True(t, service.ExpireDayToExpiresAt(today+8).Equal(got.ExpiresAt))
	require.NotNil(t, got.Notes)
	for _, code := range codes {
		require.Contains(t, *got.Notes, code)
	}
	require.Contains(t, *got.Notes, "initial")
	require.EqualValues(t, 2, repo.lockReads.Load())
}
