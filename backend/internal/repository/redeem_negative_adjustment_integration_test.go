//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type synchronizedRedeemUserRepo struct {
	service.UserRepository
	delegate *userRepository
	arrived  chan<- struct{}
	release  <-chan struct{}
}

func (r *synchronizedRedeemUserRepo) ApplyRedeemBalanceAdjustment(ctx context.Context, id int64, delta float64) error {
	r.arrived <- struct{}{}
	<-r.release
	return r.delegate.ApplyRedeemBalanceAdjustment(ctx, id, delta)
}

func (r *synchronizedRedeemUserRepo) ApplyRedeemConcurrencyAdjustment(ctx context.Context, id int64, delta int) error {
	r.arrived <- struct{}{}
	<-r.release
	return r.delegate.ApplyRedeemConcurrencyAdjustment(ctx, id, delta)
}

type failingRedeemUserRepo struct {
	service.UserRepository
	err error
}

func (r *failingRedeemUserRepo) ApplyRedeemBalanceAdjustment(context.Context, int64, float64) error {
	return r.err
}

func (r *failingRedeemUserRepo) ApplyRedeemConcurrencyAdjustment(context.Context, int64, int) error {
	return r.err
}

func TestRedeemService_NegativeAdjustmentsFloorAtZeroConcurrently(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		codeType string
	}{
		{name: "balance", codeType: service.RedeemTypeBalance},
		{name: "concurrency", codeType: service.RedeemTypeConcurrency},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := testEntClient(t)
			delegate := newUserRepositoryWithSQL(client, integrationDB)
			arrived := make(chan struct{}, 2)
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseBoth := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseBoth()

			userRepo := &synchronizedRedeemUserRepo{
				UserRepository: delegate,
				delegate:       delegate,
				arrived:        arrived,
				release:        release,
			}
			redeemRepo := NewRedeemCodeRepository(client)
			svc := service.NewRedeemService(redeemRepo, userRepo, nil, nil, nil, client, nil, nil)

			user, err := client.User.Create().
				SetEmail(fmt.Sprintf("redeem-floor-%s-%d@test.com", tt.name, time.Now().UnixNano())).
				SetPasswordHash("hash").
				SetRole(service.RoleUser).
				SetStatus(service.StatusActive).
				SetBalance(10).
				SetConcurrency(10).
				Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { _ = client.User.DeleteOneID(user.ID).Exec(ctx) })

			codes := make([]*service.RedeemCode, 2)
			for i := range codes {
				codes[i] = &service.RedeemCode{
					Code:   fmt.Sprintf("rf%s-%x-%d", tt.name[:1], time.Now().UnixNano(), i),
					Type:   tt.codeType,
					Value:  -7,
					Status: service.StatusUnused,
				}
				require.NoError(t, redeemRepo.Create(ctx, codes[i]))
			}
			t.Cleanup(func() {
				for _, code := range codes {
					_ = client.RedeemCode.DeleteOneID(code.ID).Exec(ctx)
				}
			})

			errs := make(chan error, len(codes))
			var wg sync.WaitGroup
			for _, code := range codes {
				wg.Add(1)
				go func(code string) {
					defer wg.Done()
					_, err := svc.Redeem(ctx, user.ID, code)
					errs <- err
				}(code.Code)
			}

			for range codes {
				select {
				case <-arrived:
				case <-time.After(10 * time.Second):
					t.Fatal("redeem attempts did not both reach the atomic adjustment")
				}
			}
			releaseBoth()
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}

			updated, err := delegate.GetByID(ctx, user.ID)
			require.NoError(t, err)
			if tt.codeType == service.RedeemTypeBalance {
				require.Equal(t, float64(0), updated.Balance)
				require.Equal(t, 10, updated.Concurrency)
			} else {
				require.Equal(t, 0, updated.Concurrency)
				require.Equal(t, float64(10), updated.Balance)
			}
			for _, code := range codes {
				used, err := redeemRepo.GetByID(ctx, code.ID)
				require.NoError(t, err)
				require.Equal(t, service.StatusUsed, used.Status)
			}
		})
	}
}

func TestRedeemService_NegativeAdjustmentFailureRollsBackCodeUse(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	delegate := newUserRepositoryWithSQL(client, integrationDB)
	redeemRepo := NewRedeemCodeRepository(client)

	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("redeem-floor-rollback-%d@test.com", time.Now().UnixNano())).
		SetPasswordHash("hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		SetBalance(10).
		SetConcurrency(10).
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.User.DeleteOneID(user.ID).Exec(ctx) })

	code := &service.RedeemCode{
		Code:   fmt.Sprintf("rf-r-%x", time.Now().UnixNano()),
		Type:   service.RedeemTypeBalance,
		Value:  -7,
		Status: service.StatusUnused,
	}
	require.NoError(t, redeemRepo.Create(ctx, code))
	t.Cleanup(func() { _ = client.RedeemCode.DeleteOneID(code.ID).Exec(ctx) })

	adjustmentErr := errors.New("forced atomic adjustment failure")
	svc := service.NewRedeemService(
		redeemRepo,
		&failingRedeemUserRepo{UserRepository: delegate, err: adjustmentErr},
		nil,
		nil,
		nil,
		client,
		nil,
		nil,
	)

	_, err = svc.Redeem(ctx, user.ID, code.Code)
	require.ErrorIs(t, err, adjustmentErr)

	stored, err := redeemRepo.GetByID(ctx, code.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusUnused, stored.Status)
}
