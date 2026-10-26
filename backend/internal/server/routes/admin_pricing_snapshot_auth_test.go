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

// 价格快照的固定与批准是 C 档动作：只有登录管理员的 JWT 会话能做，
// 机器令牌与旧的全局 admin API key 一律 403，且请求到不了处理器。
func TestPricingSnapshotPinAndApproveRequireJWTSession(t *testing.T) {
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
	do := func(router *gin.Engine, path string) int {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, adminRoutePrefix+path, nil))
		return rec.Code
	}
	guarded := []string{"/pricing/snapshots/pin", "/pricing/snapshots/7/approve"}
	notGuarded := []string{"/pricing/snapshots/fetch", "/pricing/snapshots/7/preview", "/pricing/snapshots/7/reject"}

	for _, method := range []string{service.AuditAuthMethodAdminToken, service.AuditAuthMethodAdminAPIKey} {
		router := build(method)
		for _, path := range guarded {
			require.Equalf(t, http.StatusForbidden, do(router, path), "%s via %s", path, method)
		}
		for _, path := range notGuarded {
			require.NotEqualf(t, http.StatusForbidden, do(router, path), "%s via %s 不受 JWT 限制", path, method)
		}
	}

	jwt := build(service.AuditAuthMethodJWT)
	for _, path := range guarded {
		// 越过限制后请求到达（nil 的）处理器：400 或被恢复的 500，总之不再是 403。
		require.NotEqualf(t, http.StatusForbidden, do(jwt, path), "%s 的 JWT 会话应当越过限制", path)
	}
}
