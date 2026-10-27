package repository

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

var pricingSnapshotCols = []string{"id", "label", "source", "source_url", "content_sha256", "model_count",
	"parent_snapshot_id", "candidate_snapshot_id", "status", "fetched_by", "fetched_at", "approved_by", "approved_at", "change_set_id", "note"}

var pricingSnapshotSHA = strings.Repeat("ab", 32)

func TestPricingSnapshotRepo_GetActiveMeta(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewPricingSnapshotRepository(db)
	ts := time.Date(2026, 10, 5, 1, 2, 3, 0, time.UTC)

	mock.ExpectQuery(`FROM pricing_snapshots WHERE status = 'active'`).
		WillReturnRows(sqlmock.NewRows(pricingSnapshotCols).
			AddRow(int64(3), "bootstrap-x", "bootstrap", "", pricingSnapshotSHA, 120, nil, nil, "active", nil, ts, int64(9), ts, nil, "n"))
	got, err := repo.GetActiveMeta(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(3), got.ID)
	require.Equal(t, "bootstrap", got.Source)
	require.Equal(t, pricingSnapshotSHA, got.ContentSHA256)
	require.Equal(t, 120, got.ModelCount)
	require.Nil(t, got.ParentSnapshotID)
	require.Nil(t, got.FetchedBy)
	require.Equal(t, int64(9), *got.ApprovedBy)
	require.Equal(t, ts, *got.ApprovedAt)
	require.Nil(t, got.ChangeSetID)

	mock.ExpectQuery(`FROM pricing_snapshots`).
		WillReturnRows(sqlmock.NewRows(pricingSnapshotCols).
			AddRow(int64(4), "merged", "merged", "u", pricingSnapshotSHA, 1, int64(3), int64(2), "active", int64(1), ts, nil, nil, int64(77), ""))
	got, err = repo.GetActiveMeta(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(3), *got.ParentSnapshotID)
	require.Equal(t, int64(2), *got.CandidateSnapshotID)
	require.Equal(t, int64(77), *got.ChangeSetID)
	require.Nil(t, got.ApprovedAt)

	mock.ExpectQuery(`FROM pricing_snapshots`).WillReturnRows(sqlmock.NewRows(pricingSnapshotCols))
	_, err = repo.GetActiveMeta(context.Background())
	require.ErrorIs(t, err, service.ErrPricingSnapshotNotFound)

	mock.ExpectQuery(`FROM pricing_snapshots`).WillReturnError(errors.New("boom"))
	_, err = repo.GetActiveMeta(context.Background())
	require.Error(t, err)
	require.NotErrorIs(t, err, service.ErrPricingSnapshotNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingSnapshotRepo_GetPayload(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewPricingSnapshotRepository(db)
	payload := []byte(`{"gpt-5.1":{"input_cost_per_token":1e-6}}`)
	gz, err := gzipPricingPayload(payload)
	require.NoError(t, err)

	mock.ExpectQuery(`SELECT payload_gz FROM pricing_snapshots WHERE id = \$1`).WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"payload_gz"}).AddRow(gz))
	got, err := repo.GetPayload(context.Background(), 3)
	require.NoError(t, err)
	require.Equal(t, payload, got)

	mock.ExpectQuery(`SELECT payload_gz`).WithArgs(int64(4)).WillReturnRows(sqlmock.NewRows([]string{"payload_gz"}))
	_, err = repo.GetPayload(context.Background(), 4)
	require.ErrorIs(t, err, service.ErrPricingSnapshotNotFound)

	mock.ExpectQuery(`SELECT payload_gz`).WithArgs(int64(5)).WillReturnError(errors.New("boom"))
	_, err = repo.GetPayload(context.Background(), 5)
	require.Error(t, err)

	mock.ExpectQuery(`SELECT payload_gz`).WithArgs(int64(6)).
		WillReturnRows(sqlmock.NewRows([]string{"payload_gz"}).AddRow([]byte("not gzip")))
	_, err = repo.GetPayload(context.Background(), 6)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPricingSnapshotGzipRoundTripAndLimit(t *testing.T) {
	payload := bytes.Repeat([]byte("litellm "), 1000)
	gz, err := gzipPricingPayload(payload)
	require.NoError(t, err)
	require.Less(t, len(gz), len(payload))
	back, err := gunzipPricingPayload(gz)
	require.NoError(t, err)
	require.Equal(t, payload, back)

	old := pricingSnapshotMaxPayloadBytes
	pricingSnapshotMaxPayloadBytes = 100
	defer func() { pricingSnapshotMaxPayloadBytes = old }()
	_, err = gunzipPricingPayload(gz)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds")

	// 截断的 gzip 流在读取阶段报错。
	pricingSnapshotMaxPayloadBytes = old
	_, err = gunzipPricingPayload(gz[:len(gz)/2])
	require.Error(t, err)
}

func newPricingSnapshotInput() service.NewPricingSnapshot {
	adminID := int64(9)
	return service.NewPricingSnapshot{
		Label: "bootstrap-2026-10-05-abababab", Source: service.PricingSnapshotSourceBootstrap,
		ContentSHA256: pricingSnapshotSHA, ModelCount: 2, Payload: []byte(`{"a":1}`),
		FetchedBy: &adminID, ApprovedBy: &adminID, Note: "n",
	}
}

func TestPricingSnapshotRepo_ActivateNew(t *testing.T) {
	ts := time.Date(2026, 10, 5, 1, 2, 3, 0, time.UTC)
	insertRe := `INSERT INTO pricing_snapshots`
	row := func() *sqlmock.Rows {
		return sqlmock.NewRows(pricingSnapshotCols).
			AddRow(int64(11), "bootstrap-2026-10-05-abababab", "bootstrap", "", pricingSnapshotSHA, 2, nil, nil, "active", int64(9), ts, int64(9), ts, nil, "n")
	}

	t.Run("success supersedes then inserts in one transaction", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`UPDATE pricing_snapshots SET status = 'superseded' WHERE status = 'active'`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(insertRe).WillReturnRows(row())
		mock.ExpectCommit()
		got, err := NewPricingSnapshotRepository(db).ActivateNew(context.Background(), newPricingSnapshotInput())
		require.NoError(t, err)
		require.Equal(t, int64(11), got.ID)
		require.Equal(t, service.PricingSnapshotStatusActive, got.Status)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects a malformed hash before touching the database", func(t *testing.T) {
		db, mock := newSQLMock(t)
		in := newPricingSnapshotInput()
		in.ContentSHA256 = "short"
		_, err := NewPricingSnapshotRepository(db).ActivateNew(context.Background(), in)
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("begin fails", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin().WillReturnError(errors.New("boom"))
		_, err := NewPricingSnapshotRepository(db).ActivateNew(context.Background(), newPricingSnapshotInput())
		require.Error(t, err)
	})

	t.Run("lock_timeout fails", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnError(errors.New("boom"))
		mock.ExpectRollback()
		_, err := NewPricingSnapshotRepository(db).ActivateNew(context.Background(), newPricingSnapshotInput())
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("supersede fails", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`UPDATE pricing_snapshots`).WillReturnError(errors.New("boom"))
		mock.ExpectRollback()
		_, err := NewPricingSnapshotRepository(db).ActivateNew(context.Background(), newPricingSnapshotInput())
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("concurrent activation loses on the unique index", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`UPDATE pricing_snapshots`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(insertRe).WillReturnError(&pq.Error{Code: "23505"})
		mock.ExpectRollback()
		_, err := NewPricingSnapshotRepository(db).ActivateNew(context.Background(), newPricingSnapshotInput())
		require.ErrorIs(t, err, service.ErrPricingSnapshotConflict)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("insert fails", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`UPDATE pricing_snapshots`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(insertRe).WillReturnError(errors.New("boom"))
		mock.ExpectRollback()
		_, err := NewPricingSnapshotRepository(db).ActivateNew(context.Background(), newPricingSnapshotInput())
		require.Error(t, err)
		require.NotErrorIs(t, err, service.ErrPricingSnapshotConflict)
	})

	t.Run("commit fails", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`UPDATE pricing_snapshots`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(insertRe).WillReturnRows(row())
		mock.ExpectCommit().WillReturnError(errors.New("boom"))
		_, err := NewPricingSnapshotRepository(db).ActivateNew(context.Background(), newPricingSnapshotInput())
		require.Error(t, err)
		require.NotErrorIs(t, err, service.ErrPricingSnapshotConflict)
	})

	t.Run("commit loses on the unique index", func(t *testing.T) {
		db, mock := newSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`UPDATE pricing_snapshots`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(insertRe).WillReturnRows(row())
		mock.ExpectCommit().WillReturnError(&pq.Error{Code: "23505"})
		_, err := NewPricingSnapshotRepository(db).ActivateNew(context.Background(), newPricingSnapshotInput())
		require.ErrorIs(t, err, service.ErrPricingSnapshotConflict)
	})
}

// W6-M5（迁移 216，PR9）：快照表只新增对象，不改任何现有表，也不写数据。
func TestW6PricingSnapshotsMigrationShape(t *testing.T) {
	const name = "216_w6_pricing_snapshots.sql"
	code := sqlWithoutComments(readMigrationForTest(t, name))

	require.Contains(t, code, "SET LOCAL lock_timeout")
	require.Contains(t, code, "SET LOCAL statement_timeout")
	require.Contains(t, code, "CREATE TABLE IF NOT EXISTS pricing_snapshots")
	require.Contains(t, code, "CREATE TABLE IF NOT EXISTS pricing_snapshot_diffs")
	require.Contains(t, code, "payload_gz            BYTEA        NOT NULL")
	require.Contains(t, code, "CHECK (source IN ('bootstrap', 'remote', 'merged'))")
	require.Contains(t, code, "CHECK (status IN ('candidate', 'active', 'superseded', 'rejected'))")
	// 生效指针：同时最多一个 active；候选按内容哈希去重。
	require.Contains(t, code, "CREATE UNIQUE INDEX IF NOT EXISTS uq_pricing_snapshots_one_active")
	require.Contains(t, code, "ON pricing_snapshots ((status)) WHERE status = 'active'")
	require.Contains(t, code, "CREATE UNIQUE INDEX IF NOT EXISTS uq_pricing_snapshots_candidate_sha")
	require.Contains(t, code, "ON pricing_snapshots (content_sha256) WHERE status = 'candidate'")
	// 差异行随候选快照级联删除，是唯一的外键。
	require.Equal(t, 1, strings.Count(code, "REFERENCES"))
	require.Contains(t, code, "REFERENCES pricing_snapshots(id) ON DELETE CASCADE")

	forbidden := regexp.MustCompile(`(?i)\b(DROP\s|TRUNCATE|ALTER\s+TABLE|INSERT\s+INTO|UPDATE\s+\w+\s+SET|DELETE\s+FROM|CONCURRENTLY)\b`)
	require.Empty(t, forbidden.FindString(code), "迁移只能新增对象，不改旧表、不写数据")

	entries, err := os.ReadDir(filepath.Join("..", "..", "migrations"))
	require.NoError(t, err)
	var sameNumber []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "216_") {
			sameNumber = append(sameNumber, e.Name())
		}
	}
	require.Equal(t, []string{name}, sameNumber, "迁移号 216 只能有一个文件")
}
