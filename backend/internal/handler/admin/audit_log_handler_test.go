//go:build unit

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type auditLogHandlerRepo struct {
	filter *service.AuditLogFilter
	calls  int
}

func (r *auditLogHandlerRepo) Insert(context.Context, *service.AuditLog) error { return nil }
func (r *auditLogHandlerRepo) BatchInsert(context.Context, []*service.AuditLog) (int64, error) {
	return 0, nil
}
func (r *auditLogHandlerRepo) List(_ context.Context, filter *service.AuditLogFilter) (*service.AuditLogList, error) {
	r.calls++
	r.filter = filter
	return &service.AuditLogList{
		Logs:  []*service.AuditLog{{ID: 1, Route: "/api/v1/admin/users/:id"}},
		Total: 1, Page: filter.Page, PageSize: filter.PageSize,
	}, nil
}
func (r *auditLogHandlerRepo) DeleteBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func newAuditLogHandlerRouter(repo *auditLogHandlerRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/admin/audit-logs", NewAuditLogHandler(repo).List)
	return router
}

func getAuditLogs(router *gin.Engine, query string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/audit-logs"+query, nil))
	return w
}

func TestAuditLogHandlerPassesAllFilters(t *testing.T) {
	repo := &auditLogHandlerRepo{}
	router := newAuditLogHandlerRouter(repo)

	w := getAuditLogs(router, "?page=2&page_size=50&actor_user_id=7&auth_kind=admin_token&token_id=3"+
		"&route=/api/v1/admin/users&target_type=users&target_id=42&method=POST"+
		"&start_time=2026-10-01T00:00:00Z&end_time=2026-10-02&status_min=400&status_max=499")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, repo.calls)

	f := repo.filter
	require.Equal(t, 2, f.Page)
	require.Equal(t, 50, f.PageSize)
	require.NotNil(t, f.ActorUserID)
	require.Equal(t, int64(7), *f.ActorUserID)
	require.Equal(t, service.AuditAuthKindAdminToken, f.AuthKind)
	require.NotNil(t, f.TokenID)
	require.Equal(t, int64(3), *f.TokenID)
	require.Equal(t, "/api/v1/admin/users", f.RoutePrefix)
	require.Equal(t, "users", f.TargetType)
	require.Equal(t, "42", f.TargetID)
	require.Equal(t, "POST", f.Method)
	require.NotNil(t, f.StartTime)
	require.True(t, f.StartTime.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)))
	require.NotNil(t, f.EndTime)
	require.True(t, f.EndTime.After(time.Date(2026, 10, 2, 23, 59, 0, 0, time.UTC)), "a bare end date includes that whole day")
	require.True(t, f.EndTime.Before(time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)))
	require.NotNil(t, f.StatusMin)
	require.Equal(t, 400, *f.StatusMin)
	require.NotNil(t, f.StatusMax)
	require.Equal(t, 499, *f.StatusMax)
}

func TestAuditLogHandlerWithoutFilters(t *testing.T) {
	repo := &auditLogHandlerRepo{}
	w := getAuditLogs(newAuditLogHandlerRouter(repo), "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	f := repo.filter
	require.Nil(t, f.ActorUserID)
	require.Nil(t, f.TokenID)
	require.Nil(t, f.StartTime)
	require.Nil(t, f.EndTime)
	require.Nil(t, f.StatusMin)
	require.Nil(t, f.StatusMax)
	require.Empty(t, f.AuthKind)
	require.Empty(t, f.RoutePrefix)
	require.Equal(t, 1, f.Page)
}

func TestAuditLogHandlerRejectsBadFilters(t *testing.T) {
	for _, query := range []string{
		"?actor_user_id=abc",
		"?actor_user_id=0",
		"?token_id=-1",
		"?auth_kind=root",
		"?status_min=99",
		"?status_max=600",
		"?status_min=500&status_max=400",
		"?start_time=yesterday",
		"?end_time=2026-13-45",
		"?start_time=2026-10-02&end_time=2026-10-01",
	} {
		t.Run(query, func(t *testing.T) {
			repo := &auditLogHandlerRepo{}
			w := getAuditLogs(newAuditLogHandlerRouter(repo), query)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			require.Zero(t, repo.calls, "an invalid filter must not reach the repository")
		})
	}
}
