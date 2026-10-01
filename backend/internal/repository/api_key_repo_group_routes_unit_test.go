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
	require.True(t, repo.keyHasGroupRoutes(context.Background(), 5))

	mock.ExpectQuery(keyHasGroupRoutesSQL).WithArgs(int64(6)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	require.False(t, repo.keyHasGroupRoutes(context.Background(), 6))

	require.NoError(t, mock.ExpectationsWereMet())
}

// 查询失败时按「没有链」处理（等同功能关闭时的现状），不能让鉴权失败。
func TestAPIKeyRepository_KeyHasGroupRoutes_ErrorFailsOpenToNoChain(t *testing.T) {
	repo, mock := newAPIKeyRepoSQLMock(t)
	mock.ExpectQuery(keyHasGroupRoutesSQL).WithArgs(int64(7)).WillReturnError(errors.New("relation does not exist"))
	require.False(t, repo.keyHasGroupRoutes(context.Background(), 7))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAPIKeyRepository_KeyHasGroupRoutes_NoQueryWhenNotApplicable(t *testing.T) {
	repo, mock := newAPIKeyRepoSQLMock(t)
	require.False(t, repo.keyHasGroupRoutes(context.Background(), 0))

	repo.sql = nil
	require.False(t, repo.keyHasGroupRoutes(context.Background(), 5))
	require.NoError(t, mock.ExpectationsWereMet())
}
