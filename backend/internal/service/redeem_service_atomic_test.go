//go:build unit

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type atomicRedeemUserRepoStub struct {
	service.UserRepository
	user                *service.User
	atomicBalanceDeltas []float64
	atomicConcurrency   []int
	normalBalanceDeltas []float64
	normalConcurrency   []int
	balanceErr          error
	concurrencyErr      error
}

func (r *atomicRedeemUserRepoStub) GetByID(context.Context, int64) (*service.User, error) {
	copy := *r.user
	return &copy, nil
}

func (r *atomicRedeemUserRepoStub) UpdateBalance(_ context.Context, _ int64, amount float64) error {
	r.normalBalanceDeltas = append(r.normalBalanceDeltas, amount)
	return nil
}

func (r *atomicRedeemUserRepoStub) UpdateConcurrency(_ context.Context, _ int64, delta int) error {
	r.normalConcurrency = append(r.normalConcurrency, delta)
	return nil
}

func (r *atomicRedeemUserRepoStub) ApplyRedeemBalanceAdjustment(_ context.Context, _ int64, delta float64) error {
	r.atomicBalanceDeltas = append(r.atomicBalanceDeltas, delta)
	return r.balanceErr
}

func (r *atomicRedeemUserRepoStub) ApplyRedeemConcurrencyAdjustment(_ context.Context, _ int64, delta int) error {
	r.atomicConcurrency = append(r.atomicConcurrency, delta)
	return r.concurrencyErr
}

type nonAtomicRedeemUserRepoStub struct {
	service.UserRepository
	user *service.User
}

func (r *nonAtomicRedeemUserRepoStub) GetByID(context.Context, int64) (*service.User, error) {
	copy := *r.user
	return &copy, nil
}

type atomicRedeemCodeRepoStub struct {
	service.RedeemCodeRepository
	code *service.RedeemCode
}

func (r *atomicRedeemCodeRepoStub) GetByCode(context.Context, string) (*service.RedeemCode, error) {
	copy := *r.code
	return &copy, nil
}

func (r *atomicRedeemCodeRepoStub) GetByID(context.Context, int64) (*service.RedeemCode, error) {
	copy := *r.code
	return &copy, nil
}

func (r *atomicRedeemCodeRepoStub) Use(context.Context, int64, int64) error {
	r.code.Status = service.StatusUsed
	return nil
}

func TestRedeem_CodeAdjustmentsUseExpectedRepositoryMethod(t *testing.T) {
	tests := []struct {
		name                  string
		codeType              string
		value                 float64
		wantAtomicBalance     []float64
		wantAtomicConcurrency []int
		wantNormalBalance     []float64
		wantNormalConcurrency []int
	}{
		{
			name:              "negative balance",
			codeType:          service.RedeemTypeBalance,
			value:             -7,
			wantAtomicBalance: []float64{-7},
		},
		{
			name:                  "negative concurrency",
			codeType:              service.RedeemTypeConcurrency,
			value:                 -7,
			wantAtomicConcurrency: []int{-7},
		},
		{
			name:              "positive balance",
			codeType:          service.RedeemTypeBalance,
			value:             7,
			wantNormalBalance: []float64{7},
		},
		{
			name:                  "positive concurrency",
			codeType:              service.RedeemTypeConcurrency,
			value:                 7,
			wantNormalConcurrency: []int{7},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client := newRedeemSubscriptionTestEnt(t)
			userRepo := &atomicRedeemUserRepoStub{
				user: &service.User{ID: 1, Balance: 10, Concurrency: 10},
			}
			redeemRepo := &atomicRedeemCodeRepoStub{
				code: &service.RedeemCode{
					ID:     1,
					Code:   "ATOMIC-REDEEM",
					Type:   tt.codeType,
					Value:  tt.value,
					Status: service.StatusUnused,
				},
			}
			svc := service.NewRedeemService(redeemRepo, userRepo, nil, nil, nil, client, nil, nil)

			_, err := svc.Redeem(context.Background(), 1, "ATOMIC-REDEEM")
			require.NoError(t, err)
			require.Equal(t, tt.wantAtomicBalance, userRepo.atomicBalanceDeltas)
			require.Equal(t, tt.wantAtomicConcurrency, userRepo.atomicConcurrency)
			require.Equal(t, tt.wantNormalBalance, userRepo.normalBalanceDeltas)
			require.Equal(t, tt.wantNormalConcurrency, userRepo.normalConcurrency)
		})
	}
}

func TestRedeem_NegativeCodesRequireAtomicUserRepository(t *testing.T) {
	tests := []struct {
		name     string
		codeType string
	}{
		{name: "balance", codeType: service.RedeemTypeBalance},
		{name: "concurrency", codeType: service.RedeemTypeConcurrency},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client := newRedeemSubscriptionTestEnt(t)
			userRepo := &nonAtomicRedeemUserRepoStub{
				user: &service.User{ID: 1, Balance: 10, Concurrency: 10},
			}
			redeemRepo := &atomicRedeemCodeRepoStub{
				code: &service.RedeemCode{
					ID:     1,
					Code:   "ATOMIC-REDEEM",
					Type:   tt.codeType,
					Value:  -7,
					Status: service.StatusUnused,
				},
			}
			svc := service.NewRedeemService(redeemRepo, userRepo, nil, nil, nil, client, nil, nil)

			_, err := svc.Redeem(context.Background(), 1, "ATOMIC-REDEEM")
			require.ErrorContains(t, err, "does not support atomic redeem")
		})
	}
}

func TestRedeem_AtomicAdjustmentErrorsAreReturned(t *testing.T) {
	tests := []struct {
		name             string
		codeType         string
		configureFailure func(*atomicRedeemUserRepoStub, error)
	}{
		{
			name:     "balance",
			codeType: service.RedeemTypeBalance,
			configureFailure: func(repo *atomicRedeemUserRepoStub, err error) {
				repo.balanceErr = err
			},
		},
		{
			name:     "concurrency",
			codeType: service.RedeemTypeConcurrency,
			configureFailure: func(repo *atomicRedeemUserRepoStub, err error) {
				repo.concurrencyErr = err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client := newRedeemSubscriptionTestEnt(t)
			adjustmentErr := errors.New("atomic adjustment failed")
			userRepo := &atomicRedeemUserRepoStub{
				user: &service.User{ID: 1, Balance: 10, Concurrency: 10},
			}
			tt.configureFailure(userRepo, adjustmentErr)
			redeemRepo := &atomicRedeemCodeRepoStub{
				code: &service.RedeemCode{
					ID:     1,
					Code:   "ATOMIC-REDEEM",
					Type:   tt.codeType,
					Value:  -7,
					Status: service.StatusUnused,
				},
			}
			svc := service.NewRedeemService(redeemRepo, userRepo, nil, nil, nil, client, nil, nil)

			_, err := svc.Redeem(context.Background(), 1, "ATOMIC-REDEEM")
			require.ErrorIs(t, err, adjustmentErr)
		})
	}
}
