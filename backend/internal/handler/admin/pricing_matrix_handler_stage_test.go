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
}

func (s *stubStageOperator) Switch(_ context.Context, req service.PricingStageSwitchRequest) (*service.PricingStageSwitchResult, error) {
	s.switchCalls++
	s.switchReq = req
	return s.switchResult, s.switchErr
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
