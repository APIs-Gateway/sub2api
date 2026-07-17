package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterPromptAuditRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{
		PromptAudit: securityaudit.NewPromptEventAdminHandler(nil),
	}}

	registerPromptAuditRoutes(router.Group("/api/v1/admin"), handlers)

	routes := router.Routes()
	require.Len(t, routes, 2)
	require.Equal(t, "GET", routes[0].Method)
	require.Equal(t, "/api/v1/admin/prompt-audit/events", routes[0].Path)
	require.Equal(t, "GET", routes[1].Method)
	require.Equal(t, "/api/v1/admin/prompt-audit/events/:id", routes[1].Path)
}
