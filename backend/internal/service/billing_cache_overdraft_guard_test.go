//go:build unit

package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type overdraftGuardBalanceCache struct {
	billingCacheMissStub
}

func (c *overdraftGuardBalanceCache) GetUserBalance(context.Context, int64) (float64, error) {
	return 100, nil
}

type overdraftGuardSubRepo struct {
	userSubRepoNoop

	calls atomic.Int64
	err   error
}

func (r *overdraftGuardSubRepo) ListActiveByUserID(context.Context, int64) ([]UserSubscription, error) {
	r.calls.Add(1)
	if r.err != nil {
		return nil, r.err
	}
	now := time.Now()
	one := 1
	return []UserSubscription{{
		ID:               1,
		UserID:           10,
		StartsAt:         now,
		ActivatedAt:      &now,
		GrantedTotalUSD:  600,
		DailyAmountUSD:   60,
		ConsumedUSD:      60,
		DailySpentUSD:    60,
		DailySpentDay:    0,
		MaxOverdraftDays: &one,
	}}, nil
}

func TestBillingCacheService_CheckBalanceEligibility_OverdraftGuardControlsLookup(t *testing.T) {
	subRepo := &overdraftGuardSubRepo{}
	svc := NewBillingCacheService(&overdraftGuardBalanceCache{}, nil, subRepo, nil, nil, nil, &config.Config{}, nil, nil)

	err := svc.checkBalanceEligibility(context.Background(), &User{ID: 10, Role: RoleUser})
	require.NoError(t, err)
	require.Zero(t, subRepo.calls.Load(), "guard=false 时不应查询活跃订阅")

	err = svc.checkBalanceEligibility(context.Background(), &User{ID: 10, Role: RoleUser, SubscriptionOverdraftGuard: true})
	require.NoError(t, err)
	require.Equal(t, int64(1), subRepo.calls.Load(), "guard=true 时才启用订阅透支闸门")
}

func TestBillingCacheService_CheckBalanceEligibility_OverdraftGuardLookupFailOpen(t *testing.T) {
	subRepo := &overdraftGuardSubRepo{err: errors.New("db down")}
	svc := NewBillingCacheService(&overdraftGuardBalanceCache{}, nil, subRepo, nil, nil, nil, &config.Config{}, nil, nil)

	err := svc.checkBalanceEligibility(context.Background(), &User{ID: 10, Role: RoleUser, SubscriptionOverdraftGuard: true})

	require.NoError(t, err)
	require.Equal(t, int64(1), subRepo.calls.Load())
}
