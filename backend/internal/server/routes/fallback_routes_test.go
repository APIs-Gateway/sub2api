package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func routeSet(router *gin.Engine) map[string]struct{} {
	out := make(map[string]struct{})
	for _, r := range router.Routes() {
		out[r.Method+" "+r.Path] = struct{}{}
	}
	return out
}

// 用户端回退链路由：注册成功本身就证明 /keys/fallback-chains 与 /keys/:id 没有路由冲突（gin 冲突会 panic）。
func TestRegisterUserRoutesIncludesKeyFallbackChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{APIKeyFallback: handler.NewAPIKeyFallbackHandler(nil)}
	noopAuth := middleware.JWTAuthMiddleware(func(c *gin.Context) { c.Next() })

	require.NotPanics(t, func() {
		RegisterUserRoutes(router.Group("/api/v1"), handlers, noopAuth, nil, nil)
	})

	routes := routeSet(router)
	require.Contains(t, routes, "GET /api/v1/keys/fallback-chains")
	require.Contains(t, routes, "GET /api/v1/keys/:id/fallback-chain")
	require.Contains(t, routes, "PUT /api/v1/keys/:id/fallback-chain")
	// 既有 Key 路由不受影响。
	require.Contains(t, routes, "GET /api/v1/keys/:id")
	require.Contains(t, routes, "PUT /api/v1/keys/:id")

	// 用户端不存在任何隐藏链接口。
	for route := range routes {
		require.NotContains(t, route, "hidden")
	}
}

// 管理端回退链路由。这些路由的 scope 已在 middleware/admin_token_scope.go 登记：
// PUT/DELETE hidden-fallback-chain 与 PUT reference-models 为 danger，
// GET fallback-chain 在 adminReviewedReadRules（路径含 api-keys）；
// 覆盖由 admin_token_scope_coverage_test.go 保证。
//
// 与既有的 /admin/groups/:id 等路由放在同一棵路由树里注册：注册成功本身就证明没有路由冲突。
func TestRegisterAdminRoutesIncludesKeyFallbackChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{APIKeyFallback: admin.NewAPIKeyFallbackHandler(nil)}}

	require.NotPanics(t, func() {
		adminGroup := router.Group("/api/v1/admin")
		registerGroupRoutes(adminGroup, handlers)
		registerAdminAPIKeyRoutes(adminGroup, handlers)
	})

	routes := routeSet(router)
	for _, want := range []string{
		"GET /api/v1/admin/api-keys/:id/fallback-chain",
		"PUT /api/v1/admin/api-keys/:id/hidden-fallback-chain",
		"DELETE /api/v1/admin/api-keys/:id/hidden-fallback-chain",
		"GET /api/v1/admin/groups/:id/fallback-routes",
		"GET /api/v1/admin/key-editor/reference-models",
		"PUT /api/v1/admin/key-editor/reference-models",
		// 既有路由不受影响。
		"PUT /api/v1/admin/api-keys/:id",
		"GET /api/v1/admin/groups/:id",
	} {
		require.Contains(t, routes, want)
	}
}
