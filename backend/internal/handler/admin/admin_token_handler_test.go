//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type adminTokenHandlerRepo struct {
	mu     sync.Mutex
	next   int64
	tokens []*service.AdminToken
}

func (r *adminTokenHandlerRepo) Create(_ context.Context, token *service.AdminToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	token.ID = r.next
	token.CreatedAt = time.Now()
	token.UpdatedAt = token.CreatedAt
	clone := *token
	r.tokens = append(r.tokens, &clone)
	return nil
}

func (r *adminTokenHandlerRepo) GetByHash(_ context.Context, hash string) (*service.AdminToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, token := range r.tokens {
		if token.TokenHash == hash {
			clone := *token
			return &clone, nil
		}
	}
	return nil, service.ErrAdminTokenNotFound
}

func (r *adminTokenHandlerRepo) List(context.Context) ([]*service.AdminToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*service.AdminToken, 0, len(r.tokens))
	for i := len(r.tokens) - 1; i >= 0; i-- {
		clone := *r.tokens[i]
		out = append(out, &clone)
	}
	return out, nil
}

func (r *adminTokenHandlerRepo) Revoke(_ context.Context, id int64, at time.Time) (*service.AdminToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, token := range r.tokens {
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

func (r *adminTokenHandlerRepo) TouchLastUsed(context.Context, int64, time.Time, string) error {
	return nil
}

type adminTokenHandlerEnv struct {
	router *gin.Engine
	repo   *adminTokenHandlerRepo
}

func newAdminTokenHandlerEnv(t *testing.T) *adminTokenHandlerEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	users := map[int64]*service.User{
		1: {ID: 1, Email: "admin@example.com", Role: service.RoleAdmin, Status: service.StatusActive},
		2: {ID: 2, Email: "other-admin@example.com", Role: service.RoleAdmin, Status: service.StatusActive},
		3: {ID: 3, Email: "user@example.com", Role: service.RoleUser, Status: service.StatusActive},
	}
	repo := &adminTokenHandlerRepo{}
	tokenService := service.NewAdminTokenService(repo, func(_ context.Context, id int64) (*service.User, error) {
		user, ok := users[id]
		if !ok {
			return nil, service.ErrUserNotFound
		}
		return user, nil
	})
	handler := NewAdminTokenHandler(tokenService)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		// Stand-in for the admin auth middleware: the signed-in administrator is user 1.
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Next()
	})
	router.POST("/admin-tokens", handler.Create)
	router.GET("/admin-tokens", handler.List)
	router.DELETE("/admin-tokens/:id", handler.Revoke)
	return &adminTokenHandlerEnv{router: router, repo: repo}
}

func (e *adminTokenHandlerEnv) do(method, path string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

type adminTokenEnvelope struct {
	Code   int             `json:"code"`
	Reason string          `json:"reason"`
	Data   json.RawMessage `json:"data"`
}

func decodeEnvelope(t *testing.T, w *httptest.ResponseRecorder) adminTokenEnvelope {
	t.Helper()
	var env adminTokenEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
	return env
}

func validCreateBody() map[string]any {
	return map[string]any{
		"name":       "ops-bot",
		"scope":      "write",
		"expires_at": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339),
	}
}

func TestAdminTokenHandlerCreateReturnsPlaintextOnce(t *testing.T) {
	env := newAdminTokenHandlerEnv(t)
	body := validCreateBody()
	body["ip_allowlist"] = []string{"10.0.0.0/8", "203.0.113.9"}

	w := env.do(http.MethodPost, "/admin-tokens", body)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))

	var created struct {
		Token        string   `json:"token"`
		ID           int64    `json:"id"`
		Name         string   `json:"name"`
		TokenPrefix  string   `json:"token_prefix"`
		Scope        string   `json:"scope"`
		ActingUserID int64    `json:"acting_user_id"`
		IPAllowlist  []string `json:"ip_allowlist"`
		Status       string   `json:"status"`
	}
	require.NoError(t, json.Unmarshal(decodeEnvelope(t, w).Data, &created))
	require.True(t, strings.HasPrefix(created.Token, "s2a_"), "plaintext uses the s2a_ format")
	require.Equal(t, created.Token[:8], created.TokenPrefix)
	require.Equal(t, "ops-bot", created.Name)
	require.Equal(t, "write", created.Scope)
	require.EqualValues(t, 1, created.ActingUserID, "defaults to the administrator creating the token")
	require.Equal(t, []string{"10.0.0.0/8", "203.0.113.9/32"}, created.IPAllowlist)
	require.Equal(t, "active", created.Status)

	// Only the hash is stored, never the plaintext.
	require.Len(t, env.repo.tokens, 1)
	require.Equal(t, service.HashAdminToken(created.Token), env.repo.tokens[0].TokenHash)
	require.NotContains(t, w.Body.String(), env.repo.tokens[0].TokenHash)
	require.NotNil(t, env.repo.tokens[0].CreatedByUserID)
	require.EqualValues(t, 1, *env.repo.tokens[0].CreatedByUserID)

	// The list never shows the plaintext or the hash.
	list := env.do(http.MethodGet, "/admin-tokens", nil)
	require.Equal(t, http.StatusOK, list.Code)
	require.NotContains(t, list.Body.String(), created.Token)
	require.NotContains(t, list.Body.String(), env.repo.tokens[0].TokenHash)
	require.NotContains(t, list.Body.String(), "token_hash")
	require.Contains(t, list.Body.String(), created.TokenPrefix)
}

func TestAdminTokenHandlerCreateActingUser(t *testing.T) {
	env := newAdminTokenHandlerEnv(t)

	body := validCreateBody()
	body["acting_user_id"] = 2
	w := env.do(http.MethodPost, "/admin-tokens", body)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.EqualValues(t, 2, env.repo.tokens[0].ActingUserID)

	for name, id := range map[string]int64{"not an administrator": 3, "does not exist": 99} {
		body := validCreateBody()
		body["acting_user_id"] = id
		w := env.do(http.MethodPost, "/admin-tokens", body)
		require.Equal(t, http.StatusBadRequest, w.Code, name+": "+w.Body.String())
		require.Equal(t, "ADMIN_TOKEN_ACTING_USER_INVALID", decodeEnvelope(t, w).Reason, name)
	}
	require.Len(t, env.repo.tokens, 1, "rejected requests store nothing")
}

func TestAdminTokenHandlerCreateValidation(t *testing.T) {
	env := newAdminTokenHandlerEnv(t)

	cases := []struct {
		name       string
		mutate     func(map[string]any)
		wantReason string // empty: rejected by request binding
	}{
		{"missing name", func(b map[string]any) { delete(b, "name") }, ""},
		{"blank name", func(b map[string]any) { b["name"] = "   " }, "ADMIN_TOKEN_NAME_INVALID"},
		{"missing scope", func(b map[string]any) { delete(b, "scope") }, ""},
		{"unknown scope", func(b map[string]any) { b["scope"] = "root" }, "ADMIN_TOKEN_SCOPE_INVALID"},
		{"missing expires_at", func(b map[string]any) { delete(b, "expires_at") }, ""},
		{"expires_at in the past", func(b map[string]any) {
			b["expires_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		}, "ADMIN_TOKEN_EXPIRY_INVALID"},
		{"expires_at beyond 90 days", func(b map[string]any) {
			b["expires_at"] = time.Now().Add(91 * 24 * time.Hour).UTC().Format(time.RFC3339)
		}, "ADMIN_TOKEN_EXPIRY_TOO_LONG"},
		{"invalid CIDR", func(b map[string]any) { b["ip_allowlist"] = []string{"10.0.0.0/33"} }, "ADMIN_TOKEN_IP_ALLOWLIST_INVALID"},
		{"not an IP", func(b map[string]any) { b["ip_allowlist"] = []string{"localhost"} }, "ADMIN_TOKEN_IP_ALLOWLIST_INVALID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := validCreateBody()
			tc.mutate(body)
			w := env.do(http.MethodPost, "/admin-tokens", body)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			if tc.wantReason != "" {
				require.Equal(t, tc.wantReason, decodeEnvelope(t, w).Reason)
			}
			require.Empty(t, env.repo.tokens)
		})
	}

	t.Run("expiry exactly within 90 days is accepted", func(t *testing.T) {
		body := validCreateBody()
		body["expires_at"] = time.Now().Add(89 * 24 * time.Hour).UTC().Format(time.RFC3339)
		w := env.do(http.MethodPost, "/admin-tokens", body)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	})
}

func TestAdminTokenHandlerCreateRequiresAuthenticatedSubject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewAdminTokenHandler(service.NewAdminTokenService(&adminTokenHandlerRepo{}, nil))
	router.POST("/admin-tokens", handler.Create)

	raw, _ := json.Marshal(validCreateBody())
	req := httptest.NewRequest(http.MethodPost, "/admin-tokens", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAdminTokenHandlerRevoke(t *testing.T) {
	env := newAdminTokenHandlerEnv(t)
	created := env.do(http.MethodPost, "/admin-tokens", validCreateBody())
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	w := env.do(http.MethodDelete, "/admin-tokens/1", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var dto struct {
		Status    string     `json:"status"`
		RevokedAt *time.Time `json:"revoked_at"`
	}
	require.NoError(t, json.Unmarshal(decodeEnvelope(t, w).Data, &dto))
	require.Equal(t, "revoked", dto.Status)
	require.NotNil(t, dto.RevokedAt)

	// Soft revoke: the row is still listed, now as revoked; revoking again is harmless.
	require.Len(t, env.repo.tokens, 1)
	again := env.do(http.MethodDelete, "/admin-tokens/1", nil)
	require.Equal(t, http.StatusOK, again.Code)
	list := env.do(http.MethodGet, "/admin-tokens", nil)
	require.Contains(t, list.Body.String(), `"status":"revoked"`)

	missing := env.do(http.MethodDelete, "/admin-tokens/404", nil)
	require.Equal(t, http.StatusNotFound, missing.Code)
	require.Equal(t, "ADMIN_TOKEN_NOT_FOUND", decodeEnvelope(t, missing).Reason)

	for _, id := range []string{"abc", "0", "-3"} {
		bad := env.do(http.MethodDelete, "/admin-tokens/"+id, nil)
		require.Equal(t, http.StatusBadRequest, bad.Code, id)
	}
}
