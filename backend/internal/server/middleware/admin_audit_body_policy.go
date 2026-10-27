package middleware

import (
	"net/http"
	"strings"
)

// Request bodies that are never stored
// ------------------------------------
//
// Redacting a body by key name is not enough for routes whose whole purpose is
// to submit credentials: a pasted Codex auth.json arrives as one string,
// account credentials are edited as free-form objects, OAuth codes and
// cookies are exchanged for tokens. Those routes therefore do not store a
// body at all; the audit row only says "body_omitted" and how many bytes were
// sent. Reading the audit trail (read scope) can then never reveal a secret,
// whatever shape the body had.
//
// A route is "body omitted" when it is on adminAuditBodyOmittedRules, or when
// its template contains one of adminAuditBodyOmittedKeywords. The keywords
// exist so that a newly added OAuth / import / credential route is covered
// without anybody remembering this file; they only ever remove information
// from the trail, never add it. The route coverage tests in
// internal/server/routes check that every entry of the explicit list is a real
// route.

// adminAuditBodyOmittedRules lists routes (relative to /api/v1/admin) whose
// body carries credentials or other secrets and is not caught by a keyword.
var adminAuditBodyOmittedRules = []AdminRouteRule{
	// --- Upstream accounts ------------------------------------------------
	{http.MethodPost, "/accounts", "account credentials"},
	{http.MethodPut, "/accounts/:id", "account credentials"},
	{http.MethodPost, "/accounts/batch", "account credentials"},
	{http.MethodPost, "/accounts/data", "account export file: credentials in clear text"},
	{http.MethodPost, "/accounts/bulk-update", "may set credentials on many accounts"},
	{http.MethodPost, "/accounts/import/codex-session", "pasted Codex auth.json (access/refresh/id tokens)"},
	{http.MethodPost, "/accounts/:id/reauth/codex-session", "pasted Codex auth.json (access/refresh/id tokens)"},
	{http.MethodPost, "/accounts/:id/apply-oauth-credentials", "OAuth credentials"},
	{http.MethodPost, "/accounts/batch-update-credentials", "credentials"},
	{http.MethodPost, "/accounts/sync/crs", "CRS admin credentials"},
	{http.MethodPost, "/accounts/sync/crs/preview", "CRS admin credentials"},
	{http.MethodPost, "/accounts/exchange-code", "OAuth authorization code"},
	{http.MethodPost, "/accounts/exchange-setup-token-code", "OAuth authorization code"},
	{http.MethodPost, "/accounts/cookie-auth", "session cookie"},
	{http.MethodPost, "/accounts/setup-token-cookie-auth", "session cookie"},
	{http.MethodPost, "/openai/exchange-code", "OAuth authorization code"},
	{http.MethodPost, "/openai/refresh-token", "refresh token"},
	{http.MethodPost, "/openai/create-from-oauth", "OAuth session / tokens"},
	{http.MethodPost, "/openai/create-from-codex-pat", "personal access token"},
	{http.MethodPost, "/gemini/oauth/exchange-code", "OAuth authorization code"},
	{http.MethodPost, "/antigravity/oauth/exchange-code", "OAuth authorization code"},
	{http.MethodPost, "/antigravity/oauth/refresh-token", "refresh token"},
	{http.MethodPost, "/grok/oauth/exchange-code", "OAuth authorization code"},
	{http.MethodPost, "/grok/oauth/refresh-token", "refresh token"},
	{http.MethodPost, "/grok/oauth/create-from-oauth", "OAuth session / tokens"},

	// --- Proxies (passwords) ---------------------------------------------
	{http.MethodPost, "/proxies", "proxy password"},
	{http.MethodPut, "/proxies/:id", "proxy password"},
	{http.MethodPost, "/proxies/batch", "proxy passwords"},
	{http.MethodPost, "/proxies/data", "proxy export file: passwords in clear text"},

	// --- Users (passwords, identities) -----------------------------------
	{http.MethodPost, "/users", "initial password"},
	{http.MethodPut, "/users/:id", "password"},
	{http.MethodPost, "/users/:id/auth-identities", "login identity binding"},

	// --- Settings and integrations (SMTP, payment, storage, API keys) ----
	{http.MethodPut, "/settings", "SMTP password and other secrets"},
	{http.MethodPost, "/settings/test-smtp", "SMTP password"},
	{http.MethodPost, "/settings/send-test-email", "SMTP password"},
	{http.MethodPut, "/settings/web-search-emulation", "search provider API keys"},
	{http.MethodPost, "/settings/web-search-emulation/test", "search provider API keys"},
	{http.MethodPut, "/payment/config", "payment configuration secrets"},
	{http.MethodPut, "/data-management/config", "storage configuration secrets"},
	{http.MethodPost, "/data-management/s3/test", "S3 credentials"},
	{http.MethodPut, "/backups/s3-config", "S3 credentials"},
	{http.MethodPost, "/backups/s3-config/test", "S3 credentials"},
	{http.MethodPut, "/risk-control/config", "moderation provider API keys"},
	{http.MethodPost, "/risk-control/api-keys/test", "moderation provider API keys"},
	{http.MethodPut, "/ops/email-notification/config", "notification credentials"},
}

// adminAuditBodyOmittedKeywords are matched, case-insensitively, against the
// route template. Any state-changing route containing one is body omitted.
var adminAuditBodyOmittedKeywords = []string{
	"oauth",
	"exchange-code",
	"refresh-token",
	"credential",
	"cookie",
	"codex-session",
	"reauth",
	"import",
	"create-from-",
	// storage / data source profiles (S3 keys)
	"/profiles",
	// payment provider credentials
	"/providers",
	"s3-config",
}

var adminAuditBodyOmittedSet = buildAdminRouteSet(adminAuditBodyOmittedRules)

// AdminAuditBodyOmittedRules returns a copy of the explicit list.
func AdminAuditBodyOmittedRules() []AdminRouteRule {
	return copyAdminRules(adminAuditBodyOmittedRules)
}

// AdminAuditBodyOmitted reports whether the body of a state-changing request
// to the route (gin.Context.FullPath()) is left out of the audit trail.
func AdminAuditBodyOmitted(method, route string) bool { return adminAuditBodyOmitted(method, route) }

// adminAuditBodyOmitted reports whether the request body of a route must not
// be stored. route is gin.Context.FullPath(); an empty route fails closed.
func adminAuditBodyOmitted(method, route string) bool {
	if route == "" {
		return true
	}
	if _, ok := adminAuditBodyOmittedSet[strings.ToUpper(method)+" "+route]; ok {
		return true
	}
	lower := strings.ToLower(strings.TrimPrefix(route, adminAPIPrefix))
	for _, keyword := range adminAuditBodyOmittedKeywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}
