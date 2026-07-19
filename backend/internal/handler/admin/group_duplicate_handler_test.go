package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type duplicateAdminServiceStub struct {
	*stubAdminService
	group *service.Group
	calls int
}

func (s *duplicateAdminServiceStub) DuplicateGroup(_ context.Context, id int64, actorScope, operationKey string) (*service.Group, error) {
	s.calls++
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
