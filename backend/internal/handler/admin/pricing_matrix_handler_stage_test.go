//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubStageOperator struct {
	switchReq    service.PricingStageSwitchRequest
	switchCalls  int
	switchResult *service.PricingStageSwitchResult
	switchErr    error
	samples      []service.PricingShadowSample
	samplesErr   error
	gotGroup     int64
	gotLimit     int

	previewReq    service.PricingStagePreviewRequest
	previewResult *service.PricingStagePreview
	previewErr    error
	audit         []service.StageAuditEntry
	auditErr      error
}

func (s *stubStageOperator) Switch(_ context.Context, req service.PricingStageSwitchRequest) (*service.PricingStageSwitchResult, error) {
	s.switchCalls++
	s.switchReq = req
	return s.switchResult, s.switchErr
}

func (s *stubStageOperator) Preview(_ context.Context, req service.PricingStagePreviewRequest) (*service.PricingStagePreview, error) {
	s.previewReq = req
	return s.previewResult, s.previewErr
}

func (s *stubStageOperator) Audit(_ context.Context, groupID int64, limit int) ([]service.StageAuditEntry, error) {
	s.gotGroup, s.gotLimit = groupID, limit
	return s.audit, s.auditErr
}

func (s *stubStageOperator) ShadowStats() service.PricingShadowStats {
	return service.PricingShadowStats{PanicsTotal: 3}
}

func (s *stubStageOperator) MatrixSnapshotStats() service.MatrixSnapshotStats {
	return service.MatrixSnapshotStats{}
}

func (s *stubStageOperator) ShadowSamples(_ context.Context, groupID int64, limit int) ([]service.PricingShadowSample, error) {
	s.gotGroup, s.gotLimit = groupID, limit
	return s.samples, s.samplesErr
}

func doStageRequest(t *testing.T, op *stubStageOperator, withUser bool, method, path, body string) (*httptest.ResponseRecorder, pricingMatrixEnvelope) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &PricingMatrixHandler{stage: op}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if withUser {
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
		}
		c.Next()
	})
	router.PUT("/pricing-matrix/groups/:id/stage", h.SwitchStage)
	router.POST("/pricing-matrix/groups/:id/stage/preview", h.PreviewStage)
	router.GET("/pricing-matrix/groups/:id/stage/audit", h.StageAudit)
	router.GET("/pricing-matrix/shadow/stats", h.ShadowStats)
	router.GET("/pricing-matrix/shadow/diffs", h.ShadowDiffs)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewBufferString(body)))
	var env pricingMatrixEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	return rec, env
}

func TestPricingMatrixHandler_SwitchStage(t *testing.T) {
	op := &stubStageOperator{switchResult: &service.PricingStageSwitchResult{Action: service.PricingActionStageSwitch}}
	rec, env := doStageRequest(t, op, true, http.MethodPut, "/pricing-matrix/groups/7/stage", `{"stage":" shadow ","confirm":true}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, env.Code)
	require.Equal(t, service.PricingStageSwitchRequest{GroupID: 7, To: service.PricingStageShadow, OperatorID: 42, Confirm: true}, op.switchReq)
	require.Contains(t, string(env.Data), service.PricingActionStageSwitch)
}

func TestPricingMatrixHandler_SwitchStageCarriesApprovalAndAuthMethod(t *testing.T) {
	op := &stubStageOperator{switchResult: &service.PricingStageSwitchResult{Action: service.PricingActionStageSwitch}}
	rec, _ := doStageRequest(t, op, true, http.MethodPut, "/pricing-matrix/groups/7/stage", `{"stage":"v2","confirm":true,"approval_id":12}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 12, op.switchReq.ApprovalID)
	require.Equal(t, service.PricingStageV2, op.switchReq.To)
	// 测试里没有鉴权中间件，auth_method 为空：不能被当作交互式会话。
	require.Empty(t, op.switchReq.AuthMethod)
	require.False(t, service.PriceWriteActorFromAuthMethod(1, op.switchReq.AuthMethod).Interactive)
}

func TestPricingMatrixHandler_PreviewStageAndAudit(t *testing.T) {
	op := &stubStageOperator{previewResult: &service.PricingStagePreview{Action: service.PricingActionStageSwitch, ApprovalID: 5},
		audit: []service.StageAuditEntry{{ID: 1, GroupID: 7}}}
	rec, env := doStageRequest(t, op, true, http.MethodPost, "/pricing-matrix/groups/7/stage/preview", `{"stage":" v2 "}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.PricingStagePreviewRequest{GroupID: 7, To: service.PricingStageV2, OperatorID: 42}, op.previewReq)
	require.Contains(t, string(env.Data), `"approval_id":5`)

	rec, _ = doStageRequest(t, op, false, http.MethodPost, "/pricing-matrix/groups/7/stage/preview", `{"stage":"v2"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	rec, env = doStageRequest(t, op, true, http.MethodPost, "/pricing-matrix/groups/7/stage/preview", `{}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "VALIDATION_ERROR", env.Reason)

	rec, env = doStageRequest(t, op, true, http.MethodGet, "/pricing-matrix/groups/7/stage/audit?limit=10", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 7, op.gotGroup)
	require.Equal(t, 10, op.gotLimit)
	require.Contains(t, string(env.Data), `"items"`)
	rec, env = doStageRequest(t, op, true, http.MethodGet, "/pricing-matrix/groups/7/stage/audit?limit=0", "")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "INVALID_PARAMETER", env.Reason)
}

func TestPricingMatrixHandler_SwitchStageRejections(t *testing.T) {
	op := &stubStageOperator{}
	rec, env := doStageRequest(t, op, true, http.MethodPut, "/pricing-matrix/groups/abc/stage", `{"stage":"shadow"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "INVALID_PARAMETER", env.Reason)

	rec, env = doStageRequest(t, op, true, http.MethodPut, "/pricing-matrix/groups/7/stage", `{}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "VALIDATION_ERROR", env.Reason)

	rec, _ = doStageRequest(t, op, false, http.MethodPut, "/pricing-matrix/groups/7/stage", `{"stage":"shadow","confirm":true}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Zero(t, op.switchCalls)

	op.switchErr = infraerrors.BadRequest(service.ReasonPricingStageNotAllowed, "no")
	rec, env = doStageRequest(t, op, true, http.MethodPut, "/pricing-matrix/groups/7/stage", `{"stage":"v2","confirm":true}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, service.ReasonPricingStageNotAllowed, env.Reason)
}

func TestPricingMatrixHandler_ShadowStats(t *testing.T) {
	rec, env := doStageRequest(t, &stubStageOperator{}, true, http.MethodGet, "/pricing-matrix/shadow/stats", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var data map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(env.Data, &data))
	require.Contains(t, data, "metrics")
	require.Contains(t, data, "snapshot")
}

func TestPricingMatrixHandler_ShadowDiffs(t *testing.T) {
	op := &stubStageOperator{samples: []service.PricingShadowSample{{GroupID: 9, Kind: "cost"}}}
	rec, env := doStageRequest(t, op, true, http.MethodGet, "/pricing-matrix/shadow/diffs", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, string(env.Data), `"items"`)
	require.Equal(t, int64(0), op.gotGroup)
	require.Equal(t, 50, op.gotLimit)

	rec, _ = doStageRequest(t, op, true, http.MethodGet, "/pricing-matrix/shadow/diffs?group_id=9&limit=5", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(9), op.gotGroup)
	require.Equal(t, 5, op.gotLimit)

	for _, q := range []string{"group_id=x", "group_id=0", "limit=x", "limit=0"} {
		rec, env = doStageRequest(t, op, true, http.MethodGet, "/pricing-matrix/shadow/diffs?"+q, "")
		require.Equal(t, http.StatusBadRequest, rec.Code, q)
		require.Equal(t, "INVALID_PARAMETER", env.Reason, q)
	}

	op.samplesErr = errors.New("db down")
	rec, _ = doStageRequest(t, op, true, http.MethodGet, "/pricing-matrix/shadow/diffs", "")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

// 阶段切换提交挂了 RequireAdminJWT：机器令牌（任何 scope）与旧的全局 admin key 拿不到交互式会话，切换请求到不了 handler。
func TestPricingMatrixHandler_SwitchStageIsJWTOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	run := func(authMethod string) (int, *stubStageOperator) {
		op := &stubStageOperator{switchResult: &service.PricingStageSwitchResult{Action: service.PricingActionStageSwitch}}
		h := &PricingMatrixHandler{stage: op}
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set("auth_method", authMethod)
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
			c.Next()
		})
		router.PUT("/pricing-matrix/groups/:id/stage", middleware2.RequireAdminJWT(), h.SwitchStage)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/pricing-matrix/groups/7/stage",
			bytes.NewBufferString(`{"stage":"v2","confirm":true,"approval_id":3}`)))
		return rec.Code, op
	}
	for _, method := range []string{"admin_token", "api_key", ""} {
		code, op := run(method)
		require.Equal(t, http.StatusForbidden, code, method)
		require.Zero(t, op.switchCalls, method)
	}
	code, op := run(service.AuditAuthMethodJWT)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, 1, op.switchCalls)
	require.True(t, service.PriceWriteActorFromAuthMethod(1, op.switchReq.AuthMethod).Interactive)
}

func TestPricingMatrixHandler_PreviewAndAuditRejectionsAndErrorMapping(t *testing.T) {
	op := &stubStageOperator{}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/pricing-matrix/groups/abc/stage/preview", `{"stage":"v2"}`},
		{http.MethodGet, "/pricing-matrix/groups/abc/stage/audit", ""},
		{http.MethodGet, "/pricing-matrix/groups/7/stage/audit?limit=abc", ""},
	} {
		rec, env := doStageRequest(t, op, true, c.method, c.path, c.body)
		require.Equal(t, http.StatusBadRequest, rec.Code, c.path)
		require.Equal(t, "INVALID_PARAMETER", env.Reason, c.path)
	}
	require.Zero(t, op.gotGroup, "no request reached the service")

	op.previewErr = infraerrors.Conflict("PRICING_GATE_FAILED", "gate")
	rec, env := doStageRequest(t, op, true, http.MethodPost, "/pricing-matrix/groups/7/stage/preview", `{"stage":"v2"}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "PRICING_GATE_FAILED", env.Reason)

	op.auditErr = infraerrors.NotFound("GROUP_NOT_FOUND", "no such group")
	rec, env = doStageRequest(t, op, true, http.MethodGet, "/pricing-matrix/groups/7/stage/audit", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "GROUP_NOT_FOUND", env.Reason)
}
