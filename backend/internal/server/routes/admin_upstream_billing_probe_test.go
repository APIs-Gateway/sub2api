package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterAccountRoutesIncludesUpstreamBillingProbeRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	accountHandler := admin.NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Account: accountHandler}}

	registerAccountRoutes(router.Group("/admin"), handlers)

	routes := make(map[string]struct{}, len(router.Routes()))
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
	}
	for _, route := range []string{
		"GET /admin/accounts/upstream-billing-probe/settings",
		"PUT /admin/accounts/upstream-billing-probe/settings",
		"POST /admin/accounts/upstream-billing-probe/batch",
		"PUT /admin/accounts/:id/upstream-billing-probe",
		"POST /admin/accounts/:id/upstream-billing-probe",
	} {
		_, ok := routes[route]
		require.True(t, ok, route)
	}
}
