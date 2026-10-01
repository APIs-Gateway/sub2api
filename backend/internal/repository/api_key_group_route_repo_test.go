package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var routeRowColumns = []string{"id", "api_key_id", "group_id", "platform", "source", "placement", "position", "note", "created_by", "created_at", "updated_at"}

func TestAPIKeyGroupRouteRepo_ListByKey(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewAPIKeyGroupRouteRepository(db)
	ts := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	// 全部 source：不带 source 条件；同时校验软删除过滤的 JOIN 存在
	mock.ExpectQuery(`(?s)FROM api_key_group_routes r\s+JOIN api_keys k ON k.id = r.api_key_id AND k.deleted_at IS NULL\s+JOIN groups g ON g.id = r.group_id AND g.deleted_at IS NULL\s+WHERE r.api_key_id = \$1 ORDER BY`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows(routeRowColumns).
			AddRow(int64(1), int64(7), int64(2), "openai", "user", "tail", 0, nil, nil, ts, ts).
			AddRow(int64(2), int64(7), int64(3), "openai", "admin", "head", 0, "pin", int64(99), ts, ts))
	got, err := repo.ListByKey(context.Background(), 7, "")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "", got[0].Note)
	require.Nil(t, got[0].CreatedBy)
	require.Equal(t, "pin", got[1].Note)
	require.Equal(t, int64(99), *got[1].CreatedBy)
	require.Equal(t, service.RoutePlacementHead, got[1].Placement)

	// 指定 source：SQL 层强制 WHERE source（用户端读取不得依赖应用层过滤）
	mock.ExpectQuery(`AND r.source = \$2`).
		WithArgs(int64(7), "user").
		WillReturnRows(sqlmock.NewRows(routeRowColumns))
	got, err = repo.ListByKey(context.Background(), 7, "user")
	require.NoError(t, err)
	require.Empty(t, got)

	mock.ExpectQuery(`FROM api_key_group_routes`).WillReturnError(errors.New("boom"))
	_, err = repo.ListByKey(context.Background(), 7, "")
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAPIKeyGroupRouteRepo_ReplaceChain_LocksThenReplaces(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewAPIKeyGroupRouteRepository(db)
	by := int64(5)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT group_id FROM api_keys WHERE id = \$1 AND deleted_at IS NULL FOR UPDATE`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(1)))
	mock.ExpectExec(`DELETE FROM api_key_group_routes WHERE api_key_id = \$1 AND source = \$2`).
		WithArgs(int64(7), "admin").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO api_key_group_routes`).
		WithArgs(int64(7), int64(2), "openai", "admin", "head", 0, "pin", by).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO api_key_group_routes`).
		WithArgs(int64(7), int64(3), "openai", "admin", "tail", 0, nil, nil).
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()

	err := repo.ReplaceChain(context.Background(), service.ReplaceRoutesParams{
		APIKeyID: 7, Source: "admin", ExpectedPrimaryGroupID: 1,
		Items: []service.RouteItem{
			{GroupID: 2, Platform: "openai", Placement: "head", Position: 0, Note: "pin", CreatedBy: &by, Source: "user"}, // Source 会被覆盖
			{GroupID: 3, Platform: "openai", Placement: "tail", Position: 0},
		},
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAPIKeyGroupRouteRepo_ReplaceChain_KeyMissingOrChanged(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewAPIKeyGroupRouteRepository(db)

	// Key 不存在 / 已软删除
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	err := repo.ReplaceChain(context.Background(), service.ReplaceRoutesParams{APIKeyID: 7, Source: "user"})
	require.ErrorIs(t, err, service.ErrAPIKeyNotFound)

	// 主分组被并发修改：不得写入
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(9)))
	mock.ExpectRollback()
	err = repo.ReplaceChain(context.Background(), service.ReplaceRoutesParams{APIKeyID: 7, Source: "user", ExpectedPrimaryGroupID: 1})
	require.ErrorIs(t, err, service.ErrFallbackKeyChanged)

	// Key 被取消分组
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(nil))
	mock.ExpectRollback()
	err = repo.ReplaceChain(context.Background(), service.ReplaceRoutesParams{APIKeyID: 7, Source: "user", ExpectedPrimaryGroupID: 1})
	require.ErrorIs(t, err, service.ErrFallbackKeyChanged)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAPIKeyGroupRouteRepo_ReplaceChain_InsertErrorRollsBack(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewAPIKeyGroupRouteRepository(db)

	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(1)))
	mock.ExpectExec(`DELETE FROM api_key_group_routes`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO api_key_group_routes`).WillReturnError(errors.New("unique violation"))
	mock.ExpectRollback()

	err := repo.ReplaceChain(context.Background(), service.ReplaceRoutesParams{
		APIKeyID: 7, Source: "user", ExpectedPrimaryGroupID: 1,
		Items: []service.RouteItem{{GroupID: 2, Platform: "openai", Placement: "tail"}},
	})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAPIKeyGroupRouteRepo_ApplyPrimaryGroupChange(t *testing.T) {
	ts := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	selectRows := func() *sqlmock.Rows {
		return sqlmock.NewRows(routeRowColumns).
			AddRow(int64(1), int64(7), int64(2), "openai", "user", "tail", 0, nil, nil, ts, ts).
			AddRow(int64(2), int64(7), int64(3), "openai", "user", "tail", 1, nil, nil, ts, ts)
	}

	t.Run("new primary was in the chain: delete and compact", func(t *testing.T) {
		db, mock := newSQLMock(t)
		repo := NewAPIKeyGroupRouteRepository(db)
		mock.ExpectBegin()
		mock.ExpectQuery(`FOR UPDATE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(2)))
		mock.ExpectQuery(`FROM api_key_group_routes WHERE api_key_id = \$1 ORDER BY id`).WithArgs(int64(7)).WillReturnRows(selectRows())
		mock.ExpectExec(`DELETE FROM api_key_group_routes WHERE api_key_id = \$1`).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 2))
		// 剩下的 group 3 压实到 position 0，并保留原 created_at
		mock.ExpectExec(`INSERT INTO api_key_group_routes`).
			WithArgs(int64(7), int64(3), "openai", "user", "tail", 0, nil, nil, ts).
			WillReturnResult(sqlmock.NewResult(3, 1))
		mock.ExpectCommit()
		require.NoError(t, repo.ApplyPrimaryGroupChange(context.Background(), 7, 2, "openai"))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("nothing to change: commit without rewriting", func(t *testing.T) {
		db, mock := newSQLMock(t)
		repo := NewAPIKeyGroupRouteRepository(db)
		mock.ExpectBegin()
		mock.ExpectQuery(`FOR UPDATE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(9)))
		mock.ExpectQuery(`FROM api_key_group_routes WHERE api_key_id`).WithArgs(int64(7)).WillReturnRows(selectRows())
		mock.ExpectCommit()
		require.NoError(t, repo.ApplyPrimaryGroupChange(context.Background(), 7, 9, "openai"))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("key missing", func(t *testing.T) {
		db, mock := newSQLMock(t)
		repo := NewAPIKeyGroupRouteRepository(db)
		mock.ExpectBegin()
		mock.ExpectQuery(`FOR UPDATE`).WithArgs(int64(7)).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		require.ErrorIs(t, repo.ApplyPrimaryGroupChange(context.Background(), 7, 9, "openai"), service.ErrAPIKeyNotFound)
	})
}

func TestPlanPrimaryGroupChange(t *testing.T) {
	item := func(id, gid int64, platform, source, placement string, pos int) service.RouteItem {
		return service.RouteItem{ID: id, GroupID: gid, Platform: platform, Source: source, Placement: placement, Position: pos}
	}

	t.Run("no change keeps rows untouched", func(t *testing.T) {
		cur := []service.RouteItem{item(1, 2, "openai", "user", "tail", 0)}
		kept, changed := planPrimaryGroupChange(cur, 9, "openai")
		require.False(t, changed)
		require.Equal(t, cur, kept)
	})

	t.Run("removes the new primary from both sources and compacts per partition", func(t *testing.T) {
		cur := []service.RouteItem{
			item(1, 2, "openai", "user", "tail", 0),
			item(2, 3, "openai", "user", "tail", 1),
			item(3, 4, "openai", "user", "tail", 2),
			item(4, 3, "openai", "admin", "head", 0), // 同一个分组在 admin 链里也要删
			item(5, 5, "openai", "admin", "head", 1),
			item(6, 6, "openai", "admin", "tail", 0),
		}
		kept, changed := planPrimaryGroupChange(cur, 3, "openai")
		require.True(t, changed)
		got := map[int64]int{}
		for _, it := range kept {
			got[it.ID] = it.Position
		}
		require.Equal(t, map[int64]int{1: 0, 3: 1, 5: 0, 6: 0}, got)
	})

	t.Run("platform change clears user chain but keeps admin rows", func(t *testing.T) {
		cur := []service.RouteItem{
			item(1, 2, "openai", "user", "tail", 0),
			item(2, 3, "openai", "admin", "tail", 0),
		}
		kept, changed := planPrimaryGroupChange(cur, 50, "anthropic")
		require.True(t, changed)
		require.Len(t, kept, 1)
		require.Equal(t, service.RouteSourceAdmin, kept[0].Source)
		require.Equal(t, "openai", kept[0].Platform, "admin row keeps its platform => runtime treats it as invalid_platform")
	})

	t.Run("compaction is stable for equal positions", func(t *testing.T) {
		cur := []service.RouteItem{
			item(9, 2, "openai", "user", "tail", 3),
			item(8, 3, "openai", "user", "tail", 3),
			item(7, 4, "openai", "user", "tail", 1),
			item(6, 5, "openai", "user", "tail", 0),
		}
		kept, changed := planPrimaryGroupChange(cur, 5, "openai")
		require.True(t, changed)
		got := map[int64]int{}
		for _, it := range kept {
			got[it.ID] = it.Position
		}
		require.Equal(t, map[int64]int{7: 0, 8: 1, 9: 2}, got)
	})
}
