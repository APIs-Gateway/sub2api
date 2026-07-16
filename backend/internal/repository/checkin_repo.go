package repository

import (
	"context"
	"database/sql"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type checkinRepository struct {
	db *sql.DB
}

// NewCheckinRepository 基于裸 *sql.DB 实现签到记录的查询与原子领取。
func NewCheckinRepository(db *sql.DB) service.CheckinRepository {
	return &checkinRepository{db: db}
}

func (r *checkinRepository) HasDailyCheckin(ctx context.Context, userID int64, date string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM checkin_records WHERE user_id = $1 AND checkin_date = $2 AND type = 'daily')`,
		userID, date,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func (r *checkinRepository) CountBonusOnDate(ctx context.Context, userID int64, date string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM checkin_records WHERE user_id = $1 AND checkin_date = $2 AND type = 'bonus'`,
		userID, date,
	).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (r *checkinRepository) ClaimDaily(ctx context.Context, userID int64, date string, amount float64) (float64, error) {
	return r.claim(ctx, userID, date, "daily", amount, func(ctx context.Context, tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM checkin_records WHERE user_id = $1 AND checkin_date = $2 AND type = 'daily')`,
			userID, date,
		).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return service.ErrCheckinAlreadyClaimed
		}
		return nil
	})
}

func (r *checkinRepository) ClaimBonus(ctx context.Context, userID int64, date string, amount float64, maxBonus int) (float64, error) {
	return r.claim(ctx, userID, date, "bonus", amount, func(ctx context.Context, tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM checkin_records WHERE user_id = $1 AND checkin_date = $2 AND type = 'bonus'`,
			userID, date,
		).Scan(&n); err != nil {
			return err
		}
		if n >= maxBonus {
			return service.ErrCheckinNoBonus
		}
		return nil
	})
}

// claim 在单个事务内：按用户加事务级 advisory lock 串行化 → 复核资格 →
// 写入签到记录 → 把 amount 加到用户余额（仅余额，不动 total_recharged）。
func (r *checkinRepository) claim(
	ctx context.Context,
	userID int64,
	date, ctype string,
	amount float64,
	check func(ctx context.Context, tx *sql.Tx) error,
) (balance float64, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// 同一用户的签到领取串行化，避免并发重复/超领。
	if _, err = tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('checkin:' || $1::text, 0))`, userID,
	); err != nil {
		return 0, err
	}

	if err = check(ctx, tx); err != nil {
		return 0, err
	}

	if _, insErr := tx.ExecContext(ctx,
		`INSERT INTO checkin_records (user_id, checkin_date, type, amount) VALUES ($1, $2, $3, $4)`,
		userID, date, ctype, amount,
	); insErr != nil {
		// daily 分区唯一索引兜底：并发/重放命中唯一约束时返回语义化错误而非通用 500。
		if ctype == "daily" && isUniqueConstraintViolation(insErr) {
			err = service.ErrCheckinAlreadyClaimed
		} else {
			err = insErr
		}
		return 0, err
	}

	res, execErr := tx.ExecContext(ctx,
		`UPDATE users SET balance = balance + $1 WHERE id = $2 AND deleted_at IS NULL`,
		amount, userID,
	)
	if execErr != nil {
		err = execErr
		return 0, err
	}
	affected, raErr := res.RowsAffected()
	if raErr != nil {
		err = raErr
		return 0, err
	}
	if affected == 0 {
		err = service.ErrUserNotFound
		return 0, err
	}

	err = tx.QueryRowContext(ctx,
		`SELECT balance FROM users WHERE id = $1 AND deleted_at IS NULL`,
		userID,
	).Scan(&balance)
	if err != nil {
		return 0, err
	}

	err = tx.Commit()
	if err != nil {
		return 0, err
	}
	return balance, nil
}
