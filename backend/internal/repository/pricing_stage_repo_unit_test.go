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

var stageBoom = errors.New("boom")

func newStageMock(t *testing.T) (service.PricingStageStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return NewPricingStageStore(db), mock
}

func expectStageLock(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL lock_timeout").WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestPricingStageStore_SwitchStageChanged(t *testing.T) {
	store, mock := newStageMock(t)
	expectStageLock(mock)
	mock.ExpectQuery("FOR UPDATE OF c").WithArgs(int64(5)).
		WillReturnRows(sqlmock.NewRows([]string{"pricing_stage", "revision"}).AddRow("legacy", int64(3)))
	mock.ExpectExec("UPDATE group_model_config").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	change, err := store.SwitchStage(context.Background(), 5, service.PricingStageShadow, 9, time.Now())
	require.NoError(t, err)
	require.True(t, change.Changed)
	require.Equal(t, service.PricingStageLegacy, change.From)
	require.Equal(t, int64(4), change.Revision)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingStageStore_SwitchStageUnchanged(t *testing.T) {
	store, mock := newStageMock(t)
	expectStageLock(mock)
	mock.ExpectQuery("FOR UPDATE OF c").
		WillReturnRows(sqlmock.NewRows([]string{"pricing_stage", "revision"}).AddRow("shadow", int64(3)))
	mock.ExpectCommit()

	change, err := store.SwitchStage(context.Background(), 5, service.PricingStageShadow, 9, time.Now())
	require.NoError(t, err)
	require.False(t, change.Changed)
	require.Equal(t, int64(3), change.Revision)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingStageStore_SwitchStageMissingRow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		exists bool
		want   error
	}{
		{"group missing", false, service.ErrGroupNotFound},
		{"not derived", true, service.ErrPricingStageNotDerived},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := newStageMock(t)
			expectStageLock(mock)
			mock.ExpectQuery("FOR UPDATE OF c").WillReturnRows(sqlmock.NewRows([]string{"pricing_stage", "revision"}))
			mock.ExpectQuery("SELECT EXISTS").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(tc.exists))
			mock.ExpectRollback()
			_, err := store.SwitchStage(context.Background(), 5, service.PricingStageShadow, 9, time.Now())
			require.ErrorIs(t, err, tc.want)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPricingStageStore_SwitchStageErrors(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, setup func(sqlmock.Sqlmock)) {
		store, mock := newStageMock(t)
		setup(mock)
		_, err := store.SwitchStage(ctx, 5, service.PricingStageShadow, 9, time.Now())
		require.ErrorIs(t, err, stageBoom)
		require.NoError(t, mock.ExpectationsWereMet())
	}
	row := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"pricing_stage", "revision"}).AddRow("legacy", int64(1))
	}
	t.Run("begin", func(t *testing.T) {
		run(t, func(m sqlmock.Sqlmock) { m.ExpectBegin().WillReturnError(stageBoom) })
	})
	t.Run("lock timeout", func(t *testing.T) {
		run(t, func(m sqlmock.Sqlmock) {
			m.ExpectBegin()
			m.ExpectExec("SET LOCAL lock_timeout").WillReturnError(stageBoom)
			m.ExpectRollback()
		})
	})
	t.Run("select", func(t *testing.T) {
		run(t, func(m sqlmock.Sqlmock) {
			expectStageLock(m)
			m.ExpectQuery("FOR UPDATE OF c").WillReturnError(stageBoom)
			m.ExpectRollback()
		})
	})
	t.Run("classify", func(t *testing.T) {
		run(t, func(m sqlmock.Sqlmock) {
			expectStageLock(m)
			m.ExpectQuery("FOR UPDATE OF c").WillReturnRows(sqlmock.NewRows([]string{"pricing_stage", "revision"}))
			m.ExpectQuery("SELECT EXISTS").WillReturnError(stageBoom)
			m.ExpectRollback()
		})
	})
	t.Run("update", func(t *testing.T) {
		run(t, func(m sqlmock.Sqlmock) {
			expectStageLock(m)
			m.ExpectQuery("FOR UPDATE OF c").WillReturnRows(row())
			m.ExpectExec("UPDATE group_model_config").WillReturnError(stageBoom)
			m.ExpectRollback()
		})
	})
	t.Run("commit", func(t *testing.T) {
		run(t, func(m sqlmock.Sqlmock) {
			expectStageLock(m)
			m.ExpectQuery("FOR UPDATE OF c").WillReturnRows(row())
			m.ExpectExec("UPDATE group_model_config").WillReturnResult(sqlmock.NewResult(0, 1))
			m.ExpectCommit().WillReturnError(stageBoom)
		})
	})
}

func newShadowMock(t *testing.T) (service.PricingShadowStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return NewPricingShadowStore(db), mock
}

func shadowSample() service.PricingShadowSample {
	return service.PricingShadowSample{
		CreatedAt: time.Now(), GroupID: 3, Model: "m", Kind: service.ShadowKindCost, Class: service.ShadowClassTranslation,
		UsageRef: "u", LegacyView: []byte(`{}`), V2View: []byte(`{}`),
	}
}

func anyArgs(n int) []driverArg {
	out := make([]driverArg, n)
	for i := range out {
		out[i] = sqlmock.AnyArg()
	}
	return out
}

type driverArg = interface{}

func TestPricingShadowStore_InsertDiffs(t *testing.T) {
	ctx := context.Background()
	store, mock := newShadowMock(t)
	require.NoError(t, store.InsertDiffs(ctx, nil))

	mock.ExpectBegin()
	mock.ExpectPrepare("INSERT INTO pricing_shadow_diffs")
	mock.ExpectExec("INSERT INTO pricing_shadow_diffs").WithArgs(anyArgs(8)...).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO pricing_shadow_diffs").WithArgs(anyArgs(8)...).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	require.NoError(t, store.InsertDiffs(ctx, []service.PricingShadowSample{shadowSample(), shadowSample()}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingShadowStore_InsertDiffsErrors(t *testing.T) {
	ctx := context.Background()
	one := []service.PricingShadowSample{shadowSample()}

	store, mock := newShadowMock(t)
	mock.ExpectBegin().WillReturnError(stageBoom)
	require.ErrorIs(t, store.InsertDiffs(ctx, one), stageBoom)

	store, mock = newShadowMock(t)
	mock.ExpectBegin()
	mock.ExpectPrepare("INSERT INTO pricing_shadow_diffs").WillReturnError(stageBoom)
	mock.ExpectRollback()
	require.ErrorIs(t, store.InsertDiffs(ctx, one), stageBoom)

	store, mock = newShadowMock(t)
	mock.ExpectBegin()
	mock.ExpectPrepare("INSERT INTO pricing_shadow_diffs")
	mock.ExpectExec("INSERT INTO pricing_shadow_diffs").WillReturnError(stageBoom)
	mock.ExpectRollback()
	require.ErrorIs(t, store.InsertDiffs(ctx, one), stageBoom)

	store, mock = newShadowMock(t)
	mock.ExpectBegin()
	mock.ExpectPrepare("INSERT INTO pricing_shadow_diffs")
	mock.ExpectExec("INSERT INTO pricing_shadow_diffs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit().WillReturnError(stageBoom)
	require.ErrorIs(t, store.InsertDiffs(ctx, one), stageBoom)
}

func TestPricingShadowStore_Purge(t *testing.T) {
	ctx := context.Background()
	store, mock := newShadowMock(t)
	mock.ExpectExec("DELETE FROM pricing_shadow_diffs").WillReturnResult(sqlmock.NewResult(0, 4))
	n, err := store.PurgeDiffsBefore(ctx, time.Now())
	require.NoError(t, err)
	require.Equal(t, int64(4), n)

	mock.ExpectExec("DELETE FROM pricing_shadow_diffs").WillReturnError(stageBoom)
	_, err = store.PurgeDiffsBefore(ctx, time.Now())
	require.ErrorIs(t, err, stageBoom)

	mock.ExpectExec("DELETE FROM pricing_shadow_diffs").WillReturnResult(sqlmock.NewErrorResult(stageBoom))
	_, err = store.PurgeDiffsBefore(ctx, time.Now())
	require.ErrorIs(t, err, stageBoom)
}

func TestPricingShadowStore_ListDiffs(t *testing.T) {
	ctx := context.Background()
	cols := []string{"created_at", "group_id", "model", "kind", "class", "usage_ref", "legacy_view", "v2_view"}
	now := time.Now()

	store, mock := newShadowMock(t)
	mock.ExpectQuery("FROM pricing_shadow_diffs").WithArgs(int64(3), 10).
		WillReturnRows(sqlmock.NewRows(cols).AddRow(now, int64(3), "m", "cost", "translation", "u", []byte(`{"a":1}`), []byte(`{"a":2}`)))
	got, err := store.ListDiffs(ctx, 3, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "u", got[0].UsageRef)
	require.JSONEq(t, `{"a":2}`, string(got[0].V2View))

	mock.ExpectQuery("FROM pricing_shadow_diffs").WillReturnError(stageBoom)
	_, err = store.ListDiffs(ctx, 0, 10)
	require.ErrorIs(t, err, stageBoom)

	mock.ExpectQuery("FROM pricing_shadow_diffs").
		WillReturnRows(sqlmock.NewRows(cols).AddRow(now, "not-a-number", "m", "cost", "translation", "u", []byte(`{}`), []byte(`{}`)))
	_, err = store.ListDiffs(ctx, 0, 10)
	require.Error(t, err)

	mock.ExpectQuery("FROM pricing_shadow_diffs").
		WillReturnRows(sqlmock.NewRows(cols).AddRow(now, int64(3), "m", "cost", "translation", "u", []byte(`{}`), []byte(`{}`)).RowError(0, stageBoom))
	_, err = store.ListDiffs(ctx, 0, 10)
	require.ErrorIs(t, err, stageBoom)
}
