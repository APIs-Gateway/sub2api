package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCheckinClaimDailyReturnsBalanceFromCommittedTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const (
		userID = int64(42)
		date   = "2026-07-16"
		amount = 0.25
	)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(hashtextextended('checkin:' || $1::text, 0))`)).
		WithArgs(userID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM checkin_records WHERE user_id = $1 AND checkin_date = $2 AND type = 'daily')`)).
		WithArgs(userID, date).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO checkin_records (user_id, checkin_date, type, amount) VALUES ($1, $2, $3, $4)`)).
		WithArgs(userID, date, "daily", amount).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE users SET balance = balance + $1 WHERE id = $2 AND deleted_at IS NULL`)).
		WithArgs(amount, userID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT balance FROM users WHERE id = $1 AND deleted_at IS NULL`)).
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(12.75))
	mock.ExpectCommit()

	balance, err := (&checkinRepository{db: db}).ClaimDaily(context.Background(), userID, date, amount)
	if err != nil {
		t.Fatalf("claim daily: %v", err)
	}
	if balance != 12.75 {
		t.Fatalf("balance = %v, want 12.75", balance)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckinClaimDailyRollsBackWhenBalanceReadFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const (
		userID = int64(42)
		date   = "2026-07-16"
		amount = 0.25
	)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(hashtextextended('checkin:' || $1::text, 0))`)).
		WithArgs(userID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM checkin_records WHERE user_id = $1 AND checkin_date = $2 AND type = 'daily')`)).
		WithArgs(userID, date).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO checkin_records (user_id, checkin_date, type, amount) VALUES ($1, $2, $3, $4)`)).
		WithArgs(userID, date, "daily", amount).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE users SET balance = balance + $1 WHERE id = $2 AND deleted_at IS NULL`)).
		WithArgs(amount, userID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT balance FROM users WHERE id = $1 AND deleted_at IS NULL`)).
		WithArgs(userID).
		WillReturnError(errors.New("balance read failed"))
	mock.ExpectRollback()

	_, err = (&checkinRepository{db: db}).ClaimDaily(context.Background(), userID, date, amount)
	if err == nil {
		t.Fatal("expected balance read error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
