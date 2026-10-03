package repository

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

var _ service.BillingInflightRepository = (*usageBillingRepository)(nil)

// Every operation locks user before lease rows. This serializes admission with
// the existing user -> active card settlement/purchase/renewal lock order.
func lockBillingInflightUser(ctx context.Context, tx *sql.Tx, userID int64) (float64, error) {
	var balance float64
	err := tx.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, userID).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, service.ErrUserNotFound
	}
	return balance, err
}

func (r *usageBillingRepository) inflightTx(ctx context.Context, userID int64, fn func(*sql.Tx, float64) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	balance, err := lockBillingInflightUser(ctx, tx, userID)
	if err != nil {
		return err
	}
	if err = fn(tx, balance); err != nil {
		return err
	}
	return tx.Commit()
}

func billingInflightCapacity(ctx context.Context, tx *sql.Tx, userID int64, balance float64) (float64, bool, error) {
	var card service.SubWindow
	var dl, wl, ml sql.NullFloat64
	var dw, ww, mw sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT daily_limit_usd,weekly_limit_usd,monthly_limit_usd,
      daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start,expires_at,status
      FROM user_subscriptions WHERE user_id=$1 AND status='active' AND deleted_at IS NULL
      ORDER BY expires_at DESC,id DESC LIMIT 1 FOR UPDATE`, userID).Scan(&dl, &wl, &ml, &card.DailyUsageUSD, &card.WeeklyUsageUSD, &card.MonthlyUsageUSD, &dw, &ww, &mw, &card.ExpiresAt, &card.Status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	var c *service.SubWindow
	if err == nil {
		card.DailyLimitUSD = nullFloatZero(dl)
		card.WeeklyLimitUSD = nullFloatZero(wl)
		card.MonthlyLimitUSD = nullFloatZero(ml)
		card.DailyWindowStart = nullTimePtr(dw)
		card.WeeklyWindowStart = nullTimePtr(ww)
		card.MonthlyWindowStart = nullTimePtr(mw)
		c = &card
	}
	now := time.Now() // obtain current time after both locks, including midnight waits
	allowed := service.AdmitWindow(c, &service.WalletState{Balance: balance}, now)
	if !allowed && c != nil && now.Before(c.ExpiresAt) {
		switch c.ExceededLimitCode() {
		case "DAILY":
			return 0, false, service.ErrDailyLimitExceeded
		case "WEEKLY":
			return 0, false, service.ErrWeeklyLimitExceeded
		case "MONTHLY":
			return 0, false, service.ErrMonthlyLimitExceeded
		}
	}
	capacity := math.Max(balance, 0)
	if c != nil && c.Status == service.SubscriptionStatusActive && now.Before(c.ExpiresAt) {
		capacity += c.SubRemaining()
	}
	return capacity, allowed, nil
}

func (r *usageBillingRepository) ReserveBillingInflight(ctx context.Context, userID int64, id string, amount float64, exclusive bool, ttl time.Duration) (bool, error) {
	return r.reserveBillingInflight(ctx, userID, id, id+":initial", amount, exclusive, ttl, false)
}

func (r *usageBillingRepository) ResizeBillingInflight(ctx context.Context, userID int64, id, attemptID string, amount float64, exclusive bool, ttl time.Duration) (bool, error) {
	return r.reserveBillingInflight(ctx, userID, id, attemptID, amount, exclusive, ttl, true)
}

func (r *usageBillingRepository) reserveBillingInflight(ctx context.Context, userID int64, id, attemptID string, amount float64, exclusive bool, ttl time.Duration, resize bool) (bool, error) {
	if id == "" || amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) || ttl <= 0 {
		return false, service.ErrBillingInflightIdentity
	}
	allowed := false
	err := r.inflightTx(ctx, userID, func(tx *sql.Tx, balance float64) error {
		if resize {
			var live bool
			err := tx.QueryRowContext(ctx, `SELECT expires_at>clock_timestamp() FROM billing_inflight_leases WHERE user_id=$1 AND id=$2 AND phase='owner'`, userID, id).Scan(&live)
			if errors.Is(err, sql.ErrNoRows) || err == nil && !live {
				return nil
			}
			if err != nil {
				return err
			}
		}
		capacity, admitted, err := billingInflightCapacity(ctx, tx, userID, balance)
		if err != nil {
			return err
		}
		if !admitted {
			return nil
		}
		// TTL is intentionally bounded/fail-open after an owner dies or loses renewal.
		if _, err = tx.ExecContext(ctx, `DELETE FROM billing_inflight_leases WHERE user_id=$1 AND expires_at<=clock_timestamp()`, userID); err != nil {
			return err
		}
		var others int
		var held, ownPending float64
		var otherExclusive bool
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(amount),0),COALESCE(BOOL_OR(exclusive),false)
        FROM billing_inflight_leases WHERE user_id=$1 AND phase IN ('attempt','pending') AND (amount>0 OR exclusive) AND owner_id<>$2`, userID, id).Scan(&others, &held, &otherExclusive)
		if err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount),0) FROM billing_inflight_leases WHERE user_id=$1 AND owner_id=$2 AND phase IN ('attempt','pending')`, userID, id).Scan(&ownPending)
		if err != nil {
			return err
		}
		// Preserve the historical first request, including estimates above funding.
		// Unknown pricing uses an exclusive hold instead of a zero-cost bypass.
		if (amount > 0 || exclusive) && others > 0 && (exclusive || otherExclusive || held+ownPending+amount > capacity) {
			return nil
		}
		var res sql.Result
		if resize {
			res, err = tx.ExecContext(ctx, `UPDATE billing_inflight_leases SET expires_at=clock_timestamp()+$3*interval '1 second'
          WHERE id=$1 AND user_id=$2 AND phase='owner' AND expires_at>clock_timestamp()`, id, userID, ttl.Seconds())
		} else {
			res, err = tx.ExecContext(ctx, `INSERT INTO billing_inflight_leases(id,user_id,phase,amount,expires_at)
          VALUES($1,$2,'owner',0,clock_timestamp()+$3*interval '1 second') ON CONFLICT(id) DO NOTHING`, id, userID, ttl.Seconds())
		}
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n != 1 {
			return err
		}
		res, err = tx.ExecContext(ctx, `INSERT INTO billing_inflight_leases(id,user_id,owner_id,phase,amount,estimate,exclusive,expires_at)
        VALUES($1,$2,$3,'attempt',$4,$4,$5,clock_timestamp()+$6*interval '1 second') ON CONFLICT(id) DO NOTHING`, attemptID, userID, id, amount, exclusive, ttl.Seconds())
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		allowed = n == 1
		return err
	})
	return allowed, err
}

func (r *usageBillingRepository) RenewBillingInflight(ctx context.Context, userID int64, id string, ttl time.Duration) (bool, error) {
	alive := false
	err := r.inflightTx(ctx, userID, func(tx *sql.Tx, _ float64) error {
		res, err := tx.ExecContext(ctx, `UPDATE billing_inflight_leases SET expires_at=clock_timestamp()+$3*interval '1 second'
        WHERE user_id=$1 AND id=$2 AND phase='owner' AND expires_at>clock_timestamp()`, userID, id, ttl.Seconds())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		alive = true
		_, err = tx.ExecContext(ctx, `UPDATE billing_inflight_leases SET expires_at=clock_timestamp()+$3*interval '1 second' WHERE user_id=$1 AND owner_id=$2 AND phase IN ('attempt','pending') AND expires_at>clock_timestamp()`, userID, id, ttl.Seconds())
		return err
	})
	return alive, err
}

func (r *usageBillingRepository) ReleaseBillingInflight(ctx context.Context, userID int64, id string) error {
	return r.inflightTx(ctx, userID, func(tx *sql.Tx, _ float64) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM billing_inflight_leases WHERE user_id=$1 AND (id=$2 AND phase='owner' OR owner_id=$2 AND phase IN ('attempt','settled'))`, userID, id)
		return err
	})
}

func (r *usageBillingRepository) StageBillingInflight(ctx context.Context, userID int64, owner, attemptID string, cmd *service.UsageBillingCommand, ttl time.Duration) (string, error) {
	if cmd == nil || cmd.UserID != userID || owner == "" || ttl <= 0 {
		return "", service.ErrBillingInflightIdentity
	}
	cmd.Normalize()
	official := cmd.OfficialCost
	if official <= 0 {
		official = cmd.BalanceCost
	}
	m := cmd.RateMultiplier
	if m <= 0 {
		m = 1
	}
	charge := official * m
	if charge < 0 || math.IsNaN(charge) || math.IsInf(charge, 0) {
		return "", service.ErrBillingInflightIdentity
	}
	id := uuid.NewString()
	err := r.inflightTx(ctx, userID, func(tx *sql.Tx, _ float64) error {
		var existingID, fingerprint string
		var existingUser int64
		err := tx.QueryRowContext(ctx, `SELECT id,user_id,request_fingerprint FROM billing_inflight_leases WHERE owner_id=$1 AND request_id=$2 AND api_key_id=$3`, owner, cmd.RequestID, cmd.APIKeyID).Scan(&existingID, &existingUser, &fingerprint)
		if err == nil {
			if existingUser != userID || strings.TrimSpace(fingerprint) != cmd.RequestFingerprint {
				return service.ErrBillingInflightIdentity
			}
			id = existingID
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO billing_inflight_leases(id,user_id,owner_id,phase,amount,request_id,api_key_id,request_fingerprint,expires_at)
        VALUES($1,$2,$3,'pending',$4,$5,$6,$7,clock_timestamp()+$8*interval '1 second')`, id, userID, owner, charge, cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint, ttl.Seconds())
		if err != nil {
			return err
		}
		// Known obligations replace the original estimate once, never get rejected
		// because actual usage exceeds it, and remain held until atomic Apply.
		_, err = tx.ExecContext(ctx, `UPDATE billing_inflight_leases SET known_charge=known_charge+$4,amount=GREATEST(estimate-known_charge-$4,0)
        WHERE user_id=$1 AND owner_id=$2 AND id=$3 AND phase='attempt'`, userID, owner, attemptID, charge)
		return err
	})
	return id, err
}

func (r *usageBillingRepository) FinishBillingInflightAttempt(ctx context.Context, userID int64, owner, attemptID string) error {
	return r.inflightTx(ctx, userID, func(tx *sql.Tx, _ float64) error {
		_, err := tx.ExecContext(ctx, `UPDATE billing_inflight_leases SET amount=0,exclusive=false WHERE user_id=$1 AND owner_id=$2 AND id=$3 AND phase='attempt'`, userID, owner, attemptID)
		return err
	})
}

func consumeBillingInflight(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) error {
	if cmd.InflightObligationID == "" {
		return nil
	}
	if _, err := lockBillingInflightUser(ctx, tx, cmd.UserID); err != nil {
		return err
	}
	var userID, apiKeyID int64
	var requestID, fingerprint string
	err := tx.QueryRowContext(ctx, `SELECT user_id,request_id,api_key_id,request_fingerprint FROM billing_inflight_leases WHERE id=$1 FOR UPDATE`, cmd.InflightObligationID).Scan(&userID, &requestID, &apiKeyID, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} // lease TTL does not discard late actual billing
	if err != nil {
		return err
	}
	if userID != cmd.UserID || requestID != cmd.RequestID || apiKeyID != cmd.APIKeyID || fingerprint != cmd.RequestFingerprint {
		return service.ErrBillingInflightIdentity
	}
	_, err = tx.ExecContext(ctx, `UPDATE billing_inflight_leases SET phase='settled',amount=0 WHERE id=$1`, cmd.InflightObligationID)
	return err
}
