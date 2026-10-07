//go:build unit

package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func unpricedPolicyCtx(authMethod string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", nil)
	if authMethod != "" {
		c.Set("auth_method", authMethod)
	}
	return c, rec
}

func unpricedStrPtr(s string) *string { return &s }

func TestResolveBillingUnpricedPolicyWrite(t *testing.T) {
	// 未提供字段：不改，也不要求交互式会话（其它设置照常能用 admin API key 改）。
	for _, method := range []string{"", service.AuditAuthMethodAdminAPIKey, service.AuditAuthMethodAdminToken, service.AuditAuthMethodJWT} {
		c, _ := unpricedPolicyCtx(method)
		v, ok := resolveBillingUnpricedPolicyWrite(c, nil)
		require.True(t, ok, method)
		require.Empty(t, v, method)
	}

	// 交互式管理员会话：合法值通过（规范化去空白）。
	for in, want := range map[string]string{"observe": "observe", "block_allowlist": "block_allowlist", " block_allowlist ": "block_allowlist"} {
		c, _ := unpricedPolicyCtx(service.AuditAuthMethodJWT)
		v, ok := resolveBillingUnpricedPolicyWrite(c, unpricedStrPtr(in))
		require.True(t, ok, in)
		require.Equal(t, want, v, in)
	}

	// 非法值：400（先于权限判断，对谁都一样）。
	for _, method := range []string{service.AuditAuthMethodJWT, service.AuditAuthMethodAdminAPIKey} {
		for _, bad := range []string{"", "block", "Observe", "off"} {
			c, rec := unpricedPolicyCtx(method)
			_, ok := resolveBillingUnpricedPolicyWrite(c, unpricedStrPtr(bad))
			require.False(t, ok)
			require.Equal(t, http.StatusBadRequest, rec.Code, "%s %q", method, bad)
		}
	}

	// 非交互式凭证（全局管理员密钥、机器令牌、没有鉴权信息）：合法值也是 403，不写。
	for _, method := range []string{service.AuditAuthMethodAdminAPIKey, service.AuditAuthMethodAdminToken, ""} {
		for _, value := range []string{"observe", "block_allowlist"} {
			c, rec := unpricedPolicyCtx(method)
			v, ok := resolveBillingUnpricedPolicyWrite(c, unpricedStrPtr(value))
			require.False(t, ok, "%q %s", method, value)
			require.Empty(t, v)
			require.Equal(t, http.StatusForbidden, rec.Code, "%q %s", method, value)
			require.Contains(t, rec.Body.String(), "BILLING_UNPRICED_POLICY_INTERACTIVE")
		}
	}
}
