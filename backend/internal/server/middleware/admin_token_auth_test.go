//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// --- test doubles ----------------------------------------------------------

type adminTokenMemRepo struct {
	mu     sync.Mutex
	next   int64
	byHash map[string]*service.AdminToken
}

func newAdminTokenMemRepo() *adminTokenMemRepo {
	return &adminTokenMemRepo{byHash: make(map[string]*service.AdminToken)}
}

func (r *adminTokenMemRepo) Create(_ context.Context, token *service.AdminToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	token.ID = r.next
	token.CreatedAt = time.Now()
	token.UpdatedAt = token.CreatedAt
	clone := *token
	r.byHash[token.TokenHash] = &clone
	return nil
}

func (r *adminTokenMemRepo) GetByHash(_ context.Context, hash string) (*service.AdminToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	token, ok := r.byHash[hash]
	if !ok {
		return nil, service.ErrAdminTokenNotFound
	}
	clone := *token
	return &clone, nil
}

func (r *adminTokenMemRepo) List(context.Context) ([]*service.AdminToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*service.AdminToken, 0, len(r.byHash))
	for _, token := range r.byHash {
		clone := *token
		out = append(out, &clone)
	}
	return out, nil
}

func (r *adminTokenMemRepo) Revoke(_ context.Context, id int64, at time.Time) (*service.AdminToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, token := range r.byHash {
		if token.ID == id {
			if token.RevokedAt == nil {
				token.RevokedAt = &at
			}
			clone := *token
			return &clone, nil
		}
	}
	return nil, service.ErrAdminTokenNotFound
}

func (r *adminTokenMemRepo) TouchLastUsed(_ context.Context, id int64, at time.Time, clientIP string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, token := range r.byHash {
		if token.ID == id {
			token.LastUsedAt = &at
			token.LastUsedIP = clientIP
		}
	}
	return nil
}

type adminTokenTestUserRepo struct {
	service.UserRepository
	users map[int64]*service.User
}

func (r *adminTokenTestUserRepo) GetByID(_ context.Context, id int64) (*service.User, error) {
	user, ok := r.users[id]
	if !ok {
		return nil, service.ErrUserNotFound
	}
	clone := *user
	return &clone, nil
}

func (r *adminTokenTestUserRepo) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

func (r *adminTokenTestUserRepo) GetFirstAdmin(context.Context) (*service.User, error) {
	for _, user := range r.users {
		if user.Role == service.RoleAdmin && user.Status == service.StatusActive {
			clone := *user
			return &clone, nil
		}
	}
	return nil, service.ErrUserNotFound
}

const adminTokenTestLegacyKey = "admin-legacy-test-key"

type adminTokenTestEnv struct {
	router      *gin.Engine
	tokens      *adminTokenMemRepo
	users       *adminTokenTestUserRepo
	authService *service.AuthService
	adminUser   *service.User
	lastKeys    map[string]any
	reached     int
	sink        *adminAuditTestSink
	tokenSvc    *service.AdminTokenService
	userSvc     *service.UserService
	settingSvc  *service.SettingService
}

func newAdminTokenTestEnv(t *testing.T) *adminTokenTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{JWT: config.JWTConfig{Secret: "test-secret", ExpireHour: 1}}
	authService := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)

	adminUser := &service.User{
		ID: 1, Email: "admin@example.com", Role: service.RoleAdmin,
		Status: service.StatusActive, Concurrency: 1,
	}
	users := &adminTokenTestUserRepo{users: map[int64]*service.User{adminUser.ID: adminUser}}
	userService := service.NewUserService(users, nil, nil, nil)

	repo := newAdminTokenMemRepo()
	tokenService := service.NewAdminTokenService(repo, userService.GetByID)
	settingService := service.NewSettingService(
		fakeSettingRepo{values: map[string]string{service.SettingKeyAdminAPIKey: adminTokenTestLegacyKey}},
		&config.Config{},
	)

	env := &adminTokenTestEnv{
		tokens: repo, users: users, authService: authService, adminUser: adminUser,
		sink: &adminAuditTestSink{accept: true}, tokenSvc: tokenService, userSvc: userService, settingSvc: settingService,
	}

	router := gin.New()
	// A panicking handler must still reach the project's recovery middleware.
	router.Use(Recovery())
	// Use the connection address only, never X-Forwarded-For, for client IP.
	require.NoError(t, router.SetTrustedProxies(nil))
	router.Use(func(c *gin.Context) {
		c.Next()
		env.lastKeys = make(map[string]any, len(c.Keys))
		for k, v := range c.Keys {
			env.lastKeys[k] = v
		}
	})

	handler := func(c *gin.Context) {
		env.reached++
		subject, _ := GetAuthSubjectFromContext(c)
		tokenID, _ := AdminTokenIDFromContext(c)
		c.JSON(http.StatusOK, gin.H{
			"auth_method": c.GetString("auth_method"),
			"kind":        AdminAuthKindFromContext(c),
			"scope":       AdminScopeFromContext(c),
			"label":       c.GetString(string(ContextKeyAdminActorLabel)),
			"token_id":    tokenID,
			"user_id":     subject.UserID,
		})
	}

	admin := router.Group("/api/v1/admin")
	admin.Use(withAdminAudit(adminAuth(authService, userService, settingService, tokenService), env.sink))
	admin.GET("/things", handler)  // read
	admin.POST("/things", handler) // write
	// A handler that, like a real one, consumes the request body (the audit
	// middleware records the body as the handler reads it).
	readBodyThenHandle := func(c *gin.Context) {
		_, _ = io.Copy(io.Discard, c.Request.Body)
		handler(c)
	}
	admin.POST("/things/:id/notes", readBodyThenHandle) // write, with a path parameter
	// Routes whose bodies carry credentials (see admin_audit_body_policy.go).
	admin.POST("/accounts/import/codex-session", readBodyThenHandle)
	admin.POST("/accounts/:id/reauth/codex-session", readBodyThenHandle)
	// A handler that adds its own audit facts, like POST /admin-tokens.
	admin.POST("/things/:id/spawn", func(c *gin.Context) {
		SetAdminAuditTarget(c, "things", "99")
		SetAdminAuditExtra(c, "spawned_name", "child")
		handler(c)
	})
	admin.POST("/fail", func(c *gin.Context) { // write, handler answers an error
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "NOPE"})
	})
	admin.POST("/boom", func(c *gin.Context) { // write, handler panics
		panic("kaboom")
	})
	admin.POST("/echo", func(c *gin.Context) { // write, handler reads the whole body
		n, _ := io.Copy(io.Discard, c.Request.Body)
		c.String(http.StatusOK, "%d", n)
	})
	admin.DELETE("/users/:id", handler)                     // danger (see adminDangerRules)
	admin.GET("/admin-tokens", RequireAdminJWT(), handler)  // danger + JWT only
	admin.POST("/admin-tokens", RequireAdminJWT(), handler) // danger + JWT only
	admin.DELETE("/admin-tokens/:id", RequireAdminJWT(), handler)

	// The same routes behind the three-argument constructor (no admin token support).
	legacyOnly := router.Group("/legacy")
	legacyOnly.Use(gin.HandlerFunc(NewAdminAuthMiddleware(authService, userService, settingService)))
	legacyOnly.GET("/things", handler)

	env.router = router
	return env
}

// mint stores a token directly (so tests can create expired / revoked ones)
// and returns its plaintext.
func (e *adminTokenTestEnv) mint(t *testing.T, name, scope string, mutate func(*service.AdminToken)) (string, *service.AdminToken) {
	t.Helper()
	plaintext, err := service.GenerateAdminTokenPlaintext()
	require.NoError(t, err)
	token := &service.AdminToken{
		Name:         name,
		TokenHash:    service.HashAdminToken(plaintext),
		TokenPrefix:  plaintext[:service.AdminTokenDisplayPrefixLen],
		Scope:        scope,
		ActingUserID: e.adminUser.ID,
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	if mutate != nil {
		mutate(token)
	}
	require.NoError(t, e.tokens.Create(context.Background(), token))
	return plaintext, token
}

func (e *adminTokenTestEnv) jwt(t *testing.T) string {
	t.Helper()
	token, err := e.authService.GenerateToken(&service.User{
		ID: e.adminUser.ID, Email: e.adminUser.Email, Role: e.adminUser.Role,
	})
	require.NoError(t, err)
	return token
}

type adminTokenTestRequest struct {
	method     string
	path       string
	header     map[string]string
	remoteAddr string
	body       string
	// omitReason leaves out the X-Reason that do() adds to state-changing requests.
	omitReason bool
}

func (e *adminTokenTestEnv) do(req adminTokenTestRequest) *httptest.ResponseRecorder {
	var body io.Reader
	if req.body != "" {
		body = strings.NewReader(req.body)
	}
	r := httptest.NewRequest(req.method, req.path, body)
	for k, v := range req.header {
		r.Header.Set(k, v)
	}
	if !req.omitReason && !isAdminReadOnlyMethod(req.method) && r.Header.Get(AdminReasonHeader) == "" {
		r.Header.Set(AdminReasonHeader, "integration test change")
	}
	if req.remoteAddr != "" {
		r.RemoteAddr = req.remoteAddr
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, r)
	return w
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}
func apiKey(token string) map[string]string { return map[string]string{"x-api-key": token} }

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	return body.Code
}

func decodeIdentity(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	return body
}

// --- authentication --------------------------------------------------------

func TestAdminTokenAuthAcceptsXAPIKeyAndBearer(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	plaintext, token := env.mint(t, "ops-bot", service.AdminTokenScopeWrite, nil)

	for name, header := range map[string]map[string]string{
		"x-api-key":     apiKey(plaintext),
		"bearer":        bearer(plaintext),
		"bearer-casing": {"Authorization": "bearer " + plaintext},
	} {
		t.Run(name, func(t *testing.T) {
			w := env.do(adminTokenTestRequest{method: http.MethodGet, path: "/api/v1/admin/things", header: header})
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			got := decodeIdentity(t, w)
			require.Equal(t, service.AuditAuthMethodAdminToken, got["auth_method"])
			require.Equal(t, service.AuditAuthKindAdminToken, got["kind"])
			require.Equal(t, service.AdminTokenScopeWrite, got["scope"])
			require.EqualValues(t, token.ID, got["token_id"])
			require.EqualValues(t, env.adminUser.ID, got["user_id"], "the request acts as the acting administrator")
			require.Equal(t, "token:ops-bot#1", got["label"])
		})
	}
}

func TestAdminTokenAuthRejectsUnknownAndMalformedTokens(t *testing.T) {
	env := newAdminTokenTestEnv(t)

	for name, credential := range map[string]string{
		"unknown":   "s2a_" + "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8S9t0U1v",
		"prefix":    "s2a_",
		"malformed": "s2a_not a token",
	} {
		t.Run(name, func(t *testing.T) {
			for _, header := range []map[string]string{apiKey(credential), bearer(credential)} {
				before := env.reached
				w := env.do(adminTokenTestRequest{method: http.MethodGet, path: "/api/v1/admin/things", header: header})
				require.Equal(t, http.StatusUnauthorized, w.Code)
				require.Equal(t, "ADMIN_TOKEN_INVALID", errorCode(t, w))
				require.Equal(t, before, env.reached, "handler must not run")
			}
		})
	}
}

func TestAdminTokenAuthRejectsRevokedExpiredAndForeignIP(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	now := time.Now()

	revoked, _ := env.mint(t, "revoked", service.AdminTokenScopeDanger, func(tk *service.AdminToken) {
		at := now.Add(-time.Minute)
		tk.RevokedAt = &at
	})
	expired, _ := env.mint(t, "expired", service.AdminTokenScopeDanger, func(tk *service.AdminToken) {
		tk.ExpiresAt = now.Add(-time.Second)
	})
	restricted, restrictedToken := env.mint(t, "restricted", service.AdminTokenScopeDanger, func(tk *service.AdminToken) {
		tk.IPAllowlist = []string{"10.0.0.0/8"}
	})

	cases := []struct {
		name       string
		credential string
		remoteAddr string
		wantStatus int
		wantCode   string
	}{
		{"revoked", revoked, "10.1.2.3:4000", http.StatusUnauthorized, "ADMIN_TOKEN_REVOKED"},
		{"expired", expired, "10.1.2.3:4000", http.StatusUnauthorized, "ADMIN_TOKEN_EXPIRED"},
		{"ip_outside_allowlist", restricted, "198.51.100.7:4000", http.StatusForbidden, "ADMIN_TOKEN_IP_NOT_ALLOWED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := env.reached
			w := env.do(adminTokenTestRequest{
				method: http.MethodGet, path: "/api/v1/admin/things",
				header: apiKey(tc.credential), remoteAddr: tc.remoteAddr,
			})
			require.Equal(t, tc.wantStatus, w.Code, w.Body.String())
			require.Equal(t, tc.wantCode, errorCode(t, w))
			require.Equal(t, before, env.reached, "handler must not run")
			require.Equal(t, tc.wantCode, env.lastKeys[string(ContextKeyAdminRejectCode)], "reject code is recorded for audit")
			require.NotContains(t, env.lastKeys, string(ContextKeyUser), "a rejected token must not authenticate anybody")
		})
	}

	t.Run("ip_inside_allowlist", func(t *testing.T) {
		w := env.do(adminTokenTestRequest{
			method: http.MethodGet, path: "/api/v1/admin/things",
			header: apiKey(restricted), remoteAddr: "10.1.2.3:4000",
		})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})

	t.Run("forwarded_for_header_does_not_bypass_allowlist", func(t *testing.T) {
		w := env.do(adminTokenTestRequest{
			method: http.MethodGet, path: "/api/v1/admin/things",
			header:     map[string]string{"x-api-key": restricted, "X-Forwarded-For": "10.1.2.3"},
			remoteAddr: "198.51.100.7:4000",
		})
		require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		require.Equal(t, "ADMIN_TOKEN_IP_NOT_ALLOWED", errorCode(t, w))
	})

	t.Run("rejected_attempts_are_attributed_to_the_token", func(t *testing.T) {
		env.do(adminTokenTestRequest{
			method: http.MethodGet, path: "/api/v1/admin/things",
			header: apiKey(restricted), remoteAddr: "198.51.100.7:4000",
		})
		require.Equal(t, restrictedToken.ID, env.lastKeys[string(ContextKeyAdminTokenID)])
		require.Equal(t, "token:restricted#3", env.lastKeys[string(ContextKeyAdminActorLabel)])
		require.Equal(t, service.AuditAuthKindAdminToken, env.lastKeys[string(ContextKeyAdminAuthKind)])
	})
}

func TestAdminTokenAuthRejectsInvalidActingUser(t *testing.T) {
	env := newAdminTokenTestEnv(t)

	cases := map[string]func(){
		"user_deleted": func() { delete(env.users.users, 2) },
		"user_disabled": func() {
			env.users.users[2] = &service.User{ID: 2, Email: "x@example.com", Role: service.RoleAdmin, Status: service.StatusDisabled}
		},
		"user_demoted": func() {
			env.users.users[2] = &service.User{ID: 2, Email: "x@example.com", Role: service.RoleUser, Status: service.StatusActive}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			env.users.users[2] = &service.User{ID: 2, Email: "x@example.com", Role: service.RoleAdmin, Status: service.StatusActive}
			plaintext, _ := env.mint(t, name, service.AdminTokenScopeDanger, func(tk *service.AdminToken) { tk.ActingUserID = 2 })
			// Works while the acting user is a valid administrator ...
			w := env.do(adminTokenTestRequest{method: http.MethodGet, path: "/api/v1/admin/things", header: apiKey(plaintext)})
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			// ... and stops working as soon as that stops being true.
			setup()
			w = env.do(adminTokenTestRequest{method: http.MethodGet, path: "/api/v1/admin/things", header: apiKey(plaintext)})
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			require.Equal(t, "ADMIN_TOKEN_ACTING_USER_INVALID", errorCode(t, w))
		})
	}
}

func TestAdminTokenAuthWithoutTokenServiceRejectsTokenCredentials(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	plaintext, _ := env.mint(t, "any", service.AdminTokenScopeDanger, nil)

	w := env.do(adminTokenTestRequest{method: http.MethodGet, path: "/legacy/things", header: apiKey(plaintext)})
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, "ADMIN_TOKEN_INVALID", errorCode(t, w))

	// The legacy key still works on the same stack.
	w = env.do(adminTokenTestRequest{method: http.MethodGet, path: "/legacy/things", header: apiKey(adminTokenTestLegacyKey)})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestAdminTokenAuthMissingCredentialsStillUnauthorized(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	w := env.do(adminTokenTestRequest{method: http.MethodGet, path: "/api/v1/admin/things"})
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, "UNAUTHORIZED", errorCode(t, w))
}

// --- scopes -----------------------------------------------------------------

func TestAdminTokenScopeEnforcement(t *testing.T) {
	env := newAdminTokenTestEnv(t)

	type call struct {
		name   string
		method string
		path   string
		need   string
	}
	calls := []call{
		{"read route", http.MethodGet, "/api/v1/admin/things", service.AdminTokenScopeRead},
		{"write route", http.MethodPost, "/api/v1/admin/things", service.AdminTokenScopeWrite},
		{"danger route", http.MethodDelete, "/api/v1/admin/users/9", service.AdminTokenScopeDanger},
	}

	for _, scope := range []string{service.AdminTokenScopeRead, service.AdminTokenScopeWrite, service.AdminTokenScopeDanger} {
		plaintext, _ := env.mint(t, "scope-"+scope, scope, nil)
		for _, c := range calls {
			t.Run(scope+" token on "+c.name, func(t *testing.T) {
				before := env.reached
				w := env.do(adminTokenTestRequest{method: c.method, path: c.path, header: bearer(plaintext)})
				if service.AdminTokenScopeRank(scope) >= service.AdminTokenScopeRank(c.need) {
					require.Equal(t, http.StatusOK, w.Code, w.Body.String())
					require.Equal(t, before+1, env.reached)
					return
				}
				require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
				require.Equal(t, "ADMIN_TOKEN_SCOPE_INSUFFICIENT", errorCode(t, w))
				require.Equal(t, before, env.reached, "handler must not run")
				require.Equal(t, "ADMIN_TOKEN_SCOPE_INSUFFICIENT", env.lastKeys[string(ContextKeyAdminRejectCode)])
			})
		}
	}
}

// --- legacy global admin API key and JWT ------------------------------------

func TestAdminTokenLegacyAPIKeyBehaviourIsUnchanged(t *testing.T) {
	env := newAdminTokenTestEnv(t)

	for _, c := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/admin/things"},
		{http.MethodPost, "/api/v1/admin/things"},
		{http.MethodDelete, "/api/v1/admin/users/9"}, // danger route: the legacy key is not scope-limited
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			w := env.do(adminTokenTestRequest{method: c.method, path: c.path, header: apiKey(adminTokenTestLegacyKey)})
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			got := decodeIdentity(t, w)
			require.Equal(t, service.AuditAuthMethodAdminAPIKey, got["auth_method"], "auth_method keeps its historical value")
			require.Equal(t, service.AuditAuthKindLegacyAPIKey, got["kind"])
			require.Equal(t, service.AdminTokenScopeDanger, got["scope"])
			require.Equal(t, "legacy_api_key", got["label"])
			require.EqualValues(t, env.adminUser.ID, got["user_id"])
		})
	}

	t.Run("wrong key", func(t *testing.T) {
		w := env.do(adminTokenTestRequest{method: http.MethodGet, path: "/api/v1/admin/things", header: apiKey("admin-wrong")})
		require.Equal(t, http.StatusUnauthorized, w.Code)
		require.Equal(t, "INVALID_ADMIN_KEY", errorCode(t, w))
	})
}

func TestAdminTokenJWTBehaviourIsUnchanged(t *testing.T) {
	env := newAdminTokenTestEnv(t)

	for _, c := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/admin/things"},
		{http.MethodPost, "/api/v1/admin/things"},
		{http.MethodDelete, "/api/v1/admin/users/9"},
	} {
		w := env.do(adminTokenTestRequest{method: c.method, path: c.path, header: bearer(env.jwt(t))})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		got := decodeIdentity(t, w)
		require.Equal(t, service.AuditAuthMethodJWT, got["auth_method"])
		require.Equal(t, service.AuditAuthKindJWT, got["kind"])
		require.Equal(t, service.AdminTokenScopeDanger, got["scope"], "interactive administrators are not scope-limited")
		require.Equal(t, "jwt:admin@example.com", got["label"])
	}
}

// --- token management is JWT-only ------------------------------------------

func TestAdminTokenManagementRoutesRejectMachineCredentials(t *testing.T) {
	env := newAdminTokenTestEnv(t)

	readToken, _ := env.mint(t, "r", service.AdminTokenScopeRead, nil)
	writeToken, _ := env.mint(t, "w", service.AdminTokenScopeWrite, nil)
	dangerToken, _ := env.mint(t, "d", service.AdminTokenScopeDanger, nil)

	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/admin-tokens"},
		{http.MethodPost, "/api/v1/admin/admin-tokens"},
		{http.MethodDelete, "/api/v1/admin/admin-tokens/1"},
	}
	credentials := map[string]map[string]string{
		"read token":       apiKey(readToken),
		"write token":      bearer(writeToken),
		"danger token":     apiKey(dangerToken),
		"danger token (b)": bearer(dangerToken),
		"legacy api key":   apiKey(adminTokenTestLegacyKey),
	}
	for name, header := range credentials {
		for _, route := range routes {
			t.Run(name+" "+route.method, func(t *testing.T) {
				before := env.reached
				w := env.do(adminTokenTestRequest{method: route.method, path: route.path, header: header})
				require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
				require.Equal(t, before, env.reached, "handler must not run")
				// Low scopes are stopped by the scope check, the highest scope and
				// the legacy key by the JWT-only guard; both are 403.
				require.Contains(t, []string{"ADMIN_TOKEN_SCOPE_INSUFFICIENT", "ADMIN_TOKEN_MANAGEMENT_JWT_ONLY"}, errorCode(t, w))
			})
		}
	}

	t.Run("danger token gets the JWT-only error", func(t *testing.T) {
		w := env.do(adminTokenTestRequest{method: http.MethodPost, path: "/api/v1/admin/admin-tokens", header: apiKey(dangerToken)})
		require.Equal(t, "ADMIN_TOKEN_MANAGEMENT_JWT_ONLY", errorCode(t, w))
	})

	t.Run("jwt administrator is accepted", func(t *testing.T) {
		for _, route := range routes {
			w := env.do(adminTokenTestRequest{method: route.method, path: route.path, header: bearer(env.jwt(t))})
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		}
	})
}

// --- step-up ---------------------------------------------------------------

func TestEnforceStepUpRejectsAdminTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/sensitive", nil)
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})
	c.Set("auth_method", service.AuditAuthMethodAdminToken)

	// Even a user with TOTP and a valid grant must not be usable through a token.
	ok := EnforceStepUpAlways(c,
		stepUpGrantCheckerStub{granted: true},
		stepUpUserReaderStub{user: &service.User{ID: 1, TotpEnabled: true}},
	)

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_ADMIN_TOKEN_FORBIDDEN")
}
