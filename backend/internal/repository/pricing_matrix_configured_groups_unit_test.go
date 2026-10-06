//go:build unit

package repository

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func newConfiguredGroupsRepo(t *testing.T) (*pricingMatrixRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &pricingMatrixRepository{db: db}, mock
}

func TestListConfiguredGroups(t *testing.T) {
	repo, mock := newConfiguredGroupsRepo(t)
	mock.ExpectQuery("FROM group_model_config").
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "pricing_stage"}).AddRow(int64(2), "legacy").AddRow(int64(7), "v2"))
	groups, err := repo.ListConfiguredGroups(context.Background())
	require.NoError(t, err)
	require.Equal(t, []service.ConfiguredGroup{{ID: 2, Stage: service.PricingStageLegacy}, {ID: 7, Stage: service.PricingStageV2}}, groups)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListConfiguredGroups_Errors(t *testing.T) {
	repo, mock := newConfiguredGroupsRepo(t)
	mock.ExpectQuery("FROM group_model_config").WillReturnError(stageBoom)
	_, err := repo.ListConfiguredGroups(context.Background())
	require.ErrorIs(t, err, stageBoom)

	repo, mock = newConfiguredGroupsRepo(t)
	mock.ExpectQuery("FROM group_model_config").
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "pricing_stage"}).AddRow("not-a-number", "v2"))
	_, err = repo.ListConfiguredGroups(context.Background())
	require.Error(t, err)

	repo, mock = newConfiguredGroupsRepo(t)
	mock.ExpectQuery("FROM group_model_config").
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "pricing_stage"}).AddRow(int64(1), "v2").RowError(0, stageBoom))
	_, err = repo.ListConfiguredGroups(context.Background())
	require.Error(t, err)
}
