//go:build unit

package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// W6 PR4b-2b-2：价格写入的提交类接口（含成本核算规则、已知免费名单、目录状态）只有登录管理员的 JWT 会话能调；
// 机器令牌与旧的全局管理员密钥一律 403，且请求到不了处理器。预览与只读接口不受这个限制。
func TestPricingWriteCommitRoutesRequireJWTSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	build := func(authMethod string) *gin.Engine {
		router := gin.New()
		router.Use(gin.Recovery()) // 处理器是 nil 指针：能走到处理器的请求会 panic，被恢复成 500
		admin := router.Group(adminRoutePrefix)
		admin.Use(func(c *gin.Context) {
			c.Set("auth_method", authMethod)
			c.Next()
		})
		registerPricingRoutes(admin, &handler.Handlers{Admin: &handler.AdminHandlers{}})
		return router
	}
	type route struct{ method, path string }
	do := func(router *gin.Engine, r route) int {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(r.method, adminRoutePrefix+r.path, nil))
		return rec.Code
	}
	guarded := []route{
		{http.MethodPost, "/pricing-matrix/cells/commit"},
		{http.MethodPut, "/pricing-matrix/groups/3/config"},
		{http.MethodPost, "/pricing-matrix/groups/3/cost-rules"},
		{http.MethodPut, "/pricing-matrix/groups/3/cost-rules/4"},
		{http.MethodDelete, "/pricing-matrix/groups/3/cost-rules/4"},
		{http.MethodPut, "/pricing-matrix/known-free-list"},
		{http.MethodPut, "/model-catalog/3/status"},
		{http.MethodPost, "/model-catalog"},
	}
	notGuarded := []route{
		{http.MethodPost, "/pricing-matrix/cells/preview"},
		{http.MethodPost, "/pricing-matrix/groups/3/config/preview"},
		{http.MethodGet, "/pricing-matrix/groups/3/publish-check"},
		{http.MethodGet, "/pricing-matrix/known-free-list"},
		{http.MethodPost, "/pricing-matrix/known-free-list/preview"},
		{http.MethodGet, "/model-catalog/3/transition-preview"},
	}

	for _, method := range []string{service.AuditAuthMethodAdminToken, service.AuditAuthMethodAdminAPIKey, ""} {
		router := build(method)
		for _, r := range guarded {
			require.Equalf(t, http.StatusForbidden, do(router, r), "%s %s via %q", r.method, r.path, method)
		}
		for _, r := range notGuarded {
			require.NotEqualf(t, http.StatusForbidden, do(router, r), "%s %s via %q 不受 JWT 限制", r.method, r.path, method)
		}
	}

	jwt := build(service.AuditAuthMethodJWT)
	for _, r := range guarded {
		require.NotEqualf(t, http.StatusForbidden, do(jwt, r), "%s %s 的 JWT 会话应当越过限制", r.method, r.path)
	}
}
