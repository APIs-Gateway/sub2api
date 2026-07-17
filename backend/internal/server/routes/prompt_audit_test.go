package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterPromptAuditRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{
		PromptAudit: securityaudit.NewPromptEventAdminHandler(nil),
	}}

	RegisterAdminRoutes(router.Group("/api/v1"), handlers, middleware.AdminAuthMiddleware(func(c *gin.Context) {
		c.Next()
	}), nil)

	routes := router.Routes()
	paths := make(map[string]string)
	for _, route := range routes {
		if route.Path == "/api/v1/admin/prompt-audit/events" || route.Path == "/api/v1/admin/prompt-audit/events/:id" {
			paths[route.Path] = route.Method
		}
	}
	require.Equal(t, map[string]string{
		"/api/v1/admin/prompt-audit/events":     "GET",
		"/api/v1/admin/prompt-audit/events/:id": "GET",
	}, paths)
}
