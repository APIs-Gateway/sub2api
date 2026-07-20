package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type duplicateAdminServiceStub struct {
	*stubAdminService
	group        *service.Group
	calls        int
	err          error
	actorScope   string
	operationKey string
}

func (s *duplicateAdminServiceStub) DuplicateGroup(_ context.Context, id int64, actorScope, operationKey string) (*service.Group, error) {
	s.calls++
	s.actorScope = actorScope
	s.operationKey = operationKey
	if s.err != nil {
		return nil, s.err
	}
	s.group = &service.Group{
		ID:                   id + 100,
		Name:                 "source (Copy)",
		Platform:             service.PlatformOpenAI,
		Status:               "inactive",
		DuplicateOperationID: actorScope + ":" + operationKey,
	}
	return s.group, nil
}

func (s *duplicateAdminServiceStub) RecoverDuplicateGroup(_ context.Context, _ int64, _, _ string) (*service.Group, error) {
	return s.group, nil
}

func TestGroupHandlerDuplicateEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &duplicateAdminServiceStub{stubAdminService: newStubAdminService()}
	handler := NewGroupHandler(svc, nil, nil)
	router := gin.New()
	router.POST("/api/v1/admin/groups/:id/duplicate", handler.Duplicate)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/7/duplicate", nil)
	req.Header.Set("Idempotency-Key", "copy-1")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, svc.calls)
	require.Contains(t, rec.Body.String(), "source (Copy)")
	require.Contains(t, rec.Body.String(), "openai")
}

func TestGroupHandlerDuplicateRejectsInvalidID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &duplicateAdminServiceStub{stubAdminService: newStubAdminService()}
	handler := NewGroupHandler(svc, nil, nil)
	router := gin.New()
	router.POST("/api/v1/admin/groups/:id/duplicate", handler.Duplicate)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/not-a-number/duplicate", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Zero(t, svc.calls)
}

func TestGroupHandlerDuplicateRejectsUnavailableService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewGroupHandler(newStubAdminService(), nil, nil)
	router := gin.New()
	router.POST("/api/v1/admin/groups/:id/duplicate", handler.Duplicate)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/7/duplicate", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "Group duplication is unavailable")
}

func TestGroupHandlerDuplicateReplaysForAuthenticatedAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousCoordinator := service.DefaultIdempotencyCoordinator()
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(
		newMemoryIdempotencyRepoStub(),
		service.DefaultIdempotencyConfig(),
	))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previousCoordinator) })

	svc := &duplicateAdminServiceStub{stubAdminService: newStubAdminService()}
	handler := NewGroupHandler(svc, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
		c.Next()
	})
	router.POST("/api/v1/admin/groups/:id/duplicate", handler.Duplicate)

	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/7/duplicate", nil)
		req.Header.Set("Idempotency-Key", "copy-1")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	first := call()
	second := call()
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, 1, svc.calls)
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, "admin:42", svc.actorScope)
	require.Equal(t, "copy-1", svc.operationKey)
}

func TestGroupHandlerDuplicateReturnsServiceError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &duplicateAdminServiceStub{
		stubAdminService: newStubAdminService(),
		err:              errors.New("copy failed"),
	}
	handler := NewGroupHandler(svc, nil, nil)
	router := gin.New()
	router.POST("/api/v1/admin/groups/:id/duplicate", handler.Duplicate)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/7/duplicate", nil)
	req.Header.Set("Idempotency-Key", "copy-error")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestGroupHandlerDuplicateRecoversWhenIdempotencyStoreUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousCoordinator := service.DefaultIdempotencyCoordinator()
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(
		storeUnavailableRepoStub{},
		service.DefaultIdempotencyConfig(),
	))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previousCoordinator) })

	svc := &duplicateAdminServiceStub{
		stubAdminService: newStubAdminService(),
		group: &service.Group{
			ID:       107,
			Name:     "source (Copy)",
			Platform: service.PlatformOpenAI,
			Status:   "inactive",
		},
	}
	handler := NewGroupHandler(svc, nil, nil)
	router := gin.New()
	router.POST("/api/v1/admin/groups/:id/duplicate", handler.Duplicate)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/7/duplicate", nil)
	req.Header.Set("Idempotency-Key", "copy-recovery")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", rec.Header().Get("X-Idempotency-Recovered"))
	require.Zero(t, svc.calls)
	require.Contains(t, rec.Body.String(), "source (Copy)")
}
