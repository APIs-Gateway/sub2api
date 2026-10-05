//go:build unit

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var prT0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func newReplayRepoMock(t *testing.T) (service.PricingReplayDataSource, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return NewPricingReplayDataSource(db), mock
}

func TestPricingReplayReadOnlyDSN(t *testing.T) {
	got := PricingReplayReadOnlyDSN("host=h dbname=d", 120*time.Second, 2*time.Second)
	require.Equal(t, "host=h dbname=d default_transaction_read_only=on statement_timeout=120000 lock_timeout=2000", got)
}

func TestVerifyPricingReplaySession(t *testing.T) {
	q := "SELECT current_setting"
	for name, tc := range map[string]struct {
		row     []any
		err     error
		wantErr string
	}{
		"ok":         {row: []any{"on", "120s", "2s"}},
		"writable":   {row: []any{"off", "120s", "2s"}, wantErr: "default_transaction_read_only"},
		"no timeout": {row: []any{"on", "0", "2s"}, wantErr: "statement_timeout"},
		"query":      {err: errors.New("boom"), wantErr: "read session settings"},
	} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		exp := mock.ExpectQuery(q)
		if tc.err != nil {
			exp.WillReturnError(tc.err)
		} else {
			exp.WillReturnRows(sqlmock.NewRows([]string{"a", "b", "c"}).AddRow(tc.row...))
		}
		got, err := VerifyPricingReplaySession(context.Background(), db)
		if tc.wantErr != "" {
			require.ErrorContains(t, err, tc.wantErr, name)
		} else {
			require.NoError(t, err, name)
			require.Equal(t, map[string]string{"default_transaction_read_only": "on", "statement_timeout": "120s", "lock_timeout": "2s"}, got)
		}
		_ = db.Close()
	}
}

func TestPricingReplayRepo_UsageGroupCounts(t *testing.T) {
	repo, mock := newReplayRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("FROM usage_logs").WithArgs(prT0, prT0.Add(time.Hour)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "count"}).AddRow(1, 5).AddRow(2, 7))
	mock.ExpectRollback()
	got, err := repo.UsageGroupCounts(context.Background(), prT0, prT0.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{1: 5, 2: 7}, got)
	require.NoError(t, mock.ExpectationsWereMet())

	repo, mock = newReplayRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("FROM usage_logs").WillReturnError(errors.New("boom"))
	mock.ExpectRollback()
	_, err = repo.UsageGroupCounts(context.Background(), prT0, prT0.Add(time.Hour))
	require.ErrorContains(t, err, "count usage rows by group")

	repo, mock = newReplayRepoMock(t)
	mock.ExpectBegin().WillReturnError(errors.New("no tx"))
	_, err = repo.UsageGroupCounts(context.Background(), prT0, prT0.Add(time.Hour))
	require.ErrorContains(t, err, "begin read-only tx")

	repo, mock = newReplayRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("FROM usage_logs").
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "count"}).AddRow("x", 1))
	mock.ExpectRollback()
	_, err = repo.UsageGroupCounts(context.Background(), prT0, prT0.Add(time.Hour))
	require.Error(t, err)
}

func TestPricingReplayRepo_LoadGroups(t *testing.T) {
	repo, mock := newReplayRepoMock(t)
	got, err := repo.LoadGroups(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, got)

	mock.ExpectBegin()
	mock.ExpectQuery("FROM groups").WillReturnRows(sqlmock.NewRows(
		[]string{"id", "platform", "rate_multiplier", "image_rate_independent", "image_rate_multiplier", "p1", "p2", "p4"}).
		AddRow(1, "openai", 1.5, true, 2.0, 0.1, nil, 0.4))
	mock.ExpectRollback()
	got, err = repo.LoadGroups(context.Background(), []int64{1, 2})
	require.NoError(t, err)
	g := got[1]
	require.NotNil(t, g)
	require.Equal(t, "openai", g.Platform)
	require.Equal(t, 1.5, g.RateMultiplier)
	require.NotNil(t, g.ImagePrice1K)
	require.Equal(t, 0.1, *g.ImagePrice1K)
	require.Nil(t, g.ImagePrice2K)
	require.Equal(t, 0.4, *g.ImagePrice4K)
	require.NotContains(t, got, int64(2))

	repo, mock = newReplayRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("FROM groups").WillReturnError(errors.New("boom"))
	mock.ExpectRollback()
	_, err = repo.LoadGroups(context.Background(), []int64{1})
	require.ErrorContains(t, err, "load groups")
}

func TestPricingReplayRepo_IDBounds(t *testing.T) {
	repo, mock := newReplayRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("MIN\\(id\\)").WillReturnRows(sqlmock.NewRows([]string{"min", "max"}).AddRow(3, 99))
	mock.ExpectRollback()
	lo, hi, err := repo.IDBounds(context.Background(), prT0, prT0.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, [2]int64{3, 99}, [2]int64{lo, hi})

	repo, mock = newReplayRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("MIN\\(id\\)").WillReturnError(errors.New("boom"))
	mock.ExpectRollback()
	_, _, err = repo.IDBounds(context.Background(), prT0, prT0.Add(time.Hour))
	require.ErrorContains(t, err, "read id bounds")
}

func TestPricingReplayRepo_Batch(t *testing.T) {
	cols := []string{"id", "created_at", "user_id", "account_id", "group_id", "served", "model", "requested", "upstream", "tier", "mode",
		"in", "out", "cc", "cr", "cc5", "cc1", "ii", "io", "ic", "size", "isize", "osize", "cost"}
	repo, mock := newReplayRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("ORDER BY id").WithArgs(int64(10), int64(99), prT0, prT0.Add(time.Hour), sqlmock.AnyArg(), 2).
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow(11, prT0, 7, 3, 1, 0, "m", "req", nil, nil, "token", 1, 2, 3, 4, 5, 6, 7, 8, 9, nil, nil, nil, 0.5).
			AddRow(12, prT0, 7, 3, 1, 2, "m", nil, "up", "flex", nil, 1, 2, 3, 4, 5, 6, 7, 8, 9, "2K", "1K", "4K", 1.5))
	mock.ExpectRollback()
	got, err := repo.Batch(context.Background(), service.PricingReplayBatchQuery{
		AfterID: 10, MaxID: 99, From: prT0, To: prT0.Add(time.Hour), GroupIDs: []int64{1}, Limit: 2})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "req", got[0].RequestedModel)
	require.Empty(t, got[0].UpstreamModel)
	require.Equal(t, 0.5, got[0].StoredActualCost)
	require.Equal(t, int64(2), got[1].ServedGroupID)
	require.Equal(t, "up", got[1].UpstreamModel)
	require.Equal(t, "flex", got[1].ServiceTier)
	require.Equal(t, "4K", got[1].ImageOutputSize)
	require.Equal(t, 9, got[1].ImageCount)

	repo, mock = newReplayRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("ORDER BY id").WillReturnError(errors.New("boom"))
	mock.ExpectRollback()
	_, err = repo.Batch(context.Background(), service.PricingReplayBatchQuery{AfterID: 5, Limit: 1})
	require.ErrorContains(t, err, "read usage batch after id 5")

	repo, mock = newReplayRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("ORDER BY id").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectRollback()
	_, err = repo.Batch(context.Background(), service.PricingReplayBatchQuery{Limit: 1})
	require.Error(t, err, "a short row fails the scan")
}

func TestPricingReplayRepo_UserGroupRates(t *testing.T) {
	repo, mock := newReplayRepoMock(t)
	got, err := repo.UserGroupRates(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, got)

	mock.ExpectBegin()
	mock.ExpectQuery("user_group_rate_multipliers").
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "group_id", "rate"}).AddRow(7, 2, 0.8))
	mock.ExpectRollback()
	got, err = repo.UserGroupRates(context.Background(), []int64{7})
	require.NoError(t, err)
	require.Equal(t, map[service.PricingReplayRateKey]float64{{UserID: 7, GroupID: 2}: 0.8}, got)

	repo, mock = newReplayRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("user_group_rate_multipliers").WillReturnError(errors.New("boom"))
	mock.ExpectRollback()
	_, err = repo.UserGroupRates(context.Background(), []int64{7})
	require.ErrorContains(t, err, "read user group rates")
}

func TestPricingReplayRepo_Fingerprint(t *testing.T) {
	repo, mock := newReplayRepoMock(t)
	mock.ExpectBegin()
	for range pricingReplayChannelTables {
		mock.ExpectQuery("md5\\(string_agg").WillReturnRows(sqlmock.NewRows([]string{"h"}).AddRow("abc"))
	}
	mock.ExpectQuery("group_model_config").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"h"}).AddRow("m1"))
	mock.ExpectQuery("group_model_config").WithArgs(int64(2)).WillReturnRows(sqlmock.NewRows([]string{"h"}).AddRow("m2"))
	mock.ExpectRollback()
	fp, err := repo.Fingerprint(context.Background(), []int64{1, 2})
	require.NoError(t, err)
	require.Len(t, fp.ChannelConfigHash, 64)
	require.Equal(t, map[int64]string{1: "m1", 2: "m2"}, fp.GroupMatrixHash)
	require.NoError(t, mock.ExpectationsWereMet())

	repo, mock = newReplayRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("md5\\(string_agg").WillReturnError(errors.New("boom"))
	mock.ExpectRollback()
	_, err = repo.Fingerprint(context.Background(), nil)
	require.ErrorContains(t, err, "hash channels")

	repo, mock = newReplayRepoMock(t)
	mock.ExpectBegin()
	for range pricingReplayChannelTables {
		mock.ExpectQuery("md5\\(string_agg").WillReturnRows(sqlmock.NewRows([]string{"h"}).AddRow(""))
	}
	mock.ExpectQuery("group_model_config").WillReturnError(errors.New("boom"))
	mock.ExpectRollback()
	_, err = repo.Fingerprint(context.Background(), []int64{9})
	require.ErrorContains(t, err, "hash matrix rows of group 9")
}
