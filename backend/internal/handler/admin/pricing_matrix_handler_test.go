//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubDeriveViewer struct {
	groupCalls   int
	channelCalls int
	lastID       int64
	groupView    *service.GroupDeriveView
	channelViews []service.GroupDeriveView
	err          error
	stats        service.PricingDeriveStats
}

func (s *stubDeriveViewer) ViewGroup(_ context.Context, id int64) (*service.GroupDeriveView, error) {
	s.groupCalls++
	s.lastID = id
	return s.groupView, s.err
}

func (s *stubDeriveViewer) ViewChannel(_ context.Context, id int64) ([]service.GroupDeriveView, error) {
	s.channelCalls++
	s.lastID = id
	return s.channelViews, s.err
}

func (s *stubDeriveViewer) Stats() service.PricingDeriveStats { return s.stats }

type stubCatalogLister struct {
	calls      int
	lastFilter service.ModelCatalogFilter
	entries    []service.ModelCatalogEntry
	err        error
}

func (s *stubCatalogLister) List(_ context.Context, filter service.ModelCatalogFilter) ([]service.ModelCatalogEntry, error) {
	s.calls++
	s.lastFilter = filter
	return s.entries, s.err
}

type pricingMatrixEnvelope struct {
	Code     int               `json:"code"`
	Reason   string            `json:"reason"`
	Metadata map[string]string `json:"metadata"`
	Data     json.RawMessage   `json:"data"`
}

func doPricingMatrixRequest(t *testing.T, derive *stubDeriveViewer, catalog *stubCatalogLister, path string) (*httptest.ResponseRecorder, pricingMatrixEnvelope) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &PricingMatrixHandler{derive: derive, catalog: catalog}
	router := gin.New()
	router.GET("/pricing-matrix/groups/:id/derive", h.ViewGroupDerive)
	router.GET("/pricing-matrix/channels/:id/derive", h.ViewChannelDerive)
	router.GET("/pricing-matrix/hook-stats", h.HookStats)
	router.GET("/model-catalog", h.ListModelCatalog)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var env pricingMatrixEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	return rec, env
}

func TestPricingMatrixHandler_IDValidation(t *testing.T) {
	for _, path := range []string{
		"/pricing-matrix/groups/abc/derive", "/pricing-matrix/groups/0/derive", "/pricing-matrix/groups/-3/derive",
		"/pricing-matrix/channels/abc/derive", "/pricing-matrix/channels/0/derive",
	} {
		t.Run(path, func(t *testing.T) {
			derive := &stubDeriveViewer{}
			rec, env := doPricingMatrixRequest(t, derive, &stubCatalogLister{}, path)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, "INVALID_PARAMETER", env.Reason)
			require.Equal(t, "id", env.Metadata["param"])
			require.Zero(t, derive.groupCalls+derive.channelCalls, "参数校验失败时不调用服务")
		})
	}
}

func TestPricingMatrixHandler_ViewGroupDerive(t *testing.T) {
	derive := &stubDeriveViewer{groupView: &service.GroupDeriveView{GroupID: 7, Platform: "openai", InSync: true}}
	rec, env := doPricingMatrixRequest(t, derive, &stubCatalogLister{}, "/pricing-matrix/groups/7/derive")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, env.Code)
	require.Equal(t, int64(7), derive.lastID)

	var data map[string]any
	require.NoError(t, json.Unmarshal(env.Data, &data))
	require.EqualValues(t, 7, data["group_id"])
	require.Equal(t, "openai", data["platform"])
	require.Equal(t, true, data["in_sync"])
}

func TestPricingMatrixHandler_ViewChannelDerive(t *testing.T) {
	derive := &stubDeriveViewer{channelViews: []service.GroupDeriveView{{GroupID: 1}, {GroupID: 2}}}
	rec, env := doPricingMatrixRequest(t, derive, &stubCatalogLister{}, "/pricing-matrix/channels/5/derive")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(5), derive.lastID)

	var data struct {
		ChannelID int64                     `json:"channel_id"`
		Groups    []service.GroupDeriveView `json:"groups"`
	}
	require.NoError(t, json.Unmarshal(env.Data, &data))
	require.Equal(t, int64(5), data.ChannelID)
	require.Len(t, data.Groups, 2)
}

func TestPricingMatrixHandler_ServiceErrorsKeepTheirStatus(t *testing.T) {
	derive := &stubDeriveViewer{err: service.ErrGroupNotFound}
	rec, env := doPricingMatrixRequest(t, derive, &stubCatalogLister{}, "/pricing-matrix/groups/7/derive")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "GROUP_NOT_FOUND", env.Reason)

	derive = &stubDeriveViewer{err: service.ErrChannelNotFound}
	rec, env = doPricingMatrixRequest(t, derive, &stubCatalogLister{}, "/pricing-matrix/channels/7/derive")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "CHANNEL_NOT_FOUND", env.Reason)

	derive = &stubDeriveViewer{err: errors.New("db down")}
	rec, _ = doPricingMatrixRequest(t, derive, &stubCatalogLister{}, "/pricing-matrix/groups/7/derive")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestPricingMatrixHandler_HookStats(t *testing.T) {
	derive := &stubDeriveViewer{stats: service.PricingDeriveStats{Runs: 5, Failures: 2, Panics: 1}}
	rec, env := doPricingMatrixRequest(t, derive, &stubCatalogLister{}, "/pricing-matrix/hook-stats")
	require.Equal(t, http.StatusOK, rec.Code)
	var stats service.PricingDeriveStats
	require.NoError(t, json.Unmarshal(env.Data, &stats))
	require.Equal(t, derive.stats, stats)
}

func TestPricingMatrixHandler_ListModelCatalog(t *testing.T) {
	catalog := &stubCatalogLister{entries: []service.ModelCatalogEntry{{ID: 1, ModelKey: "gpt-5.5", Platform: "openai", Status: service.ModelCatalogActive, Aliases: []string{}}}}
	rec, env := doPricingMatrixRequest(t, &stubDeriveViewer{}, catalog, "/model-catalog?platform=%20openai%20&status=active")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.ModelCatalogFilter{Platform: "openai", Status: service.ModelCatalogActive}, catalog.lastFilter)

	var data struct {
		Items []service.ModelCatalogEntry `json:"items"`
	}
	require.NoError(t, json.Unmarshal(env.Data, &data))
	require.Len(t, data.Items, 1)
	require.Equal(t, "gpt-5.5", data.Items[0].ModelKey)

	_, _ = doPricingMatrixRequest(t, &stubDeriveViewer{}, catalog, "/model-catalog")
	require.Equal(t, service.ModelCatalogFilter{}, catalog.lastFilter, "不带参数时不过滤")

	catalog.err = service.ErrModelCatalogInvalid
	rec, env = doPricingMatrixRequest(t, &stubDeriveViewer{}, catalog, "/model-catalog?status=weird")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "MODEL_CATALOG_INVALID", env.Reason)
}
