package middleware

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// SessionBindingContext attaches the security client IP and User-Agent to the
// request context before auth/token issuance.
func SessionBindingContext(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		binding := &service.SessionBinding{
			IP:        ip.GetSecurityClientIP(c, cfg != nil && cfg.TrustForwardedIPForAPIKeyACL()),
			UserAgent: c.Request.UserAgent(),
		}
		c.Request = c.Request.WithContext(service.WithSessionBinding(c.Request.Context(), binding))
		c.Next()
	}
}

// SecurityClientIP returns the IP used by security-sensitive records.
func SecurityClientIP(c *gin.Context) string {
	if c != nil && c.Request != nil {
		if binding := service.SessionBindingFromContext(c.Request.Context()); binding != nil &&
			strings.TrimSpace(binding.IP) != "" {
			return binding.IP
		}
	}
	return ip.GetTrustedClientIP(c)
}
