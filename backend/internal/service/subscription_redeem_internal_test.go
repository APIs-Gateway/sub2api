package service

import (
	"context"
	"errors"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type redeemSubscriptionGroupRepoStub struct {
	groupRepoNoop
	group *Group
	err   error
}

func (s redeemSubscriptionGroupRepoStub) GetByID(context.Context, int64) (*Group, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.group, nil
}

func TestResolveRedeemSubscriptionDailyAmount(t *testing.T) {
	ctx := context.Background()
	daily := 12.5
	groupID := int64(7)

	tests := []struct {
		name    string
		repo    GroupRepository
		code    *RedeemCode
		want    float64
		wantErr string
	}{
		{
			name: "uses cdk value first",
			code: &RedeemCode{Value: 30},
			want: 30,
		},
		{
			name:    "rejects nil code",
			wantErr: "invalid subscription redeem code",
		},
		{
			name:    "rejects missing value and group",
			code:    &RedeemCode{},
			wantErr: "requires daily amount",
		},
		{
			name:    "rejects missing group repository",
			code:    &RedeemCode{GroupID: &groupID},
			wantErr: "group repository unavailable",
		},
		{
			name:    "rejects missing legacy group",
			repo:    redeemSubscriptionGroupRepoStub{err: ErrGroupNotFound},
			code:    &RedeemCode{GroupID: &groupID},
			wantErr: "subscription redeem group not found",
		},
		{
			name:    "returns non not-found repository errors",
			repo:    redeemSubscriptionGroupRepoStub{err: errors.New("database unavailable")},
			code:    &RedeemCode{GroupID: &groupID},
			wantErr: "database unavailable",
		},
		{
			name:    "rejects legacy group without positive daily limit",
			repo:    redeemSubscriptionGroupRepoStub{group: &Group{}},
			code:    &RedeemCode{GroupID: &groupID},
			wantErr: ErrInvalidDailyAmount.Error(),
		},
		{
			name: "falls back to legacy group daily limit",
			repo: redeemSubscriptionGroupRepoStub{group: &Group{DailyLimitUSD: &daily}},
			code: &RedeemCode{GroupID: &groupID},
			want: daily,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveRedeemSubscriptionDailyAmount(ctx, tt.repo, tt.code)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				var coded *infraerrors.Error
				if errors.As(err, &coded) {
					require.NotEmpty(t, coded.Code)
				}
				return
			}
			require.NoError(t, err)
			require.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestMergeRedeemSubscriptionIntoActiveRejectsInvalidActiveCard(t *testing.T) {
	svc := &SubscriptionService{}
	_, err := svc.mergeRedeemSubscriptionIntoActive(context.Background(), nil, &RedeemSubscriptionInput{}, TodayEastDayNumber(), time.Now())
	require.ErrorIs(t, err, ErrSubscriptionNotFound)

	_, err = svc.mergeRedeemSubscriptionIntoActive(context.Background(), &dbent.UserSubscription{DailyAmountUsd: 0}, &RedeemSubscriptionInput{}, TodayEastDayNumber(), time.Now())
	require.ErrorIs(t, err, ErrInvalidDailyAmount)
}

func TestAssignSubscriptionWithReuseRejectsMissingLegacyGroup(t *testing.T) {
	svc := NewSubscriptionService(redeemSubscriptionGroupRepoStub{err: ErrGroupNotFound}, newSubscriptionUserSubRepoStub(), nil, nil, nil, nil, nil, nil)

	sub, reused, err := svc.assignSubscriptionWithReuse(context.Background(), &AssignSubscriptionInput{
		UserID:         1001,
		GroupID:        7,
		DailyAmountUSD: 30,
		ValidityDays:   30,
	})

	require.Error(t, err)
	require.Nil(t, sub)
	require.False(t, reused)
	require.Contains(t, err.Error(), "group not found")
}

func TestSubscriptionProgressReadsNoGroupCards(t *testing.T) {
	repo := newSubscriptionUserSubRepoStub()
	today := TodayEastDayNumber()
	repo.seed(&UserSubscription{
		ID:              77,
		UserID:          1001,
		GroupID:         0,
		Status:          SubscriptionStatusActive,
		DailyAmountUSD:  30,
		DailyLimitUSD:   ptrFloat64(30),
		WeeklyLimitUSD:  ptrFloat64(210),
		MonthlyLimitUSD: ptrFloat64(900),
		GrantedTotalUSD: 900,
		TodayRemaining:  25,
		TodayDay:        today,
		StartDay:        today,
		ExpireDay:       today + 29,
		ExpiresAt:       ExpireDayToExpiresAt(today + 29),
	})
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)

	progress, err := svc.GetSubscriptionProgress(context.Background(), 77)
	require.NoError(t, err)
	require.Equal(t, "All groups", progress.GroupName)
	require.NotNil(t, progress.Burndown)
	require.InDelta(t, 900, progress.Burndown.RemainingUSD, 1e-9)

	list, err := svc.GetUserSubscriptionsWithProgress(context.Background(), 1001)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "All groups", list[0].GroupName)
}

func TestReduceOrCancelSubscriptionUsesUserWideActiveCard(t *testing.T) {
	repo := newSubscriptionUserSubRepoStub()
	repo.seed(&UserSubscription{
		ID:        88,
		UserID:    1001,
		GroupID:   0,
		Status:    SubscriptionStatusActive,
		ExpiresAt: time.Now().Add(48 * time.Hour),
		Notes:     "before",
	})
	subSvc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil, nil, nil, nil)
	redeemSvc := &RedeemService{subscriptionService: subSvc}

	err := redeemSvc.reduceOrCancelSubscription(context.Background(), 1001, 1, "NEG-SUB")
	require.NoError(t, err)

	updated, err := repo.GetByID(context.Background(), 88)
	require.NoError(t, err)
	require.Contains(t, updated.Notes, "before")
	require.Contains(t, updated.Notes, "NEG-SUB")
}
