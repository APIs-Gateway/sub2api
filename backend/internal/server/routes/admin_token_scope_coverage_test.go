//go:build unit

package routes

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const adminRoutePrefix = "/api/v1/admin"

var (
	routeParamPattern    = regexp.MustCompile(`:[A-Za-z0-9_]+`)
	routeWildcardPattern = regexp.MustCompile(`\*[A-Za-z0-9_]+`)
)

// buildAdminRouter builds the real admin route table: every route group that
// is mounted behind the admin auth middleware. authStub is installed as that
// middleware (nil: abort with 204). Handlers are nil pointers; method values
// on a nil receiver are fine for route registration, and a stub that aborts
// keeps requests from ever reaching a handler.
//
// The returned set collects "METHOD /route/template" for every request that
// actually ran the admin auth middleware.
func buildAdminRouter(t *testing.T, authStub gin.HandlerFunc) (*gin.Engine, map[string]struct{}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")

	protected := make(map[string]struct{})
	adminAuth := middleware.AdminAuthMiddleware(func(c *gin.Context) {
		protected[c.Request.Method+" "+c.FullPath()] = struct{}{}
		if authStub != nil {
			authStub(c)
			return
		}
		c.AbortWithStatus(http.StatusNoContent)
	})
	jwtAuth := middleware.JWTAuthMiddleware(func(c *gin.Context) {
		c.AbortWithStatus(http.StatusTeapot)
	})

	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{}}
	RegisterAdminRoutes(v1, handlers, adminAuth, nil, nil)
	RegisterPaymentRoutes(v1, nil, nil, nil, jwtAuth, adminAuth, nil, nil)
	handler.RegisterPageRoutes(v1, t.TempDir(), gin.HandlerFunc(jwtAuth), gin.HandlerFunc(adminAuth), nil)
	return router, protected
}

// adminProtectedRoutes builds the router with an aborting auth stub and drives
// one request through every route that is supposed to be an admin route, so
// that "behind the admin auth middleware" is verified rather than assumed.
func adminProtectedRoutes(t *testing.T) (*gin.Engine, map[string]struct{}) {
	t.Helper()
	router, protected := buildAdminRouter(t, nil)
	for _, route := range router.Routes() {
		isAdminRoute := strings.HasPrefix(route.Path, adminRoutePrefix+"/") ||
			(route.Method == http.MethodGet && route.Path == "/api/v1/pages")
		if !isAdminRoute {
			continue
		}
		path := routeParamPattern.ReplaceAllString(route.Path, "1")
		path = routeWildcardPattern.ReplaceAllString(path, "x")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(route.Method, path, nil))
	}
	return router, protected
}

func routeKeys(rules []middleware.AdminRouteRule) []string {
	keys := make([]string, 0, len(rules))
	for _, rule := range rules {
		keys = append(keys, rule.Key())
	}
	return keys
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestEveryAdminRouteRunsAdminAuth(t *testing.T) {
	router, protected := adminProtectedRoutes(t)

	var missing []string
	for _, route := range router.Routes() {
		if !strings.HasPrefix(route.Path, adminRoutePrefix+"/") {
			continue
		}
		if _, ok := protected[route.Method+" "+route.Path]; !ok {
			missing = append(missing, route.Method+" "+route.Path)
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing, "admin routes that do not run the admin auth middleware cannot be scope-checked")
	require.Contains(t, protected, "GET /api/v1/pages", "the admin page list is protected by the admin auth middleware")
	require.Contains(t, protected, "POST /api/v1/admin/payment/orders/:id/refund", "payment admin routes must be covered")
	require.Contains(t, protected, "GET /api/v1/admin/admin-tokens")
}

// (a) Every entry on the danger list (and on the reviewed lists) must be a
// real route, so a rename or removal cannot leave a stale entry that silently
// stops protecting anything.
func TestAdminScopeListsOnlyContainExistingRoutes(t *testing.T) {
	_, protected := adminProtectedRoutes(t)

	lists := map[string][]middleware.AdminRouteRule{
		"danger":         middleware.AdminDangerRules(),
		"reviewed write": middleware.AdminReviewedWriteRules(),
		"reviewed read":  middleware.AdminReviewedReadRules(),
	}
	for name, rules := range lists {
		var stale []string
		for _, key := range routeKeys(rules) {
			if _, ok := protected[key]; !ok {
				stale = append(stale, key)
			}
		}
		require.Emptyf(t, stale, "%s list has entries that are not real admin routes", name)
	}
}

func TestAdminScopeListsAreWellFormed(t *testing.T) {
	danger := middleware.AdminDangerRules()
	reviewedWrite := middleware.AdminReviewedWriteRules()
	reviewedRead := middleware.AdminReviewedReadRules()

	require.NotEmpty(t, danger)

	seen := map[string]string{}
	for name, rules := range map[string][]middleware.AdminRouteRule{
		"danger": danger, "reviewed write": reviewedWrite, "reviewed read": reviewedRead,
	} {
		for _, rule := range rules {
			require.NotEmptyf(t, strings.TrimSpace(rule.Reason), "%s %s needs a reason", rule.Method, rule.Path)
			require.Truef(t, strings.HasPrefix(rule.Path, "/"), "%s path %q must start with /", name, rule.Path)
			require.Equalf(t, strings.ToUpper(rule.Method), rule.Method, "%s method of %s must be upper case", name, rule.Path)
		}
		for _, key := range routeKeys(rules) {
			if other, dup := seen[key]; dup {
				t.Fatalf("%s is listed in both %q and %q (or twice in one list)", key, other, name)
			}
			seen[key] = name
		}
	}
	for _, rule := range reviewedWrite {
		require.NotEqualf(t, http.MethodGet, rule.Method, "reviewed-write list holds mutations only: %s", rule.Path)
	}
	for _, rule := range reviewedRead {
		require.Equalf(t, http.MethodGet, rule.Method, "reviewed-read list holds GET routes only: %s", rule.Path)
	}
}

// Keywords that make a mutating admin route "look sensitive". DELETE routes
// are always sensitive.
var sensitiveWriteKeywords = []string{
	"refund", "compensat", "balance", "delete", "price", "pricing", "rate", "settings",
	"batch", "bulk", "import", "credential", "group",
}

// Keywords that make a GET route look like it could reveal a secret.
var sensitiveReadKeywords = []string{"export", "download", "api-keys", "redeem-codes"}

// (b) A sensitive-looking route must be a conscious decision: it is either on
// the danger list or on the reviewed-as-write (or, for GET, reviewed-as-read)
// allowlist. Adding such a route without classifying it fails this test.
func TestSensitiveAdminRoutesAreClassified(t *testing.T) {
	_, protected := adminProtectedRoutes(t)

	danger := map[string]struct{}{}
	for _, key := range routeKeys(middleware.AdminDangerRules()) {
		danger[key] = struct{}{}
	}
	reviewedWrite := map[string]struct{}{}
	for _, key := range routeKeys(middleware.AdminReviewedWriteRules()) {
		reviewedWrite[key] = struct{}{}
	}
	reviewedRead := map[string]struct{}{}
	for _, key := range routeKeys(middleware.AdminReviewedReadRules()) {
		reviewedRead[key] = struct{}{}
	}

	var unclassified []string
	for _, key := range sortedKeys(protected) {
		method, path, _ := strings.Cut(key, " ")
		if !strings.HasPrefix(path, adminRoutePrefix+"/") {
			continue
		}
		lower := strings.ToLower(path)
		_, isDanger := danger[key]

		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			sensitive := strings.HasSuffix(lower, "/data")
			for _, keyword := range sensitiveReadKeywords {
				if strings.Contains(lower, keyword) {
					sensitive = true
				}
			}
			if _, reviewed := reviewedRead[key]; sensitive && !isDanger && !reviewed {
				unclassified = append(unclassified, key)
			}
		default:
			// "/data" routes import or export whole files (accounts, proxies).
			sensitive := method == http.MethodDelete || strings.HasSuffix(lower, "/data")
			for _, keyword := range sensitiveWriteKeywords {
				if strings.Contains(lower, keyword) {
					sensitive = true
				}
			}
			if _, reviewed := reviewedWrite[key]; sensitive && !isDanger && !reviewed {
				unclassified = append(unclassified, key)
			}
		}
	}
	require.Empty(t, unclassified,
		"sensitive-looking admin routes must be added to the danger list or to the reviewed-as-write/read lists in "+
			"internal/server/middleware/admin_token_scope.go")
}

// (c) Every route on the explicit "body not stored" list must exist, and the
// routes that submit credentials must be covered by it (explicitly or by the
// keyword rules), whatever shape their body has.
func TestAdminAuditBodyOmittedRoutesExistAndCoverCredentialRoutes(t *testing.T) {
	_, protected := adminProtectedRoutes(t)

	var stale []string
	for _, key := range routeKeys(middleware.AdminAuditBodyOmittedRules()) {
		if _, ok := protected[key]; !ok {
			stale = append(stale, key)
		}
	}
	require.Empty(t, stale, "body-omitted list has entries that are not real admin routes")

	mustOmit := []string{
		// the Codex auth.json routes the review called out
		"POST /api/v1/admin/accounts/import/codex-session",
		"POST /api/v1/admin/accounts/:id/reauth/codex-session",
		"POST /api/v1/admin/accounts/data",
		// account credential edits
		"POST /api/v1/admin/accounts",
		"PUT /api/v1/admin/accounts/:id",
		"POST /api/v1/admin/accounts/batch-update-credentials",
		"POST /api/v1/admin/accounts/:id/apply-oauth-credentials",
		// OAuth and cookie exchanges
		"POST /api/v1/admin/accounts/exchange-code",
		"POST /api/v1/admin/accounts/cookie-auth",
		"POST /api/v1/admin/openai/exchange-code",
		"POST /api/v1/admin/openai/refresh-token",
		"POST /api/v1/admin/openai/create-from-oauth",
		"POST /api/v1/admin/gemini/oauth/exchange-code",
		"POST /api/v1/admin/grok/oauth/create-from-oauth",
		// proxies, users, settings, storage, payment
		"POST /api/v1/admin/proxies",
		"PUT /api/v1/admin/proxies/:id",
		"POST /api/v1/admin/users",
		"PUT /api/v1/admin/users/:id",
		"PUT /api/v1/admin/settings",
		"PUT /api/v1/admin/payment/config",
		"POST /api/v1/admin/payment/providers",
		"PUT /api/v1/admin/payment/providers/:id",
		"POST /api/v1/admin/data-management/s3/profiles",
		"PUT /api/v1/admin/data-management/sources/:source_type/profiles/:profile_id",
		"PUT /api/v1/admin/backups/s3-config",
	}
	for _, key := range mustOmit {
		method, path, _ := strings.Cut(key, " ")
		_, exists := protected[key]
		require.Truef(t, exists, "%s is not a real admin route", key)
		require.Truef(t, middleware.AdminAuditBodyOmitted(method, path), "%s submits credentials; its body must not be audited", key)
	}

	// An ordinary route keeps its (redacted) body.
	require.False(t, middleware.AdminAuditBodyOmitted("POST", "/api/v1/admin/announcements"))
	require.False(t, middleware.AdminAuditBodyOmitted("POST", "/api/v1/admin/users/:id/balance"))
	// An empty route fails closed.
	require.True(t, middleware.AdminAuditBodyOmitted("POST", ""))
}

// The categories the task calls out must be on the danger list: refunds,
// balance/compensation, deleting users/groups/accounts, pricing, system
// settings writes, admin tokens themselves, bulk delete and subscription void.
func TestAdminDangerListCoversRequiredCategories(t *testing.T) {
	required := []string{
		// refunds
		"POST /api/v1/admin/payment/orders/:id/refund",
		// balance / compensation
		"POST /api/v1/admin/users/:id/balance",
		"POST /api/v1/admin/subscriptions/:id/reset-quota",
		// deleting users, groups, accounts
		"DELETE /api/v1/admin/users/:id",
		"DELETE /api/v1/admin/groups/:id",
		"DELETE /api/v1/admin/accounts/:id",
		// pricing: channel prices, group multipliers
		"PUT /api/v1/admin/channels/:id",
		"PUT /api/v1/admin/groups/:id/rate-multipliers",
		// system settings
		"PUT /api/v1/admin/settings",
		// admin tokens
		"GET /api/v1/admin/admin-tokens",
		"POST /api/v1/admin/admin-tokens",
		"DELETE /api/v1/admin/admin-tokens/:id",
		// bulk delete
		"POST /api/v1/admin/proxies/batch-delete",
		"POST /api/v1/admin/redeem-codes/batch-delete",
		// subscription card void
		"DELETE /api/v1/admin/subscriptions/:id",
		// credentials, bulk edits, group moves (review round 1)
		"POST /api/v1/admin/accounts/bulk-update",
		"POST /api/v1/admin/accounts/import/codex-session",
		"POST /api/v1/admin/accounts/data",
		"PUT /api/v1/admin/accounts/:id",
		"PUT /api/v1/admin/api-keys/:id",
		"POST /api/v1/admin/users/:id/replace-group",
		"POST /api/v1/admin/users/batch-concurrency",
		"POST /api/v1/admin/users/batch-limits",
		"PUT /api/v1/admin/users/:id/platform-quotas",
	}
	for _, key := range required {
		method, path, _ := strings.Cut(key, " ")
		require.Equalf(t, service.AdminTokenScopeDanger, middleware.AdminRouteRequiredScope(method, path),
			"%s must require danger scope", key)
	}
}

func TestAdminRouteRequiredScopeDefaults(t *testing.T) {
	require.Equal(t, service.AdminTokenScopeRead, middleware.AdminRouteRequiredScope("GET", "/api/v1/admin/dashboard/stats"))
	require.Equal(t, service.AdminTokenScopeRead, middleware.AdminRouteRequiredScope("HEAD", "/api/v1/admin/dashboard/stats"))
	require.Equal(t, service.AdminTokenScopeWrite, middleware.AdminRouteRequiredScope("POST", "/api/v1/admin/accounts"))
	require.Equal(t, service.AdminTokenScopeWrite, middleware.AdminRouteRequiredScope("PUT", "/api/v1/admin/accounts/:id"))
	require.Equal(t, service.AdminTokenScopeWrite, middleware.AdminRouteRequiredScope("DELETE", "/api/v1/admin/proxies/:id"))
	// A route the table has never heard of defaults by method, but an empty
	// route (should be impossible behind adminAuth) fails closed.
	require.Equal(t, service.AdminTokenScopeWrite, middleware.AdminRouteRequiredScope("POST", "/api/v1/admin/brand-new/:id"))
	require.Equal(t, service.AdminTokenScopeDanger, middleware.AdminRouteRequiredScope("GET", ""))
}

// The token management routes are JWT-only: a machine credential, including
// the legacy global key and a danger-scope token, gets a 403 before any handler
// runs (the handlers are nil here, so reaching one would panic).
func TestAdminTokenManagementRoutesRejectMachineCredentials(t *testing.T) {
	for _, authMethod := range []string{
		service.AuditAuthMethodAdminToken,
		service.AuditAuthMethodAdminAPIKey,
		"",
	} {
		authMethod := authMethod
		t.Run("auth_method="+authMethod, func(t *testing.T) {
			router, _ := buildAdminRouter(t, func(c *gin.Context) {
				c.Set("auth_method", authMethod)
				c.Next()
			})
			for _, call := range []struct{ method, path string }{
				{http.MethodGet, "/api/v1/admin/admin-tokens"},
				{http.MethodPost, "/api/v1/admin/admin-tokens"},
				{http.MethodDelete, "/api/v1/admin/admin-tokens/1"},
			} {
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest(call.method, call.path, nil))
				require.Equalf(t, http.StatusForbidden, recorder.Code, "%s %s", call.method, call.path)
				require.Contains(t, recorder.Body.String(), "ADMIN_TOKEN_MANAGEMENT_JWT_ONLY")
			}
		})
	}
}
