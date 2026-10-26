package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterOpsRoutesIncludesUnpricedUsageAsReadOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Ops: admin.NewOpsHandler(nil)}}

	registerOpsRoutes(router.Group("/admin"), handlers)

	var methods []string
	for _, route := range router.Routes() {
		if route.Path == "/admin/ops/unpriced-usage" {
			methods = append(methods, route.Method)
		}
	}
	// 未定价用量只读：只有 GET，没有任何写方法。
	require.Equal(t, []string{"GET"}, methods)
}
