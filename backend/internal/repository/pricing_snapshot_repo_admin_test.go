package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func pricingSnapshotRow(id int64, status string) *sqlmock.Rows {
	ts := time.Date(2026, 10, 5, 1, 2, 3, 0, time.UTC)
	return sqlmock.NewRows(pricingSnapshotCols).
		AddRow(id, "label", "remote", "https://example.com/p.json", pricingSnapshotSHA, 10, int64(1), nil, status, nil, ts, nil, nil, nil, "")
}

func TestPricingSnapshotRepo_GetMeta(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewPricingSnapshotRepository(db)
	mock.ExpectQuery(`FROM pricing_snapshots WHERE id = \$1`).WithArgs(int64(7)).WillReturnRows(pricingSnapshotRow(7, "candidate"))
	got, err := repo.GetMeta(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(7), got.ID)
	require.Equal(t, "candidate", got.Status)

	mock.ExpectQuery(`FROM pricing_snapshots WHERE id`).WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows(pricingSnapshotCols))
	_, err = repo.GetMeta(context.Background(), 8)
	require.ErrorIs(t, err, service.ErrPricingSnapshotNotFound)

	mock.ExpectQuery(`FROM pricing_snapshots WHERE id`).WithArgs(int64(9)).WillReturnError(errors.New("boom"))
	_, err = repo.GetMeta(context.Background(), 9)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingSnapshotRepo_List(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewPricingSnapshotRepository(db)

	mock.ExpectQuery(`ORDER BY fetched_at DESC, id DESC LIMIT \$2`).WithArgs(pq.Array([]string{"candidate"}), 20).
		WillReturnRows(pricingSnapshotRow(3, "candidate").AddRow(int64(2), "l", "remote", "", pricingSnapshotSHA, 1, nil, nil, "candidate", nil, time.Now(), nil, nil, nil, ""))
	got, err := repo.List(context.Background(), []string{"candidate"}, 20)
	require.NoError(t, err)
	require.Len(t, got, 2)

	// 没有状态过滤时传空数组；非法的上限回落到最大值。
	mock.ExpectQuery(`FROM pricing_snapshots`).WithArgs(pq.Array([]string{}), pricingSnapshotListMaxLimit).WillReturnRows(sqlmock.NewRows(pricingSnapshotCols))
	got, err = repo.List(context.Background(), nil, 0)
	require.NoError(t, err)
	require.Equal(t, []service.PricingSnapshotMeta{}, got)

	mock.ExpectQuery(`FROM pricing_snapshots`).WillReturnError(errors.New("boom"))
	_, err = repo.List(context.Background(), nil, 10)
	require.Error(t, err)

	bad := sqlmock.NewRows([]string{"id"}).AddRow(int64(1))
	mock.ExpectQuery(`FROM pricing_snapshots`).WillReturnRows(bad)
	_, err = repo.List(context.Background(), nil, 10)
	require.Error(t, err, "列数不对时扫描失败")

	rowErr := pricingSnapshotRow(5, "candidate").RowError(0, errors.New("iter"))
	mock.ExpectQuery(`FROM pricing_snapshots`).WillReturnRows(rowErr)
	_, err = repo.List(context.Background(), nil, 10)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingSnapshotRepo_InsertCandidate(t *testing.T) {
	adminID := int64(9)
	in := newPricingSnapshotInput()
	in.Source = service.PricingSnapshotSourceRemote
	in.FetchedBy = &adminID
	insertRe := `INSERT INTO pricing_snapshots[\s\S]*ON CONFLICT \(content_sha256\) WHERE status = 'candidate' DO NOTHING`

	t.Run("created", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectQuery(insertRe).WillReturnRows(pricingSnapshotRow(21, "candidate"))
		got, created, err := NewPricingSnapshotRepository(db).InsertCandidate(context.Background(), in)
		require.NoError(t, err)
		require.True(t, created)
		require.Equal(t, int64(21), got.ID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("same content already a candidate", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectQuery(insertRe).WillReturnRows(sqlmock.NewRows(pricingSnapshotCols))
		mock.ExpectQuery(`WHERE content_sha256 = \$1 AND status = 'candidate'`).WithArgs(in.ContentSHA256).WillReturnRows(pricingSnapshotRow(20, "candidate"))
		got, created, err := NewPricingSnapshotRepository(db).InsertCandidate(context.Background(), in)
		require.NoError(t, err)
		require.False(t, created)
		require.Equal(t, int64(20), got.ID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("the conflicting candidate vanished in between", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectQuery(insertRe).WillReturnRows(sqlmock.NewRows(pricingSnapshotCols))
		mock.ExpectQuery(`WHERE content_sha256 = \$1`).WillReturnRows(sqlmock.NewRows(pricingSnapshotCols))
		_, _, err := NewPricingSnapshotRepository(db).InsertCandidate(context.Background(), in)
		require.ErrorIs(t, err, service.ErrPricingSnapshotConflict)
	})

	t.Run("errors", func(t *testing.T) {
		db, mock := newSQLMock(t)
		repo := NewPricingSnapshotRepository(db)
		bad := in
		bad.ContentSHA256 = "short"
		_, _, err := repo.InsertCandidate(context.Background(), bad)
		require.Error(t, err)

		mock.ExpectQuery(insertRe).WillReturnError(errors.New("boom"))
		_, _, err = repo.InsertCandidate(context.Background(), in)
		require.Error(t, err)

		mock.ExpectQuery(insertRe).WillReturnRows(sqlmock.NewRows(pricingSnapshotCols))
		mock.ExpectQuery(`WHERE content_sha256 = \$1`).WillReturnError(errors.New("boom"))
		_, _, err = repo.InsertCandidate(context.Background(), in)
		require.Error(t, err)
		require.NotErrorIs(t, err, service.ErrPricingSnapshotConflict)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestPricingSnapshotRepo_RejectAndDeleteExpired(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewPricingSnapshotRepository(db)
	ctx := context.Background()

	mock.ExpectExec(`UPDATE pricing_snapshots SET status = 'rejected' WHERE id = \$1 AND status = 'candidate'`).WithArgs(int64(4)).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.RejectCandidate(ctx, 4))
	mock.ExpectExec(`UPDATE pricing_snapshots SET status = 'rejected'`).WithArgs(int64(5)).WillReturnResult(sqlmock.NewResult(0, 0))
	require.ErrorIs(t, repo.RejectCandidate(ctx, 5), service.ErrPricingSnapshotNotCandidate)
	mock.ExpectExec(`UPDATE pricing_snapshots SET status = 'rejected'`).WithArgs(int64(6)).WillReturnError(errors.New("boom"))
	require.Error(t, repo.RejectCandidate(ctx, 6))
	mock.ExpectExec(`UPDATE pricing_snapshots SET status = 'rejected'`).WithArgs(int64(7)).WillReturnResult(sqlmock.NewErrorResult(errors.New("rows")))
	require.Error(t, repo.RejectCandidate(ctx, 7))

	cutoff := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec(`DELETE FROM pricing_snapshots WHERE status IN \('candidate', 'rejected'\) AND fetched_at < \$1`).WithArgs(cutoff).WillReturnResult(sqlmock.NewResult(0, 3))
	n, err := repo.DeleteExpiredCandidates(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(3), n)
	mock.ExpectExec(`DELETE FROM pricing_snapshots`).WillReturnError(errors.New("boom"))
	_, err = repo.DeleteExpiredCandidates(ctx, cutoff)
	require.Error(t, err)
	mock.ExpectExec(`DELETE FROM pricing_snapshots`).WillReturnResult(sqlmock.NewErrorResult(errors.New("rows")))
	_, err = repo.DeleteExpiredCandidates(ctx, cutoff)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func applyMergedRequest() service.ApplyMergedSnapshot {
	baseID, candID := int64(1), int64(2)
	in := newPricingSnapshotInput()
	in.Source = service.PricingSnapshotSourceMerged
	in.ParentSnapshotID, in.CandidateSnapshotID = &baseID, &candID
	return service.ApplyMergedSnapshot{
		New:               in,
		ExpectedActiveID:  1,
		ExpectedActiveSHA: pricingSnapshotSHA,
		ConsumeCandidate:  true,
		Diffs: []service.PricingSnapshotDiffRecord{
			{ModelKey: "a", ChangeType: service.PricingDiffChanged, OldPrice: json.RawMessage(`{"x":1}`), NewPrice: json.RawMessage(`{"x":2}`), ChangedFields: []string{"x"}, Decision: service.PricingDiffDecisionApprove},
			{ModelKey: "b", ChangeType: service.PricingDiffAdded, NewPrice: json.RawMessage(`{"x":3}`), Decision: service.PricingDiffDecisionHold},
		},
	}
}

func expectApplyPrelude(mock sqlmock.Sqlmock, activeID int64, activeSHA string) {
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id, content_sha256 FROM pricing_snapshots WHERE status = 'active' FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "content_sha256"}).AddRow(activeID, activeSHA))
}

func TestPricingSnapshotRepo_ApplyMerged(t *testing.T) {
	ctx := context.Background()
	insertRe := `INSERT INTO pricing_snapshots`
	diffRe := `INSERT INTO pricing_snapshot_diffs[\s\S]*ON CONFLICT \(snapshot_id, model_key\) DO UPDATE`

	t.Run("success: baseline, supersede, insert, diffs, consume, check, commit", func(t *testing.T) {
		db, mock := newSQLMock(t)
		expectApplyPrelude(mock, 1, pricingSnapshotSHA)
		mock.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE status = 'active'`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(insertRe).WillReturnRows(pricingSnapshotRow(30, "active"))
		mock.ExpectExec(diffRe).WithArgs(int64(2), int64(1), "a", "changed", `{"x":1}`, `{"x":2}`, pq.Array([]string{"x"}), "approve").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(diffRe).WithArgs(int64(2), int64(1), "b", "added", nil, `{"x":3}`, pq.Array([]string{}), "hold").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE id = \$1 AND status = 'candidate'`).WithArgs(int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		req := applyMergedRequest()
		checked := 0
		req.Check = func(_ context.Context, exec service.MatrixExecutor) error {
			_, isTx := exec.(*sql.Tx)
			require.True(t, isTx, "保存时校验拿到的是批准事务的连接")
			checked++
			return nil
		}
		got, err := NewPricingSnapshotRepository(db).ApplyMerged(ctx, req)
		require.NoError(t, err)
		require.Equal(t, int64(30), got.ID)
		require.Equal(t, 1, checked)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("not consuming the candidate and no check", func(t *testing.T) {
		db, mock := newSQLMock(t)
		expectApplyPrelude(mock, 1, pricingSnapshotSHA)
		mock.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE status = 'active'`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(insertRe).WillReturnRows(pricingSnapshotRow(30, "active"))
		mock.ExpectCommit()
		req := applyMergedRequest()
		req.Diffs, req.ConsumeCandidate = nil, false
		_, err := NewPricingSnapshotRepository(db).ApplyMerged(ctx, req)
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("without a source candidate no diff rows are written", func(t *testing.T) {
		db, mock := newSQLMock(t)
		expectApplyPrelude(mock, 1, pricingSnapshotSHA)
		mock.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE status = 'active'`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(insertRe).WillReturnRows(pricingSnapshotRow(30, "active"))
		mock.ExpectCommit()
		req := applyMergedRequest()
		req.New.CandidateSnapshotID = nil
		_, err := NewPricingSnapshotRepository(db).ApplyMerged(ctx, req)
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("baseline changed: id or hash differs", func(t *testing.T) {
		for _, tc := range []struct {
			id  int64
			sha string
		}{{9, pricingSnapshotSHA}, {1, "cd" + pricingSnapshotSHA[2:]}} {
			db, mock := newSQLMock(t)
			expectApplyPrelude(mock, tc.id, tc.sha)
			mock.ExpectRollback()
			_, err := NewPricingSnapshotRepository(db).ApplyMerged(ctx, applyMergedRequest())
			require.ErrorIs(t, err, service.ErrPricingSnapshotBaselineChanged)
			require.NoError(t, mock.ExpectationsWereMet())
		}
	})

	t.Run("no active snapshot", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"id", "content_sha256"}))
		mock.ExpectRollback()
		_, err := NewPricingSnapshotRepository(db).ApplyMerged(ctx, applyMergedRequest())
		require.ErrorIs(t, err, service.ErrPricingSnapshotBaselineChanged)
	})

	t.Run("the save-time check rejects: everything rolls back and the error is returned as is", func(t *testing.T) {
		db, mock := newSQLMock(t)
		expectApplyPrelude(mock, 1, pricingSnapshotSHA)
		mock.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE status = 'active'`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(insertRe).WillReturnRows(pricingSnapshotRow(30, "active"))
		mock.ExpectExec(diffRe).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(diffRe).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE id`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectRollback()
		violation := errors.New("EXPOSURE_UNPRICED")
		req := applyMergedRequest()
		req.Check = func(context.Context, service.MatrixExecutor) error { return violation }
		_, err := NewPricingSnapshotRepository(db).ApplyMerged(ctx, req)
		require.Equal(t, violation, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("step failures", func(t *testing.T) {
		boom := errors.New("boom")
		type step func(mock sqlmock.Sqlmock)
		steps := map[string]step{
			"begin": func(m sqlmock.Sqlmock) { m.ExpectBegin().WillReturnError(boom) },
			"lock_timeout": func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectExec(`SET LOCAL lock_timeout`).WillReturnError(boom)
				m.ExpectRollback()
			},
			"lock row": func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
				m.ExpectQuery(`FOR UPDATE`).WillReturnError(boom)
				m.ExpectRollback()
			},
			"supersede": func(m sqlmock.Sqlmock) {
				expectApplyPrelude(m, 1, pricingSnapshotSHA)
				m.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE status = 'active'`).WillReturnError(boom)
				m.ExpectRollback()
			},
			"insert": func(m sqlmock.Sqlmock) {
				expectApplyPrelude(m, 1, pricingSnapshotSHA)
				m.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE status = 'active'`).WillReturnResult(sqlmock.NewResult(0, 1))
				m.ExpectQuery(insertRe).WillReturnError(boom)
				m.ExpectRollback()
			},
			"diff row": func(m sqlmock.Sqlmock) {
				expectApplyPrelude(m, 1, pricingSnapshotSHA)
				m.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE status = 'active'`).WillReturnResult(sqlmock.NewResult(0, 1))
				m.ExpectQuery(insertRe).WillReturnRows(pricingSnapshotRow(30, "active"))
				m.ExpectExec(diffRe).WillReturnError(boom)
				m.ExpectRollback()
			},
			"consume candidate": func(m sqlmock.Sqlmock) {
				expectApplyPrelude(m, 1, pricingSnapshotSHA)
				m.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE status = 'active'`).WillReturnResult(sqlmock.NewResult(0, 1))
				m.ExpectQuery(insertRe).WillReturnRows(pricingSnapshotRow(30, "active"))
				m.ExpectExec(diffRe).WillReturnResult(sqlmock.NewResult(0, 1))
				m.ExpectExec(diffRe).WillReturnResult(sqlmock.NewResult(0, 1))
				m.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE id`).WillReturnError(boom)
				m.ExpectRollback()
			},
			"commit": func(m sqlmock.Sqlmock) {
				expectApplyPrelude(m, 1, pricingSnapshotSHA)
				m.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE status = 'active'`).WillReturnResult(sqlmock.NewResult(0, 1))
				m.ExpectQuery(insertRe).WillReturnRows(pricingSnapshotRow(30, "active"))
				m.ExpectCommit().WillReturnError(boom)
			},
		}
		for name, setup := range steps {
			db, mock := newSQLMock(t)
			setup(mock)
			req := applyMergedRequest()
			if name == "commit" {
				req.Diffs, req.ConsumeCandidate = nil, false
			}
			_, err := NewPricingSnapshotRepository(db).ApplyMerged(ctx, req)
			require.Error(t, err, name)
			require.NoError(t, mock.ExpectationsWereMet(), name)
		}
	})

	t.Run("commit loses on the unique index", func(t *testing.T) {
		db, mock := newSQLMock(t)
		expectApplyPrelude(mock, 1, pricingSnapshotSHA)
		mock.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE status = 'active'`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(insertRe).WillReturnRows(pricingSnapshotRow(30, "active"))
		mock.ExpectCommit().WillReturnError(&pq.Error{Code: "23505"})
		req := applyMergedRequest()
		req.Diffs, req.ConsumeCandidate = nil, false
		_, err := NewPricingSnapshotRepository(db).ApplyMerged(ctx, req)
		require.ErrorIs(t, err, service.ErrPricingSnapshotConflict)
	})

	t.Run("insert hits the unique index", func(t *testing.T) {
		db, mock := newSQLMock(t)
		expectApplyPrelude(mock, 1, pricingSnapshotSHA)
		mock.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE status = 'active'`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(insertRe).WillReturnError(&pq.Error{Code: "23505"})
		mock.ExpectRollback()
		_, err := NewPricingSnapshotRepository(db).ApplyMerged(ctx, applyMergedRequest())
		require.ErrorIs(t, err, service.ErrPricingSnapshotConflict)
	})

	t.Run("malformed hash", func(t *testing.T) {
		db, _ := newSQLMock(t)
		req := applyMergedRequest()
		req.New.ContentSHA256 = "short"
		_, err := NewPricingSnapshotRepository(db).ApplyMerged(ctx, req)
		require.Error(t, err)
	})
}

func TestPricingSnapshotRepo_RecentBillingModels(t *testing.T) {
	ctx := context.Background()
	t.Run("success", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL statement_timeout = '15s'`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT DISTINCT model FROM usage_logs`).WithArgs(7, pricingSnapshotRecentModelsLimit).
			WillReturnRows(sqlmock.NewRows([]string{"model"}).AddRow("gpt-5.1").AddRow("claude-sonnet-4"))
		mock.ExpectRollback()
		got, err := NewPricingSnapshotRepository(db).RecentBillingModels(ctx, 0)
		require.NoError(t, err)
		require.Equal(t, []string{"gpt-5.1", "claude-sonnet-4"}, got)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("failures", func(t *testing.T) {
		boom := errors.New("boom")
		cases := map[string]func(m sqlmock.Sqlmock){
			"begin": func(m sqlmock.Sqlmock) { m.ExpectBegin().WillReturnError(boom) },
			"timeout": func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectExec(`SET LOCAL statement_timeout`).WillReturnError(boom)
				m.ExpectRollback()
			},
			"query": func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectExec(`SET LOCAL statement_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
				m.ExpectQuery(`FROM usage_logs`).WillReturnError(boom)
				m.ExpectRollback()
			},
			"scan": func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectExec(`SET LOCAL statement_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
				m.ExpectQuery(`FROM usage_logs`).WillReturnRows(sqlmock.NewRows([]string{"model"}).AddRow(nil))
				m.ExpectRollback()
			},
			"iterate": func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectExec(`SET LOCAL statement_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
				m.ExpectQuery(`FROM usage_logs`).WillReturnRows(sqlmock.NewRows([]string{"model"}).AddRow("a").RowError(0, boom))
				m.ExpectRollback()
			},
		}
		for name, setup := range cases {
			db, mock := newSQLMock(t)
			setup(mock)
			_, err := NewPricingSnapshotRepository(db).RecentBillingModels(ctx, 7)
			require.Error(t, err, name)
		}
	})
}

func TestNullableJSONTextAndNonNilStrings(t *testing.T) {
	require.Nil(t, nullableJSONText(nil))
	require.Equal(t, `{"a":1}`, nullableJSONText([]byte(`{"a":1}`)))
	require.Equal(t, []string{}, nonNilStrings(nil))
	require.Equal(t, []string{"x"}, nonNilStrings([]string{"x"}))
}
