package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUserHandlerCreateMapsRoleAndRejectsInvalidRole(t *testing.T) {
	router, adminSvc := setupAdminRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewBufferString(`{"email":"user@example.com","password":"password","role":"user"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, adminSvc.createdUserInput)
	require.Equal(t, service.RoleUser, adminSvc.createdUserInput.Role)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewBufferString(`{"email":"owner@example.com","password":"password","role":"owner"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, service.RoleUser, adminSvc.createdUserInput.Role, "invalid role must not reach the service")
}

func TestUserHandlerCreateAdminRequiresStepUp(t *testing.T) {
	router, adminSvc := setupAdminRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewBufferString(`{"email":"admin@example.com","password":"password","role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Nil(t, adminSvc.createdUserInput)
}

func TestUserHandlerUpdateMapsRoleAndPreventsSelfDowngrade(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	handler := NewUserHandler(adminSvc, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Next()
	})
	router.PUT("/api/v1/admin/users/:id", handler.Update)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/2", bytes.NewBufferString(`{"role":"user"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(2), adminSvc.updatedUserID)
	require.NotNil(t, adminSvc.updatedUserInput)
	require.Equal(t, service.RoleUser, adminSvc.updatedUserInput.Role)
	require.Equal(t, int64(1), adminSvc.updatedUserInput.ActorAdminID, "操作者 ID 应从 JWT 传入 service 以便审计")

	adminSvc.updatedUserInput = nil
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/1", bytes.NewBufferString(`{"role":"user"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "CANNOT_DEMOTE_SELF", "自我降级应返回稳定 reason 供前端本地化")
	require.Nil(t, adminSvc.updatedUserInput)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/2", bytes.NewBufferString(`{"role":"owner"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Nil(t, adminSvc.updatedUserInput)
}

func TestUserHandlerPromoteUserRequiresStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	handler := NewUserHandler(adminSvc, nil, nil, nil, nil, nil)
	router := gin.New()
	router.PUT("/api/v1/admin/users/:id", handler.Update)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/2", bytes.NewBufferString(`{"role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Nil(t, adminSvc.updatedUserInput)
}

func TestUserHandlerCreatePassesActorAdminID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	handler := NewUserHandler(adminSvc, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 3})
		c.Next()
	})
	router.POST("/api/v1/admin/users", handler.Create)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewBufferString(`{"email":"user@example.com","password":"password","role":"user"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, adminSvc.createdUserInput)
	require.Equal(t, int64(3), adminSvc.createdUserInput.ActorAdminID, "操作者 ID 应从 JWT 传入 service 以便审计")
}
