package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type adminStepUpUserRepoStub struct {
	service.UserRepository
	user *service.User
}

func (s *adminStepUpUserRepoStub) GetByID(context.Context, int64) (*service.User, error) {
	return s.user, nil
}

func (s *adminStepUpUserRepoStub) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

type adminStepUpCacheStub struct {
	service.TotpCache
	granted bool
}

func (s *adminStepUpCacheStub) HasStepUpGrant(context.Context, int64, string) (bool, error) {
	return s.granted, nil
}

func newAdminStepUpTestRouter(adminSvc *stubAdminService, cache *adminStepUpCacheStub) *gin.Engine {
	actorRepo := &adminStepUpUserRepoStub{user: &service.User{
		ID:          1,
		Role:        service.RoleAdmin,
		TotpEnabled: true,
	}}
	userSvc := service.NewUserService(actorRepo, nil, nil, nil)
	totpSvc := service.NewTotpService(nil, nil, cache, nil, nil, nil)
	handler := NewUserHandler(adminSvc, nil, nil, nil, totpSvc, userSvc)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Next()
	})
	router.POST("/users", handler.Create)
	router.PUT("/users/:id", handler.Update)
	return router
}

func TestUserHandlerRoleChangesCoverStepUpAndExistingAdminBranches(t *testing.T) {
	adminSvc := newStubAdminService()
	adminSvc.users = append(adminSvc.users, service.User{ID: 3, Role: service.RoleAdmin, Status: service.StatusActive})
	cache := &adminStepUpCacheStub{}
	router := newAdminStepUpTestRouter(adminSvc, cache)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"email":"admin@example.com","password":"password","role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_REQUIRED")

	cache.granted = true
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"email":"admin@example.com","password":"password","role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.RoleAdmin, adminSvc.createdUserInput.Role)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/users/2", strings.NewReader(`{"role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.RoleAdmin, adminSvc.updatedUserInput.Role)

	cache.granted = false
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/users/3", strings.NewReader(`{"role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestUserHandlerPromotionReturnsLookupError(t *testing.T) {
	adminSvc := newStubAdminService()
	adminSvc.getUserErr = errors.New("lookup failed")
	router := newAdminStepUpTestRouter(adminSvc, &adminStepUpCacheStub{granted: true})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/users/2", strings.NewReader(`{"role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
}
