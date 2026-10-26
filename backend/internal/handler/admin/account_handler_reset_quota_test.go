//go:build unit

package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type resetQuotaAdminService struct {
	*stubAdminService
	resetErr   error
	resetCalls int
}

func (s *resetQuotaAdminService) ResetAccountQuota(_ context.Context, _ int64) error {
	s.resetCalls++
	return s.resetErr
}

func setupResetQuotaRouter(svc service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.POST("/api/v1/admin/accounts/:id/reset-quota", handler.ResetQuota)
	return router
}

func TestAccountHandlerResetQuota_MissingAccountReturns404(t *testing.T) {
	svc := &resetQuotaAdminService{stubAdminService: newStubAdminService(), resetErr: service.ErrAccountNotFound}
	router := setupResetQuotaRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/404/reset-quota", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "ACCOUNT_NOT_FOUND")
	require.Equal(t, 1, svc.resetCalls)
}

func TestAccountHandlerResetQuota_RepositoryFailureReturns500(t *testing.T) {
	svc := &resetQuotaAdminService{stubAdminService: newStubAdminService(), resetErr: errors.New("db down")}
	router := setupResetQuotaRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/42/reset-quota", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, 1, svc.resetCalls)
}

func TestAccountHandlerResetQuota_InvalidIDSkipsService(t *testing.T) {
	svc := &resetQuotaAdminService{stubAdminService: newStubAdminService()}
	router := setupResetQuotaRouter(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/abc/reset-quota", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Zero(t, svc.resetCalls)
}
