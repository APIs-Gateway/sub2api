package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// Context keys describing *how* an admin request authenticated. They are set
// by the admin auth middleware and read by scope enforcement and by the audit
// recorder. The pre-existing "auth_method" key (jwt / admin_api_key) and the
// ContextKeyUser subject are still set exactly as before.
const (
	// ContextKeyAdminAuthKind is one of service.AuditAuthKindJWT,
	// service.AuditAuthKindAdminToken or service.AuditAuthKindLegacyAPIKey.
	ContextKeyAdminAuthKind ContextKey = "admin_auth_kind"
	// ContextKeyAdminTokenID is the admin_tokens.id (int64) of the presented
	// admin token. It is also set when a token was identified but rejected.
	ContextKeyAdminTokenID ContextKey = "admin_token_id"
	// ContextKeyAdminTokenScope is the effective scope of the credential:
	// the token's own scope, or "danger" for the legacy global API key.
	ContextKeyAdminTokenScope ContextKey = "admin_token_scope"
	// ContextKeyAdminActorUserID is the administrator (users.id, int64) the
	// request is attributed to. For an admin token that was identified but
	// rejected it is the token's acting user.
	ContextKeyAdminActorUserID ContextKey = "admin_actor_user_id"
	// ContextKeyAdminActorLabel is a human readable actor, e.g. "jwt:a@b.c",
	// "token:ops-bot#7" or "legacy_api_key".
	ContextKeyAdminActorLabel ContextKey = "admin_actor_label"
	// ContextKeyAdminActorEmail is the email of the administrator the request
	// is attributed to.
	ContextKeyAdminActorEmail ContextKey = "admin_actor_email"
	// ContextKeyAdminCredentialMasked is a masked form of the credential
	// (never the credential itself).
	ContextKeyAdminCredentialMasked ContextKey = "admin_credential_masked"
	// ContextKeyAdminRejectCode records the error code when the admin
	// middleware itself rejected an identified caller.
	ContextKeyAdminRejectCode ContextKey = "admin_reject_code"
)

// AdminAuthKindFromContext returns how the current admin request
// authenticated, or "" if it has not (yet).
func AdminAuthKindFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return c.GetString(string(ContextKeyAdminAuthKind))
}

// AdminTokenIDFromContext returns the admin token id for token-authenticated
// requests.
func AdminTokenIDFromContext(c *gin.Context) (int64, bool) {
	if c == nil {
		return 0, false
	}
	value, exists := c.Get(string(ContextKeyAdminTokenID))
	if !exists {
		return 0, false
	}
	id, ok := value.(int64)
	return id, ok
}

// AdminScopeFromContext returns the effective scope of the authenticated
// admin credential. Interactive (JWT) administrators are unrestricted and
// report "danger".
func AdminScopeFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if scope := c.GetString(string(ContextKeyAdminTokenScope)); scope != "" {
		return scope
	}
	if AdminAuthKindFromContext(c) == service.AuditAuthKindJWT {
		return service.AdminTokenScopeDanger
	}
	return ""
}

// setAdminIdentity records the identity of the caller for scope checks and
// audit. It deliberately does not touch ContextKeyUser, so calling it for a
// credential that is later rejected does not authenticate anyone.
func setAdminIdentity(c *gin.Context, kind string, userID int64, label, email, maskedCredential string) {
	c.Set(string(ContextKeyAdminAuthKind), kind)
	c.Set(string(ContextKeyAdminActorUserID), userID)
	c.Set(string(ContextKeyAdminActorLabel), label)
	if email != "" {
		c.Set(string(ContextKeyAdminActorEmail), email)
	}
	if maskedCredential != "" {
		c.Set(string(ContextKeyAdminCredentialMasked), maskedCredential)
	}
}

// abortAdminAuth rejects the request with the project's flat error shape and
// remembers the code so the audit recorder can include it.
func abortAdminAuth(c *gin.Context, status int, code, message string) {
	c.Set(string(ContextKeyAdminRejectCode), code)
	AbortWithError(c, status, code, message)
}
