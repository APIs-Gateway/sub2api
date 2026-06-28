//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 邀请返利积分制（issue #11）—— 后台 admin.PointsHandler 的 handler 层单测。
// fake PointsRepository + SettingRepository 拼真实 PointsService（admin 方法只触达这二者）。

type fakeAdminPtsRepo struct {
	withdrawals []service.PointsWithdrawal
	wTotal      int64
	ledger      []service.PointsLedgerEntry
	ledgerTotal int64

	// 错误注入（覆盖 handler 的 ErrorFrom 分支）
	listWErr  error
	reviewErr error
	listLErr  error
}

func (r *fakeAdminPtsRepo) EnsureAccount(ctx context.Context, userID int64) (*service.PointsAccount, error) {
	return &service.PointsAccount{UserID: userID}, nil
}
func (r *fakeAdminPtsRepo) GetAccount(ctx context.Context, userID int64) (*service.PointsAccount, error) {
	return &service.PointsAccount{UserID: userID}, nil
}
func (r *fakeAdminPtsRepo) EarnPoints(ctx context.Context, in service.EarnPointsInput) (bool, error) {
	return true, nil
}
func (r *fakeAdminPtsRepo) ClawbackByOrder(ctx context.Context, sourceOrderID int64, refundAmount, originalAmount float64) (int64, error) {
	return 0, nil
}
func (r *fakeAdminPtsRepo) ThawDuePoints(ctx context.Context, userID int64) (int64, error) {
	return 0, nil
}
func (r *fakeAdminPtsRepo) RedeemToBalance(ctx context.Context, userID, points int64, balanceDelta, pegAt float64) (float64, error) {
	return 0, nil
}
func (r *fakeAdminPtsRepo) DeductForPlan(ctx context.Context, userID, points int64, pegAt float64, note, idempotencyKey string) error {
	return nil
}
func (r *fakeAdminPtsRepo) CreateWithdrawal(ctx context.Context, in service.CreateWithdrawalInput) (*service.PointsWithdrawal, error) {
	return &service.PointsWithdrawal{ID: 1}, nil
}
func (r *fakeAdminPtsRepo) GetWithdrawal(ctx context.Context, id int64) (*service.PointsWithdrawal, error) {
	return &service.PointsWithdrawal{ID: id}, nil
}
func (r *fakeAdminPtsRepo) ReviewWithdrawal(ctx context.Context, id, adminID int64, approve bool, note, payoutProof string) (*service.PointsWithdrawal, error) {
	if r.reviewErr != nil {
		return nil, r.reviewErr
	}
	return &service.PointsWithdrawal{ID: id}, nil
}
func (r *fakeAdminPtsRepo) ListUserWithdrawals(ctx context.Context, userID int64, limit int) ([]service.PointsWithdrawal, error) {
	return nil, nil
}
func (r *fakeAdminPtsRepo) ListUserLedger(ctx context.Context, userID int64, page, pageSize int) ([]service.PointsLedgerEntry, int64, error) {
	return nil, 0, nil
}
func (r *fakeAdminPtsRepo) ListWithdrawals(ctx context.Context, filter service.PointsWithdrawalFilter) ([]service.PointsWithdrawal, int64, error) {
	return r.withdrawals, r.wTotal, r.listWErr
}
func (r *fakeAdminPtsRepo) ListLedger(ctx context.Context, filter service.PointsLedgerFilter) ([]service.PointsLedgerEntry, int64, error) {
	return r.ledger, r.ledgerTotal, r.listLErr
}

type fakeAdminSetRepo struct {
	values      map[string]string
	setMultiErr error
}

func (r *fakeAdminSetRepo) Get(ctx context.Context, key string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}
func (r *fakeAdminSetRepo) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}
func (r *fakeAdminSetRepo) Set(ctx context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
func (r *fakeAdminSetRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	return nil, nil
}
func (r *fakeAdminSetRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	if r.setMultiErr != nil {
		return r.setMultiErr
	}
	for k, v := range settings {
		r.values[k] = v
	}
	return nil
}
func (r *fakeAdminSetRepo) GetAll(ctx context.Context) (map[string]string, error) {
	return r.values, nil
}
func (r *fakeAdminSetRepo) Delete(ctx context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func adminSettings() map[string]string {
	return map[string]string{
		service.SettingKeyPointsEnabled:            "true",
		service.SettingKeyPointsPeg:                "0.01",
		service.SettingKeyPointsCashbackRate:       "20",
		service.SettingKeyPointsWithdrawEnabled:    "true",
		service.SettingKeyPointsWithdrawFeePercent: "10",
	}
}

func newTestAdminPointsHandler(repo service.PointsRepository, settings map[string]string) *PointsHandler {
	setSvc := service.NewSettingService(&fakeAdminSetRepo{values: settings}, nil)
	ptsSvc := service.NewPointsService(repo, setSvc, nil, nil, nil, nil, nil, nil)
	return NewPointsHandler(ptsSvc)
}

func adminTestCtx(method, body string, withSubject bool, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewBufferString(body)
	}
	c.Request = httptest.NewRequest(method, "/", rdr)
	c.Request.Header.Set("Content-Type", "application/json")
	if params != nil {
		c.Params = params
	}
	if withSubject {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 99})
	}
	return c, rec
}

func adminCode(t *testing.T, rec *httptest.ResponseRecorder) int {
	var resp struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.Code
}

func TestAdminPointsHandler_GetSettings(t *testing.T) {
	h := newTestAdminPointsHandler(&fakeAdminPtsRepo{}, adminSettings())
	c, rec := adminTestCtx(http.MethodGet, "", true, nil)
	h.GetSettings(c)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, adminCode(t, rec))
}

func TestAdminPointsHandler_UpdateSettings(t *testing.T) {
	t.Run("success with peg change audit", func(t *testing.T) {
		h := newTestAdminPointsHandler(&fakeAdminPtsRepo{}, adminSettings())
		// peg 0.01 -> 0.02 触发 audit 分支（需 subject）。
		c, rec := adminTestCtx(http.MethodPut, `{"enabled":true,"peg":0.02,"cashback_rate_percent":20,"withdraw_enabled":true}`, true, nil)
		h.UpdateSettings(c)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, 0, adminCode(t, rec))
	})
	t.Run("bad json", func(t *testing.T) {
		h := newTestAdminPointsHandler(&fakeAdminPtsRepo{}, adminSettings())
		c, rec := adminTestCtx(http.MethodPut, `{bad`, true, nil)
		h.UpdateSettings(c)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestAdminPointsHandler_ListWithdrawals(t *testing.T) {
	h := newTestAdminPointsHandler(&fakeAdminPtsRepo{withdrawals: []service.PointsWithdrawal{{ID: 1}}, wTotal: 1}, adminSettings())
	c, rec := adminTestCtx(http.MethodGet, "", true, nil)
	c.Request = httptest.NewRequest(http.MethodGet, "/?status=pending&search=x", nil)
	h.ListWithdrawals(c)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, adminCode(t, rec))
}

func TestAdminPointsHandler_Review(t *testing.T) {
	t.Run("approve success", func(t *testing.T) {
		h := newTestAdminPointsHandler(&fakeAdminPtsRepo{}, adminSettings())
		c, rec := adminTestCtx(http.MethodPost, `{"payout_proof":"txhash"}`, true, gin.Params{{Key: "id", Value: "5"}})
		h.ApproveWithdrawal(c)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, 0, adminCode(t, rec))
	})
	t.Run("reject success", func(t *testing.T) {
		h := newTestAdminPointsHandler(&fakeAdminPtsRepo{}, adminSettings())
		c, rec := adminTestCtx(http.MethodPost, `{"note":"invalid account"}`, true, gin.Params{{Key: "id", Value: "5"}})
		h.RejectWithdrawal(c)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, 0, adminCode(t, rec))
	})
	t.Run("bad id", func(t *testing.T) {
		h := newTestAdminPointsHandler(&fakeAdminPtsRepo{}, adminSettings())
		c, rec := adminTestCtx(http.MethodPost, `{}`, true, gin.Params{{Key: "id", Value: "abc"}})
		h.ApproveWithdrawal(c)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("unauthorized", func(t *testing.T) {
		h := newTestAdminPointsHandler(&fakeAdminPtsRepo{}, adminSettings())
		c, rec := adminTestCtx(http.MethodPost, `{}`, false, gin.Params{{Key: "id", Value: "5"}})
		h.ApproveWithdrawal(c)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
	t.Run("empty body tolerated", func(t *testing.T) {
		h := newTestAdminPointsHandler(&fakeAdminPtsRepo{}, adminSettings())
		c, rec := adminTestCtx(http.MethodPost, "", true, gin.Params{{Key: "id", Value: "5"}})
		h.RejectWithdrawal(c)
		require.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestAdminPointsHandler_ErrorBranches(t *testing.T) {
	t.Run("update settings persist error", func(t *testing.T) {
		setSvc := service.NewSettingService(&fakeAdminSetRepo{values: adminSettings(), setMultiErr: errors.New("db")}, nil)
		ptsSvc := service.NewPointsService(&fakeAdminPtsRepo{}, setSvc, nil, nil, nil, nil, nil, nil)
		h := NewPointsHandler(ptsSvc)
		c, rec := adminTestCtx(http.MethodPut, `{"enabled":true,"peg":0.02}`, true, nil)
		h.UpdateSettings(c)
		require.NotEqual(t, 0, adminCode(t, rec))
	})
	t.Run("list withdrawals error", func(t *testing.T) {
		h := newTestAdminPointsHandler(&fakeAdminPtsRepo{listWErr: errors.New("db")}, adminSettings())
		c, rec := adminTestCtx(http.MethodGet, "", true, nil)
		h.ListWithdrawals(c)
		require.NotEqual(t, 0, adminCode(t, rec))
	})
	t.Run("review error", func(t *testing.T) {
		h := newTestAdminPointsHandler(&fakeAdminPtsRepo{reviewErr: errors.New("db")}, adminSettings())
		c, rec := adminTestCtx(http.MethodPost, `{}`, true, gin.Params{{Key: "id", Value: "5"}})
		h.ApproveWithdrawal(c)
		require.NotEqual(t, 0, adminCode(t, rec))
	})
	t.Run("list ledger error", func(t *testing.T) {
		h := newTestAdminPointsHandler(&fakeAdminPtsRepo{listLErr: errors.New("db")}, adminSettings())
		c, rec := adminTestCtx(http.MethodGet, "", true, nil)
		h.ListLedger(c)
		require.NotEqual(t, 0, adminCode(t, rec))
	})
}

func TestAdminPointsHandler_ListLedger(t *testing.T) {
	h := newTestAdminPointsHandler(&fakeAdminPtsRepo{ledger: []service.PointsLedgerEntry{{ID: 2}}, ledgerTotal: 1}, adminSettings())
	c, rec := adminTestCtx(http.MethodGet, "", true, nil)
	c.Request = httptest.NewRequest(http.MethodGet, "/?kind=earn&search=y", nil)
	h.ListLedger(c)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, adminCode(t, rec))
}
