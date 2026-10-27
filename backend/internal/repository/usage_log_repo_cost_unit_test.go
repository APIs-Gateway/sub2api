package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// usage_logs.cost_unit 的写入：CNY 模式写 1，USD 模式写 NULL。
// 所有 INSERT 变体（单条、批量 CTE、best-effort 批量、best-effort 单条兜底）都共用
// prepareUsageLogInsert 产出的 args，所以这里分别验证「盖章」本身和每个变体把它绑到了正确位置。

type costUnitMode struct {
	name string
	unit service.CreditUnit
	want sql.NullInt16 // prepare 产出的参数
	// wantDriver 是 sqlmock 看到的驱动值：NULL → nil，1 → int64(1)。
	wantDriver driver.Value
}

func costUnitModes() []costUnitMode {
	return []costUnitMode{
		{
			name:       "usd_writes_null",
			unit:       service.DefaultCreditUnit(),
			want:       sql.NullInt16{},
			wantDriver: nil,
		},
		{
			name:       "cny_writes_1",
			unit:       service.CreditUnit{Currency: service.CreditCurrencyCNY, LegacyDivisor: 13},
			want:       sql.NullInt16{Int16: 1, Valid: true},
			wantDriver: int64(1),
		},
	}
}

func costUnitTestLog(requestID string) *service.UsageLog {
	return &service.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		AccountID:      3,
		RequestID:      requestID,
		Model:          "gpt-5",
		RequestedModel: "gpt-5",
		TotalCost:      1,
		ActualCost:     0.0769,
		CreatedAt:      time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
	}
}

func TestPrepareUsageLogInsert_CostUnitFollowsCreditUnit(t *testing.T) {
	n := len(usageLogInsertArgTypes)
	require.Equal(t, "smallint", usageLogInsertArgTypes[n-1], "cost_unit 必须是最后一列、smallint")

	for _, tc := range costUnitModes() {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(service.SetCreditUnitForTest(tc.unit))

			log := costUnitTestLog("req-cost-unit")
			prepared := prepareUsageLogInsert(log)
			require.Len(t, prepared.args, n)
			require.Equal(t, tc.want, prepared.args[n-1])
			if tc.want.Valid {
				require.NotNil(t, log.CostUnit)
				require.Equal(t, service.UsageLogCostUnitCNY, *log.CostUnit)
			} else {
				require.Nil(t, log.CostUnit)
			}
		})
	}
}

// 调用方预先填的 CostUnit 一律被覆盖：写入层是唯一赋值点，业务代码填错不会写进库。
func TestPrepareUsageLogInsert_CostUnitIgnoresCallerValue(t *testing.T) {
	n := len(usageLogInsertArgTypes)
	one := service.UsageLogCostUnitCNY
	bogus := int16(7)

	t.Run("usd_overrides_caller_value", func(t *testing.T) {
		t.Cleanup(service.SetCreditUnitForTest(service.DefaultCreditUnit()))
		log := costUnitTestLog("req-usd-override")
		log.CostUnit = &one
		prepared := prepareUsageLogInsert(log)
		require.Equal(t, sql.NullInt16{}, prepared.args[n-1])
		require.Nil(t, log.CostUnit)
	})
	t.Run("cny_overrides_caller_value", func(t *testing.T) {
		t.Cleanup(service.SetCreditUnitForTest(service.CreditUnit{Currency: service.CreditCurrencyCNY, LegacyDivisor: 13}))
		log := costUnitTestLog("req-cny-override")
		log.CostUnit = &bogus
		prepared := prepareUsageLogInsert(log)
		require.Equal(t, sql.NullInt16{Int16: 1, Valid: true}, prepared.args[n-1])
		require.NotNil(t, log.CostUnit)
		require.Equal(t, one, *log.CostUnit)
	})
}

// sqlmock 的期望参数：前 56 个不关心，最后一个（cost_unit）必须精确匹配。
func costUnitExpectedArgs(last driver.Value) []driver.Value {
	args := make([]driver.Value, len(usageLogInsertArgTypes))
	for i := range args {
		args[i] = sqlmock.AnyArg()
	}
	args[len(args)-1] = last
	return args
}

// 路径 1：单条写入（createSingle，事务内或无 request_id 时走这里）。
func TestUsageLogRepositoryCreate_SinglePathWritesCostUnit(t *testing.T) {
	for _, tc := range costUnitModes() {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(service.SetCreditUnitForTest(tc.unit))
			db, mock := newSQLMock(t)
			repo := &usageLogRepository{sql: db}

			createdAt := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
			mock.ExpectQuery("INSERT INTO usage_logs").
				WithArgs(costUnitExpectedArgs(tc.wantDriver)...).
				WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), createdAt))

			// 空 request_id：Create 直接走 createSingle，不进批处理队列。
			inserted, err := repo.Create(context.Background(), costUnitTestLog(""))
			require.NoError(t, err)
			require.True(t, inserted)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// 路径 4：best-effort 的单条兜底（批量失败后逐条重试）。
func TestExecUsageLogInsertNoResult_WritesCostUnit(t *testing.T) {
	for _, tc := range costUnitModes() {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(service.SetCreditUnitForTest(tc.unit))
			db, mock := newSQLMock(t)
			prepared := prepareUsageLogInsert(costUnitTestLog("req-exec"))

			mock.ExpectExec("INSERT INTO usage_logs").
				WithArgs(costUnitExpectedArgs(tc.wantDriver)...).
				WillReturnResult(sqlmock.NewResult(0, 1))

			require.NoError(t, execUsageLogInsertNoResult(context.Background(), db, prepared))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// 路径 2：批量写入（带 request_id 的 Create 经批处理器合并成一条 CTE）。
// 每行 = input_idx + 全部 args，cost_unit 是每行的最后一个值。
func TestBuildUsageLogBatchInsertQuery_BindsCostUnitPerRow(t *testing.T) {
	n := len(usageLogInsertArgTypes)
	for _, tc := range costUnitModes() {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(service.SetCreditUnitForTest(tc.unit))

			p1 := prepareUsageLogInsert(costUnitTestLog("req-batch-1"))
			p2 := prepareUsageLogInsert(costUnitTestLog("req-batch-2"))
			keys := []string{"k1", "k2"}
			query, args := buildUsageLogBatchInsertQuery(keys, map[string]usageLogInsertPrepared{"k1": p1, "k2": p2})

			rowWidth := n + 1 // input_idx + args
			require.Len(t, args, 2*rowWidth)
			require.Equal(t, tc.want, args[rowWidth-1], "第 1 行的 cost_unit")
			require.Equal(t, tc.want, args[2*rowWidth-1], "第 2 行的 cost_unit")
			require.Contains(t, query, "$58::smallint")
			require.Contains(t, query, "$116::smallint")
		})
	}
}

// 路径 3：best-effort 批量（CreateBestEffort 合并成一条 INSERT ... SELECT）。
func TestBuildUsageLogBestEffortInsertQuery_BindsCostUnitPerRow(t *testing.T) {
	n := len(usageLogInsertArgTypes)
	for _, tc := range costUnitModes() {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(service.SetCreditUnitForTest(tc.unit))

			p1 := prepareUsageLogInsert(costUnitTestLog("req-be-1"))
			p2 := prepareUsageLogInsert(costUnitTestLog("req-be-2"))
			query, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{p1, p2})

			require.Len(t, args, 2*n)
			require.Equal(t, tc.want, args[n-1], "第 1 行的 cost_unit")
			require.Equal(t, tc.want, args[2*n-1], "第 2 行的 cost_unit")
			require.Contains(t, query, "$57::smallint")
			require.Contains(t, query, "$114::smallint")
		})
	}
}

// 8 个 INSERT / CTE 变体必须都列出 cost_unit、占位符覆盖到 $57：漏一个会让线上写 usage 失败，
// 或者更糟——列和值错位，cost_unit 写进别的列。
func TestUsageLogInsertSQL_CostUnitInAllVariants(t *testing.T) {
	src, err := os.ReadFile("usage_log_repo.go")
	require.NoError(t, err)
	text := string(src)
	require.Len(t, regexp.MustCompile(`(?m)^\t+served_route_source,$`).FindAllString(text, -1), 8)
	require.Len(t, regexp.MustCompile(`(?m)^\t+cost_unit$`).FindAllString(text, -1), 8)
	require.Equal(t, 2, strings.Count(text, "$53, $54, $55, $56, $57\n"))
	require.Contains(t, text, "served_route_source, cost_unit\"", "usageLogSelectColumns 必须以 cost_unit 结尾")
}

func TestScanUsageLog_CostUnit(t *testing.T) {
	cols := strings.Split(usageLogSelectColumns, ", ")
	n := len(cols)
	require.Equal(t, "cost_unit", cols[n-1])

	// 历史行：NULL => nil
	sc := &zeroValueScanner{}
	log, err := scanUsageLog(sc)
	require.NoError(t, err)
	require.Equal(t, n, sc.gotDest, "scanUsageLog must scan exactly the selected columns")
	require.Nil(t, log.CostUnit)

	// 人民币行
	sc = &zeroValueScanner{override: map[int]any{n - 1: sql.NullInt16{Int16: 1, Valid: true}}}
	log, err = scanUsageLog(sc)
	require.NoError(t, err)
	require.NotNil(t, log.CostUnit)
	require.Equal(t, service.UsageLogCostUnitCNY, *log.CostUnit)
}
