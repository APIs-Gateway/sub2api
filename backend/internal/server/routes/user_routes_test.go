package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func TestRegisterUserRoutesIncludesTotpStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Totp: handler.NewTotpHandler(nil)}
	noopAuth := middleware.JWTAuthMiddleware(func(c *gin.Context) { c.Next() })

	RegisterUserRoutes(router.Group("/api/v1"), handlers, noopAuth, nil)

	for _, route := range router.Routes() {
		if route.Method == "POST" && route.Path == "/api/v1/user/totp/step-up" {
			return
		}
	}
	t.Fatal("TOTP step-up route was not registered")
}
