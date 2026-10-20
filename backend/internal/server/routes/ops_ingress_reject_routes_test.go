package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterOpsRoutesIncludesIngressRejectRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	opsHandler := admin.NewOpsHandler(nil)
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Ops: opsHandler}}

	registerOpsRoutes(router.Group("/admin"), handlers)

	routes := make(map[string]struct{}, len(router.Routes()))
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
	}
	for _, route := range []string{
		"GET /admin/ops/ingress-rejections",
		"GET /admin/ops/ingress-rejections/health",
	} {
		_, ok := routes[route]
		require.True(t, ok, route)
	}
}
