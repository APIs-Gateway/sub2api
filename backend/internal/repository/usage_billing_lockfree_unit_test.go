//go:build unit

package repository

import (
	"context"
	"database/sql"
	"math/rand"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func newLFMock(t *testing.T) (*usageBillingRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &usageBillingRepository{db: db}, mock
}

func expectClaim(mock sqlmock.Sqlmock, requestID string, keyID int64) {
	mock.ExpectQuery("INSERT INTO usage_billing_dedup").
		WithArgs(requestID, keyID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery("SELECT request_fingerprint.*FROM usage_billing_dedup_archive").
		WithArgs(requestID, keyID).WillReturnError(sql.ErrNoRows)
}

// 无卡：dedup → 无锁查卡 → UPDATE users（第一条锁行语句）→ api_keys 额度 → api_keys 限速 → accounts 额度 → COMMIT。
// 锁序与加锁结算一致（users 在 api_keys 之前），全程没有 SELECT ... FOR UPDATE。
func TestUsageBillingApply_LockFreeNoCardSQLOrder(t *testing.T) {
	repo, mock := newLFMock(t)
	mock.ExpectBegin()
	expectClaim(mock, "lf-1", 7)
	mock.ExpectQuery("SELECT EXISTS.*FROM user_subscriptions.*status = 'active' AND deleted_at IS NULL").
		WithArgs(int64(3)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("UPDATE users.*SET balance = balance - \\$1.*RETURNING balance::text").
		WithArgs(3.0, int64(3)).WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow("97.00000000"))
	mock.ExpectQuery("UPDATE api_keys.*SET quota_used").
		WithArgs(2.0, int64(7), service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted).
		WillReturnRows(sqlmock.NewRows([]string{"exhausted"}).AddRow(false))
	mock.ExpectExec("UPDATE api_keys SET.*usage_5h").
		WithArgs(2.0, int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res, err := repo.Apply(context.Background(), &service.UsageBillingCommand{
		RequestID: "lf-1", APIKeyID: 7, UserID: 3, OfficialCost: 2, RateMultiplier: 1.5,
		APIKeyQuotaCost: 2, APIKeyRateLimitCost: 2, BillingLockFree: true,
	})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.NotNil(t, res.NewBalance)
	require.Equal(t, 97.0, *res.NewBalance)
	require.NotNil(t, res.WalletDebit)
	require.Equal(t, 3.0, *res.WalletDebit)
	require.False(t, res.OverdraftApplied)
	require.Nil(t, res.SubscriptionID)
	require.Empty(t, res.DepletedSubscriptionGroupIDs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageBillingApply_LockFreeUserNotFoundRollsBack(t *testing.T) {
	repo, mock := newLFMock(t)
	mock.ExpectBegin()
	expectClaim(mock, "lf-2", 7)
	mock.ExpectQuery("SELECT EXISTS.*FROM user_subscriptions").
		WithArgs(int64(3)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("UPDATE users.*SET balance = balance - \\$1").
		WithArgs(1.0, int64(3)).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	_, err := repo.Apply(context.Background(), &service.UsageBillingCommand{
		RequestID: "lf-2", APIKeyID: 7, UserID: 3, OfficialCost: 1, BillingLockFree: true,
	})
	require.ErrorIs(t, err, service.ErrUserNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

// 零金额：与加锁结算一样不碰 users，也不查卡。
func TestUsageBillingApply_LockFreeZeroCostTouchesNeitherUserNorCard(t *testing.T) {
	repo, mock := newLFMock(t)
	mock.ExpectBegin()
	expectClaim(mock, "lf-3", 7)
	mock.ExpectQuery("UPDATE api_keys.*SET quota_used").
		WithArgs(0.5, int64(7), service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted).
		WillReturnRows(sqlmock.NewRows([]string{"exhausted"}).AddRow(false))
	mock.ExpectCommit()
	res, err := repo.Apply(context.Background(), &service.UsageBillingCommand{
		RequestID: "lf-3", APIKeyID: 7, UserID: 3, APIKeyQuotaCost: 0.5, BillingLockFree: true,
	})
	require.NoError(t, err)
	require.Nil(t, res.NewBalance)
	require.NoError(t, mock.ExpectationsWereMet())
}

// 有卡：查到卡后立刻回到原加锁流程（第一条就是 SELECT users FOR UPDATE）。
func TestUsageBillingApply_LockFreeWithCardFallsBackToLockedFlow(t *testing.T) {
	repo, mock := newLFMock(t)
	mock.ExpectBegin()
	expectClaim(mock, "lf-4", 7)
	mock.ExpectQuery("SELECT EXISTS.*FROM user_subscriptions").
		WithArgs(int64(3)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("SELECT balance, monthly_overdraft_count, monthly_overdraft_month.*FROM users.*FOR UPDATE").
		WithArgs(int64(3)).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	_, err := repo.Apply(context.Background(), &service.UsageBillingCommand{
		RequestID: "lf-4", APIKeyID: 7, UserID: 3, OfficialCost: 1, BillingLockFree: true,
	})
	require.ErrorIs(t, err, service.ErrUserNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

// 带在途预占 ID：consume 本来就 FOR UPDATE users，短持锁无收益，整笔走加锁流程（连查卡都不查）。
func TestUsageBillingApply_LockFreeWithObligationUsesLockedFlow(t *testing.T) {
	repo, mock := newLFMock(t)
	mock.ExpectBegin()
	mock.ExpectExec("SAVEPOINT billing_inflight_claim").WillReturnResult(sqlmock.NewResult(0, 0))
	expectClaim(mock, "lf-5", 7)
	mock.ExpectQuery("SELECT balance, monthly_overdraft_count, monthly_overdraft_month.*FROM users.*FOR UPDATE").
		WithArgs(int64(3)).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	_, err := repo.Apply(context.Background(), &service.UsageBillingCommand{
		RequestID: "lf-5", APIKeyID: 7, UserID: 3, OfficialCost: 1, BillingLockFree: true, InflightObligationID: "lease-1",
	})
	require.ErrorIs(t, err, service.ErrUserNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

// BillingLockFree=false：语句序列与原版一致，第一条副作用就是 SELECT users FOR UPDATE，没有任何多出来的查询。
func TestUsageBillingApply_LockedFlowUnchangedWhenFlagFalse(t *testing.T) {
	repo, mock := newLFMock(t)
	mock.ExpectBegin()
	expectClaim(mock, "lf-6", 7)
	mock.ExpectQuery("SELECT balance, monthly_overdraft_count, monthly_overdraft_month.*FROM users.*FOR UPDATE").
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "monthly_overdraft_count", "monthly_overdraft_month"}).AddRow(10.0, 0, ""))
	mock.ExpectQuery("SELECT id,.*FROM user_subscriptions.*FOR UPDATE").
		WithArgs(int64(3)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("UPDATE users.*SET balance = \\$1").
		WithArgs(8.0, int64(3)).WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(8.0))
	mock.ExpectCommit()
	res, err := repo.Apply(context.Background(), &service.UsageBillingCommand{
		RequestID: "lf-6", APIKeyID: 7, UserID: 3, OfficialCost: 2, RateMultiplier: 1,
	})
	require.NoError(t, err)
	require.Equal(t, 2.0, *res.WalletDebit)
	require.NoError(t, mock.ExpectationsWereMet())
}

// SettleWindow 无卡分支的扣款额与起始余额无关（正、零、负、扣完变负），且恒等于 cost×multiplier。
func TestSettleWindowNoCardDebitIsIndependentOfStartingBalance(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 20000; i++ {
		var b float64
		switch i % 4 {
		case 0:
			b = float64(rng.Intn(2_000_000_000)) / 1e8 // 小正余额
		case 1:
			b = 0
		case 2:
			b = -float64(rng.Intn(2_000_000_000)) / 1e8 // 负余额
		default:
			b = (rng.Float64() - 0.3) * 5000
		}
		cost := float64(rng.Intn(500_000_000)+1) / 1e8
		mult := []float64{1, 0.5, 1.3, 2.7, 0.01, 3}[rng.Intn(6)]
		w := service.WalletState{Balance: b}
		service.SettleWindow(nil, &w, cost, mult, time.Now())
		debit := b - w.Balance
		require.InDelta(t, cost*mult, debit, 1e-9, "b=%v cost=%v mult=%v", b, cost, mult)
	}
}

// 特权路径「b - Quantize(charge)」与加锁路径「Go 里算 b-charge 再由 PG 舍入到 8 位」：
// 起始余额在 8 位刻度上时，除第 9 位平局外逐位一致；平局上最多差 1e-8。
func TestLockFreeDeltaMatchesLockedRoundedBalance(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	exact, tie := 0, 0
	for i := 0; i < 50000; i++ {
		b := float64(rng.Intn(2_000_000_000)-1_000_000_000) / 1e8
		cost := float64(rng.Intn(900_000_000)+1) / 1e9 // 9 位小数，容易产生平局
		mult := []float64{1, 0.5, 1.3, 2.7, 0.03, 3}[rng.Intn(6)]
		w := service.WalletState{Balance: b}
		service.SettleWindow(nil, &w, cost, mult, time.Now())
		legacy := decimal.NewFromFloat(w.Balance).Round(8) // PG 对 float8 参数转 numeric(20,8) 的舍入
		lockFree := decimal.NewFromFloat(b).Sub(decimal.NewFromFloat(service.QuantizeUsageBillingAmount(cost * mult)))
		diff := legacy.Sub(lockFree).Abs()
		require.True(t, diff.LessThanOrEqual(decimal.New(1, -8)), "b=%v cost=%v mult=%v legacy=%s lockfree=%s", b, cost, mult, legacy, lockFree)
		if diff.IsZero() {
			exact++
		} else {
			tie++
		}
	}
	t.Logf("exact=%d tie-differences=%d", exact, tie)
	require.Greater(t, exact, 40000)
}
