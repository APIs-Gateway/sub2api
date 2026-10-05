package middleware

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// Admin token scope model
// -----------------------
//
// Every route behind adminAuth needs one of three scopes, and an admin token
// must hold at least that scope (read < write < danger):
//
//   - read:   GET (and HEAD/OPTIONS) routes.
//   - write:  every other method, unless the route is on the danger list.
//   - danger: the routes in adminDangerRules below.
//
// Interactive administrators (JWT) and the legacy global admin API key are not
// subject to scopes; the legacy key is treated as having "danger" scope.
//
// The danger list is an explicit allow-by-name list, so a new route is "write"
// (or "read" for GET) until someone adds it. The route coverage tests in
// internal/server/routes (admin_token_scope_coverage_test.go) exist to make
// that hard to forget: they fail when a route whose name looks sensitive
// (refund, balance, delete, pricing, settings, ...) is neither on the danger
// list nor on the reviewed-as-write/read allowlists below, and when an entry
// on any of the lists no longer matches a real route.
//
// Paths are gin route templates relative to /api/v1/admin, exactly as
// reported by gin.Context.FullPath() minus that prefix.

const adminAPIPrefix = "/api/v1/admin"

// AdminRouteRule names one route and why it is classified the way it is.
type AdminRouteRule struct {
	Method string
	// Path is the gin route template relative to /api/v1/admin.
	Path string
	// Reason is a short human readable justification (also used in tests to
	// make sure nobody adds an entry without thinking about it).
	Reason string
}

// Key returns "METHOD /api/v1/admin/<path>", the form used by lookups and by
// gin.RouteInfo (Method + " " + Path).
func (r AdminRouteRule) Key() string {
	return strings.ToUpper(r.Method) + " " + adminAPIPrefix + r.Path
}

// adminDangerRules is the explicit list of routes that need "danger" scope.
//
// Principle: a route is danger when a mistake or abuse moves money, destroys
// data, widens access, changes what customers are charged, rewrites global
// configuration, or reveals a usable secret. Everything was classified by
// reading the registered routes (routes/admin.go, routes/payment.go) and,
// where the name was ambiguous, the handler.
var adminDangerRules = []AdminRouteRule{
	// --- Refunds ---------------------------------------------------------
	{http.MethodPost, "/payment/orders/:id/refund", "refund"},
	{http.MethodPost, "/payment/orders/:id/refund/query", "refund (queries the provider and finalises the refund)"},
	{http.MethodPost, "/payment/orders/:id/refund/resolve", "refund (resolves a pending refund)"},

	// --- Balance, compensation, entitlement grants -----------------------
	{http.MethodPost, "/users/:id/balance", "balance adjustment"},
	{http.MethodPost, "/users/:id/platform-quotas/reset", "compensation (resets a user's usage window)"},
	{http.MethodPost, "/redeem-codes/create-and-redeem", "balance adjustment"},
	{http.MethodPost, "/redeem-codes/generate", "mints redeemable value"},
	{http.MethodPost, "/redeem-codes/batch-update", "edits redeemable value in bulk"},
	{http.MethodPost, "/redeem-codes/:id/expire", "voids a redeem code"},
	{http.MethodPost, "/subscriptions/assign", "grants a subscription (compensation)"},
	{http.MethodPost, "/subscriptions/bulk-assign", "grants subscriptions in bulk (compensation)"},
	{http.MethodPost, "/subscriptions/:id/extend", "extends a subscription (compensation)"},
	{http.MethodPost, "/subscriptions/:id/reset-quota", "resets subscription quota (compensation)"},
	{http.MethodDelete, "/subscriptions/:id", "subscription card void (revoke)"},
	{http.MethodPost, "/payment/orders/:id/retry", "re-runs order fulfillment, which credits balance/subscription"},
	{http.MethodPost, "/points/withdrawals/:id/approve", "payout"},
	{http.MethodPost, "/points/withdrawals/:id/reject", "payout"},
	{http.MethodPost, "/promo-codes", "creates free-balance codes"},
	{http.MethodPut, "/promo-codes/:id", "edits free-balance codes"},
	{http.MethodDelete, "/promo-codes/:id", "delete"},

	// --- Users: identity, role, credentials, deletion --------------------
	{http.MethodPost, "/users", "creates users (may set role, balance, password)"},
	{http.MethodPut, "/users/:id", "updates role, balance, password, status and per-user group rates"},
	{http.MethodDelete, "/users/:id", "delete user"},
	{http.MethodPost, "/users/:id/auth-identities", "binds a login identity to a user (account takeover)"},
	{http.MethodPost, "/users/:id/replace-group", "moves a user between groups (changes what they are charged)"},
	{http.MethodPost, "/users/batch-concurrency", "changes limits for many users"},
	{http.MethodPost, "/users/batch-limits", "changes limits for many users"},
	{http.MethodPut, "/users/:id/platform-quotas", "changes a user's usage quotas"},
	{http.MethodPut, "/api-keys/:id", "moves a customer's API key to another group (changes pricing)"},
	{http.MethodPut, "/api-keys/:id/hidden-fallback-chain", "rewrites the hidden fallback chain of a customer's key (changes which group it is billed against)"},
	{http.MethodDelete, "/api-keys/:id/hidden-fallback-chain", "clears the hidden fallback chain of a customer's key (changes which group it is billed against)"},
	{http.MethodPut, "/key-editor/reference-models", "changes the reference model used for the prices customers see in the fallback editor"},

	// --- Groups and accounts: deletion and group pricing -----------------
	{http.MethodDelete, "/groups/:id", "delete group"},
	{http.MethodPut, "/groups/:id", "group pricing (rate multiplier, prices) and access rules"},
	{http.MethodPut, "/groups/:id/rate-multipliers", "pricing (per-user group multipliers)"},
	{http.MethodDelete, "/groups/:id/rate-multipliers", "pricing (clears per-user group multipliers)"},
	{http.MethodDelete, "/accounts/:id", "delete account"},
	{http.MethodPut, "/accounts/:id", "edits an account, including its upstream credentials"},
	{http.MethodPost, "/accounts/bulk-update", "edits many accounts at once, including credentials"},
	{http.MethodPost, "/accounts/batch-update-credentials", "overwrites upstream credentials of many accounts"},
	{http.MethodPost, "/accounts/:id/apply-oauth-credentials", "overwrites an account's upstream credentials"},
	{http.MethodPost, "/accounts/:id/reauth/codex-session", "overwrites an account's upstream credentials"},
	{http.MethodPost, "/accounts/import/codex-session", "imports Codex sessions; update_existing overwrites credentials"},
	{http.MethodPost, "/accounts/data", "imports accounts (credentials) and can overwrite existing ones"},

	// --- Pricing, rebates, payment configuration -------------------------
	{http.MethodPost, "/channels", "channel pricing / routing"},
	{http.MethodPut, "/channels/:id", "channel pricing / routing"},
	{http.MethodPut, "/pricing-matrix/groups/:id/stage", "pricing stage switch (pricing.stage_switch, touches price)"},
	{http.MethodDelete, "/channels/:id", "channel pricing / routing (delete)"},
	{http.MethodPost, "/payment/plans", "subscription plan pricing"},
	{http.MethodPut, "/payment/plans/:id", "subscription plan pricing"},
	{http.MethodDelete, "/payment/plans/:id", "subscription plan pricing (delete)"},
	{http.MethodPut, "/payment/config", "payment configuration"},
	{http.MethodPost, "/payment/providers", "payment provider credentials / routing"},
	{http.MethodPut, "/payment/providers/:id", "payment provider credentials / routing"},
	{http.MethodDelete, "/payment/providers/:id", "payment provider (delete)"},
	{http.MethodPut, "/affiliates/cashback/settings", "rebate settings"},
	{http.MethodPost, "/affiliates/users/batch-rate", "rebate rates"},
	{http.MethodPut, "/affiliates/users/:user_id", "rebate rates"},
	{http.MethodDelete, "/affiliates/users/:user_id", "rebate rates (delete)"},
	{http.MethodPut, "/points/settings", "points / payout settings"},

	// --- System settings writes ------------------------------------------
	{http.MethodPut, "/settings", "system settings"},
	{http.MethodPut, "/settings/pricing-display", "system settings"},
	{http.MethodPut, "/settings/email-templates/:event/:locale", "system settings"},
	{http.MethodPost, "/settings/email-templates/:event/:locale/restore-official", "system settings"},
	{http.MethodPut, "/settings/overload-cooldown", "system settings"},
	{http.MethodPut, "/settings/rate-limit-429-cooldown", "system settings"},
	{http.MethodPut, "/settings/panel-rate-limit", "system settings"},
	{http.MethodPut, "/settings/stream-timeout", "system settings"},
	{http.MethodPut, "/settings/rectifier", "system settings"},
	{http.MethodPut, "/settings/beta-policy", "system settings"},
	{http.MethodPut, "/settings/checkin", "system settings"},
	{http.MethodPut, "/settings/public-benefit", "system settings"},
	{http.MethodPut, "/settings/web-search-emulation", "system settings"},
	{http.MethodPost, "/settings/web-search-emulation/reset-usage", "system settings"},
	{http.MethodPut, "/ops/advanced-settings", "system settings (data retention / cleanup)"},
	{http.MethodPut, "/risk-control/config", "security configuration (content moderation)"},
	{http.MethodDelete, "/risk-control/hashes/all", "bulk delete"},
	{http.MethodPut, "/data-management/config", "system settings (data management)"},
	{http.MethodPost, "/compliance/accept", "legal acknowledgement that must come from a human administrator"},

	// --- Admin credentials and admin tokens themselves -------------------
	{http.MethodPost, "/settings/admin-api-key/regenerate", "admin credential (global admin API key)"},
	{http.MethodDelete, "/settings/admin-api-key", "admin credential (global admin API key)"},
	{http.MethodGet, "/admin-tokens", "admin tokens (management is JWT only)"},
	{http.MethodPost, "/admin-tokens", "admin tokens (management is JWT only)"},
	{http.MethodDelete, "/admin-tokens/:id", "admin tokens (management is JWT only)"},

	// --- Bulk and irreversible deletion ----------------------------------
	{http.MethodPost, "/proxies/batch-delete", "bulk delete"},
	{http.MethodPost, "/redeem-codes/batch-delete", "bulk delete"},
	{http.MethodDelete, "/redeem-codes/:id", "delete (voids a redeem code)"},
	{http.MethodPost, "/usage/cleanup-tasks", "bulk-deletes usage records (billing data)"},
	{http.MethodPost, "/ops/system-logs/cleanup", "bulk-deletes system logs"},
	{http.MethodDelete, "/backups/:id", "delete backup"},

	// --- Backups, storage destinations, system lifecycle -----------------
	{http.MethodPost, "/backups/:id/restore", "restores the database from a backup"},
	{http.MethodPut, "/backups/s3-config", "backup storage credentials"},
	{http.MethodPost, "/data-management/s3/profiles", "storage credentials"},
	{http.MethodPut, "/data-management/s3/profiles/:profile_id", "storage credentials"},
	{http.MethodDelete, "/data-management/s3/profiles/:profile_id", "storage credentials (delete)"},
	{http.MethodPost, "/data-management/s3/profiles/:profile_id/activate", "switches the active storage destination"},
	{http.MethodPost, "/data-management/sources/:source_type/profiles", "data source credentials"},
	{http.MethodPut, "/data-management/sources/:source_type/profiles/:profile_id", "data source credentials"},
	{http.MethodDelete, "/data-management/sources/:source_type/profiles/:profile_id", "data source credentials (delete)"},
	{http.MethodPost, "/data-management/sources/:source_type/profiles/:profile_id/activate", "switches the active data source"},
	{http.MethodPost, "/system/update", "replaces the running binary"},
	{http.MethodPost, "/system/rollback", "replaces the running binary"},
	{http.MethodPost, "/system/restart", "restarts the service"},

	// --- Reads that reveal a usable secret or raw user content -----------
	// A "read" token must not be able to exfiltrate credentials, so these
	// GET routes require danger scope.
	{http.MethodGet, "/accounts/data", "exports upstream account credentials in clear text"},
	{http.MethodGet, "/proxies/data", "exports proxy passwords"},
	{http.MethodGet, "/redeem-codes", "lists redeem codes (bearer value)"},
	{http.MethodGet, "/redeem-codes/:id", "shows a redeem code (bearer value)"},
	{http.MethodGet, "/redeem-codes/export", "exports redeem codes (bearer value)"},
	{http.MethodGet, "/users/:id/api-keys", "lists customers' full API keys"},
	{http.MethodGet, "/groups/:id/api-keys", "lists customers' full API keys"},
	{http.MethodGet, "/backups/:id/download-url", "presigned download of a full database backup"},
	{http.MethodGet, "/prompt-audit/events/:id", "may include the full prompt text of a user request"},
}

// adminReviewedWriteRules lists non-GET routes whose name looks sensitive
// (they contain refund/compensat/balance/delete/price/pricing/rate/settings,
// or use DELETE) but were looked at and deliberately left at "write" scope.
// The route coverage test requires every such route to be either on the
// danger list or here, so adding a new sensitive-looking route forces a
// decision.
var adminReviewedWriteRules = []AdminRouteRule{
	// DELETE of low-impact, easily re-created configuration objects.
	{http.MethodDelete, "/accounts/:id/temp-unschedulable", "clears a temporary scheduling block"},
	{http.MethodDelete, "/announcements/:id", "announcement"},
	{http.MethodDelete, "/channel-monitor-templates/:id", "monitoring template"},
	{http.MethodDelete, "/channel-monitors/:id", "monitoring configuration"},
	{http.MethodDelete, "/error-passthrough-rules/:id", "error rewrite rule"},
	{http.MethodDelete, "/groups/:id/rpm-overrides", "clears RPM overrides (rate limits, not prices)"},
	{http.MethodDelete, "/ops/alert-rules/:id", "alert rule"},
	{http.MethodDelete, "/proxies/:id", "single proxy; refused by the service while in use"},
	{http.MethodDelete, "/risk-control/hashes", "removes one flagged hash"},
	{http.MethodDelete, "/scheduled-test-plans/:id", "test plan"},
	{http.MethodDelete, "/tls-fingerprint-profiles/:id", "fingerprint template"},
	{http.MethodDelete, "/user-attributes/:id", "attribute definition"},

	// batch / bulk / import / group in the name, but low impact.
	{http.MethodPut, "/groups/sort-order", "display order of groups"},
	{http.MethodPost, "/groups", "creates a group; nobody is charged through it until it is assigned"},
	{http.MethodPost, "/groups/:id/duplicate", "copies a group; nobody is charged through it until it is assigned"},
	{http.MethodPut, "/groups/:id/rpm-overrides", "per-user request-rate overrides (limits, not prices)"},
	{http.MethodPost, "/accounts/batch", "creates accounts (no existing account is changed)"},
	{http.MethodPost, "/accounts/batch-refresh", "refreshes upstream tokens"},
	{http.MethodPost, "/accounts/batch-refresh-tier", "re-reads the subscription tier from upstream"},
	{http.MethodPost, "/accounts/batch-clear-error", "clears error marks"},
	{http.MethodPost, "/accounts/today-stats/batch", "read-only statistics (POST only to carry a long id list)"},
	{http.MethodPost, "/accounts/upstream-billing-probe/batch", "runs probes; persists nothing sensitive"},
	{http.MethodPost, "/proxies/batch", "creates proxies (no existing proxy is changed)"},
	{http.MethodPost, "/proxies/data", "imports proxies"},
	{http.MethodPost, "/user-attributes/batch", "read-only lookup (POST only to carry a long id list)"},

	// "rate" in the name (rate limits, "generate"), but not about money.
	{http.MethodPost, "/accounts/:id/clear-rate-limit", "clears an upstream rate-limit mark on one account"},
	{http.MethodPost, "/accounts/generate-auth-url", "OAuth helper that only returns a URL ('rate' is part of 'generate')"},
	{http.MethodPost, "/accounts/generate-setup-token-url", "OAuth helper that only returns a URL ('rate' is part of 'generate')"},
	{http.MethodPost, "/openai/generate-auth-url", "OAuth helper that only returns a URL ('rate' is part of 'generate')"},

	// "settings" in the name, but they do not persist system settings.
	{http.MethodPut, "/accounts/upstream-billing-probe/settings", "feature toggle of the upstream billing probe"},
	{http.MethodPut, "/ops/settings/metric-thresholds", "alerting thresholds only; no effect on users or billing"},
	{http.MethodPost, "/settings/test-smtp", "connection test; persists nothing"},
	{http.MethodPost, "/settings/send-test-email", "sends one test email; persists nothing"},
	{http.MethodPost, "/settings/email-template-preview", "renders a preview; persists nothing"},
	{http.MethodPost, "/settings/web-search-emulation/test", "connection test; persists nothing"},
}

// adminReviewedReadRules lists GET routes that look like they could expose a
// secret (export/download/api-keys/redeem-codes/.../data in the name) but
// were reviewed and return only non-secret data.
var adminReviewedReadRules = []AdminRouteRule{
	{http.MethodGet, "/dashboard/api-keys-trend", "aggregate usage numbers per API key id/name"},
	{http.MethodGet, "/usage/search-api-keys", "id and name only; never the key"},
	{http.MethodGet, "/redeem-codes/stats", "counters only"},
	{http.MethodGet, "/api-keys/:id/fallback-chain", "group ids, notes and dry-run only; never the key"},
	{http.MethodGet, "/pricing/quote", "read-only price quote; no secrets"},
	{http.MethodGet, "/pricing/quote-batch", "read-only batch price quotes and official reference prices; no secrets"},
}

// adminDangerRouteSet is the lookup form of adminDangerRules. The reviewed
// lists are only consumed by the route coverage tests (through the accessors
// below), so they have no lookup set.
var adminDangerRouteSet = buildAdminRouteSet(adminDangerRules)

func buildAdminRouteSet(rules []AdminRouteRule) map[string]string {
	set := make(map[string]string, len(rules))
	for _, rule := range rules {
		set[rule.Key()] = rule.Reason
	}
	return set
}

func copyAdminRules(rules []AdminRouteRule) []AdminRouteRule {
	return append([]AdminRouteRule(nil), rules...)
}

// AdminDangerRules returns a copy of the explicit danger list.
func AdminDangerRules() []AdminRouteRule { return copyAdminRules(adminDangerRules) }

// AdminReviewedWriteRules returns a copy of the "reviewed, stays write" list.
func AdminReviewedWriteRules() []AdminRouteRule { return copyAdminRules(adminReviewedWriteRules) }

// AdminReviewedReadRules returns a copy of the "reviewed, stays read" list.
func AdminReviewedReadRules() []AdminRouteRule { return copyAdminRules(adminReviewedReadRules) }

// AdminRouteRequiredScope returns the scope an admin token needs for a route.
// fullPath is gin.Context.FullPath(). An empty fullPath (an unmatched route
// should never reach the admin middleware) fails closed to "danger".
func AdminRouteRequiredScope(method, fullPath string) string {
	if fullPath == "" {
		return service.AdminTokenScopeDanger
	}
	method = strings.ToUpper(method)
	if _, ok := adminDangerRouteSet[method+" "+fullPath]; ok {
		return service.AdminTokenScopeDanger
	}
	if isAdminReadOnlyMethod(method) {
		return service.AdminTokenScopeRead
	}
	return service.AdminTokenScopeWrite
}

// isAdminReadOnlyMethod reports whether a method is safe (does not mutate).
func isAdminReadOnlyMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// enforceAdminTokenScope returns false (and aborts with 403
// ADMIN_TOKEN_SCOPE_INSUFFICIENT) when the token's scope is lower than what
// the matched route needs.
func enforceAdminTokenScope(c *gin.Context, tokenScope string) bool {
	required := AdminRouteRequiredScope(c.Request.Method, c.FullPath())
	if service.AdminTokenScopeRank(tokenScope) >= service.AdminTokenScopeRank(required) {
		return true
	}
	abortAdminAuth(c, http.StatusForbidden, "ADMIN_TOKEN_SCOPE_INSUFFICIENT",
		fmt.Sprintf("admin token scope %q is not sufficient for this endpoint, which requires %q", tokenScope, required))
	return false
}

// RequireAdminJWT restricts a route (group) to interactive administrators
// authenticated with a JWT. Machine credentials, including admin tokens of
// every scope and the legacy global admin API key, are rejected so they can
// not mint or revoke credentials and thereby widen their own access.
func RequireAdminJWT() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetString("auth_method") != service.AuditAuthMethodJWT {
			abortAdminAuth(c, http.StatusForbidden, "ADMIN_TOKEN_MANAGEMENT_JWT_ONLY",
				"this endpoint can only be called by a signed-in administrator (JWT); API keys and admin tokens are not accepted")
			return
		}
		c.Next()
	}
}
