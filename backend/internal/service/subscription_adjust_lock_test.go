package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// lockingAdjustRepo models the fork's per-day repository semantics for
// subscription adjustments: stale reads (GetActiveByUserID) return the row as
// seen before another writer committed, while GetByIDForUpdate returns the
// current row. Close/Shorten/Grant mirror userSubscriptionRepository exactly
// (expire_day is the source of truth and expires_at is derived from it).
type lockingAdjustRepo struct {
	userSubRepoNoop

	stale   UserSubscription
	current UserSubscription
	lockErr error

	shortenErr error
	grantErr   error
	extendErr  error
	statusErr  error

	lockReads      int
	extendCalls    int
	closeCalls     int
	shortenCalls   int
	grantCalls     int
	lastReduceDays int
	lastGrantDays  int
}

func (r *lockingAdjustRepo) GetActiveByUserID(_ context.Context, userID int64) (*UserSubscription, error) {
	if r.stale.UserID != userID || r.stale.Status != SubscriptionStatusActive {
		return nil, ErrSubscriptionNotFound
	}
	cp := r.stale
	return &cp, nil
}

func (r *lockingAdjustRepo) GetByIDForUpdate(_ context.Context, id int64) (*UserSubscription, error) {
	if r.lockErr != nil {
		return nil, r.lockErr
	}
	r.lockReads++
	if r.current.ID != id {
		return nil, ErrSubscriptionNotFound
	}
	cp := r.current
	return &cp, nil
}

func (r *lockingAdjustRepo) GetByID(_ context.Context, id int64) (*UserSubscription, error) {
	if r.current.ID != id {
		return nil, ErrSubscriptionNotFound
	}
	cp := r.current
	return &cp, nil
}

func (r *lockingAdjustRepo) CloseSubscriptionWithReclaim(_ context.Context, _ int64, now time.Time, deleteRow bool) (int64, float64, error) {
	r.closeCalls++
	if deleteRow {
		return 0, 0, errors.New("unexpected delete in adjustment path")
	}
	r.current.Status = SubscriptionStatusExpired
	r.current.ExpiresAt = now
	r.current.TodayRemaining = 0
	r.current.ExpireDay = EastDayNumber(now) - 1
	return r.current.UserID, 0, nil
}

func (r *lockingAdjustRepo) ShortenSubscriptionWithReclaim(_ context.Context, _ int64, reduceDays int, _, now time.Time) (int64, float64, error) {
	r.shortenCalls++
	if r.shortenErr != nil {
		return 0, 0, r.shortenErr
	}
	r.lastReduceDays = reduceDays
	newExpireDay := r.current.ExpireDay - reduceDays
	if floor := EastDayNumber(now) - 1; newExpireDay < floor {
		newExpireDay = floor
	}
	r.current.ExpireDay = newExpireDay
	r.current.ExpiresAt = ExpireDayToExpiresAt(newExpireDay)
	return r.current.UserID, 0, nil
}

func (r *lockingAdjustRepo) GrantSubscriptionDays(_ context.Context, _ int64, addDays int, _, now time.Time) (int64, float64, error) {
	r.grantCalls++
	if r.grantErr != nil {
		return 0, 0, r.grantErr
	}
	r.lastGrantDays = addDays
	base := r.current.ExpireDay
	if today := EastDayNumber(now); base < today-1 {
		base = today - 1
	}
	r.current.ExpireDay = ClampExpireDay(base + addDays)
	r.current.ExpiresAt = ExpireDayToExpiresAt(r.current.ExpireDay)
	return r.current.UserID, 0, nil
}

func (r *lockingAdjustRepo) ExtendExpiry(_ context.Context, _ int64, newExpiresAt time.Time) error {
	r.extendCalls++
	if r.extendErr != nil {
		return r.extendErr
	}
	r.current.ExpiresAt = newExpiresAt
	return nil
}

func (r *lockingAdjustRepo) UpdateStatus(_ context.Context, _ int64, status string) error {
	if r.statusErr != nil {
		return r.statusErr
	}
	r.current.Status = status
	return nil
}

func (r *lockingAdjustRepo) UpdateNotes(_ context.Context, _ int64, notes string) error {
	r.current.Notes = notes
	return nil
}

func (r *lockingAdjustRepo) writeCalls() int {
	return r.closeCalls + r.shortenCalls + r.grantCalls + r.extendCalls
}

// 2026-09-22 12:00 东八区；today = EastDayNumber(adjustTestNow)。
var adjustTestNow = time.Date(2026, 9, 22, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))

func perDaySub(id, userID int64, expireDay int, status string) UserSubscription {
	return UserSubscription{
		ID:        id,
		UserID:    userID,
		GroupID:   0,
		Status:    status,
		ExpireDay: expireDay,
		ExpiresAt: ExpireDayToExpiresAt(expireDay),
		Notes:     "original",
	}
}

func newAdjustTestService(repo UserSubscriptionRepository) *SubscriptionService {
	svc := NewSubscriptionService(nil, repo, nil, nil, nil, nil, nil, nil)
	svc.now = func() time.Time { return adjustTestNow }
	return svc
}

func TestExtendSubscriptionUsesLockedCurrentRow(t *testing.T) {
	today := EastDayNumber(adjustTestNow)
	repo := &lockingAdjustRepo{current: perDaySub(37, 41, today+9, SubscriptionStatusActive)}
	svc := newAdjustTestService(repo)

	updated, err := svc.ExtendSubscription(context.Background(), 37, 5)

	require.NoError(t, err)
	require.Equal(t, 1, repo.lockReads)
	require.Equal(t, 1, repo.grantCalls)
	require.Equal(t, 5, repo.lastGrantDays)
	require.Equal(t, today+14, updated.ExpireDay)
	require.True(t, ExpireDayToExpiresAt(today+14).Equal(updated.ExpiresAt))
}

// 「不会缩到过期」的校验必须基于锁内读到的最新行：卡剩 today..today+2（3 个自然日）时，
// 缩 2 天后仍服务到今天结束；缩 3 天会让 expires_at 落到今天 0 点（≤ now），必须拒绝且不写库。
func TestExtendSubscriptionShortenValidatesAgainstLockedRow(t *testing.T) {
	today := EastDayNumber(adjustTestNow)
	for _, tc := range []struct {
		name          string
		days          int
		wantErr       error
		wantExpireDay int
	}{
		{name: "shorten to today keeps card", days: -2, wantExpireDay: today},
		{name: "shorten past today rejected", days: -3, wantErr: ErrAdjustWouldExpire, wantExpireDay: today + 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &lockingAdjustRepo{current: perDaySub(37, 41, today+2, SubscriptionStatusActive)}
			svc := newAdjustTestService(repo)

			updated, err := svc.ExtendSubscription(context.Background(), 37, tc.days)

			require.Equal(t, 1, repo.lockReads)
			require.Equal(t, tc.wantExpireDay, repo.current.ExpireDay)
			require.Equal(t, SubscriptionStatusActive, repo.current.Status)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				require.Nil(t, updated)
				require.Zero(t, repo.writeCalls())
				return
			}
			require.NoError(t, err)
			require.Equal(t, 1, repo.shortenCalls)
			require.Equal(t, -tc.days, repo.lastReduceDays)
			require.True(t, ExpireDayToExpiresAt(today).Equal(updated.ExpiresAt))
		})
	}
}

func TestExtendSubscriptionReactivatesLockedExpiredRow(t *testing.T) {
	today := EastDayNumber(adjustTestNow)
	repo := &lockingAdjustRepo{current: perDaySub(37, 41, today-3, SubscriptionStatusExpired)}
	svc := newAdjustTestService(repo)

	updated, err := svc.ExtendSubscription(context.Background(), 37, 5)

	require.NoError(t, err)
	// 已过期卡按续费口径从 today−1 起算：today−1+5 = today+4。
	require.Equal(t, today+4, updated.ExpireDay)
	require.Equal(t, SubscriptionStatusActive, updated.Status)
}

func TestExtendSubscriptionRejectsShorteningLockedExpiredRow(t *testing.T) {
	today := EastDayNumber(adjustTestNow)
	repo := &lockingAdjustRepo{current: perDaySub(37, 41, today-1, SubscriptionStatusActive)}
	svc := newAdjustTestService(repo)

	_, err := svc.ExtendSubscription(context.Background(), 37, -1)

	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot shorten an expired subscription")
	require.Zero(t, repo.writeCalls())
	require.Equal(t, today-1, repo.current.ExpireDay)
}

func TestExtendSubscriptionLockFailureDoesNotWrite(t *testing.T) {
	today := EastDayNumber(adjustTestNow)
	repo := &lockingAdjustRepo{
		current: perDaySub(37, 41, today+9, SubscriptionStatusActive),
		lockErr: errors.New("lock failed"),
	}
	svc := newAdjustTestService(repo)

	_, err := svc.ExtendSubscription(context.Background(), 37, 5)

	require.ErrorIs(t, err, ErrSubscriptionNotFound)
	require.Zero(t, repo.writeCalls())
	require.Equal(t, today+9, repo.current.ExpireDay)
}

func TestExtendSubscriptionZeroDaysOnlySyncsExpiry(t *testing.T) {
	today := EastDayNumber(adjustTestNow)
	repo := &lockingAdjustRepo{current: perDaySub(37, 41, today+9, SubscriptionStatusActive)}
	svc := newAdjustTestService(repo)

	updated, err := svc.ExtendSubscription(context.Background(), 37, 0)

	require.NoError(t, err)
	require.Equal(t, 1, repo.extendCalls)
	require.Zero(t, repo.shortenCalls+repo.grantCalls)
	require.Equal(t, today+9, updated.ExpireDay)
}

func TestExtendSubscriptionClampsDaysAndMaxExpiry(t *testing.T) {
	today := EastDayNumber(adjustTestNow)

	repo := &lockingAdjustRepo{current: perDaySub(37, 41, today+9, SubscriptionStatusActive)}
	svc := newAdjustTestService(repo)
	_, err := svc.ExtendSubscription(context.Background(), 37, MaxValidityDays+100)
	require.NoError(t, err)
	require.Equal(t, MaxValidityDays, repo.lastGrantDays)

	repo = &lockingAdjustRepo{current: perDaySub(37, 41, today+9, SubscriptionStatusActive)}
	svc = newAdjustTestService(repo)
	_, err = svc.ExtendSubscription(context.Background(), 37, -MaxValidityDays-100)
	require.ErrorIs(t, err, ErrAdjustWouldExpire)
	require.Zero(t, repo.writeCalls())
}

func TestExtendSubscriptionPropagatesWriteErrors(t *testing.T) {
	today := EastDayNumber(adjustTestNow)
	boom := errors.New("write failed")
	for _, tc := range []struct {
		name string
		days int
		sub  UserSubscription
		set  func(*lockingAdjustRepo)
	}{
		{name: "shorten", days: -1, sub: perDaySub(37, 41, today+9, SubscriptionStatusActive), set: func(r *lockingAdjustRepo) { r.shortenErr = boom }},
		{name: "grant", days: 1, sub: perDaySub(37, 41, today+9, SubscriptionStatusActive), set: func(r *lockingAdjustRepo) { r.grantErr = boom }},
		{name: "extend expiry", days: 0, sub: perDaySub(37, 41, today+9, SubscriptionStatusActive), set: func(r *lockingAdjustRepo) { r.extendErr = boom }},
		{name: "reactivate", days: 3, sub: perDaySub(37, 41, today-3, SubscriptionStatusExpired), set: func(r *lockingAdjustRepo) { r.statusErr = boom }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &lockingAdjustRepo{current: tc.sub}
			tc.set(repo)
			svc := newAdjustTestService(repo)

			updated, err := svc.ExtendSubscription(context.Background(), 37, tc.days)

			require.ErrorIs(t, err, boom)
			require.Nil(t, updated)
		})
	}
}

func TestSubscriptionServiceCurrentTimeDefaultsToWallClock(t *testing.T) {
	var nilSvc *SubscriptionService
	before := time.Now()
	require.False(t, nilSvc.currentTime().Before(before))
	require.False(t, (&SubscriptionService{}).currentTime().Before(before))
}
