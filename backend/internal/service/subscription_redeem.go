package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// RedeemSubscriptionInput describes a subscription CDK grant.
// DailyAmountUSD is the CDK's D, ValidityDays is its T. CDK grants are full-group
// subscription quota cards and must not create a second active card for a user.
type RedeemSubscriptionInput struct {
	UserID         int64
	DailyAmountUSD float64
	ValidityDays   int
	AssignedBy     int64
	Notes          string
}

// ApplyRedeemSubscription applies a subscription CDK grant.
// If the user has no active card, it creates a new no-group card. If the user
// already has an active card, it merges the grant into that card:
//   - D_new = D_old + D_cdk
//   - days_new = ceil((D_old*days_old + D_cdk*T_cdk) / D_new)
//
// This keeps the single-active-card invariant and avoids invisible second cards
// after redeeming a CDK while subscribed.
func (s *SubscriptionService) ApplyRedeemSubscription(ctx context.Context, input *RedeemSubscriptionInput) (*UserSubscription, bool, error) {
	if input == nil || input.UserID <= 0 {
		return nil, false, infraerrors.BadRequest("INVALID_INPUT", "user is required")
	}
	if input.DailyAmountUSD <= 0 || math.IsNaN(input.DailyAmountUSD) || math.IsInf(input.DailyAmountUSD, 0) {
		return nil, false, infraerrors.BadRequest("INVALID_INPUT", "subscription CDK requires positive daily amount")
	}
	if input.ValidityDays <= 0 {
		return nil, false, infraerrors.BadRequest("INVALID_INPUT", "subscription CDK requires positive validity_days")
	}

	var (
		out      *UserSubscription
		merged   bool
		oldGroup int64
	)
	if err := s.withSubscriptionUpdateTx(ctx, func(txCtx context.Context) error {
		tx := dbent.TxFromContext(txCtx)
		if tx == nil {
			return fmt.Errorf("subscription redeem requires ent transaction")
		}
		client := tx.Client()
		today := TodayEastDayNumber()
		now := time.Now()

		if err := s.lockUserAndPruneStaleForLifecycle(txCtx, input.UserID, today); err != nil {
			return err
		}

		activeQuery := client.UserSubscription.Query().
			Where(
				usersubscription.UserIDEQ(input.UserID),
				usersubscription.StatusEQ(SubscriptionStatusActive),
				usersubscription.DeletedAtIsNil(),
			).
			Order(
				dbent.Desc(usersubscription.FieldExpireDay),
				dbent.Desc(usersubscription.FieldID),
			).
			Limit(1)
		if client.Driver() != nil && client.Driver().Dialect() == dialect.Postgres {
			activeQuery = activeQuery.ForUpdate()
		}
		active, err := activeQuery.Only(txCtx)
		switch {
		case err == nil:
			oldGroup = entGroupIDValue(active.GroupID)
			var mergeErr error
			out, mergeErr = s.mergeRedeemSubscriptionIntoActive(txCtx, active, input, today, now)
			if mergeErr != nil {
				return mergeErr
			}
			merged = true
			return nil
		case dbent.IsNotFound(err):
			created, createErr := s.createSubscription(txCtx, &AssignSubscriptionInput{
				UserID:         input.UserID,
				GroupID:        0,
				ValidityDays:   input.ValidityDays,
				DailyAmountUSD: input.DailyAmountUSD,
				AssignedBy:     input.AssignedBy,
				Notes:          input.Notes,
			})
			if createErr != nil {
				return createErr
			}
			out = created
			return nil
		default:
			return err
		}
	}); err != nil {
		return nil, false, err
	}

	s.clearSubscriptionLockCache(input.UserID)
	s.invalidateUserBalanceCacheAsync(input.UserID)
	s.InvalidateSubCache(input.UserID, 0)
	if oldGroup != 0 {
		s.InvalidateSubCache(input.UserID, oldGroup)
		s.invalidateSubscriptionCacheAsync(input.UserID, oldGroup)
	}
	return out, merged, nil
}

func (s *SubscriptionService) mergeRedeemSubscriptionIntoActive(ctx context.Context, active *dbent.UserSubscription, input *RedeemSubscriptionInput, today int, now time.Time) (*UserSubscription, error) {
	if active == nil {
		return nil, ErrSubscriptionNotFound
	}
	oldD := active.DailyAmountUsd
	if oldD <= 0 || math.IsNaN(oldD) || math.IsInf(oldD, 0) {
		return nil, ErrInvalidDailyAmount
	}

	oldExpireDay := active.ExpireDay
	if oldExpireDay == 0 && !active.ExpiresAt.IsZero() {
		oldExpireDay = ExpiresAtToExpireDay(active.ExpiresAt)
	}
	oldDays := oldExpireDay - today + 1
	if oldDays < 0 {
		oldDays = 0
	}

	newD := oldD + input.DailyAmountUSD
	mergedValue := oldD*float64(oldDays) + input.DailyAmountUSD*float64(input.ValidityDays)
	newDays := int(math.Ceil(mergedValue / newD))
	if newDays < 1 {
		newDays = 1
	}
	newExpireDay := ClampExpireDay(today + newDays - 1)
	newExpiresAt := ExpireDayToExpiresAt(newExpireDay)
	weeklyLimit, monthlyLimit := DeriveWindowCaps(newD, newDays)

	todayRemaining := active.TodayRemaining
	dailySpent := active.DailySpentUsd
	dailySpentDay := active.DailySpentDay
	if active.TodayDay != today {
		todayRemaining = oldD
		dailySpent = 0
		dailySpentDay = today
	}
	todayRemaining += input.DailyAmountUSD
	if todayRemaining > newD {
		todayRemaining = newD
	}

	notes := ""
	if active.Notes != nil {
		notes = strings.TrimSpace(*active.Notes)
	}
	if input.Notes != "" {
		if notes != "" {
			notes += "\n"
		}
		notes += input.Notes
	}

	client := dbent.TxFromContext(ctx).Client()
	updated, err := client.UserSubscription.UpdateOneID(active.ID).
		ClearGroupID().
		SetDailyAmountUsd(newD).
		SetGrantedTotalUsd(active.ConsumedUsd + active.ClawedUsd + newD*float64(newDays)).
		SetDailyLimitUsd(newD).
		SetWeeklyLimitUsd(weeklyLimit).
		SetMonthlyLimitUsd(monthlyLimit).
		SetTodayRemaining(todayRemaining).
		SetTodayDay(today).
		SetDailySpentUsd(dailySpent).
		SetDailySpentDay(dailySpentDay).
		SetExpireDay(newExpireDay).
		SetExpiresAt(newExpiresAt).
		SetStatus(SubscriptionStatusActive).
		SetNotes(notes).
		SetUpdatedAt(now).
		Save(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, ErrSubscriptionNotFound
		}
		return nil, err
	}
	if err := s.bumpUserConcurrencyForSubscription(ctx, active.UserID, newD); err != nil {
		return nil, fmt.Errorf("bump subscription concurrency: %w", err)
	}
	return s.userSubRepo.GetByID(ctx, updated.ID)
}

func resolveRedeemSubscriptionDailyAmount(ctx context.Context, groupRepo GroupRepository, redeemCode *RedeemCode) (float64, error) {
	if redeemCode == nil {
		return 0, infraerrors.BadRequest("REDEEM_CODE_INVALID", "invalid subscription redeem code")
	}
	if redeemCode.Value > 0 && !math.IsNaN(redeemCode.Value) && !math.IsInf(redeemCode.Value, 0) {
		return redeemCode.Value, nil
	}
	if redeemCode.GroupID == nil {
		return 0, infraerrors.BadRequest("REDEEM_CODE_INVALID", "subscription redeem code requires daily amount")
	}
	if groupRepo == nil {
		return 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "group repository unavailable")
	}
	group, err := groupRepo.GetByID(ctx, *redeemCode.GroupID)
	if err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			return 0, infraerrors.BadRequest("REDEEM_CODE_INVALID", "subscription redeem group not found")
		}
		return 0, err
	}
	if group.DailyLimitUSD == nil || *group.DailyLimitUSD <= 0 {
		return 0, ErrInvalidDailyAmount
	}
	return *group.DailyLimitUSD, nil
}
