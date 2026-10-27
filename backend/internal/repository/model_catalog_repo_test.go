package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

var catalogCols = []string{"id", "model_key", "platform", "display_name", "aliases", "reference_model", "status", "note", "created_by", "created_at", "updated_at"}

func TestModelCatalogRepo_List(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewModelCatalogRepository(db)
	ts := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`FROM model_catalog\s+WHERE \(\$1::text = '' OR platform = \$1::text\) AND \(\$2::text = '' OR status = \$2::text\)\s+ORDER BY platform, model_key`).
		WithArgs("", "").
		WillReturnRows(sqlmock.NewRows(catalogCols).
			AddRow(int64(1), "gpt-5.5", "openai", "GPT 5.5", "{gpt-5-5,five}", "gpt-5.4", "active", "n", int64(9), ts, ts).
			AddRow(int64(2), "o3", "openai", "", "{}", nil, "draft", "", nil, ts, ts))
	got, err := repo.List(context.Background(), service.ModelCatalogFilter{})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, []string{"gpt-5-5", "five"}, got[0].Aliases)
	require.Equal(t, "gpt-5.4", *got[0].ReferenceModel)
	require.Equal(t, int64(9), *got[0].CreatedBy)
	require.Equal(t, service.ModelCatalogActive, got[0].Status)
	require.Equal(t, []string{}, got[1].Aliases)
	require.Nil(t, got[1].ReferenceModel)
	require.Nil(t, got[1].CreatedBy)

	mock.ExpectQuery(`FROM model_catalog`).WithArgs("openai", "retired").WillReturnRows(sqlmock.NewRows(catalogCols))
	got, err = repo.List(context.Background(), service.ModelCatalogFilter{Platform: "openai", Status: service.ModelCatalogRetired})
	require.NoError(t, err)
	require.Empty(t, got)

	mock.ExpectQuery(`FROM model_catalog`).WillReturnError(errors.New("boom"))
	_, err = repo.List(context.Background(), service.ModelCatalogFilter{})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestModelCatalogRepo_Create(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := NewModelCatalogRepository(db)
	ts := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	ref := "gpt-5.4"
	by := int64(3)

	mock.ExpectQuery(`INSERT INTO model_catalog .* RETURNING id, created_at, updated_at`).
		WithArgs("gpt-5.5", "openai", "GPT", sqlmock.AnyArg(), ref, "draft", "note", by).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(12), ts, ts))
	entry := &service.ModelCatalogEntry{ModelKey: "gpt-5.5", Platform: "openai", DisplayName: "GPT", Aliases: []string{"a"},
		ReferenceModel: &ref, Status: service.ModelCatalogDraft, Note: "note", CreatedBy: &by}
	require.NoError(t, repo.Create(context.Background(), entry))
	require.Equal(t, int64(12), entry.ID)
	require.Equal(t, ts, entry.CreatedAt)

	// 没有参考模型、没有创建人：写 NULL。
	mock.ExpectQuery(`INSERT INTO model_catalog`).
		WithArgs("o3", "openai", "", sqlmock.AnyArg(), nil, "active", "", nil).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(13), ts, ts))
	require.NoError(t, repo.Create(context.Background(), &service.ModelCatalogEntry{ModelKey: "o3", Platform: "openai", Aliases: []string{}, Status: service.ModelCatalogActive}))

	// 唯一冲突映射成业务错误。
	mock.ExpectQuery(`INSERT INTO model_catalog`).WillReturnError(&pq.Error{Code: "23505"})
	err := repo.Create(context.Background(), &service.ModelCatalogEntry{ModelKey: "o3", Platform: "openai", Aliases: []string{}, Status: service.ModelCatalogActive})
	require.ErrorIs(t, err, service.ErrModelCatalogExists)

	mock.ExpectQuery(`INSERT INTO model_catalog`).WillReturnError(errors.New("boom"))
	err = repo.Create(context.Background(), &service.ModelCatalogEntry{ModelKey: "x", Platform: "openai", Aliases: []string{}, Status: service.ModelCatalogActive})
	require.Error(t, err)
	require.NotErrorIs(t, err, service.ErrModelCatalogExists)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// 种子命令的数据库侧（S-13：只读事务、statement_timeout、dry-run 不写、输出带耗时）
// ---------------------------------------------------------------------------

func expectSeedReads(mock sqlmock.Sqlmock, timeoutMillis string) {
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL statement_timeout = ` + timeoutMillis).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT platform, model_key FROM model_catalog`).
		WillReturnRows(sqlmock.NewRows([]string{"platform", "model_key"}).AddRow("openai", "gpt-5.5"))
	mock.ExpectQuery(`FROM channel_model_pricing p\s+CROSS JOIN LATERAL jsonb_array_elements_text`).
		WillReturnRows(sqlmock.NewRows([]string{"platform", "model"}).
			AddRow("openai", "gpt-5.5").
			AddRow("anthropic", "Seed-Test-Model.1"))
}

func TestRunModelCatalogSeed_DryRunWritesNothing(t *testing.T) {
	db, mock := newSQLMock(t)
	expectSeedReads(mock, "60000")
	mock.ExpectRollback() // 只读事务一律回滚；没有 Commit，也没有任何写语句

	var out strings.Builder
	require.NoError(t, RunModelCatalogSeed(context.Background(), db, ModelCatalogSeedOptions{}, &out))
	text := out.String()

	require.Contains(t, text, "mode=dry-run")
	require.Contains(t, text, "statement_timeout=1m0s")
	require.Contains(t, text, "phase=read_catalog elapsed=")
	require.Contains(t, text, "phase=read_channel_pricing elapsed=")
	require.NotContains(t, text, "phase=read_usage_logs", "默认不扫 usage_logs")
	require.Contains(t, text, "plan: insert=")
	require.Contains(t, text, "+ anthropic/seed-test-model.1 sources=[channel_pricing]", "渠道定价里的名字规范化后登记")
	require.NotContains(t, text, "+ openai/gpt-5.5 ", "已登记的不再列出")
	require.Contains(t, text, "dry-run finished in")
	require.Contains(t, text, "nothing was written")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRunModelCatalogSeed_UsageScanIsOptInAndTimed(t *testing.T) {
	db, mock := newSQLMock(t)
	expectSeedReads(mock, "2000")
	mock.ExpectQuery(`FROM usage_logs u\s+JOIN groups g ON g.id = u.group_id\s+WHERE u.created_at >= NOW\(\) - \(\$1::int \* INTERVAL '1 day'\) AND u.model <> ''`).
		WithArgs(30).
		WillReturnRows(sqlmock.NewRows([]string{"platform", "model"}).AddRow("openai", "usage-only-model"))
	mock.ExpectRollback()

	var out strings.Builder
	require.NoError(t, RunModelCatalogSeed(context.Background(), db, ModelCatalogSeedOptions{UsageDays: 30, StatementTimeout: 2 * time.Second}, &out))
	text := out.String()
	require.Contains(t, text, "usage_days=30")
	require.Contains(t, text, "low-traffic window")
	require.Contains(t, text, "phase=read_usage_logs elapsed=")
	require.Contains(t, text, "+ openai/usage-only-model sources=[usage_logs]")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRunModelCatalogSeed_ApplyRegistersInSeparateTx(t *testing.T) {
	db, mock := newSQLMock(t)
	expectSeedReads(mock, "60000")
	mock.ExpectRollback() // 读取事务先结束

	// 与种子计划保持一致：期望的写入条数 = 计划里将登记的条数。
	candidates := append(service.DefaultModelCatalogSeedCandidates(), service.ModelCatalogSeedCandidate{
		Platform: "openai", Model: "gpt-5.5", Source: service.SeedSourceChannelPricing,
	}, service.ModelCatalogSeedCandidate{
		Platform: "anthropic", Model: "Seed-Test-Model.1", Source: service.SeedSourceChannelPricing,
	})
	plan := service.PlanModelCatalogSeed(candidates, map[string]struct{}{service.CatalogSeedKey("openai", "gpt-5.5"): {}})
	require.NotEmpty(t, plan.Insert)

	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout = '5s'`).WillReturnResult(sqlmock.NewResult(0, 0))
	for range plan.Insert {
		mock.ExpectExec(`INSERT INTO model_catalog .* ON CONFLICT \(platform, model_key\) DO NOTHING`).
			WillReturnResult(sqlmock.NewResult(1, 1))
	}
	mock.ExpectCommit()

	var out strings.Builder
	require.NoError(t, RunModelCatalogSeed(context.Background(), db, ModelCatalogSeedOptions{Apply: true}, &out))
	text := out.String()
	require.Contains(t, text, "mode=apply")
	require.Contains(t, text, "phase=insert elapsed=")
	require.Contains(t, text, "inserted=")
	require.Contains(t, text, "seed finished in")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRunModelCatalogSeed_ReadFailureStopsBeforeAnyWrite(t *testing.T) {
	db, mock := newSQLMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL statement_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`FROM model_catalog`).WillReturnError(errors.New("canceling statement due to statement timeout"))
	mock.ExpectRollback()

	var out strings.Builder
	err := RunModelCatalogSeed(context.Background(), db, ModelCatalogSeedOptions{Apply: true}, &out)
	require.ErrorContains(t, err, "statement timeout")
	require.NotContains(t, out.String(), "plan:")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyModelCatalogSeed_SkipsExistingKeys(t *testing.T) {
	db, mock := newSQLMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO model_catalog`).
		WithArgs("a", "openai", "A", sqlmock.AnyArg(), "active", "seed: default_models").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO model_catalog`).WillReturnResult(sqlmock.NewResult(0, 0)) // 并发里已被别人登记
	mock.ExpectCommit()

	n, err := applyModelCatalogSeed(context.Background(), db, []service.ModelCatalogSeedItem{
		{Platform: "openai", ModelKey: "a", DisplayName: "A", Sources: []string{service.SeedSourceDefaultModels}},
		{Platform: "openai", ModelKey: "b", Sources: []string{service.SeedSourceChannelPricing}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyModelCatalogSeed_FailureRollsBack(t *testing.T) {
	db, mock := newSQLMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO model_catalog`).WillReturnError(errors.New("boom"))
	mock.ExpectRollback()
	_, err := applyModelCatalogSeed(context.Background(), db, []service.ModelCatalogSeedItem{{Platform: "openai", ModelKey: "a"}})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
