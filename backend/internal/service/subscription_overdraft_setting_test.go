//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type overdraftSettingRepoStub struct {
	userSubRepoNoop

	sub        *UserSubscription
	activeSubs []UserSubscription
	listErr    error
	setDays    *int
	setCalls   int
}

func (r *overdraftSettingRepoStub) GetByID(_ context.Context, id int64) (*UserSubscription, error) {
	if r.sub == nil || r.sub.ID != id {
		return nil, ErrSubscriptionNotFound
	}
	cp := *r.sub
	return &cp, nil
}

func (r *overdraftSettingRepoStub) SetOverdraftDays(_ context.Context, userID, subID int64, days *int) (bool, error) {
	if r.sub == nil || r.sub.ID != subID || r.sub.UserID != userID {
		return false, nil
	}
	r.setCalls++
	if days == nil {
		r.setDays = nil
	} else {
		v := *days
		r.setDays = &v
	}
	return true, nil
}

func (r *overdraftSettingRepoStub) ListActiveByUserID(_ context.Context, userID int64) ([]UserSubscription, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := make([]UserSubscription, 0, len(r.activeSubs))
	for _, sub := range r.activeSubs {
		if sub.UserID == userID {
			out = append(out, sub)
		}
	}
	return out, nil
}

type overdraftSettingUserRepoStub struct {
	*mockUserRepo

	guardValues []bool
}

func (r *overdraftSettingUserRepoStub) SetSubscriptionOverdraftGuard(_ context.Context, _ int64, enabled bool) error {
	r.guardValues = append(r.guardValues, enabled)
	return nil
}

func TestSetSubscriptionOverdraftDays_ValueDomain(t *testing.T) {
	userID := int64(10)
	subID := int64(20)
	now := time.Now()

	run := func(t *testing.T, input *int) (*overdraftSettingRepoStub, error) {
		t.Helper()
		repo := &overdraftSettingRepoStub{sub: &UserSubscription{
			ID:          subID,
			UserID:      userID,
			StartsAt:    now,
			ActivatedAt: &now,
			ExpiresAt:   now.AddDate(0, 0, 10),
		}}
		svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
		err := svc.SetSubscriptionOverdraftDays(context.Background(), userID, subID, input)
		return repo, err
	}

	t.Run("nil_disables", func(t *testing.T) {
		repo, err := run(t, nil)
		require.NoError(t, err)
		require.Equal(t, 1, repo.setCalls)
		require.Nil(t, repo.setDays)
	})

	t.Run("negative_disables", func(t *testing.T) {
		v := -1
		repo, err := run(t, &v)
		require.NoError(t, err)
		require.Equal(t, 1, repo.setCalls)
		require.Nil(t, repo.setDays)
	})

	t.Run("zero_disables", func(t *testing.T) {
		v := 0
		repo, err := run(t, &v)
		require.NoError(t, err)
		require.Equal(t, 1, repo.setCalls)
		require.Nil(t, repo.setDays)
	})

	t.Run("one_to_five_enable", func(t *testing.T) {
		for _, value := range []int{1, MaxSubscriptionOverdraftUses} {
			t.Run("value", func(t *testing.T) {
				v := value
				repo, err := run(t, &v)
				require.NoError(t, err)
				require.Equal(t, 1, repo.setCalls)
				require.NotNil(t, repo.setDays)
				require.Equal(t, value, *repo.setDays)
			})
		}
	})

	t.Run("above_limit_rejected_without_write", func(t *testing.T) {
		v := MaxSubscriptionOverdraftUses + 1
		repo, err := run(t, &v)
		require.ErrorIs(t, err, ErrInvalidSubscriptionOverdraftDays)
		require.Zero(t, repo.setCalls)
	})
}

func TestSetSubscriptionOverdraftDays_UpdatesGuardFromRemainingEnabledCards(t *testing.T) {
	userID := int64(10)
	subID := int64(20)
	otherID := int64(21)
	now := time.Now()
	one := 1

	repo := &overdraftSettingRepoStub{sub: &UserSubscription{
		ID:          subID,
		UserID:      userID,
		StartsAt:    now,
		ActivatedAt: &now,
		ExpiresAt:   now.AddDate(0, 0, 10),
	}}
	userRepo := &overdraftSettingUserRepoStub{mockUserRepo: &mockUserRepo{}}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, userRepo, nil, nil, nil, nil, nil)

	err := svc.SetSubscriptionOverdraftDays(context.Background(), userID, subID, &one)
	require.NoError(t, err)
	require.Equal(t, []bool{true}, userRepo.guardValues)

	repo.activeSubs = []UserSubscription{{
		ID:                  otherID,
		UserID:              userID,
		MaxOverdraftDays:    &one,
		TotalOverdraftCount: 0,
	}}
	err = svc.SetSubscriptionOverdraftDays(context.Background(), userID, subID, nil)
	require.NoError(t, err)
	require.Equal(t, []bool{true, true}, userRepo.guardValues, "另一张卡仍开启透支时 guard 保持 true")

	repo.activeSubs = nil
	err = svc.SetSubscriptionOverdraftDays(context.Background(), userID, subID, nil)
	require.NoError(t, err)
	require.Equal(t, []bool{true, true, false}, userRepo.guardValues, "最后一张开启透支的卡关闭后 guard=false")
}

func TestSetSubscriptionOverdraftDays_GuardCheckFailureFailsOpen(t *testing.T) {
	userID := int64(10)
	subID := int64(20)
	now := time.Now()
	repo := &overdraftSettingRepoStub{
		sub: &UserSubscription{
			ID:          subID,
			UserID:      userID,
			StartsAt:    now,
			ActivatedAt: &now,
			ExpiresAt:   now.AddDate(0, 0, 10),
		},
		listErr: errors.New("list active failed"),
	}
	userRepo := &overdraftSettingUserRepoStub{mockUserRepo: &mockUserRepo{}}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, userRepo, nil, nil, nil, nil, nil)

	err := svc.SetSubscriptionOverdraftDays(context.Background(), userID, subID, nil)

	require.NoError(t, err)
	require.Equal(t, []bool{true}, userRepo.guardValues, "无法确认是否还有开启卡时保持 guard=true，避免误放开")
}
