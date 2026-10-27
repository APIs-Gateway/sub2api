package repository

import (
	"context"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

const keyHasGroupRoutesSQL = `SELECT EXISTS \(SELECT 1 FROM api_key_group_routes WHERE api_key_id = \$1\)`

func TestAPIKeyRepository_KeyHasGroupRoutes(t *testing.T) {
	repo, mock := newAPIKeyRepoSQLMock(t)

	mock.ExpectQuery(keyHasGroupRoutesSQL).WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	has, unknown := repo.keyHasGroupRoutes(context.Background(), 5)
	require.True(t, has)
	require.False(t, unknown)

	mock.ExpectQuery(keyHasGroupRoutesSQL).WithArgs(int64(6)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	has, unknown = repo.keyHasGroupRoutes(context.Background(), 6)
	require.False(t, has)
	require.False(t, unknown)

	require.NoError(t, mock.ExpectationsWereMet())
}

// 查询失败时本次按「没有链」处理（等同功能关闭时的现状），不能让鉴权失败；
// 同时标记 unknown，调用方据此不把这个 false 写进鉴权缓存（审查 S4）。
func TestAPIKeyRepository_KeyHasGroupRoutes_ErrorFailsOpenToNoChainButMarksUnknown(t *testing.T) {
	repo, mock := newAPIKeyRepoSQLMock(t)
	mock.ExpectQuery(keyHasGroupRoutesSQL).WithArgs(int64(7)).WillReturnError(errors.New("relation does not exist"))
	has, unknown := repo.keyHasGroupRoutes(context.Background(), 7)
	require.False(t, has)
	require.True(t, unknown)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAPIKeyRepository_KeyHasGroupRoutes_NoQueryWhenNotApplicable(t *testing.T) {
	repo, mock := newAPIKeyRepoSQLMock(t)
	has, unknown := repo.keyHasGroupRoutes(context.Background(), 0)
	require.False(t, has)
	require.False(t, unknown)

	repo.sql = nil
	has, unknown = repo.keyHasGroupRoutes(context.Background(), 5)
	require.False(t, has)
	require.False(t, unknown)
	require.NoError(t, mock.ExpectationsWereMet())
}
