//go:build unit

package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"math"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type approxFloatArg float64

func (a approxFloatArg) Match(v driver.Value) bool {
	f, ok := v.(float64)
	return ok && math.Abs(f-float64(a)) < 1e-9
}

func setRepoPromoForTest(t *testing.T, excluded string) {
	t.Helper()
	vals := map[string]string{
		service.SettingKeyPromoStartAt:                     time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
		service.SettingKeyPromoEndAt:                       time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		service.SettingKeyPromoSubscriptionDailyMultiplier: "1.5",
		service.SettingKeyPromoBalanceRateMultiplier:       "0.52",
		service.SettingKeyPromoExcludedUserIDs:             excluded,
	}
	require.NoError(t, service.SetPromoSettingsSource(func(ctx context.Context, keys []string) (map[string]string, error) { return vals, nil }))
	t.Cleanup(func() { _ = service.SetPromoSettingsSource(nil) })
}

var promoCardCols = []string{"id", "daily_limit_usd", "weekly_limit_usd", "monthly_limit_usd",
	"daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd",
	"daily_window_start", "weekly_window_start", "monthly_window_start", "expires_at", "status"}

// expectSettle 搭好 settleSubscriptionWindow 的 SQL 期望；card=nil 表示用户没有套餐卡。
// 返回的 sqlmock 已经 Begin 过，调用方直接拿 tx 调用。
func runPromoSettle(t *testing.T, userID int64, balance float64, card []driver.Value, officialCost, multiplier float64,
	wantBalance float64, wantCardUsage *[3]float64) (*subSettleResult, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT balance, monthly_overdraft_count, monthly_overdraft_month").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "c", "m"}).AddRow(balance, 0, ""))
	if card == nil {
		mock.ExpectQuery("SELECT id,.*FROM user_subscriptions").WithArgs(userID).WillReturnError(sql.ErrNoRows)
	} else {
		mock.ExpectQuery("SELECT id,.*FROM user_subscriptions").WithArgs(userID).
			WillReturnRows(sqlmock.NewRows(promoCardCols).AddRow(card...))
		mock.ExpectQuery("UPDATE user_subscriptions").
			WithArgs(approxFloatArg(wantCardUsage[0]), approxFloatArg(wantCardUsage[1]), approxFloatArg(wantCardUsage[2]),
				sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), false, int64(77)).
			WillReturnRows(sqlmock.NewRows([]string{"expired", "group_id"}).AddRow(false, nil))
	}
	mock.ExpectQuery("UPDATE users").WithArgs(approxFloatArg(wantBalance), userID).
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(wantBalance))

	tx, err := db.Begin()
	require.NoError(t, err)
	res, err := settleSubscriptionWindow(context.Background(), tx, userID, officialCost, multiplier)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	return res, mock
}

func activeCardRow(daily, weekly, monthly, dailyUsage float64) []driver.Value {
	now := time.Now()
	return []driver.Value{int64(77), daily, weekly, monthly, dailyUsage, dailyUsage, dailyUsage,
		timezone.StartOfDay(now), timezone.StartOfWeek(now), timezone.StartOfMonth(now), now.Add(30 * 24 * time.Hour), "active"}
}

func TestSettle_Promo_WalletDiscountWithoutCard(t *testing.T) {
	setRepoPromoForTest(t, "4242")
	// 官方价 10 × 倍率 1 = 10，打 0.52 折 → 钱包扣 5.2
	res, _ := runPromoSettle(t, 1, 100, nil, 10, 1, 94.8-0.0, nil)
	require.InDelta(t, 0.52, res.paidRatio, 1e-9)
	require.InDelta(t, 5.2, res.walletDebit, 1e-9)
	require.Nil(t, res.subscriptionID)
}

func TestSettle_Promo_WalletDiscountStacksOnUserRate(t *testing.T) {
	setRepoPromoForTest(t, "4242")
	// 专属倍率 2（已含在 multiplier 里）：10 × 2 × 0.52 = 10.4
	res, _ := runPromoSettle(t, 1, 100, nil, 10, 2, 89.6, nil)
	require.InDelta(t, 10.4, res.walletDebit, 1e-9)
}

func TestSettle_Promo_ExcludedUserNoDiscount(t *testing.T) {
	setRepoPromoForTest(t, "4242")
	res, _ := runPromoSettle(t, 4242, 100, nil, 10, 1, 90, nil)
	require.Equal(t, 1.0, res.paidRatio)
	require.InDelta(t, 10.0, res.walletDebit, 1e-9)
}

func TestSettle_Promo_OutsideWindowNoDiscount(t *testing.T) {
	setRepoPromoForTest(t, "")
	_ = service.SetPromoSettingsSource(func(ctx context.Context, keys []string) (map[string]string, error) {
		return map[string]string{
			service.SettingKeyPromoStartAt:               time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			service.SettingKeyPromoEndAt:                 time.Now().Add(48 * time.Hour).Format(time.RFC3339),
			service.SettingKeyPromoBalanceRateMultiplier: "0.52",
		}, nil
	})
	res, _ := runPromoSettle(t, 1, 100, nil, 10, 1, 90, nil)
	require.Equal(t, 1.0, res.paidRatio)
}

func TestSettle_Promo_ActiveCardNotOverflowingNotDiscounted(t *testing.T) {
	setRepoPromoForTest(t, "4242")
	// 卡 D=100/W=700/M=3000，今天已用 90。活动内有效日限额 150：本次 40 全部被卡覆盖（90+40=130 ≤ 150），
	// 钱包不动、不打折。
	res, _ := runPromoSettle(t, 1, 100, activeCardRow(100, 700, 3000, 90), 40, 1, 100, &[3]float64{130, 130, 130})
	require.NotNil(t, res.subscriptionID)
	require.Equal(t, 1.0, res.paidRatio)
	require.InDelta(t, 0.0, res.walletDebit, 1e-9)
}

func TestSettle_Promo_ActiveCardOverflowOnlyWalletPartDiscounted(t *testing.T) {
	setRepoPromoForTest(t, "4242")
	// 今天已用 140，本次 30：卡有效日限额 150 只剩 10 → 卡按原价覆盖 10（卡用量 +10），
	// 溢出 20 落钱包、按 0.52 折 = 10.4；钱包 100 → 89.6。实付 20.4，占折前 30 的 0.68。
	res, _ := runPromoSettle(t, 1, 100, activeCardRow(100, 700, 3000, 140), 30, 1, 89.6, &[3]float64{150, 150, 150})
	require.NotNil(t, res.subscriptionID)
	require.InDelta(t, 20.4/30.0, res.paidRatio, 1e-9)
	require.InDelta(t, 10.4, res.walletDebit, 1e-9)
}

func TestSettle_Promo_ExcludedCardUserKeepsOriginalLimit(t *testing.T) {
	setRepoPromoForTest(t, "4242")
	// 排除名单用户 4242：日限额仍是 100。已用 90、本次 40 → 覆盖 10，溢出 30 落钱包，原价。
	res, _ := runPromoSettle(t, 4242, 100, activeCardRow(100, 700, 3000, 90), 40, 1, 70, &[3]float64{100, 100, 100})
	require.InDelta(t, 30.0, res.walletDebit, 1e-9)
	require.Equal(t, 1.0, res.paidRatio)
}

func TestBillingInflightCapacity_PromoScalesCardRemaining(t *testing.T) {
	setRepoPromoForTest(t, "4242")
	run := func(userID int64) float64 {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer db.Close()
		mock.ExpectBegin()
		now := time.Now()
		mock.ExpectQuery("SELECT daily_limit_usd,weekly_limit_usd").WithArgs(userID).
			WillReturnRows(sqlmock.NewRows([]string{"dl", "wl", "ml", "du", "wu", "mu", "dw", "ww", "mw", "exp", "st"}).
				AddRow(100.0, 700.0, 3000.0, 100.0, 100.0, 100.0, timezone.StartOfDay(now), timezone.StartOfWeek(now), timezone.StartOfMonth(now), now.Add(24*time.Hour), "active"))
		tx, err := db.Begin()
		require.NoError(t, err)
		capacity, _, err := billingInflightCapacity(context.Background(), tx, userID, 0)
		require.NoError(t, err)
		return capacity
	}
	// 卡已用 100：活动内普通用户还有 50 的有效余量；排除用户为 0
	require.InDelta(t, 50.0, run(1), 1e-9)
	// 排除用户日限额已撞满且钱包为 0 → 以日限额错误拒绝，而不是返回容量
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	now := time.Now()
	mock.ExpectQuery("SELECT daily_limit_usd,weekly_limit_usd").WithArgs(int64(4242)).
		WillReturnRows(sqlmock.NewRows([]string{"dl", "wl", "ml", "du", "wu", "mu", "dw", "ww", "mw", "exp", "st"}).
			AddRow(100.0, 700.0, 3000.0, 100.0, 100.0, 100.0, timezone.StartOfDay(now), timezone.StartOfWeek(now), timezone.StartOfMonth(now), now.Add(24*time.Hour), "active"))
	tx, err := db.Begin()
	require.NoError(t, err)
	_, _, err = billingInflightCapacity(context.Background(), tx, 4242, 0)
	require.ErrorIs(t, err, service.ErrDailyLimitExceeded)
}

// ── 钱包预占容量：纯钱包用户按 1/f 放大（预估金额仍是折前），有生效卡不变 ─────────────────────

func capacityFor(t *testing.T, userID int64, balance float64, card []driver.Value) float64 {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	q := mock.ExpectQuery("SELECT daily_limit_usd,weekly_limit_usd").WithArgs(userID)
	if card == nil {
		q.WillReturnError(sql.ErrNoRows)
	} else {
		q.WillReturnRows(sqlmock.NewRows([]string{"dl", "wl", "ml", "du", "wu", "mu", "dw", "ww", "mw", "exp", "st"}).
			AddRow(card...))
	}
	tx, err := db.Begin()
	require.NoError(t, err)
	capacity, _, err := billingInflightCapacity(context.Background(), tx, userID, balance)
	require.NoError(t, err)
	return capacity
}

func inflightCardRow(daily, weekly, monthly, usage float64, expires time.Time) []driver.Value {
	now := time.Now()
	return []driver.Value{daily, weekly, monthly, usage, usage, usage,
		timezone.StartOfDay(now), timezone.StartOfWeek(now), timezone.StartOfMonth(now), expires, "active"}
}

func TestBillingInflightCapacity_PromoWalletPartScaledByInverseRate(t *testing.T) {
	setRepoPromoForTest(t, "4242")
	// 无卡：50 / 0.52
	require.InDelta(t, 50.0/0.52, capacityFor(t, 1, 50, nil), 1e-9)
	// 排除名单：不变
	require.InDelta(t, 50.0, capacityFor(t, 4242, 50, nil), 1e-9)
	// 负余额按 0 算，放大后仍是 0
	require.InDelta(t, 0.0, capacityFor(t, 1, -5, nil), 1e-9)
	// 有生效卡：钱包部分同样 ÷f（溢出到钱包的部分也打折），卡部分容量不变（日限额 100×1.5=150，已用 100 → 50）
	require.InDelta(t, 50.0/0.52+50.0, capacityFor(t, 1, 50, inflightCardRow(100, 700, 3000, 100, time.Now().Add(24*time.Hour))), 1e-9)
	// 卡已过期（status 仍为 active）：等同无卡，结算会打折，容量同样放大
	require.InDelta(t, 50.0/0.52, capacityFor(t, 1, 50, inflightCardRow(100, 700, 3000, 0, time.Now().Add(-time.Hour))), 1e-9)
}

func TestBillingInflightCapacity_NoPromoUnchanged(t *testing.T) {
	_ = service.SetPromoSettingsSource(nil)
	require.InDelta(t, 50.0, capacityFor(t, 1, 50, nil), 1e-9)
}

// ── Key 额度 / 限速窗口按实付金额累计 ──────────────────────────────────────────────

func applyWithKeyCosts(t *testing.T, userID int64, card []driver.Value, wantKeyCost float64) *service.UsageBillingApplyResult {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO usage_billing_dedup").WithArgs("promo-req", int64(7), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery("SELECT request_fingerprint.*FROM usage_billing_dedup_archive").
		WithArgs("promo-req", int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT balance, monthly_overdraft_count, monthly_overdraft_month").WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "c", "m"}).AddRow(100.0, 0, ""))
	if card == nil {
		mock.ExpectQuery("SELECT id,.*FROM user_subscriptions").WithArgs(userID).WillReturnError(sql.ErrNoRows)
	} else {
		mock.ExpectQuery("SELECT id,.*FROM user_subscriptions").WithArgs(userID).
			WillReturnRows(sqlmock.NewRows(promoCardCols).AddRow(card...))
		mock.ExpectQuery("UPDATE user_subscriptions").
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), false, int64(77)).
			WillReturnRows(sqlmock.NewRows([]string{"expired", "group_id"}).AddRow(false, nil))
	}
	mock.ExpectQuery("UPDATE users").WithArgs(sqlmock.AnyArg(), userID).
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(90.0))
	mock.ExpectQuery("UPDATE api_keys.*SET quota_used").
		WithArgs(approxFloatArg(wantKeyCost), int64(7), service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE api_keys SET.*usage_5h").
		WithArgs(approxFloatArg(wantKeyCost), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	res, err := (&usageBillingRepository{db: db}).Apply(context.Background(), &service.UsageBillingCommand{
		RequestID: "promo-req", APIKeyID: 7, UserID: userID,
		OfficialCost: 10, RateMultiplier: 1, BalanceCost: 10,
		APIKeyQuotaCost: 10, APIKeyRateLimitCost: 10,
	})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.NoError(t, mock.ExpectationsWereMet())
	return res
}

func TestApply_Promo_APIKeyCostsUseDiscountedPaidAmount(t *testing.T) {
	setRepoPromoForTest(t, "4242")
	// 纯钱包、活动内：Key 额度 / 限速按 10×0.52 = 5.2 累计
	res := applyWithKeyCosts(t, 1, nil, 5.2)
	require.InDelta(t, 0.52, res.PaidRatio, 1e-9)
	// 排除名单：10
	res = applyWithKeyCosts(t, 4242, nil, 10)
	require.Equal(t, 1.0, res.PaidRatio)
	// 有卡、未溢出：10（不折算）
	res = applyWithKeyCosts(t, 1, activeCardRow(100, 700, 3000, 0), 10)
	require.NotNil(t, res.SubscriptionID)
	require.Equal(t, 1.0, res.PaidRatio)
	// 有卡、混合行：卡已用 145（有效日限额 150）→ 卡覆盖 5，溢出 5 折后 2.6，实付 7.6，Key 计数 7.6
	res = applyWithKeyCosts(t, 1, activeCardRow(100, 700, 3000, 145), 7.6)
	require.NotNil(t, res.SubscriptionID)
	require.InDelta(t, 0.76, res.PaidRatio, 1e-9)
}

func TestApply_NoPromo_APIKeyCostsUnchanged(t *testing.T) {
	_ = service.SetPromoSettingsSource(nil)
	res := applyWithKeyCosts(t, 1, nil, 10)
	require.Equal(t, 1.0, res.PaidRatio)
}
