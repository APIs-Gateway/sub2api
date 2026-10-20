//go:build unit

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type redeemAdjustmentSQLResult struct {
	rowsAffected    int64
	rowsAffectedErr error
}

func (r redeemAdjustmentSQLResult) LastInsertId() (int64, error) {
	return 0, nil
}

func (r redeemAdjustmentSQLResult) RowsAffected() (int64, error) {
	return r.rowsAffected, r.rowsAffectedErr
}

type redeemAdjustmentSQLExecutor struct {
	query   string
	args    []any
	result  sql.Result
	execErr error
}

func (e *redeemAdjustmentSQLExecutor) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	e.query = query
	e.args = args
	if e.execErr != nil {
		return nil, e.execErr
	}
	return e.result, nil
}

func (e *redeemAdjustmentSQLExecutor) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("unexpected query")
}

func TestUserRepositoryApplyRedeemAdjustmentsUseAtomicFloor(t *testing.T) {
	tests := []struct {
		name       string
		apply      func(*userRepository) error
		wantColumn string
		wantArgs   []any
	}{
		{
			name: "balance",
			apply: func(repo *userRepository) error {
				return repo.ApplyRedeemBalanceAdjustment(context.Background(), 42, -7)
			},
			wantColumn: "GREATEST(balance + $1, 0)",
			wantArgs:   []any{float64(-7), int64(42)},
		},
		{
			name: "concurrency",
			apply: func(repo *userRepository) error {
				return repo.ApplyRedeemConcurrencyAdjustment(context.Background(), 42, -7)
			},
			wantColumn: "GREATEST(concurrency + $1, 0)",
			wantArgs:   []any{int(-7), int64(42)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &redeemAdjustmentSQLExecutor{
				result: redeemAdjustmentSQLResult{rowsAffected: 1},
			}
			repo := newUserRepositoryWithSQL(nil, exec)

			require.NoError(t, tt.apply(repo))
			require.Contains(t, exec.query, tt.wantColumn)
			require.Contains(t, exec.query, "deleted_at IS NULL")
			require.Equal(t, tt.wantArgs, exec.args)
		})
	}
}

func TestUserRepositoryApplyRedeemAdjustmentsReportNotFound(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*userRepository) error
	}{
		{
			name: "balance",
			apply: func(repo *userRepository) error {
				return repo.ApplyRedeemBalanceAdjustment(context.Background(), 404, -7)
			},
		},
		{
			name: "concurrency",
			apply: func(repo *userRepository) error {
				return repo.ApplyRedeemConcurrencyAdjustment(context.Background(), 404, -7)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newUserRepositoryWithSQL(nil, &redeemAdjustmentSQLExecutor{
				result: redeemAdjustmentSQLResult{rowsAffected: 0},
			})
			require.ErrorIs(t, tt.apply(repo), service.ErrUserNotFound)
		})
	}
}

func TestUserRepositoryApplyRedeemBalanceAdjustmentPropagatesExecutorErrors(t *testing.T) {
	execErr := errors.New("update failed")
	repo := newUserRepositoryWithSQL(nil, &redeemAdjustmentSQLExecutor{execErr: execErr})

	err := repo.ApplyRedeemBalanceAdjustment(context.Background(), 42, -7)
	require.ErrorIs(t, err, execErr)
}

func TestUserRepositoryApplyRedeemConcurrencyAdjustmentPropagatesRowsAffectedErrors(t *testing.T) {
	rowsAffectedErr := errors.New("rows affected failed")
	repo := newUserRepositoryWithSQL(nil, &redeemAdjustmentSQLExecutor{
		result: redeemAdjustmentSQLResult{rowsAffectedErr: rowsAffectedErr},
	})

	err := repo.ApplyRedeemConcurrencyAdjustment(context.Background(), 42, -7)
	require.ErrorIs(t, err, rowsAffectedErr)
}

func TestUserRepositoryApplyRedeemBalanceAdjustmentRequiresSQLExecutor(t *testing.T) {
	repo := newUserRepositoryWithSQL(nil, nil)

	err := repo.ApplyRedeemBalanceAdjustment(context.Background(), 42, -7)
	require.EqualError(t, err, "sql executor is not configured")
}
