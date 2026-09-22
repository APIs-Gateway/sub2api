//go:build unit

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProxyHandlerUpdateCredentialPresence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	check := func(name, body string, username, password *string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			svc := newStubAdminService()
			router := gin.New()
			router.PUT("/proxies/:id", NewProxyHandler(svc).Update)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/proxies/9", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Len(t, svc.updatedProxies, 1)
			require.Equal(t, username, svc.updatedProxies[0].Username)
			require.Equal(t, password, svc.updatedProxies[0].Password)
		})
	}
	empty, username, password := "", "user", "pass"
	check("omitted", `{"status":"inactive"}`, nil, nil)
	check("null preserves", `{"username":null,"password":null}`, nil, nil)
	check("clear both", `{"username":"","password":""}`, &empty, &empty)
	check("clear username only", `{"username":""}`, &empty, nil)
	check("clear password only", `{"password":""}`, nil, &empty)
	check("trim values", `{"username":" user ","password":" pass "}`, &username, &password)
	check("whitespace clears", `{"username":"  ","password":"  "}`, &empty, &empty)
}
