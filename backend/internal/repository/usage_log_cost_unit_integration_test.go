//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// usage_logs.cost_unit 在真实 PostgreSQL 上的往返：每条写入路径、两种币种模式都要落对。
// 单测用 sqlmock 验证了参数绑定，这里验证 INSERT 的列清单和 VALUES / SELECT 的顺序真的对得上。

func costUnitReadBack(t *testing.T, requestID string, apiKeyID int64) sql.NullInt16 {
	t.Helper()
	var got sql.NullInt16
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT cost_unit FROM usage_logs WHERE request_id = $1 AND api_key_id = $2`, requestID, apiKeyID,
	).Scan(&got))
	return got
}

func TestUsageLogRepo_CostUnitAllWritePaths(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("cost-unit-%d@example.com", time.Now().UnixNano())})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-cost-unit-" + uuid.NewString(), Name: "k"})
	account := mustCreateAccount(t, client, &service.Account{Name: "acc-cost-unit-" + uuid.NewString()})

	newLog := func(requestID string) *service.UsageLog {
		return &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID, RequestID: requestID,
			Model: "gpt-5", InputTokens: 1, TotalCost: 1, ActualCost: 0.0769, CreatedAt: time.Now().UTC(),
		}
	}

	for _, tc := range costUnitModes() {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(service.SetCreditUnitForTest(tc.unit))
			repo := newUsageLogRepositoryWithSQL(client, integrationDB)

			// 1) 单条：事务上下文里的 Create 直接走 createSingle。
			tx, err := client.Tx(ctx)
			require.NoError(t, err)
			singleID := uuid.NewString()
			inserted, err := repo.Create(dbent.NewTxContext(ctx, tx), newLog(singleID))
			require.NoError(t, err)
			require.True(t, inserted)
			require.NoError(t, tx.Commit())
			require.Equal(t, tc.want, costUnitReadBack(t, singleID, apiKey.ID), "single")

			// 2) 批量 CTE：带 request_id 的 Create 经批处理器。
			batchID := uuid.NewString()
			batchLog := newLog(batchID)
			inserted, err = repo.Create(ctx, batchLog)
			require.NoError(t, err)
			require.True(t, inserted)
			require.Equal(t, tc.want, costUnitReadBack(t, batchID, apiKey.ID), "batch")

			// 读回：scanUsageLog 把列读进 UsageLog.CostUnit，和写入时盖的章一致。
			got, err := repo.GetByID(ctx, batchLog.ID)
			require.NoError(t, err)
			if tc.want.Valid {
				require.NotNil(t, got.CostUnit)
				require.Equal(t, service.UsageLogCostUnitCNY, *got.CostUnit)
			} else {
				require.Nil(t, got.CostUnit)
			}

			// 3) best-effort 批量。
			bestEffortID := uuid.NewString()
			require.NoError(t, repo.CreateBestEffort(ctx, newLog(bestEffortID)))
			require.Eventually(t, func() bool {
				var n int
				err := integrationDB.QueryRowContext(ctx,
					`SELECT COUNT(*) FROM usage_logs WHERE request_id = $1 AND api_key_id = $2`, bestEffortID, apiKey.ID).Scan(&n)
				return err == nil && n == 1
			}, 3*time.Second, 20*time.Millisecond)
			require.Equal(t, tc.want, costUnitReadBack(t, bestEffortID, apiKey.ID), "best-effort batch")

			// 4) best-effort 单条兜底。
			execID := uuid.NewString()
			require.NoError(t, execUsageLogInsertNoResult(ctx, integrationDB, prepareUsageLogInsert(newLog(execID))))
			require.Equal(t, tc.want, costUnitReadBack(t, execID, apiKey.ID), "best-effort single fallback")
		})
	}
}

// 迁移 203 在 PostgreSQL 上可重复执行：测试库启动时已经应用过一次，这里在事务里再跑两遍，
// 都不能报错，最终列是可空、无默认值的 smallint（有默认值会让 PG 重写整张大表）。
func TestUsageLogCostUnitMigration_IdempotentOnPostgres(t *testing.T) {
	ctx := context.Background()
	sqlText := readMigrationForTest(t, "203_usage_log_cost_unit.sql")

	tx := testTx(t)
	for i := 0; i < 2; i++ {
		_, err := tx.ExecContext(ctx, sqlText)
		require.NoError(t, err, "第 %d 次执行", i+1)
	}

	var dataType, nullable string
	var columnDefault sql.NullString
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'usage_logs' AND column_name = 'cost_unit'
	`).Scan(&dataType, &nullable, &columnDefault))
	require.Equal(t, "smallint", dataType)
	require.Equal(t, "YES", nullable)
	require.False(t, columnDefault.Valid, "cost_unit 不能有默认值")
}
