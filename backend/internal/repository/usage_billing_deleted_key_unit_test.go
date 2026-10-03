//go:build unit

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingApply_DeletedKeyCountersCommit(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO usage_billing_dedup").
		WithArgs("deleted-key", int64(7), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery("SELECT request_fingerprint.*FROM usage_billing_dedup_archive").
		WithArgs("deleted-key", int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("UPDATE api_keys.*SET quota_used").
		WithArgs(10.0, int64(7), service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE api_keys SET.*usage_5h").
		WithArgs(10.0, int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	result, err := (&usageBillingRepository{db: db}).Apply(context.Background(), &service.UsageBillingCommand{
		RequestID: "deleted-key", APIKeyID: 7, APIKeyQuotaCost: 10, APIKeyRateLimitCost: 10,
	})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.False(t, result.APIKeyQuotaExhausted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingApply_KeyCounterErrorsRollback(t *testing.T) {
	for _, counter := range []string{"quota", "rate-limit", "rows-affected"} {
		t.Run(counter, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			mock.ExpectBegin()
			mock.ExpectQuery("INSERT INTO usage_billing_dedup").
				WithArgs("failed-counter", int64(7), sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
			mock.ExpectQuery("SELECT request_fingerprint.*FROM usage_billing_dedup_archive").
				WithArgs("failed-counter", int64(7)).WillReturnError(sql.ErrNoRows)
			cmd := &service.UsageBillingCommand{RequestID: "failed-counter", APIKeyID: 7}
			if counter == "quota" {
				cmd.APIKeyQuotaCost = 10
				mock.ExpectQuery("UPDATE api_keys.*SET quota_used").
					WithArgs(10.0, int64(7), service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted).
					WillReturnError(sql.ErrConnDone)
			} else {
				cmd.APIKeyRateLimitCost = 10
				expect := mock.ExpectExec("UPDATE api_keys SET.*usage_5h").WithArgs(10.0, int64(7))
				if counter == "rows-affected" {
					expect.WillReturnResult(sqlmock.NewErrorResult(sql.ErrConnDone))
				} else {
					expect.WillReturnError(sql.ErrConnDone)
				}
			}
			mock.ExpectRollback()
			result, err := (&usageBillingRepository{db: db}).Apply(context.Background(), cmd)
			require.ErrorIs(t, err, sql.ErrConnDone)
			require.Nil(t, result)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
