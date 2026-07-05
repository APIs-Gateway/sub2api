//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// 邀请返利积分制（issue #11）—— RedeemToPlan 早返校验分支 / ListRedeemablePlans 组合 /
// AdminUpdateSettings 钳制 的 service 层单测。订阅兑换按当前去分组化 D/T 模型报价。

// --- fake: 可写 SettingRepository（SetMultiple 落到 map，供 AdminUpdateSettings 回读） ---

type writableSettingRepo struct{ values map[string]string }

func (r *writableSettingRepo) Get(ctx context.Context, key string) (*Setting, error) {
	panic("unexpected Get")
}
func (r *writableSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}
func (r *writableSettingRepo) Set(ctx context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
func (r *writableSettingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	panic("unexpected GetMultiple")
}
func (r *writableSettingRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	for k, v := range settings {
		r.values[k] = v
	}
	return nil
}
func (r *writableSettingRepo) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll")
}
func (r *writableSettingRepo) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete")
}

// newSpendServiceFull 在 newSpendService 基础上接上零值 SubscriptionService（纯定价）。
func newSpendServiceFull(settings map[string]string, repo *fakePointsRepo) *PointsService {
	s := newSpendService(settings, repo)
	s.subscriptionSvc = &SubscriptionService{}
	return s
}

// --- RedeemToPlan 早返校验分支（DB 之前全部可单测） ---

func TestPointsService_RedeemToPlan_Guards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("disabled", func(t *testing.T) {
		s := spendSettings()
		s[SettingKeyPointsEnabled] = "false"
		_, err := newSpendServiceFull(s, &fakePointsRepo{}).RedeemToPlan(ctx, 1, 30, 30, "")
		require.ErrorIs(t, err, ErrPointsDisabled)
	})
	t.Run("redeem-plan off", func(t *testing.T) {
		s := spendSettings()
		s[SettingKeyPointsRedeemPlanOn] = "false"
		_, err := newSpendServiceFull(s, &fakePointsRepo{}).RedeemToPlan(ctx, 1, 30, 30, "")
		require.ErrorIs(t, err, ErrPointsRedeemPlanDisabled)
	})
	t.Run("missing subscription service", func(t *testing.T) {
		_, err := newSpendService(spendSettings(), &fakePointsRepo{}).RedeemToPlan(ctx, 1, 30, 30, "")
		require.Error(t, err)
	})
	t.Run("quote error (invalid days)", func(t *testing.T) {
		// validityDays=7 非 30 的整数倍 → ValidateCustom 拒 → QuoteSubscription 报错。
		_, err := newSpendServiceFull(spendSettings(), &fakePointsRepo{}).RedeemToPlan(ctx, 1, 30, 7, "")
		require.Error(t, err)
	})
	t.Run("ensure-account error (after valid quote)", func(t *testing.T) {
		repo := &fakePointsRepo{ensureErr: errors.New("db")}
		_, err := newSpendServiceFull(spendSettings(), repo).RedeemToPlan(ctx, 1, 30, 30, "")
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrPointsPlanInvalid)
	})
}

// --- ListRedeemablePlans 生成当前 D/T 兑换组合 ---

func TestPointsService_ListRedeemablePlans(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	out, err := newSpendServiceFull(spendSettings(), &fakePointsRepo{}).ListRedeemablePlans(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.EqualValues(t, 30, out[0].DailyAmountUSD)
	require.EqualValues(t, 30, out[0].ValidityDays)
	require.Greater(t, out[0].Price, 0.0)
	require.Greater(t, out[0].PointsPrice, int64(0))
	require.Greater(t, out[0].WeeklyCapUSD, 0.0)
	require.Greater(t, out[0].MonthlyCapUSD, 0.0)
}

// --- AdminUpdateSettings 钳制 + 落库回读 ---

func TestPointsService_AdminUpdateSettings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := &writableSettingRepo{values: map[string]string{}}
	setSvc := &SettingService{settingRepo: repo}
	svc := &PointsService{settingService: setSvc}

	out, err := svc.AdminUpdateSettings(ctx, PointsSettingsInput{
		Enabled:             true,
		Peg:                 -1,  // < PointsPegMin → 回退 PointsPegDefault
		CashbackRatePercent: 999, // 钳到上限
		FreezeHours:         -5,  // → 0
		WithdrawEnabled:     true,
		WithdrawMinPoints:   -10, // → 0
		WithdrawFeePercent:  999, // 钳到上限
		RedeemBalanceOn:     true,
		RedeemPlanOn:        false,
	})
	require.NoError(t, err)
	require.True(t, out.Enabled)
	require.InDelta(t, PointsPegDefault, out.Peg, 1e-9)
	require.EqualValues(t, 0, out.FreezeHours)
	require.EqualValues(t, 0, out.WithdrawMinPoints)
	require.False(t, out.RedeemPlanOn)
	require.True(t, out.RedeemBalanceOn)
	// 落库回读：再 GetSettings 应一致。
	again := svc.AdminGetSettings(ctx)
	require.InDelta(t, out.Peg, again.Peg, 1e-9)
	require.Equal(t, out.WithdrawFeePercent, again.WithdrawFeePercent)
}

// --- RedeemToBalance / CreateWithdrawal 的 EnsureAccount 错误分支 ---

func TestPointsService_EnsureAccountErrorBranches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("redeem-balance ensure error", func(t *testing.T) {
		repo := &fakePointsRepo{ensureErr: errors.New("db")}
		_, err := newSpendService(spendSettings(), repo).RedeemToBalance(ctx, 1, 100)
		require.Error(t, err)
	})
	t.Run("create-withdrawal ensure error", func(t *testing.T) {
		repo := &fakePointsRepo{ensureErr: errors.New("db")}
		_, err := newSpendService(spendSettings(), repo).CreateWithdrawal(ctx, 1, 1000, "alipay", "acc", "name", "", "")
		require.Error(t, err)
	})
}
