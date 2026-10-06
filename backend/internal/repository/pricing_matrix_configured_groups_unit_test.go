//go:build unit

package repository

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func newConfiguredGroupsRepo(t *testing.T) (*pricingMatrixRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &pricingMatrixRepository{db: db}, mock
}

func TestListConfiguredGroupIDs(t *testing.T) {
	repo, mock := newConfiguredGroupsRepo(t)
	mock.ExpectQuery("FROM group_model_config").
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(2)).AddRow(int64(7)))
	ids, err := repo.ListConfiguredGroupIDs(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{2, 7}, ids)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListConfiguredGroupIDs_Errors(t *testing.T) {
	repo, mock := newConfiguredGroupsRepo(t)
	mock.ExpectQuery("FROM group_model_config").WillReturnError(stageBoom)
	_, err := repo.ListConfiguredGroupIDs(context.Background())
	require.ErrorIs(t, err, stageBoom)

	repo, mock = newConfiguredGroupsRepo(t)
	mock.ExpectQuery("FROM group_model_config").
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow("not-a-number"))
	_, err = repo.ListConfiguredGroupIDs(context.Background())
	require.Error(t, err)

	repo, mock = newConfiguredGroupsRepo(t)
	mock.ExpectQuery("FROM group_model_config").
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(1)).RowError(0, stageBoom))
	_, err = repo.ListConfiguredGroupIDs(context.Background())
	require.Error(t, err)
}
