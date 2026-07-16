package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// SessionBindingContext attaches trusted client IP and User-Agent to the request context.
// It must run before auth/token issuance and relies on Gin's trusted proxy configuration.
func SessionBindingContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		binding := &service.SessionBinding{
			IP:        ip.GetTrustedClientIP(c),
			UserAgent: c.Request.UserAgent(),
		}
		c.Request = c.Request.WithContext(service.WithSessionBinding(c.Request.Context(), binding))
		c.Next()
	}
}
