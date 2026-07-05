//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// 邀请返利积分制（issue #11）—— PointsService 花积分 / admin / 设置 / 公共配置 的 service 层单测。
// 用可配置 fake（默认零值、记录调用）拼真实 SettingService/AffiliateService，覆盖大量校验分支。

// --- 可配置 fake PointsRepository（默认零值，按需设返回） ---

type fakePointsRepo struct {
	account     *PointsAccount
	ensureErr   error
	thawN       int64
	thawErr     error
	redeemBal   float64
	redeemErr   error
	deductErr   error
	withdrawal  *PointsWithdrawal
	createErr   error
	reviewW     *PointsWithdrawal
	reviewErr   error
	clawbackN   int64
	clawbackErr error
	ledger      []PointsLedgerEntry
	ledgerTotal int64
	withdrawals []PointsWithdrawal
	wTotal      int64

	// 记录
	redeemCall *struct{ userID, points int64 }
	createCall *CreateWithdrawalInput
	reviewCall *struct {
		id, adminID int64
		approve     bool
		note, proof string
	}
	deductCall *struct {
		userID, points int64
		key            string
	}
}

func (r *fakePointsRepo) EnsureAccount(ctx context.Context, userID int64) (*PointsAccount, error) {
	if r.ensureErr != nil {
		return nil, r.ensureErr
	}
	if r.account != nil {
		return r.account, nil
	}
	return &PointsAccount{UserID: userID}, nil
}
func (r *fakePointsRepo) GetAccount(ctx context.Context, userID int64) (*PointsAccount, error) {
	if r.account != nil {
		return r.account, nil
	}
	return &PointsAccount{UserID: userID}, nil
}
func (r *fakePointsRepo) EarnPoints(ctx context.Context, in EarnPointsInput) (bool, error) {
	return true, nil
}
func (r *fakePointsRepo) ClawbackByOrder(ctx context.Context, sourceOrderID int64, refundAmount, originalAmount float64) (int64, error) {
	return r.clawbackN, r.clawbackErr
}
func (r *fakePointsRepo) ThawDuePoints(ctx context.Context, userID int64) (int64, error) {
	return r.thawN, r.thawErr
}
func (r *fakePointsRepo) RedeemToBalance(ctx context.Context, userID, points int64, balanceDelta, pegAt float64) (float64, error) {
	r.redeemCall = &struct{ userID, points int64 }{userID, points}
	return r.redeemBal, r.redeemErr
}
func (r *fakePointsRepo) DeductForPlan(ctx context.Context, userID, points int64, pegAt float64, note, idempotencyKey string) error {
	r.deductCall = &struct {
		userID, points int64
		key            string
	}{userID, points, idempotencyKey}
	return r.deductErr
}
func (r *fakePointsRepo) CreateWithdrawal(ctx context.Context, in CreateWithdrawalInput) (*PointsWithdrawal, error) {
	c := in
	r.createCall = &c
	if r.createErr != nil {
		return nil, r.createErr
	}
	if r.withdrawal != nil {
		return r.withdrawal, nil
	}
	return &PointsWithdrawal{ID: 1, UserID: in.UserID, Points: in.Points}, nil
}
func (r *fakePointsRepo) GetWithdrawal(ctx context.Context, id int64) (*PointsWithdrawal, error) {
	return r.reviewW, r.reviewErr
}
func (r *fakePointsRepo) ReviewWithdrawal(ctx context.Context, id, adminID int64, approve bool, note, payoutProof string) (*PointsWithdrawal, error) {
	r.reviewCall = &struct {
		id, adminID int64
		approve     bool
		note, proof string
	}{id, adminID, approve, note, payoutProof}
	if r.reviewErr != nil {
		return nil, r.reviewErr
	}
	return &PointsWithdrawal{ID: id}, nil
}
func (r *fakePointsRepo) ListUserWithdrawals(ctx context.Context, userID int64, limit int) ([]PointsWithdrawal, error) {
	return r.withdrawals, nil
}
func (r *fakePointsRepo) ListUserLedger(ctx context.Context, userID int64, page, pageSize int) ([]PointsLedgerEntry, int64, error) {
	return r.ledger, r.ledgerTotal, nil
}
func (r *fakePointsRepo) ListWithdrawals(ctx context.Context, filter PointsWithdrawalFilter) ([]PointsWithdrawal, int64, error) {
	return r.withdrawals, r.wTotal, nil
}
func (r *fakePointsRepo) ListLedger(ctx context.Context, filter PointsLedgerFilter) ([]PointsLedgerEntry, int64, error) {
	return r.ledger, r.ledgerTotal, nil
}

// --- 构造启用的 PointsService（peg=0.01，提现开，换余额开，换套餐开） ---

func spendSettings() map[string]string {
	return map[string]string{
		SettingKeyPointsEnabled:            "true",
		SettingKeyPointsPeg:                "0.01",
		SettingKeyPointsCashbackRate:       "20",
		SettingKeyPointsFreezeHours:        "0",
		SettingKeyPointsWithdrawEnabled:    "true",
		SettingKeyPointsWithdrawMin:        "0",
		SettingKeyPointsWithdrawFeePercent: "10",
		SettingKeyPointsWithdrawUSDCNYRate: "7.2",
		SettingKeyPointsRedeemBalanceOn:    "true",
		SettingKeyPointsRedeemPlanOn:       "true",
	}
}

func newSpendService(settings map[string]string, repo *fakePointsRepo) *PointsService {
	setSvc := &SettingService{settingRepo: &pointsEarnSettingRepo{values: settings}}
	affSvc := NewAffiliateService(&pointsEarnAffiliateRepo{}, setSvc, nil, nil)
	return &PointsService{repo: repo, settingService: setSvc, affiliateService: affSvc}
}

func TestPointsService_IsEnabledAndPeg(t *testing.T) {
	t.Parallel()
	svc := newSpendService(spendSettings(), &fakePointsRepo{})
	require.True(t, svc.IsEnabled(context.Background()))
	require.InDelta(t, 0.01, svc.peg(context.Background()), 1e-9)
}

func TestPointsService_GetUserPoints(t *testing.T) {
	t.Parallel()
	repo := &fakePointsRepo{account: &PointsAccount{UserID: 5, Available: 30, Frozen: 10}, thawN: 0}
	svc := newSpendService(spendSettings(), repo)
	acct, err := svc.GetUserPoints(context.Background(), 5)
	require.NoError(t, err)
	require.EqualValues(t, 30, acct.Available)

	// EnsureAccount 失败透传。
	bad := &fakePointsRepo{ensureErr: errors.New("db")}
	_, err = newSpendService(spendSettings(), bad).GetUserPoints(context.Background(), 5)
	require.Error(t, err)
}

func TestPointsService_ListUserLedgerAndWithdrawals(t *testing.T) {
	t.Parallel()
	repo := &fakePointsRepo{
		ledger:      []PointsLedgerEntry{{ID: 1}},
		ledgerTotal: 1,
		withdrawals: []PointsWithdrawal{{ID: 9}},
	}
	svc := newSpendService(spendSettings(), repo)
	items, total, err := svc.ListUserLedger(context.Background(), 1, 1, 20)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.EqualValues(t, 1, total)
	ws, err := svc.ListUserWithdrawals(context.Background(), 1, 10)
	require.NoError(t, err)
	require.Len(t, ws, 1)
}

// --- RedeemToBalance ---

func TestPointsService_RedeemToBalance(t *testing.T) {
	t.Parallel()

	t.Run("disabled", func(t *testing.T) {
		s := spendSettings()
		s[SettingKeyPointsEnabled] = "false"
		_, err := newSpendService(s, &fakePointsRepo{}).RedeemToBalance(context.Background(), 1, 100)
		require.ErrorIs(t, err, ErrPointsDisabled)
	})
	t.Run("redeem-balance off", func(t *testing.T) {
		s := spendSettings()
		s[SettingKeyPointsRedeemBalanceOn] = "false"
		_, err := newSpendService(s, &fakePointsRepo{}).RedeemToBalance(context.Background(), 1, 100)
		require.ErrorIs(t, err, ErrPointsRedeemBalanceOff)
	})
	t.Run("invalid points", func(t *testing.T) {
		_, err := newSpendService(spendSettings(), &fakePointsRepo{}).RedeemToBalance(context.Background(), 1, 0)
		require.ErrorIs(t, err, ErrPointsAmountInvalid)
	})
	t.Run("success", func(t *testing.T) {
		repo := &fakePointsRepo{redeemBal: 12.34}
		bal, err := newSpendService(spendSettings(), repo).RedeemToBalance(context.Background(), 7, 100)
		require.NoError(t, err)
		require.InDelta(t, 12.34, bal, 1e-9)
		require.NotNil(t, repo.redeemCall)
		require.EqualValues(t, 100, repo.redeemCall.points)
	})
	t.Run("repo error", func(t *testing.T) {
		repo := &fakePointsRepo{redeemErr: errors.New("boom")}
		_, err := newSpendService(spendSettings(), repo).RedeemToBalance(context.Background(), 7, 100)
		require.Error(t, err)
	})
}

// --- CreateWithdrawal（校验面最大） ---

func TestPointsService_CreateWithdrawal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("disabled", func(t *testing.T) {
		s := spendSettings()
		s[SettingKeyPointsEnabled] = "false"
		_, err := newSpendService(s, &fakePointsRepo{}).CreateWithdrawal(ctx, 1, 100, "alipay", "a", "b", "", "")
		require.ErrorIs(t, err, ErrPointsDisabled)
	})
	t.Run("withdraw off", func(t *testing.T) {
		s := spendSettings()
		s[SettingKeyPointsWithdrawEnabled] = "false"
		_, err := newSpendService(s, &fakePointsRepo{}).CreateWithdrawal(ctx, 1, 100, "alipay", "a", "b", "", "")
		require.ErrorIs(t, err, ErrPointsWithdrawDisabled)
	})
	t.Run("invalid points", func(t *testing.T) {
		_, err := newSpendService(spendSettings(), &fakePointsRepo{}).CreateWithdrawal(ctx, 1, 0, "alipay", "a", "b", "", "")
		require.ErrorIs(t, err, ErrPointsAmountInvalid)
	})
	t.Run("below min", func(t *testing.T) {
		s := spendSettings()
		s[SettingKeyPointsWithdrawMin] = "500"
		_, err := newSpendService(s, &fakePointsRepo{}).CreateWithdrawal(ctx, 1, 100, "alipay", "acc", "name", "", "")
		require.ErrorIs(t, err, ErrPointsWithdrawBelowMin)
	})
	t.Run("alipay missing fields", func(t *testing.T) {
		_, err := newSpendService(spendSettings(), &fakePointsRepo{}).CreateWithdrawal(ctx, 1, 100, "alipay", "", "", "", "")
		require.ErrorIs(t, err, ErrPointsWithdrawPayout)
	})
	t.Run("usdt missing address", func(t *testing.T) {
		_, err := newSpendService(spendSettings(), &fakePointsRepo{}).CreateWithdrawal(ctx, 1, 100, "usdt", "", "", "TRC20", "")
		require.ErrorIs(t, err, ErrPointsWithdrawPayout)
	})
	t.Run("usdt missing chain", func(t *testing.T) {
		_, err := newSpendService(spendSettings(), &fakePointsRepo{}).CreateWithdrawal(ctx, 1, 100, "usdt", "", "", "", "addr")
		require.ErrorIs(t, err, ErrPointsWithdrawPayout)
	})
	t.Run("usdt unsupported chain", func(t *testing.T) {
		_, err := newSpendService(spendSettings(), &fakePointsRepo{}).CreateWithdrawal(ctx, 1, 100, "usdt", "", "", "SOL", "addr")
		require.ErrorIs(t, err, ErrPointsWithdrawPayout)
	})
	t.Run("unknown method", func(t *testing.T) {
		_, err := newSpendService(spendSettings(), &fakePointsRepo{}).CreateWithdrawal(ctx, 1, 100, "paypal", "", "", "", "")
		require.ErrorIs(t, err, ErrPointsWithdrawPayout)
	})
	t.Run("net<=0 rejected", func(t *testing.T) {
		s := spendSettings()
		s[SettingKeyPointsWithdrawFeePercent] = "100" // 全额手续费 → net=0
		_, err := newSpendService(s, &fakePointsRepo{}).CreateWithdrawal(ctx, 1, 100, "alipay", "acc", "name", "", "")
		require.ErrorIs(t, err, ErrPointsAmountInvalid)
	})
	t.Run("alipay success", func(t *testing.T) {
		repo := &fakePointsRepo{}
		w, err := newSpendService(spendSettings(), repo).CreateWithdrawal(ctx, 7, 1000, "alipay", " acc ", " name ", "ignored", "ignored")
		require.NoError(t, err)
		require.NotNil(t, w)
		require.NotNil(t, repo.createCall)
		require.EqualValues(t, 1000, repo.createCall.Points)
		require.Equal(t, "alipay", repo.createCall.PayoutMethod)
		require.Equal(t, "acc", repo.createCall.PayoutAlipayAccount, "trim 后入库")
		require.Equal(t, "", repo.createCall.PayoutUSDTChain, "alipay 不留 usdt chain")
		require.Equal(t, "", repo.createCall.PayoutUSDTAddress, "alipay 不留 usdt")
		require.Equal(t, PointsPayoutCurrencyCNY, repo.createCall.PayoutCurrency)
		require.InDelta(t, 0, repo.createCall.USDCNYRateAt, 1e-9)
		// gross=1000×0.01=10；fee 10%→1；net=9。
		require.InDelta(t, 10, repo.createCall.GrossAmount, 1e-9)
		require.InDelta(t, 9, repo.createCall.NetAmount, 1e-9)
	})
	t.Run("usdt success", func(t *testing.T) {
		repo := &fakePointsRepo{}
		_, err := newSpendService(spendSettings(), repo).CreateWithdrawal(ctx, 7, 1000, "usdt", "x", "y", " trc20 ", " addr ")
		require.NoError(t, err)
		require.Equal(t, "TRC20", repo.createCall.PayoutUSDTChain)
		require.Equal(t, "addr", repo.createCall.PayoutUSDTAddress)
		require.Equal(t, "", repo.createCall.PayoutAlipayAccount, "usdt 不留 alipay")
		require.Equal(t, PointsPayoutCurrencyUSD, repo.createCall.PayoutCurrency)
		require.InDelta(t, 7.3, repo.createCall.USDCNYRateAt, 1e-9)
		require.InDelta(t, 10.0/7.3, repo.createCall.GrossAmount, 1e-8)
		require.InDelta(t, 9.0/7.3, repo.createCall.NetAmount, 1e-8)
	})
}

// --- Clawback / Admin / 公共配置 ---

func TestPointsService_ClawbackForOrder(t *testing.T) {
	t.Parallel()
	// 停用 → 0、不触达 repo。
	s := spendSettings()
	s[SettingKeyPointsEnabled] = "false"
	n, err := newSpendService(s, &fakePointsRepo{clawbackN: 99}).ClawbackForOrder(context.Background(), 1, 50, 100)
	require.NoError(t, err)
	require.EqualValues(t, 0, n)
	// 启用 → 透传 repo。
	n, err = newSpendService(spendSettings(), &fakePointsRepo{clawbackN: 40}).ClawbackForOrder(context.Background(), 1, 40, 100)
	require.NoError(t, err)
	require.EqualValues(t, 40, n)
}

func TestPointsService_AdminPassthroughs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := &fakePointsRepo{withdrawals: []PointsWithdrawal{{ID: 1}}, wTotal: 1, ledger: []PointsLedgerEntry{{ID: 2}}, ledgerTotal: 1}
	svc := newSpendService(spendSettings(), repo)

	ws, total, err := svc.AdminListWithdrawals(ctx, PointsWithdrawalFilter{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, ws, 1)
	require.EqualValues(t, 1, total)

	ls, ltotal, err := svc.AdminListLedger(ctx, PointsLedgerFilter{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, ls, 1)
	require.EqualValues(t, 1, ltotal)

	// note 截断到 255 + trim。
	longNote := ""
	for i := 0; i < 300; i++ {
		longNote += "x"
	}
	_, err = svc.AdminReviewWithdrawal(ctx, 3, 9, false, "  "+longNote+"  ", "  proof  ")
	require.NoError(t, err)
	require.NotNil(t, repo.reviewCall)
	require.Len(t, []rune(repo.reviewCall.note), 255, "note 截断到 255")
	require.Equal(t, "proof", repo.reviewCall.proof)
	require.EqualValues(t, 3, repo.reviewCall.id)
}

func TestPointsService_PublicConfigAndSettings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := newSpendService(spendSettings(), &fakePointsRepo{})

	pub := svc.PublicConfig(ctx)
	require.True(t, pub.Enabled)
	require.InDelta(t, 0.01, pub.Peg, 1e-9)
	require.True(t, pub.WithdrawEnabled)
	require.InDelta(t, 7.2, pub.WithdrawUSDCNYRate, 1e-9)
	require.True(t, pub.RedeemBalanceOn)
	require.True(t, pub.RedeemPlanOn)

	st := svc.AdminGetSettings(ctx)
	require.InDelta(t, 20, st.CashbackRatePercent, 1e-9)
	require.InDelta(t, 7.2, st.WithdrawUSDCNYRate, 1e-9)

	// EffectiveRateForUser：无专属 → 全局 20。
	require.InDelta(t, 20, svc.EffectiveRateForUser(ctx, 1), 1e-9)
}
