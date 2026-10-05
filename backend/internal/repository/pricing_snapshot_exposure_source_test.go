package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestPricingSnapshotExposureSource_AllowlistGroupIDsTx(t *testing.T) {
	db, mock := newSQLMock(t)
	src := NewPricingSnapshotExposureSource()

	mock.ExpectQuery(`FROM group_model_config c\s+JOIN groups g ON g.id = c.group_id\s+WHERE c.access_mode = \$1 AND g.deleted_at IS NULL`).
		WithArgs("allowlist").
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(3)).AddRow(int64(9)))
	ids, err := src.AllowlistGroupIDsTx(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, []int64{3, 9}, ids)

	mock.ExpectQuery(`FROM group_model_config`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
	ids, err = src.AllowlistGroupIDsTx(context.Background(), db)
	require.NoError(t, err)
	require.Empty(t, ids)

	mock.ExpectQuery(`FROM group_model_config`).WillReturnError(context.DeadlineExceeded)
	_, err = src.AllowlistGroupIDsTx(context.Background(), db)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	mock.ExpectQuery(`FROM group_model_config`).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow("not-a-number"))
	_, err = src.AllowlistGroupIDsTx(context.Background(), db)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
