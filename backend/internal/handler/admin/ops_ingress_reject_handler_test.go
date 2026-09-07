package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newIngressRejectHandlerForTest() *OpsHandler {
	return NewOpsHandler(service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
}

func TestListIngressRejectsValidatesFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{
		"/api/v1/admin/ops/ingress-rejections?reason=not-valid",
		"/api/v1/admin/ops/ingress-rejections?route_family=not-valid",
		"/api/v1/admin/ops/ingress-rejections?protocol=not-valid",
		"/api/v1/admin/ops/ingress-rejections?user_id=0",
		"/api/v1/admin/ops/ingress-rejections?api_key_id=-1",
		"/api/v1/admin/ops/ingress-rejections?user_id=not-a-number",
	} {
		t.Run(path, func(t *testing.T) {
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = httptest.NewRequest(http.MethodGet, path, nil)
			newIngressRejectHandlerForTest().ListIngressRejects(context)
			require.Equal(t, http.StatusBadRequest, context.Writer.Status())
		})
	}
}

func TestListIngressRejectsReturnsAggregatesForValidFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/ops/ingress-rejections?reason=invalid_api_key&route_family=messages&protocol=anthropic&client_ip=203.0.113.0%2F24&user_id=1&api_key_id=2",
		nil)
	newIngressRejectHandlerForTest().ListIngressRejects(context)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"items"`)
}

func TestListIngressRejectsServiceUnavailableWithoutOpsService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/ingress-rejections", nil)
	NewOpsHandler(nil).ListIngressRejects(context)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestGetIngressRejectHealthAvailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/ingress-rejections/health", nil)
	newIngressRejectHandlerForTest().GetIngressRejectHealth(context)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"capacity":8192`)
}

func TestGetIngressRejectHealthServiceUnavailableWithoutOpsService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/ingress-rejections/health", nil)
	NewOpsHandler(nil).GetIngressRejectHealth(context)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}
