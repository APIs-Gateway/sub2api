package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type unpricedUsageRepoStub struct {
	service.OpsRepository
	rows   []*service.OpsUnpricedBillingRow
	filter *service.OpsUnpricedBillingFilter
}

func (s *unpricedUsageRepoStub) ListUnpricedBillingUsage(_ context.Context, filter *service.OpsUnpricedBillingFilter) ([]*service.OpsUnpricedBillingRow, error) {
	s.filter = filter
	return s.rows, nil
}

func newUnpricedUsageHandlerForTest(repo service.OpsRepository) *OpsHandler {
	return NewOpsHandler(service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
}

func callGetUnpricedUsage(h *OpsHandler, target string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	h.GetUnpricedUsage(c)
	return recorder
}

func TestGetUnpricedUsageValidatesQuery(t *testing.T) {
	for _, path := range []string{
		"/api/v1/admin/ops/unpriced-usage?window=30d",
		"/api/v1/admin/ops/unpriced-usage?window=1h",
		"/api/v1/admin/ops/unpriced-usage?group_id=0",
		"/api/v1/admin/ops/unpriced-usage?group_id=-3",
		"/api/v1/admin/ops/unpriced-usage?group_id=abc",
		"/api/v1/admin/ops/unpriced-usage?limit=0",
		"/api/v1/admin/ops/unpriced-usage?limit=-1",
		"/api/v1/admin/ops/unpriced-usage?limit=abc",
	} {
		t.Run(path, func(t *testing.T) {
			recorder := callGetUnpricedUsage(newUnpricedUsageHandlerForTest(&unpricedUsageRepoStub{}), path)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

func TestGetUnpricedUsageServiceUnavailableWithoutOpsService(t *testing.T) {
	recorder := callGetUnpricedUsage(NewOpsHandler(nil), "/api/v1/admin/ops/unpriced-usage")
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestGetUnpricedUsageReturnsEmptyReportWhenRepositoryHasNoSupport(t *testing.T) {
	recorder := callGetUnpricedUsage(newUnpricedUsageHandlerForTest(nil), "/api/v1/admin/ops/unpriced-usage")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"items":[]`)
}

func TestGetUnpricedUsageReturnsReportAndPassesFilters(t *testing.T) {
	groupID := int64(16)
	repo := &unpricedUsageRepoStub{rows: []*service.OpsUnpricedBillingRow{
		{GroupID: &groupID, GroupName: "openai-main", Platform: "openai", Model: "gpt-x", Rows: 4, InputTokens: 400},
	}}

	recorder := callGetUnpricedUsage(newUnpricedUsageHandlerForTest(repo),
		"/api/v1/admin/ops/unpriced-usage?window=7d&platform=OpenAI&group_id=16&limit=10")
	require.Equal(t, http.StatusOK, recorder.Code)

	var body struct {
		Data struct {
			Window       string `json:"window"`
			Platform     string `json:"platform"`
			TotalRows    int64  `json:"total_rows"`
			UnpricedRows int64  `json:"unpriced_rows"`
			Items        []struct {
				GroupID   *int64 `json:"group_id"`
				GroupName string `json:"group_name"`
				Model     string `json:"model"`
				Rows      int64  `json:"rows"`
				KnownFree bool   `json:"known_free"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, "7d", body.Data.Window)
	require.Equal(t, "openai", body.Data.Platform)
	require.EqualValues(t, 4, body.Data.TotalRows)
	require.EqualValues(t, 4, body.Data.UnpricedRows)
	require.Len(t, body.Data.Items, 1)
	require.Equal(t, "gpt-x", body.Data.Items[0].Model)
	require.Equal(t, "openai-main", body.Data.Items[0].GroupName)
	require.False(t, body.Data.Items[0].KnownFree)

	require.NotNil(t, repo.filter)
	require.Equal(t, "openai", repo.filter.Platform)
	require.NotNil(t, repo.filter.GroupID)
	require.EqualValues(t, 16, *repo.filter.GroupID)
}
