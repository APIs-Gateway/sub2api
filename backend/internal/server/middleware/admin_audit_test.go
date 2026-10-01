//go:build unit

package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// --- test doubles ----------------------------------------------------------

// adminAuditTestSink collects the rows the middleware hands over.
type adminAuditTestSink struct {
	mu      sync.Mutex
	entries []*service.AuditLog
	accept  bool // value Enqueue returns
	panics  bool // Enqueue panics
}

func (s *adminAuditTestSink) Enqueue(entry *service.AuditLog) bool {
	if s.panics {
		panic("sink exploded")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
	return s.accept
}

func (s *adminAuditTestSink) all() []*service.AuditLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*service.AuditLog(nil), s.entries...)
}

func (s *adminAuditTestSink) only(t *testing.T) *service.AuditLog {
	t.Helper()
	entries := s.all()
	require.Len(t, entries, 1, "expected exactly one audit row")
	return entries[0]
}

// auditRepoStub is an in-memory service.AuditLogRepository.
type auditRepoStub struct {
	mu   sync.Mutex
	rows []*service.AuditLog
}

func (r *auditRepoStub) Insert(_ context.Context, entry *service.AuditLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, entry)
	return nil
}

func (r *auditRepoStub) BatchInsert(_ context.Context, entries []*service.AuditLog) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, entries...)
	return int64(len(entries)), nil
}

func (r *auditRepoStub) List(context.Context, *service.AuditLogFilter) (*service.AuditLogList, error) {
	return &service.AuditLogList{}, nil
}

func (r *auditRepoStub) DeleteBefore(context.Context, time.Time, int) (int64, error) { return 0, nil }

// --- what gets recorded ----------------------------------------------------

func TestAdminAuditRecordsSuccessfulTokenWrite(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	plaintext, token := env.mint(t, "ops-bot", service.AdminTokenScopeWrite, nil)

	w := env.do(adminTokenTestRequest{
		method: http.MethodPost,
		path:   "/api/v1/admin/things/7/notes?page=2&api_key=sk-live-123",
		header: map[string]string{
			"x-api-key":    plaintext,
			"X-Reason":     "annotate thing 7 for the audit demo",
			"User-Agent":   "audit-test/1.0",
			"Content-Type": "application/json",
			"X-Request-ID": "req-123",
		},
		body:       `{"note":"hello","password":"hunter2","nested":{"api_key":"sk-abc","items":[{"token":"t-1","ok":true}]}}`,
		remoteAddr: "10.9.8.7:5555",
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	entry := env.sink.only(t)
	require.Equal(t, service.AuditAuthKindAdminToken, entry.AuthKind)
	require.Equal(t, service.AuditAuthMethodAdminToken, entry.AuthMethod)
	require.Equal(t, "token:ops-bot#1", entry.ActorLabel)
	require.NotNil(t, entry.TokenID)
	require.Equal(t, token.ID, *entry.TokenID)
	require.NotNil(t, entry.ActorUserID)
	require.Equal(t, env.adminUser.ID, *entry.ActorUserID)
	require.Equal(t, "admin@example.com", entry.ActorEmail)
	require.Equal(t, "admin", entry.ActorRole)
	require.Equal(t, plaintext[:service.AdminTokenDisplayPrefixLen], entry.CredentialMasked, "only the display prefix is kept")
	require.NotContains(t, entry.CredentialMasked, plaintext[service.AdminTokenDisplayPrefixLen:])

	require.Equal(t, http.MethodPost, entry.Method)
	require.Equal(t, "/api/v1/admin/things/:id/notes", entry.Route)
	require.Equal(t, "POST /things/:id/notes", entry.Action)
	require.Equal(t, "things", entry.TargetType)
	require.Equal(t, "7", entry.TargetID)
	require.Equal(t, http.StatusOK, entry.StatusCode)
	require.Equal(t, "annotate thing 7 for the audit demo", entry.Reason)
	require.Equal(t, "10.9.8.7", entry.ClientIP)
	require.Equal(t, "audit-test/1.0", entry.UserAgent)
	require.False(t, entry.CreatedAt.IsZero())
	require.GreaterOrEqual(t, entry.LatencyMs, int64(0))
	require.Empty(t, entry.Before)
	require.Empty(t, entry.After)

	// The path carries the query with secrets replaced.
	require.True(t, strings.HasPrefix(entry.Path, "/api/v1/admin/things/7/notes?"), entry.Path)
	require.NotContains(t, entry.Path, "sk-live-123")
	query, err := url.ParseQuery(strings.SplitN(entry.Path, "?", 2)[1])
	require.NoError(t, err)
	require.Equal(t, "2", query.Get("page"))
	require.Equal(t, "[REDACTED]", query.Get("api_key"))

	// The body is redacted recursively, including inside arrays.
	for _, secret := range []string{"hunter2", "sk-abc", "t-1"} {
		require.NotContains(t, entry.RequestBody, secret)
	}
	require.Contains(t, entry.RequestBody, `"note":"hello"`)
	require.Contains(t, entry.RequestBody, `"ok":true`)
	require.Contains(t, entry.RequestBody, "[REDACTED]")
	require.NotContains(t, entry.RequestBody, plaintext)
}

func TestAdminAuditRecordsFailedAndPanickingRequests(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	plaintext, _ := env.mint(t, "ops-bot", service.AdminTokenScopeWrite, nil)

	t.Run("handler error", func(t *testing.T) {
		before := len(env.sink.all())
		w := env.do(adminTokenTestRequest{method: http.MethodPost, path: "/api/v1/admin/fail", header: apiKey(plaintext)})
		require.Equal(t, http.StatusUnprocessableEntity, w.Code)

		entries := env.sink.all()
		require.Len(t, entries, before+1)
		last := entries[len(entries)-1]
		require.Equal(t, http.StatusUnprocessableEntity, last.StatusCode)
		require.Equal(t, "/api/v1/admin/fail", last.Route)
		require.Equal(t, "fail", last.TargetType)
		require.Empty(t, last.TargetID)
	})

	t.Run("handler panic", func(t *testing.T) {
		before := len(env.sink.all())
		w := env.do(adminTokenTestRequest{method: http.MethodPost, path: "/api/v1/admin/boom", header: apiKey(plaintext)})
		// The panic was not swallowed: the project's recovery middleware answered.
		require.Equal(t, http.StatusInternalServerError, w.Code)

		entries := env.sink.all()
		require.Len(t, entries, before+1)
		last := entries[len(entries)-1]
		require.Equal(t, http.StatusInternalServerError, last.StatusCode)
		require.Equal(t, true, last.Extra["panic"])
		require.Equal(t, "token:ops-bot#1", last.ActorLabel)
		require.Equal(t, "POST /boom", last.Action)
	})
}

func TestAdminAuditRecordsRejectedTokenAttempts(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	now := time.Now()

	revoked, revokedToken := env.mint(t, "revoked", service.AdminTokenScopeDanger, func(tk *service.AdminToken) {
		at := now.Add(-time.Minute)
		tk.RevokedAt = &at
	})
	expired, _ := env.mint(t, "expired", service.AdminTokenScopeDanger, func(tk *service.AdminToken) {
		tk.ExpiresAt = now.Add(-time.Second)
	})
	restricted, _ := env.mint(t, "restricted", service.AdminTokenScopeDanger, func(tk *service.AdminToken) {
		tk.IPAllowlist = []string{"10.0.0.0/8"}
	})
	reader, _ := env.mint(t, "reader", service.AdminTokenScopeRead, nil)
	writer, _ := env.mint(t, "writer", service.AdminTokenScopeWrite, nil)

	cases := []struct {
		name       string
		req        adminTokenTestRequest
		wantStatus int
		wantCode   string
		wantLabel  string
	}{
		{"revoked", adminTokenTestRequest{path: "/api/v1/admin/things", header: apiKey(revoked)}, 401, "ADMIN_TOKEN_REVOKED", "token:revoked#1"},
		{"expired", adminTokenTestRequest{path: "/api/v1/admin/things", header: apiKey(expired)}, 401, "ADMIN_TOKEN_EXPIRED", "token:expired#2"},
		{"ip not allowed", adminTokenTestRequest{path: "/api/v1/admin/things", header: apiKey(restricted), remoteAddr: "198.51.100.7:1"}, 403, "ADMIN_TOKEN_IP_NOT_ALLOWED", "token:restricted#3"},
		{"scope too low", adminTokenTestRequest{path: "/api/v1/admin/things", header: apiKey(reader)}, 403, "ADMIN_TOKEN_SCOPE_INSUFFICIENT", "token:reader#4"},
		{"danger route with write token", adminTokenTestRequest{method: http.MethodDelete, path: "/api/v1/admin/users/9", header: apiKey(writer)}, 403, "ADMIN_TOKEN_SCOPE_INSUFFICIENT", "token:writer#5"},
		{"missing reason", adminTokenTestRequest{path: "/api/v1/admin/things", header: apiKey(writer), omitReason: true}, 400, "ADMIN_REASON_REQUIRED", "token:writer#5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			if req.method == "" {
				req.method = http.MethodPost
			}
			before := len(env.sink.all())
			w := env.do(req)
			require.Equal(t, tc.wantStatus, w.Code, w.Body.String())

			entries := env.sink.all()
			require.Len(t, entries, before+1, "a rejected attempt with an identified token is audited")
			last := entries[len(entries)-1]
			require.Equal(t, tc.wantStatus, last.StatusCode)
			require.Equal(t, tc.wantCode, last.Extra["reject_code"])
			require.Equal(t, tc.wantLabel, last.ActorLabel)
			require.Equal(t, service.AuditAuthKindAdminToken, last.AuthKind)
			require.Equal(t, service.AuditAuthMethodAdminToken, last.AuthMethod)
			require.NotNil(t, last.TokenID)
		})
	}
	require.Equal(t, revokedToken.ID, *env.sink.all()[0].TokenID)
}

func TestAdminAuditSkipsReadsAndUnidentifiedCallers(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	plaintext, _ := env.mint(t, "ops-bot", service.AdminTokenScopeDanger, nil)

	// Reads are not audited.
	for _, header := range []map[string]string{apiKey(plaintext), apiKey(adminTokenTestLegacyKey), bearer(env.jwt(t))} {
		w := env.do(adminTokenTestRequest{method: http.MethodGet, path: "/api/v1/admin/things", header: header})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}

	// Callers that identify nobody are not audited either (they could otherwise
	// fill the table from the open internet).
	for name, req := range map[string]adminTokenTestRequest{
		"no credentials":    {method: http.MethodPost, path: "/api/v1/admin/things"},
		"unknown token":     {method: http.MethodPost, path: "/api/v1/admin/things", header: apiKey("s2a_" + strings.Repeat("A", 43))},
		"malformed token":   {method: http.MethodPost, path: "/api/v1/admin/things", header: apiKey("s2a_nope")},
		"wrong legacy key":  {method: http.MethodPost, path: "/api/v1/admin/things", header: apiKey("admin-wrong")},
		"garbage jwt":       {method: http.MethodPost, path: "/api/v1/admin/things", header: bearer("not-a-jwt")},
		"options preflight": {method: http.MethodOptions, path: "/api/v1/admin/things", header: apiKey(plaintext)},
	} {
		_ = env.do(req)
		require.Empty(t, env.sink.all(), name)
	}
}

func TestAdminAuditRecordsLegacyKeyAndJWTWrites(t *testing.T) {
	env := newAdminTokenTestEnv(t)

	t.Run("legacy api key, reason not required but recorded when sent", func(t *testing.T) {
		w := env.do(adminTokenTestRequest{
			method: http.MethodPost, path: "/api/v1/admin/things",
			header: map[string]string{"x-api-key": adminTokenTestLegacyKey, "X-Reason": "nightly sync"},
		})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())

		entry := env.sink.all()[0]
		require.Equal(t, service.AuditAuthKindLegacyAPIKey, entry.AuthKind)
		require.Equal(t, "legacy_api_key", entry.ActorLabel)
		require.Equal(t, service.AuditAuthMethodAdminAPIKey, entry.AuthMethod)
		require.Nil(t, entry.TokenID)
		require.Equal(t, "nightly sync", entry.Reason)
		require.NotEmpty(t, entry.CredentialMasked)
		require.NotContains(t, entry.CredentialMasked, adminTokenTestLegacyKey, "the key is masked, never stored")
		require.Contains(t, entry.CredentialMasked, "****")
	})

	t.Run("jwt, no reason", func(t *testing.T) {
		w := env.do(adminTokenTestRequest{
			method: http.MethodPost, path: "/api/v1/admin/things",
			header: bearer(env.jwt(t)), omitReason: true,
		})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())

		entries := env.sink.all()
		require.Len(t, entries, 2)
		entry := entries[1]
		require.Equal(t, service.AuditAuthKindJWT, entry.AuthKind)
		require.Equal(t, "jwt:admin@example.com", entry.ActorLabel)
		require.Equal(t, service.AuditAuthMethodJWT, entry.AuthMethod)
		require.Empty(t, entry.CredentialMasked)
		require.Empty(t, entry.Reason)
		require.Nil(t, entry.TokenID)
	})
}

func TestAdminAuditBodyHandling(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	plaintext, _ := env.mint(t, "ops-bot", service.AdminTokenScopeWrite, nil)
	last := func() *service.AuditLog {
		entries := env.sink.all()
		return entries[len(entries)-1]
	}

	t.Run("non-JSON bodies record only their size", func(t *testing.T) {
		env.do(adminTokenTestRequest{
			method: http.MethodPost, path: "/api/v1/admin/echo",
			header: map[string]string{"x-api-key": plaintext, "Content-Type": "multipart/form-data; boundary=x"},
			body:   "--x\r\nContent-Disposition: form-data; name=\"password\"\r\n\r\nhunter2\r\n--x--",
		})
		body := last().RequestBody
		require.NotContains(t, body, "hunter2")
		require.Contains(t, body, "non-json body omitted")
		require.Contains(t, body, "bytes")
	})

	t.Run("JSON without a content type is sniffed and redacted", func(t *testing.T) {
		env.do(adminTokenTestRequest{
			method: http.MethodPost, path: "/api/v1/admin/echo",
			header: map[string]string{"x-api-key": plaintext, "Content-Type": "application/x-www-form-urlencoded"},
			body:   `{"name":"x","secret":"s3cr3t"}`,
		})
		body := last().RequestBody
		require.NotContains(t, body, "s3cr3t")
		require.Contains(t, body, `"name":"x"`)
	})

	t.Run("large bodies are truncated to 8KB", func(t *testing.T) {
		env.do(adminTokenTestRequest{
			method: http.MethodPost, path: "/api/v1/admin/echo",
			header: map[string]string{"x-api-key": plaintext, "Content-Type": "application/json"},
			body:   `{"note":"` + strings.Repeat("a", 20000) + `"}`,
		})
		body := last().RequestBody
		require.LessOrEqual(t, len(body), 8*1024)
		require.True(t, strings.HasSuffix(body, "...<truncated>"), "truncation is marked")
	})

	t.Run("bodies over the capture limit are omitted", func(t *testing.T) {
		env.do(adminTokenTestRequest{
			method: http.MethodPost, path: "/api/v1/admin/echo",
			header: map[string]string{"x-api-key": plaintext, "Content-Type": "application/json"},
			body:   `{"note":"` + strings.Repeat("a", service.AuditRequestBodyCaptureLimit+10) + `"}`,
		})
		entry := last()
		require.Contains(t, entry.RequestBody, "exceeds")
		require.Greater(t, entry.Extra["body_bytes"], int64(service.AuditRequestBodyCaptureLimit))
	})

	t.Run("the handler still receives the whole body", func(t *testing.T) {
		payload := `{"note":"` + strings.Repeat("b", 3000) + `"}`
		w := env.do(adminTokenTestRequest{
			method: http.MethodPost, path: "/api/v1/admin/echo",
			header: map[string]string{"x-api-key": plaintext, "Content-Type": "application/json"},
			body:   payload,
		})
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, fmt.Sprint(len(payload)), w.Body.String())
	})
}

// --- auditing never affects the request ------------------------------------

func TestAdminAuditNeverChangesTheOutcome(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	plaintext, _ := env.mint(t, "ops-bot", service.AdminTokenScopeWrite, nil)

	t.Run("queue full (sink refuses the row)", func(t *testing.T) {
		env.sink.accept = false
		w := env.do(adminTokenTestRequest{method: http.MethodPost, path: "/api/v1/admin/things", header: apiKey(plaintext)})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Len(t, env.sink.all(), 1)
	})

	t.Run("sink panics", func(t *testing.T) {
		env.sink.accept = true
		env.sink.panics = true
		before := env.reached
		w := env.do(adminTokenTestRequest{method: http.MethodPost, path: "/api/v1/admin/things", header: apiKey(plaintext)})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, before+1, env.reached)
	})
}

func TestAdminAuditWithoutSinkIsTransparent(t *testing.T) {
	called := false
	inner := func(c *gin.Context) { called = true }
	wrapped := withAdminAudit(inner, nil)

	c, _ := gin.CreateTestContext(nil)
	wrapped(c)
	require.True(t, called)
}

func TestProvideAdminAuthMiddlewareWritesAuditThroughTheRealWriter(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	plaintext, _ := env.mint(t, "ops-bot", service.AdminTokenScopeWrite, nil)

	repo := &auditRepoStub{}
	writer := service.NewAdminAuditWriter(repo, 16)
	writer.Start()

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	group := router.Group("/api/v1/admin")
	group.Use(gin.HandlerFunc(ProvideAdminAuthMiddleware(env.authService, env.userSvc, env.settingSvc, env.tokenSvc, writer)))
	group.POST("/things", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := adminTokenTestRequest{method: http.MethodPost, path: "/api/v1/admin/things", header: apiKey(plaintext)}
	env.router = router
	w := env.do(req)
	require.Equal(t, http.StatusNoContent, w.Code)

	writer.Stop() // flushes the queue
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.rows, 1)
	require.Equal(t, "token:ops-bot#1", repo.rows[0].ActorLabel)
	require.Equal(t, writer.Stats().Written, uint64(1))
}

func TestProvideAdminAuthMiddlewareToleratesNilWriter(t *testing.T) {
	env := newAdminTokenTestEnv(t)
	plaintext, _ := env.mint(t, "ops-bot", service.AdminTokenScopeWrite, nil)

	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	group := router.Group("/api/v1/admin")
	group.Use(gin.HandlerFunc(ProvideAdminAuthMiddleware(env.authService, env.userSvc, env.settingSvc, env.tokenSvc, nil)))
	group.POST("/things", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	env.router = router
	w := env.do(adminTokenTestRequest{method: http.MethodPost, path: "/api/v1/admin/things", header: apiKey(plaintext)})
	require.Equal(t, http.StatusNoContent, w.Code)
}

// --- target derivation -----------------------------------------------------

func TestAdminAuditTarget(t *testing.T) {
	cases := []struct {
		route      string
		params     gin.Params
		wantType   string
		wantID     string
		wantAction string
	}{
		{"/api/v1/admin/users/:id/balance", gin.Params{{Key: "id", Value: "42"}}, "users", "42", "POST /users/:id/balance"},
		{"/api/v1/admin/users", nil, "users", "", "POST /users"},
		{"/api/v1/admin/settings/smtp/test", nil, "settings", "", "POST /settings/smtp/test"},
		{"/api/v1/admin/payment/orders/:id/refund", gin.Params{{Key: "id", Value: "9"}}, "orders", "9", "POST /payment/orders/:id/refund"},
		{"/api/v1/admin/payment/config", nil, "config", "", "POST /payment/config"},
		{"/api/v1/admin/groups/:id/rate-multipliers/:user_id", gin.Params{{Key: "id", Value: "3"}, {Key: "user_id", Value: "8"}}, "groups", "3", "POST /groups/:id/rate-multipliers/:user_id"},
		{"/api/v1/admin/accounts/batch-update", nil, "accounts", "", "POST /accounts/batch-update"},
		{"/api/v1/admin/files/*path", gin.Params{{Key: "path", Value: "/a/b"}}, "files", "a/b", "POST /files/*path"},
		{"/api/v1/admin", nil, "", "", "POST "},
	}
	for _, tc := range cases {
		t.Run(tc.route, func(t *testing.T) {
			gotType, gotID := adminAuditTarget(tc.route, tc.params)
			require.Equal(t, tc.wantType, gotType)
			require.Equal(t, tc.wantID, gotID)
			require.Equal(t, tc.wantAction, adminAuditAction(http.MethodPost, tc.route))
		})
	}
}

func TestAuditBodyCaptureKeepsAtMostOneByteOverTheLimit(t *testing.T) {
	big := strings.Repeat("x", service.AuditRequestBodyCaptureLimit+1000)
	capture := newAuditBodyCapture(readCloser{strings.NewReader(big)})
	require.NotNil(t, capture)

	buf := make([]byte, 4096)
	var read int
	for {
		n, err := capture.Read(buf)
		read += n
		if err != nil {
			break
		}
	}
	require.Equal(t, len(big), read, "every byte still reaches the handler")

	captured, total := capture.snapshot()
	require.Equal(t, int64(len(big)), total)
	require.Len(t, captured, service.AuditRequestBodyCaptureLimit+1)

	require.Nil(t, newAuditBodyCapture(nil))
	require.Nil(t, newAuditBodyCapture(http.NoBody))
}

type readCloser struct{ *strings.Reader }

func (readCloser) Close() error { return nil }
