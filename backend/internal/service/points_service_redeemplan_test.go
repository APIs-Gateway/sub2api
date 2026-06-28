//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

// 邀请返利积分制（issue #11）—— RedeemToPlan 早返校验分支 / ListRedeemablePlans 过滤 /
// AdminUpdateSettings 钳制 的 service 层单测。用 fake GroupRepository + 零值 SubscriptionService
// （QuoteSubscription 走纯函数 DefaultSubscriptionPricingConfig）覆盖 DB 之前的全部分支。

// --- fake: GroupRepository（仅 GetByID / ListActive 真实，其余 panic/零值） ---

type fakePointsGroupRepo struct {
	byID       map[int64]*Group
	getByIDErr error
	active     []Group
	activeErr  error
}

func (r *fakePointsGroupRepo) GetByID(ctx context.Context, id int64) (*Group, error) {
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	return r.byID[id], nil
}
func (r *fakePointsGroupRepo) ListActive(ctx context.Context) ([]Group, error) {
	return r.active, r.activeErr
}
func (r *fakePointsGroupRepo) Create(ctx context.Context, group *Group) error { panic("unexpected") }
func (r *fakePointsGroupRepo) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	panic("unexpected")
}
func (r *fakePointsGroupRepo) Update(ctx context.Context, group *Group) error { panic("unexpected") }
func (r *fakePointsGroupRepo) Delete(ctx context.Context, id int64) error     { panic("unexpected") }
func (r *fakePointsGroupRepo) DeleteCascade(ctx context.Context, id int64) ([]int64, error) {
	panic("unexpected")
}
func (r *fakePointsGroupRepo) List(ctx context.Context, params pagination.PaginationParams) ([]Group, *pagination.PaginationResult, error) {
	panic("unexpected")
}
func (r *fakePointsGroupRepo) ListWithFilters(ctx context.Context, params pagination.PaginationParams, platform, status, search string, isExclusive *bool) ([]Group, *pagination.PaginationResult, error) {
	panic("unexpected")
}
func (r *fakePointsGroupRepo) ListActiveByPlatform(ctx context.Context, platform string) ([]Group, error) {
	panic("unexpected")
}
func (r *fakePointsGroupRepo) ExistsByName(ctx context.Context, name string) (bool, error) {
	panic("unexpected")
}
func (r *fakePointsGroupRepo) GetAccountCount(ctx context.Context, groupID int64) (int64, int64, error) {
	panic("unexpected")
}
func (r *fakePointsGroupRepo) DeleteAccountGroupsByGroupID(ctx context.Context, groupID int64) (int64, error) {
	panic("unexpected")
}
func (r *fakePointsGroupRepo) GetAccountIDsByGroupIDs(ctx context.Context, groupIDs []int64) ([]int64, error) {
	panic("unexpected")
}
func (r *fakePointsGroupRepo) BindAccountsToGroup(ctx context.Context, groupID int64, accountIDs []int64) error {
	panic("unexpected")
}
func (r *fakePointsGroupRepo) UpdateSortOrders(ctx context.Context, updates []GroupSortOrderUpdate) error {
	panic("unexpected")
}

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

// newSpendServiceFull 在 newSpendService 基础上接上 groupRepo + 零值 SubscriptionService（纯定价）。
func newSpendServiceFull(settings map[string]string, repo *fakePointsRepo, groupRepo GroupRepository) *PointsService {
	s := newSpendService(settings, repo)
	s.groupRepo = groupRepo
	s.subscriptionSvc = &SubscriptionService{}
	return s
}

func ptrF(v float64) *float64 { return &v }

func subGroup(id int64, daily float64, days int) Group {
	return Group{
		ID:                  id,
		Name:                "g",
		Status:              StatusActive,
		SubscriptionType:    SubscriptionTypeSubscription,
		DailyLimitUSD:       ptrF(daily),
		DefaultValidityDays: days,
	}
}

// --- RedeemToPlan 早返校验分支（DB 之前全部可单测） ---

func TestPointsService_RedeemToPlan_Guards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("disabled", func(t *testing.T) {
		s := spendSettings()
		s[SettingKeyPointsEnabled] = "false"
		_, err := newSpendServiceFull(s, &fakePointsRepo{}, &fakePointsGroupRepo{}).RedeemToPlan(ctx, 1, 2, 30, "")
		require.ErrorIs(t, err, ErrPointsDisabled)
	})
	t.Run("redeem-plan off", func(t *testing.T) {
		s := spendSettings()
		s[SettingKeyPointsRedeemPlanOn] = "false"
		_, err := newSpendServiceFull(s, &fakePointsRepo{}, &fakePointsGroupRepo{}).RedeemToPlan(ctx, 1, 2, 30, "")
		require.ErrorIs(t, err, ErrPointsRedeemPlanDisabled)
	})
	t.Run("group lookup error", func(t *testing.T) {
		gr := &fakePointsGroupRepo{getByIDErr: errors.New("db")}
		_, err := newSpendServiceFull(spendSettings(), &fakePointsRepo{}, gr).RedeemToPlan(ctx, 1, 2, 30, "")
		require.Error(t, err)
	})
	t.Run("group nil", func(t *testing.T) {
		gr := &fakePointsGroupRepo{byID: map[int64]*Group{}}
		_, err := newSpendServiceFull(spendSettings(), &fakePointsRepo{}, gr).RedeemToPlan(ctx, 1, 2, 30, "")
		require.ErrorIs(t, err, ErrPointsPlanInvalid)
	})
	t.Run("group wrong type", func(t *testing.T) {
		g := subGroup(2, 2.0, 30)
		g.SubscriptionType = "balance"
		gr := &fakePointsGroupRepo{byID: map[int64]*Group{2: &g}}
		_, err := newSpendServiceFull(spendSettings(), &fakePointsRepo{}, gr).RedeemToPlan(ctx, 1, 2, 30, "")
		require.ErrorIs(t, err, ErrPointsPlanInvalid)
	})
	t.Run("group inactive", func(t *testing.T) {
		g := subGroup(2, 2.0, 30)
		g.Status = "inactive"
		gr := &fakePointsGroupRepo{byID: map[int64]*Group{2: &g}}
		_, err := newSpendServiceFull(spendSettings(), &fakePointsRepo{}, gr).RedeemToPlan(ctx, 1, 2, 30, "")
		require.ErrorIs(t, err, ErrPointsPlanInvalid)
	})
	t.Run("daily limit nil", func(t *testing.T) {
		g := subGroup(2, 2.0, 30)
		g.DailyLimitUSD = nil
		gr := &fakePointsGroupRepo{byID: map[int64]*Group{2: &g}}
		_, err := newSpendServiceFull(spendSettings(), &fakePointsRepo{}, gr).RedeemToPlan(ctx, 1, 2, 30, "")
		require.ErrorIs(t, err, ErrPointsPlanInvalid)
	})
	t.Run("quote error (invalid days)", func(t *testing.T) {
		g := subGroup(2, 2.0, 30)
		gr := &fakePointsGroupRepo{byID: map[int64]*Group{2: &g}}
		// validityDays=7 非 30 的整数倍 → ValidateCustom 拒 → QuoteSubscription 报错。
		_, err := newSpendServiceFull(spendSettings(), &fakePointsRepo{}, gr).RedeemToPlan(ctx, 1, 2, 7, "")
		require.Error(t, err)
	})
	t.Run("ensure-account error (after valid quote)", func(t *testing.T) {
		g := subGroup(2, 2.0, 30)
		gr := &fakePointsGroupRepo{byID: map[int64]*Group{2: &g}}
		repo := &fakePointsRepo{ensureErr: errors.New("db")}
		// t=0 → 回退 DefaultValidityDays=30；quote 合法、need>0；到 EnsureAccount 失败返回（tx 之前）。
		_, err := newSpendServiceFull(spendSettings(), repo, gr).RedeemToPlan(ctx, 1, 2, 0, "")
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrPointsPlanInvalid)
	})
}

// --- ListRedeemablePlans 过滤 ---

func TestPointsService_ListRedeemablePlans(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("list error", func(t *testing.T) {
		gr := &fakePointsGroupRepo{activeErr: errors.New("db")}
		_, err := newSpendServiceFull(spendSettings(), &fakePointsRepo{}, gr).ListRedeemablePlans(ctx)
		require.Error(t, err)
	})

	t.Run("filters non-subscription / nil-daily / no-default-days / bad-quote", func(t *testing.T) {
		ok := subGroup(1, 2.0, 30) // 合法，入选
		ok.Name = "keep"

		notSub := subGroup(2, 2.0, 30)
		notSub.SubscriptionType = "balance"

		nilDaily := subGroup(3, 2.0, 30)
		nilDaily.DailyLimitUSD = nil

		noDays := subGroup(4, 2.0, 0) // DefaultValidityDays<=0 → skip

		badQuote := subGroup(5, 999.0, 30) // d=999 超出 DMax=50 → QuoteSubscription 报错 → skip

		gr := &fakePointsGroupRepo{active: []Group{ok, notSub, nilDaily, noDays, badQuote}}
		out, err := newSpendServiceFull(spendSettings(), &fakePointsRepo{}, gr).ListRedeemablePlans(ctx)
		require.NoError(t, err)
		require.Len(t, out, 1)
		require.Equal(t, "keep", out[0].Name)
		require.EqualValues(t, 1, out[0].GroupID)
		require.EqualValues(t, 30, out[0].ValidityDays)
		require.InDelta(t, 2.0, out[0].DailyAmountUSD, 1e-9)
		require.Greater(t, out[0].Price, 0.0)
		require.Greater(t, out[0].PointsPrice, int64(0))
	})
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
		_, err := newSpendService(spendSettings(), repo).CreateWithdrawal(ctx, 1, 1000, "alipay", "acc", "name", "")
		require.Error(t, err)
	})
}
