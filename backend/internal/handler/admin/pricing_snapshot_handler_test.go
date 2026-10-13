//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubSnapshotAdmin struct {
	err error

	overview *service.SnapshotOverview
	pinRes   *service.PricingSnapshotPinResult
	fetchRes *service.PricingSnapshotFetchResult
	plan     *service.SnapshotApprovalPlan
	approved *service.PricingSnapshotMeta

	calls      []string
	lastAdmin  int64
	lastID     int64
	lastHolds  []string
	lastHash   string
	rejectedID int64
}

func (s *stubSnapshotAdmin) Overview(context.Context) (*service.SnapshotOverview, error) {
	s.calls = append(s.calls, "overview")
	return s.overview, s.err
}

func (s *stubSnapshotAdmin) Pin(_ context.Context, adminID int64) (*service.PricingSnapshotPinResult, error) {
	s.calls = append(s.calls, "pin")
	s.lastAdmin = adminID
	return s.pinRes, s.err
}

func (s *stubSnapshotAdmin) FetchCandidate(_ context.Context, adminID int64) (*service.PricingSnapshotFetchResult, error) {
	s.calls = append(s.calls, "fetch")
	s.lastAdmin = adminID
	return s.fetchRes, s.err
}

func (s *stubSnapshotAdmin) Preview(_ context.Context, id int64, holds []string) (*service.SnapshotApprovalPlan, error) {
	s.calls = append(s.calls, "preview")
	s.lastID, s.lastHolds = id, holds
	return s.plan, s.err
}

func (s *stubSnapshotAdmin) Approve(_ context.Context, id int64, holds []string, hash string, adminID int64) (*service.PricingSnapshotMeta, error) {
	s.calls = append(s.calls, "approve")
	s.lastID, s.lastHolds, s.lastHash, s.lastAdmin = id, holds, hash, adminID
	return s.approved, s.err
}

func (s *stubSnapshotAdmin) Reject(_ context.Context, id int64) error {
	s.calls = append(s.calls, "reject")
	s.rejectedID = id
	return s.err
}

type snapshotEnvelope struct {
	Code     int               `json:"code"`
	Reason   string            `json:"reason"`
	Metadata map[string]string `json:"metadata"`
	Data     json.RawMessage   `json:"data"`
}

func doSnapshotRequest(t *testing.T, stub *stubSnapshotAdmin, withSubject bool, method, path, body string) (*httptest.ResponseRecorder, snapshotEnvelope) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &PricingSnapshotHandler{svc: stub}
	router := gin.New()
	if withSubject {
		router.Use(func(c *gin.Context) {
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
			c.Next()
		})
	}
	router.GET("/snapshots", h.Overview)
	router.POST("/snapshots/pin", h.Pin)
	router.POST("/snapshots/fetch", h.Fetch)
	router.POST("/snapshots/:id/preview", h.Preview)
	router.POST("/snapshots/:id/approve", h.Approve)
	router.POST("/snapshots/:id/reject", h.Reject)

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var env snapshotEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	return rec, env
}

func TestPricingSnapshotHandler_OverviewAndFetch(t *testing.T) {
	stub := &stubSnapshotAdmin{
		overview: &service.SnapshotOverview{Mode: "pinned", PendingCandidates: []service.PricingSnapshotMeta{{ID: 5}}},
		fetchRes: &service.PricingSnapshotFetchResult{Unchanged: true},
	}
	rec, env := doSnapshotRequest(t, stub, true, http.MethodGet, "/snapshots", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, string(env.Data), `"mode":"pinned"`)

	rec, env = doSnapshotRequest(t, stub, true, http.MethodPost, "/snapshots/fetch", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, string(env.Data), `"unchanged":true`)
	require.Equal(t, int64(42), stub.lastAdmin)

	rec, _ = doSnapshotRequest(t, stub, false, http.MethodPost, "/snapshots/fetch", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestPricingSnapshotHandler_PinRequiresConfirmation(t *testing.T) {
	stub := &stubSnapshotAdmin{pinRes: &service.PricingSnapshotPinResult{Snapshot: &service.PricingSnapshotMeta{ID: 3}, Created: true}}
	for _, body := range []string{"", "{}", `{"confirm": false}`, "not json"} {
		rec, env := doSnapshotRequest(t, stub, true, http.MethodPost, "/snapshots/pin", body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
		require.Equal(t, "CONFIRMATION_REQUIRED", env.Reason)
	}
	require.Empty(t, stub.calls, "没有确认时不执行")

	rec, env := doSnapshotRequest(t, stub, true, http.MethodPost, "/snapshots/pin", `{"confirm": true}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, string(env.Data), `"created":true`)
	require.Equal(t, int64(42), stub.lastAdmin)

	rec, _ = doSnapshotRequest(t, stub, false, http.MethodPost, "/snapshots/pin", `{"confirm": true}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestPricingSnapshotHandler_PreviewAndApprove(t *testing.T) {
	stub := &stubSnapshotAdmin{
		plan:     &service.SnapshotApprovalPlan{PlanHash: "abc"},
		approved: &service.PricingSnapshotMeta{ID: 11},
	}
	rec, env := doSnapshotRequest(t, stub, true, http.MethodPost, "/snapshots/7/preview", `{"hold_models": ["a", "b"]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, string(env.Data), `"plan_hash":"abc"`)
	require.Equal(t, int64(7), stub.lastID)
	require.Equal(t, []string{"a", "b"}, stub.lastHolds)

	// 预览的请求体可以省略；写坏的 JSON 才是 400。
	rec, _ = doSnapshotRequest(t, stub, true, http.MethodPost, "/snapshots/7/preview", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, stub.lastHolds)
	rec, _ = doSnapshotRequest(t, stub, true, http.MethodPost, "/snapshots/7/preview", "{bad")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	for _, body := range []string{"", `{"confirm": true}`, `{"plan_hash": "abc"}`, `{"plan_hash": "abc", "confirm": false}`} {
		rec, env = doSnapshotRequest(t, stub, true, http.MethodPost, "/snapshots/7/approve", body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
		require.Equal(t, "CONFIRMATION_REQUIRED", env.Reason)
	}
	require.NotContains(t, stub.calls, "approve")

	rec, env = doSnapshotRequest(t, stub, true, http.MethodPost, "/snapshots/7/approve", `{"plan_hash": "abc", "confirm": true, "hold_models": ["a"]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, string(env.Data), `"id":11`)
	require.Equal(t, "abc", stub.lastHash)
	require.Equal(t, []string{"a"}, stub.lastHolds)
	require.Equal(t, int64(42), stub.lastAdmin)

	rec, _ = doSnapshotRequest(t, stub, false, http.MethodPost, "/snapshots/7/approve", `{"plan_hash": "abc", "confirm": true}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestPricingSnapshotHandler_RejectAndIDValidation(t *testing.T) {
	stub := &stubSnapshotAdmin{}
	rec, env := doSnapshotRequest(t, stub, true, http.MethodPost, "/snapshots/9/reject", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, string(env.Data), `"status":"rejected"`)
	require.Equal(t, int64(9), stub.rejectedID)

	for _, path := range []string{"/snapshots/abc/reject", "/snapshots/0/preview", "/snapshots/-1/approve"} {
		rec, env = doSnapshotRequest(t, stub, true, http.MethodPost, path, `{"plan_hash":"x","confirm":true}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		require.Equal(t, "INVALID_PARAMETER", env.Reason)
	}
	// 非法 id 在 approve 里先于管理员身份检查。
	rec, _ = doSnapshotRequest(t, stub, false, http.MethodPost, "/snapshots/abc/approve", `{}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPricingSnapshotHandler_ErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		reason string
	}{
		{service.ErrPricingSnapshotNotFound, http.StatusNotFound, "PRICING_SNAPSHOT_NOT_FOUND"},
		{service.ErrPricingNotPinned, http.StatusConflict, "PRICING_NOT_PINNED"},
		{service.ErrPricingAlreadyPinned, http.StatusConflict, "PRICING_ALREADY_PINNED"},
		{service.ErrPricingBootstrapMismatch, http.StatusConflict, "PRICING_BOOTSTRAP_MISMATCH"},
		{service.ErrPricingSnapshotNotCandidate, http.StatusConflict, "PRICING_SNAPSHOT_NOT_CANDIDATE"},
		{service.ErrPricingSnapshotPlanMismatch, http.StatusConflict, "PRICING_SNAPSHOT_PLAN_CHANGED"},
		{service.ErrPricingSnapshotBaselineChanged, http.StatusConflict, "PRICING_SNAPSHOT_BASELINE_CHANGED"},
		{service.ErrPricingSnapshotConflict, http.StatusConflict, "PRICING_SNAPSHOT_BASELINE_CHANGED"},
		{service.ErrPricingSnapshotNothingToApprove, http.StatusBadRequest, "PRICING_SNAPSHOT_NOTHING_TO_APPROVE"},
		{fmt.Errorf("%w: zz", service.ErrPricingSnapshotUnknownHold), http.StatusBadRequest, "PRICING_SNAPSHOT_UNKNOWN_HOLD"},
		{service.ErrPricingSnapshotExposureUnavailable, http.StatusInternalServerError, "PRICING_SNAPSHOT_EXPOSURE_UNAVAILABLE"},
	}
	for _, tc := range cases {
		stub := &stubSnapshotAdmin{err: tc.err}
		rec, env := doSnapshotRequest(t, stub, true, http.MethodPost, "/snapshots/3/preview", "")
		require.Equal(t, tc.status, rec.Code, tc.reason)
		require.Equal(t, tc.reason, env.Reason)
	}

	// 其余错误不被改写，也不暴露细节以外的信息：按 500 返回。
	stub := &stubSnapshotAdmin{err: errors.New("boom")}
	rec, _ := doSnapshotRequest(t, stub, true, http.MethodGet, "/snapshots", "")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	for _, path := range []string{"/snapshots/pin", "/snapshots/fetch"} {
		body := ""
		if strings.HasSuffix(path, "pin") {
			body = `{"confirm": true}`
		}
		rec, _ = doSnapshotRequest(t, stub, true, http.MethodPost, path, body)
		require.Equal(t, http.StatusInternalServerError, rec.Code, path)
	}
	rec, _ = doSnapshotRequest(t, stub, true, http.MethodPost, "/snapshots/3/approve", `{"plan_hash":"x","confirm":true}`)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	rec, _ = doSnapshotRequest(t, stub, true, http.MethodPost, "/snapshots/3/reject", "")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}
