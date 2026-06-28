//go:build unit

package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

// 订阅生命周期（续费/转套餐/透支）service 入口的 guard / 纯映射分支单测。
// 这些 fail-fast 校验在触达 DB 仓库之前返回，可用零值 service 或 sqlite ent client 直接覆盖
// （白盒 service 测试不能 import repository——会构成测试导入环，故只覆盖触库前的分支）。

func newSubscriptionGuardsTestClient(t *testing.T) *dbent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", "file:subscription_lifecycle_guards?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestMapOverdraftErr_AllBranches(t *testing.T) {
	require.Equal(t, "OVERDRAFT_NO_ACTIVE_CARD", infraerrors.Reason(mapOverdraftErr(ErrOverdraftNoActiveCard)))
	require.Equal(t, "OVERDRAFT_DAILY_NOT_EXHAUSTED", infraerrors.Reason(mapOverdraftErr(ErrOverdraftDailyNotExhausted)))
	require.Equal(t, "OVERDRAFT_MONTHLY_LIMIT", infraerrors.Reason(mapOverdraftErr(ErrOverdraftMonthlyLimit)))
	require.Equal(t, "OVERDRAFT_NO_FUTURE_DAY", infraerrors.Reason(mapOverdraftErr(ErrOverdraftNoFutureDay)))
	// default：非透支错误原样透传。
	sentinel := errors.New("some other failure")
	require.ErrorIs(t, mapOverdraftErr(sentinel), sentinel)
}

func TestManualOverdraft_GuardBranches(t *testing.T) {
	ctx := context.Background()

	// entClient 未配 → 配置错误。
	_, err := (&SubscriptionService{}).ManualOverdraft(ctx, 1)
	require.Error(t, err)

	// userID<=0 → INVALID_INPUT（entClient 已配，越过 nil 闸）。
	client := newSubscriptionGuardsTestClient(t)
	_, err = (&SubscriptionService{entClient: client}).ManualOverdraft(ctx, 0)
	require.Equal(t, "INVALID_INPUT", infraerrors.Reason(err))
}

func TestMonthlyOverdraftRemaining_Branches(t *testing.T) {
	ctx := context.Background()

	// entClient 未配 → 错误。
	_, err := (&SubscriptionService{}).MonthlyOverdraftRemaining(ctx, 1)
	require.Error(t, err)

	client := newSubscriptionGuardsTestClient(t)
	svc := &SubscriptionService{entClient: client}

	// 用户不存在 → Get 错误透传。
	_, err = svc.MonthlyOverdraftRemaining(ctx, 999999)
	require.Error(t, err)

	// 当月已用 2 次 → 剩 3。
	uThisMonth := client.User.Create().
		SetEmail("ovd-thismonth@example.com").
		SetPasswordHash("h").
		SetMonthlyOverdraftCount(2).
		SetMonthlyOverdraftMonth(CurrentEastMonthKey()).
		SaveX(ctx)
	remaining, err := svc.MonthlyOverdraftRemaining(ctx, uThisMonth.ID)
	require.NoError(t, err)
	require.Equal(t, MaxMonthlyOverdraftUses-2, remaining)

	// 跨月（记录的是旧月份）→ 计数视为 0 → 剩满额。
	uOldMonth := client.User.Create().
		SetEmail("ovd-oldmonth@example.com").
		SetPasswordHash("h").
		SetMonthlyOverdraftCount(5).
		SetMonthlyOverdraftMonth("200001"). // 东八区 YYYYMM、旧月份
		SaveX(ctx)
	remaining, err = svc.MonthlyOverdraftRemaining(ctx, uOldMonth.ID)
	require.NoError(t, err)
	require.Equal(t, MaxMonthlyOverdraftUses, remaining)

	// 当月计数超上限（异常脏数据）→ 剩余夹到 0，不为负。
	uOverflow := client.User.Create().
		SetEmail("ovd-overflow@example.com").
		SetPasswordHash("h").
		SetMonthlyOverdraftCount(MaxMonthlyOverdraftUses + 10).
		SetMonthlyOverdraftMonth(CurrentEastMonthKey()).
		SaveX(ctx)
	remaining, err = svc.MonthlyOverdraftRemaining(ctx, uOverflow.ID)
	require.NoError(t, err)
	require.Equal(t, 0, remaining)
}

func TestQuoteRenewOrder_GuardBranches(t *testing.T) {
	ctx := context.Background()
	svc := &SubscriptionService{} // 触库前返回，无需 deps

	// userID<=0 → INVALID_INPUT。
	_, err := svc.QuoteRenewOrder(ctx, 0, 30)
	require.Equal(t, "INVALID_INPUT", infraerrors.Reason(err))

	// 续费天数非整月 / 越界 → INVALID_SUBSCRIPTION_PARAMS（TMin=30,TStep=30）。
	_, err = svc.QuoteRenewOrder(ctx, 1, 7)
	require.Equal(t, "INVALID_SUBSCRIPTION_PARAMS", infraerrors.Reason(err))
}

func TestQuoteChangePlanOrder_UserIDGuard(t *testing.T) {
	ctx := context.Background()
	_, err := (&SubscriptionService{}).QuoteChangePlanOrder(ctx, 0, 20, 30)
	require.Equal(t, "INVALID_INPUT", infraerrors.Reason(err))
}

func TestApplyChangePlanFromOrder_GuardBranches(t *testing.T) {
	ctx := context.Background()

	// entClient 未配 → 配置错误。
	_, err := (&SubscriptionService{}).ApplyChangePlanFromOrder(ctx, 1, 20, 30)
	require.Error(t, err)

	// 非法入参（旧卡 ID / 新 D / 新 T 任一 <=0）→ INVALID_INPUT（触库前返回）。
	client := newSubscriptionGuardsTestClient(t)
	svc := &SubscriptionService{entClient: client}
	_, err = svc.ApplyChangePlanFromOrder(ctx, 0, 20, 30)
	require.Equal(t, "INVALID_INPUT", infraerrors.Reason(err))
	_, err = svc.ApplyChangePlanFromOrder(ctx, 1, 0, 30)
	require.Equal(t, "INVALID_INPUT", infraerrors.Reason(err))
	_, err = svc.ApplyChangePlanFromOrder(ctx, 1, 20, 0)
	require.Equal(t, "INVALID_INPUT", infraerrors.Reason(err))
}

func TestApplyRenewFromOrder_GuardBranches(t *testing.T) {
	ctx := context.Background()

	// entClient 未配 → 配置错误。
	_, err := (&SubscriptionService{}).ApplyRenewFromOrder(ctx, 1, 30)
	require.Error(t, err)

	// subscriptionID<=0 / addDays<=0 → INVALID_INPUT（entClient 已配，越过 nil 闸，触库前返回）。
	client := newSubscriptionGuardsTestClient(t)
	svc := &SubscriptionService{entClient: client}
	_, err = svc.ApplyRenewFromOrder(ctx, 0, 30)
	require.Equal(t, "INVALID_INPUT", infraerrors.Reason(err))
	_, err = svc.ApplyRenewFromOrder(ctx, 1, 0)
	require.Equal(t, "INVALID_INPUT", infraerrors.Reason(err))
}
