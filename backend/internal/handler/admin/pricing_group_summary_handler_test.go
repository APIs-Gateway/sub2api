//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubGroupSummaries struct {
	calls   int
	lastIDs []int64
	res     *service.GroupPricingSummaryResult
	err     error
}

func (s *stubGroupSummaries) GroupSummaries(_ context.Context, ids []int64) (*service.GroupPricingSummaryResult, error) {
	s.calls++
	s.lastIDs = ids
	return s.res, s.err
}

func doGroupSummariesRequest(t *testing.T, stub *stubGroupSummaries, path string) (*httptest.ResponseRecorder, pricingMatrixEnvelope) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &PricingMatrixHandler{summaries: stub}
	router := gin.New()
	router.GET("/pricing-matrix/groups/summary", h.GroupSummaries)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var env pricingMatrixEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	return rec, env
}

func TestPricingMatrixHandler_GroupSummaries(t *testing.T) {
	changed := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	stub := &stubGroupSummaries{res: &service.GroupPricingSummaryResult{
		Items: []service.GroupPricingSummary{{
			GroupID: 3, Name: "codex", Platform: "openai", HasConfig: true, Stage: service.PricingStageShadow,
			Revision: 5, Access: service.MatrixAccessAllowlist, CostMode: service.MatrixCostAccountRate,
			StageChangedAt: &changed, CostRules: service.CostRuleSummary{Total: 2, Enabled: 1, Manual: 2},
		}},
		MissingIDs: []int64{9},
	}}
	rec, env := doGroupSummariesRequest(t, stub, "/pricing-matrix/groups/summary?ids=3,%209,3")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []int64{3, 9, 3}, stub.lastIDs, "去重交给服务层")

	var data struct {
		Items     []map[string]any `json:"items"`
		Missing   []int64          `json:"missing_ids"`
		Truncated bool             `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal(env.Data, &data))
	require.Len(t, data.Items, 1)
	it := data.Items[0]
	require.EqualValues(t, 3, it["group_id"])
	require.Equal(t, "shadow", it["pricing_stage"])
	require.EqualValues(t, 5, it["config_revision"])
	require.Equal(t, "allowlist", it["access_mode"])
	require.Equal(t, "2026-10-05T08:00:00Z", it["stage_changed_at"])
	require.Nil(t, it["config_updated_at"])
	require.Equal(t, map[string]any{"total": float64(2), "enabled": float64(1), "legacy_derived": float64(0),
		"legacy_frozen": float64(0), "manual": float64(2)}, it["cost_rules"])
	require.Equal(t, []int64{9}, data.Missing)
	require.False(t, data.Truncated)
}

func TestPricingMatrixHandler_GroupSummaries_NoIDsMeansAll(t *testing.T) {
	stub := &stubGroupSummaries{res: &service.GroupPricingSummaryResult{Items: []service.GroupPricingSummary{}, MissingIDs: []int64{}}}
	for _, path := range []string{"/pricing-matrix/groups/summary", "/pricing-matrix/groups/summary?ids="} {
		rec, _ := doGroupSummariesRequest(t, stub, path)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Empty(t, stub.lastIDs)
	}
	require.Equal(t, 2, stub.calls)
}

func TestPricingMatrixHandler_GroupSummaries_Validation(t *testing.T) {
	for _, path := range []string{
		"/pricing-matrix/groups/summary?ids=abc",
		"/pricing-matrix/groups/summary?ids=1,0",
		"/pricing-matrix/groups/summary?ids=-2",
		"/pricing-matrix/groups/summary?ids=,",
	} {
		stub := &stubGroupSummaries{}
		rec, env := doGroupSummariesRequest(t, stub, path)
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		require.Equal(t, "INVALID_PARAMETER", env.Reason, path)
		require.Equal(t, "ids", env.Metadata["param"], path)
		require.Zero(t, stub.calls, path)
	}
}

func TestPricingMatrixHandler_GroupSummaries_ServiceErrors(t *testing.T) {
	stub := &stubGroupSummaries{err: infraerrors.BadRequest(service.ReasonGroupSummaryTooMany, "too many group ids")}
	rec, env := doGroupSummariesRequest(t, stub, "/pricing-matrix/groups/summary?ids=1")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, service.ReasonGroupSummaryTooMany, env.Reason)

	stub = &stubGroupSummaries{err: errors.New("db down")}
	rec, _ = doGroupSummariesRequest(t, stub, "/pricing-matrix/groups/summary")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}
