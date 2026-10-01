//go:build unit

package admin

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProxyPasswordHiddenOnlyForAdminTokensBelowDanger(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name   string
		kind   string // "" means no identity recorded
		scope  string
		hidden bool
	}{
		{"read token", service.AuditAuthKindAdminToken, service.AdminTokenScopeRead, true},
		{"write token", service.AuditAuthKindAdminToken, service.AdminTokenScopeWrite, true},
		{"danger token", service.AuditAuthKindAdminToken, service.AdminTokenScopeDanger, false},
		{"jwt administrator", service.AuditAuthKindJWT, service.AdminTokenScopeDanger, false},
		{"legacy api key", service.AuditAuthKindLegacyAPIKey, service.AdminTokenScopeDanger, false},
		{"no identity (unit tests, internal callers)", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if tc.kind != "" {
				c.Set(string(middleware.ContextKeyAdminAuthKind), tc.kind)
				c.Set(string(middleware.ContextKeyAdminTokenScope), tc.scope)
			}
			require.Equal(t, tc.hidden, proxyPasswordHidden(c))
		})
	}
}
