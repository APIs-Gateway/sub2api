//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 邀请返利积分制（issue #11）—— 用户端 PointsHandler 的 handler 层单测。
// 用实现 service 仓储接口的 fake 拼真实 PointsService/AffiliateService，断言状态码 + 关键响应字段。

// --- fake: service.PointsRepository ---

type fakePtsRepo struct {
	account     *service.PointsAccount
	redeemBal   float64
	withdrawal  *service.PointsWithdrawal
	ledger      []service.PointsLedgerEntry
	ledgerTotal int64
	withdrawals []service.PointsWithdrawal
	wTotal      int64

	// 错误注入（覆盖 handler 的 ErrorFrom 分支）
	ensureErr error
	ledgerErr error
	uwErr     error
	createErr error
}

func (r *fakePtsRepo) EnsureAccount(ctx context.Context, userID int64) (*service.PointsAccount, error) {
	if r.ensureErr != nil {
		return nil, r.ensureErr
	}
	if r.account != nil {
		return r.account, nil
	}
	return &service.PointsAccount{UserID: userID}, nil
}
func (r *fakePtsRepo) GetAccount(ctx context.Context, userID int64) (*service.PointsAccount, error) {
	if r.account != nil {
		return r.account, nil
	}
	return &service.PointsAccount{UserID: userID}, nil
}
func (r *fakePtsRepo) EarnPoints(ctx context.Context, in service.EarnPointsInput) (bool, error) {
	return true, nil
}
func (r *fakePtsRepo) ClawbackByOrder(ctx context.Context, sourceOrderID int64, refundAmount, originalAmount float64) (int64, error) {
	return 0, nil
}
func (r *fakePtsRepo) ThawDuePoints(ctx context.Context, userID int64) (int64, error) {
	return 0, nil
}
func (r *fakePtsRepo) RedeemToBalance(ctx context.Context, userID, points int64, balanceDelta, pegAt float64) (float64, error) {
	return r.redeemBal, nil
}
func (r *fakePtsRepo) DeductForPlan(ctx context.Context, userID, points int64, pegAt float64, note, idempotencyKey string) error {
	return nil
}
func (r *fakePtsRepo) CreateWithdrawal(ctx context.Context, in service.CreateWithdrawalInput) (*service.PointsWithdrawal, error) {
	if r.createErr != nil {
		return nil, r.createErr
	}
	if r.withdrawal != nil {
		return r.withdrawal, nil
	}
	return &service.PointsWithdrawal{ID: 1, UserID: in.UserID, Points: in.Points}, nil
}
func (r *fakePtsRepo) GetWithdrawal(ctx context.Context, id int64) (*service.PointsWithdrawal, error) {
	return nil, nil
}
func (r *fakePtsRepo) ReviewWithdrawal(ctx context.Context, id, adminID int64, approve bool, note, payoutProof string) (*service.PointsWithdrawal, error) {
	return &service.PointsWithdrawal{ID: id}, nil
}
func (r *fakePtsRepo) ListUserWithdrawals(ctx context.Context, userID int64, limit int) ([]service.PointsWithdrawal, error) {
	return r.withdrawals, r.uwErr
}
func (r *fakePtsRepo) ListUserLedger(ctx context.Context, userID int64, page, pageSize int) ([]service.PointsLedgerEntry, int64, error) {
	return r.ledger, r.ledgerTotal, r.ledgerErr
}
func (r *fakePtsRepo) ListWithdrawals(ctx context.Context, filter service.PointsWithdrawalFilter) ([]service.PointsWithdrawal, int64, error) {
	return r.withdrawals, r.wTotal, nil
}
func (r *fakePtsRepo) ListLedger(ctx context.Context, filter service.PointsLedgerFilter) ([]service.PointsLedgerEntry, int64, error) {
	return r.ledger, r.ledgerTotal, nil
}

// --- fake: service.SettingRepository（读 map） ---

type fakeSetRepo struct{ values map[string]string }

func (r *fakeSetRepo) Get(ctx context.Context, key string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}
func (r *fakeSetRepo) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}
func (r *fakeSetRepo) Set(ctx context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
func (r *fakeSetRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	return nil, nil
}
func (r *fakeSetRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	for k, v := range settings {
		r.values[k] = v
	}
	return nil
}
func (r *fakeSetRepo) GetAll(ctx context.Context) (map[string]string, error) { return r.values, nil }
func (r *fakeSetRepo) Delete(ctx context.Context, key string) error {
	delete(r.values, key)
	return nil
}

// --- fake: service.GroupRepository（ListActive 真实，其余零值/panic） ---

type fakeGrpRepo struct {
	active    []service.Group
	activeErr error
}

func (r *fakeGrpRepo) ListActive(ctx context.Context) ([]service.Group, error) {
	return r.active, r.activeErr
}
func (r *fakeGrpRepo) GetByID(ctx context.Context, id int64) (*service.Group, error) {
	for i := range r.active {
		if r.active[i].ID == id {
			g := r.active[i]
			return &g, nil
		}
	}
	return nil, nil
}
func (r *fakeGrpRepo) Create(ctx context.Context, group *service.Group) error { panic("unexpected") }
func (r *fakeGrpRepo) GetByIDLite(ctx context.Context, id int64) (*service.Group, error) {
	panic("unexpected")
}
func (r *fakeGrpRepo) Update(ctx context.Context, group *service.Group) error { panic("unexpected") }
func (r *fakeGrpRepo) Delete(ctx context.Context, id int64) error             { panic("unexpected") }
func (r *fakeGrpRepo) DeleteCascade(ctx context.Context, id int64) ([]int64, error) {
	panic("unexpected")
}
func (r *fakeGrpRepo) List(ctx context.Context, params pagination.PaginationParams) ([]service.Group, *pagination.PaginationResult, error) {
	panic("unexpected")
}
func (r *fakeGrpRepo) ListWithFilters(ctx context.Context, params pagination.PaginationParams, platform, status, search string, isExclusive *bool) ([]service.Group, *pagination.PaginationResult, error) {
	panic("unexpected")
}
func (r *fakeGrpRepo) ListActiveByPlatform(ctx context.Context, platform string) ([]service.Group, error) {
	panic("unexpected")
}
func (r *fakeGrpRepo) ExistsByName(ctx context.Context, name string) (bool, error) {
	panic("unexpected")
}
func (r *fakeGrpRepo) GetAccountCount(ctx context.Context, groupID int64) (int64, int64, error) {
	panic("unexpected")
}
func (r *fakeGrpRepo) DeleteAccountGroupsByGroupID(ctx context.Context, groupID int64) (int64, error) {
	panic("unexpected")
}
func (r *fakeGrpRepo) GetAccountIDsByGroupIDs(ctx context.Context, groupIDs []int64) ([]int64, error) {
	panic("unexpected")
}
func (r *fakeGrpRepo) BindAccountsToGroup(ctx context.Context, groupID int64, accountIDs []int64) error {
	panic("unexpected")
}
func (r *fakeGrpRepo) UpdateSortOrders(ctx context.Context, updates []service.GroupSortOrderUpdate) error {
	panic("unexpected")
}

// --- fake: service.AffiliateRepository（EnsureUserAffiliate + ListInvitees 真实，其余零值） ---

type fakeAffRepo struct {
	summary   *service.AffiliateSummary
	invitees  []service.AffiliateInvitee
	ensureErr error
}

func (r *fakeAffRepo) EnsureUserAffiliate(ctx context.Context, userID int64) (*service.AffiliateSummary, error) {
	if r.ensureErr != nil {
		return nil, r.ensureErr
	}
	if r.summary != nil {
		return r.summary, nil
	}
	return &service.AffiliateSummary{UserID: userID, AffCode: "CODE7"}, nil
}
func (r *fakeAffRepo) ListInvitees(ctx context.Context, inviterID int64, limit int) ([]service.AffiliateInvitee, error) {
	return r.invitees, nil
}
func (r *fakeAffRepo) GetAffiliateByCode(ctx context.Context, code string) (*service.AffiliateSummary, error) {
	return nil, nil
}
func (r *fakeAffRepo) BindInviter(ctx context.Context, userID, inviterID int64) (bool, error) {
	return false, nil
}
func (r *fakeAffRepo) UpdateUserAffCode(ctx context.Context, userID int64, newCode string) error {
	return nil
}
func (r *fakeAffRepo) ResetUserAffCode(ctx context.Context, userID int64) (string, error) {
	return "", nil
}
func (r *fakeAffRepo) SetUserRebateRate(ctx context.Context, userID int64, ratePercent *float64) error {
	return nil
}
func (r *fakeAffRepo) BatchSetUserRebateRate(ctx context.Context, userIDs []int64, ratePercent *float64) error {
	return nil
}
func (r *fakeAffRepo) ListUsersWithCustomSettings(ctx context.Context, filter service.AffiliateAdminFilter) ([]service.AffiliateAdminEntry, int64, error) {
	return nil, 0, nil
}
func (r *fakeAffRepo) ListAffiliateInviteRecords(ctx context.Context, filter service.AffiliateRecordFilter) ([]service.AffiliateInviteRecord, int64, error) {
	return nil, 0, nil
}
func (r *fakeAffRepo) ListAffiliateRebateRecords(ctx context.Context, filter service.AffiliateRecordFilter) ([]service.AffiliateRebateRecord, int64, error) {
	return nil, 0, nil
}
func (r *fakeAffRepo) GetAffiliateUserOverview(ctx context.Context, userID int64) (*service.AffiliateUserOverview, error) {
	return nil, nil
}

// --- 公共构造 ---

func ptsSettings() map[string]string {
	return map[string]string{
		service.SettingKeyPointsEnabled:            "true",
		service.SettingKeyPointsPeg:                "0.01",
		service.SettingKeyPointsCashbackRate:       "20",
		service.SettingKeyPointsFreezeHours:        "0",
		service.SettingKeyPointsWithdrawEnabled:    "true",
		service.SettingKeyPointsWithdrawMin:        "0",
		service.SettingKeyPointsWithdrawFeePercent: "10",
		service.SettingKeyPointsRedeemBalanceOn:    "true",
		service.SettingKeyPointsRedeemPlanOn:       "true",
	}
}

func subGroupH(id int64) service.Group {
	d := 2.0
	return service.Group{
		ID:                  id,
		Name:                "Plan",
		Status:              service.StatusActive,
		SubscriptionType:    service.SubscriptionTypeSubscription,
		DailyLimitUSD:       &d,
		DefaultValidityDays: 30,
	}
}

func newTestPointsHandler(ptsRepo *fakePtsRepo, grpRepo service.GroupRepository, affRepo service.AffiliateRepository, settings map[string]string) *PointsHandler {
	setSvc := service.NewSettingService(&fakeSetRepo{values: settings}, nil)
	affSvc := service.NewAffiliateService(affRepo, setSvc, nil, nil)
	ptsSvc := service.NewPointsService(ptsRepo, setSvc, nil, &service.SubscriptionService{}, grpRepo, affSvc, nil, nil)
	return NewPointsHandler(ptsSvc, affSvc)
}

func ptsTestCtx(method, body string, withSubject bool) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewBufferString(body)
	}
	c.Request = httptest.NewRequest(method, "/", rdr)
	c.Request.Header.Set("Content-Type", "application/json")
	if withSubject {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
	}
	return c, rec
}

func decodeCode(t *testing.T, rec *httptest.ResponseRecorder) int {
	var resp struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.Code
}

// --- Tests ---

func TestPointsHandler_GetOverview(t *testing.T) {
	h := newTestPointsHandler(
		&fakePtsRepo{account: &service.PointsAccount{UserID: 7, Available: 100}},
		&fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())

	c, rec := ptsTestCtx(http.MethodGet, "", true)
	h.GetOverview(c)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Account   map[string]any `json:"account"`
			Affiliate map[string]any `json:"affiliate"`
			Config    map[string]any `json:"config"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.EqualValues(t, 100, resp.Data.Account["available"])
	require.Equal(t, "CODE7", resp.Data.Affiliate["aff_code"])
	require.Equal(t, true, resp.Data.Config["enabled"])
}

func TestPointsHandler_GetOverview_Unauthorized(t *testing.T) {
	h := newTestPointsHandler(&fakePtsRepo{}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
	c, rec := ptsTestCtx(http.MethodGet, "", false)
	h.GetOverview(c)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestPointsHandler_ListLedger(t *testing.T) {
	h := newTestPointsHandler(
		&fakePtsRepo{ledger: []service.PointsLedgerEntry{{ID: 1}}, ledgerTotal: 1},
		&fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
	c, rec := ptsTestCtx(http.MethodGet, "", true)
	h.ListLedger(c)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, decodeCode(t, rec))
}

func TestPointsHandler_ListPlans(t *testing.T) {
	h := newTestPointsHandler(
		&fakePtsRepo{}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
	c, rec := ptsTestCtx(http.MethodGet, "", true)
	h.ListPlans(c)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Data struct {
			Plans []map[string]any `json:"plans"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data.Plans)
	require.EqualValues(t, 30, resp.Data.Plans[0]["daily_amount_usd"])
	require.EqualValues(t, 30, resp.Data.Plans[0]["validity_days"])
	require.NotContains(t, resp.Data.Plans[0], "group_id")
}

func TestPointsHandler_RedeemBalance(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{redeemBal: 5.5}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodPost, `{"points":100}`, true)
		h.RedeemBalance(c)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, 0, decodeCode(t, rec))
	})
	t.Run("bad json", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodPost, `{bad`, true)
		h.RedeemBalance(c)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("disabled -> error", func(t *testing.T) {
		s := ptsSettings()
		s[service.SettingKeyPointsEnabled] = "false"
		h := newTestPointsHandler(&fakePtsRepo{}, &fakeGrpRepo{}, &fakeAffRepo{}, s)
		c, rec := ptsTestCtx(http.MethodPost, `{"points":100}`, true)
		h.RedeemBalance(c)
		require.NotEqual(t, 0, decodeCode(t, rec))
	})
	t.Run("unauthorized", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodPost, `{"points":100}`, false)
		h.RedeemBalance(c)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestPointsHandler_RedeemPlan(t *testing.T) {
	t.Run("bad json", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodPost, `{bad`, true)
		h.RedeemPlan(c)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("disabled -> error (avoids tx)", func(t *testing.T) {
		s := ptsSettings()
		s[service.SettingKeyPointsEnabled] = "false"
		h := newTestPointsHandler(&fakePtsRepo{}, &fakeGrpRepo{}, &fakeAffRepo{}, s)
		c, rec := ptsTestCtx(http.MethodPost, `{"daily_amount_usd":30,"validity_days":30}`, true)
		h.RedeemPlan(c)
		require.NotEqual(t, 0, decodeCode(t, rec))
	})
	t.Run("unauthorized", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodPost, `{"daily_amount_usd":30,"validity_days":30}`, false)
		h.RedeemPlan(c)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestPointsHandler_ListWithdrawals(t *testing.T) {
	h := newTestPointsHandler(
		&fakePtsRepo{withdrawals: []service.PointsWithdrawal{{ID: 3}}},
		&fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
	c, rec := ptsTestCtx(http.MethodGet, "", true)
	h.ListWithdrawals(c)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, decodeCode(t, rec))
}

// --- 错误 / 未授权分支 ---

func TestPointsHandler_ErrorAndAuthBranches(t *testing.T) {
	t.Run("overview account error", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{ensureErr: errors.New("db")}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodGet, "", true)
		h.GetOverview(c)
		require.NotEqual(t, 0, decodeCode(t, rec))
	})
	t.Run("overview affiliate error", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{}, &fakeGrpRepo{}, &fakeAffRepo{ensureErr: errors.New("db")}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodGet, "", true)
		h.GetOverview(c)
		require.NotEqual(t, 0, decodeCode(t, rec))
	})
	t.Run("list ledger unauthorized", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodGet, "", false)
		h.ListLedger(c)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
	t.Run("list ledger repo error", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{ledgerErr: errors.New("db")}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodGet, "", true)
		h.ListLedger(c)
		require.NotEqual(t, 0, decodeCode(t, rec))
	})
	t.Run("list plans ignores group repo error after degroup", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{}, &fakeGrpRepo{activeErr: errors.New("db")}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodGet, "", true)
		h.ListPlans(c)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, 0, decodeCode(t, rec))
	})
	t.Run("list withdrawals unauthorized", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodGet, "", false)
		h.ListWithdrawals(c)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
	t.Run("list withdrawals repo error", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{uwErr: errors.New("db")}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodGet, "", true)
		h.ListWithdrawals(c)
		require.NotEqual(t, 0, decodeCode(t, rec))
	})
	t.Run("create withdrawal repo error", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{createErr: errors.New("db")}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodPost, `{"points":1000,"payout_method":"alipay","payout_alipay_account":"acc","payout_alipay_name":"name"}`, true)
		h.CreateWithdrawal(c)
		require.NotEqual(t, 0, decodeCode(t, rec))
	})
}

func TestPointsHandler_CreateWithdrawal(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodPost, `{"points":1000,"payout_method":"alipay","payout_alipay_account":"acc","payout_alipay_name":"name"}`, true)
		h.CreateWithdrawal(c)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, 0, decodeCode(t, rec))
	})
	t.Run("bad json", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodPost, `{bad`, true)
		h.CreateWithdrawal(c)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("unauthorized", func(t *testing.T) {
		h := newTestPointsHandler(&fakePtsRepo{}, &fakeGrpRepo{}, &fakeAffRepo{}, ptsSettings())
		c, rec := ptsTestCtx(http.MethodPost, `{"points":1000,"payout_method":"alipay"}`, false)
		h.CreateWithdrawal(c)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}
