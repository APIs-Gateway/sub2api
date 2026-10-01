package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyGroupRouteExtras_ListByGroup(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewAPIKeyGroupRouteExtras(db)

	// 只统计未软删除的 Key（JOIN 带 deleted_at IS NULL），且有上限。
	mock.ExpectQuery(`(?s)FROM api_key_group_routes r\s+JOIN api_keys k ON k.id = r.api_key_id AND k.deleted_at IS NULL\s+WHERE r.group_id = \$1\s+ORDER BY .* LIMIT \$2`).
		WithArgs(int64(21), 201).
		WillReturnRows(sqlmock.NewRows([]string{"api_key_id", "user_id", "source", "placement"}).
			AddRow(int64(100), int64(1), "user", "tail").
			AddRow(int64(101), int64(2), "admin", "head"))

	got, err := repo.ListByGroup(context.Background(), 21, 201)
	require.NoError(t, err)
	require.Equal(t, []service.GroupRouteRef{
		{KeyID: 100, UserID: 1, Source: "user", Placement: "tail"},
		{KeyID: 101, UserID: 2, Source: "admin", Placement: "head"},
	}, got)

	// limit <= 0 回落到默认上限。
	mock.ExpectQuery(`LIMIT \$2`).WithArgs(int64(21), 200).
		WillReturnRows(sqlmock.NewRows([]string{"api_key_id", "user_id", "source", "placement"}))
	got, err = repo.ListByGroup(context.Background(), 21, 0)
	require.NoError(t, err)
	require.Empty(t, got)

	mock.ExpectQuery(`FROM api_key_group_routes`).WillReturnError(errors.New("boom"))
	_, err = repo.ListByGroup(context.Background(), 21, 10)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAPIKeyGroupRouteExtras_DeleteByKey(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewAPIKeyGroupRouteExtras(db)

	mock.ExpectExec(`DELETE FROM api_key_group_routes WHERE api_key_id = \$1`).
		WithArgs(int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 3))
	require.NoError(t, repo.DeleteByKey(context.Background(), 7))

	mock.ExpectExec(`DELETE FROM api_key_group_routes`).WillReturnError(errors.New("boom"))
	require.Error(t, repo.DeleteByKey(context.Background(), 7))
	require.NoError(t, mock.ExpectationsWereMet())
}
