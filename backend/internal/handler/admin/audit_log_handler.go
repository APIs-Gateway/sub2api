package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// AuditLogHandler serves the admin audit trail (audit_logs).
type AuditLogHandler struct {
	repo service.AuditLogRepository
}

// NewAuditLogHandler creates the handler.
func NewAuditLogHandler(repo service.AuditLogRepository) *AuditLogHandler {
	return &AuditLogHandler{repo: repo}
}

// List returns audit rows, newest first.
//
// GET /api/v1/admin/audit-logs
//
// Query parameters (all optional, combined with AND):
//
//	page, page_size   pagination (default 1 / 20, at most 200 per page)
//	actor_user_id     the administrator a row is attributed to
//	auth_kind         jwt | admin_token | legacy_api_key
//	token_id          rows written for one admin token
//	route             prefix of the route template, e.g. /api/v1/admin/users
//	target_type       e.g. users, accounts, orders
//	target_id         e.g. 42
//	method            HTTP method
//	start_time        RFC 3339 timestamp, or YYYY-MM-DD (start of that day, UTC)
//	end_time          RFC 3339 timestamp, or YYYY-MM-DD (end of that day, UTC)
//	status_min        lowest HTTP status code, inclusive
//	status_max        highest HTTP status code, inclusive
//
// It needs only read scope: request bodies are stored redacted and tokens
// appear only by prefix.
func (h *AuditLogHandler) List(c *gin.Context) {
	filter := &service.AuditLogFilter{}
	filter.Page, filter.PageSize = response.ParsePagination(c)

	var ok bool
	if filter.ActorUserID, ok = optionalInt64Query(c, "actor_user_id"); !ok {
		return
	}
	if filter.TokenID, ok = optionalInt64Query(c, "token_id"); !ok {
		return
	}
	if filter.StatusMin, ok = optionalStatusQuery(c, "status_min"); !ok {
		return
	}
	if filter.StatusMax, ok = optionalStatusQuery(c, "status_max"); !ok {
		return
	}
	if filter.StatusMin != nil && filter.StatusMax != nil && *filter.StatusMin > *filter.StatusMax {
		response.BadRequest(c, "status_min must not be greater than status_max")
		return
	}

	switch kind := strings.TrimSpace(c.Query("auth_kind")); kind {
	case "":
	case service.AuditAuthKindJWT, service.AuditAuthKindAdminToken, service.AuditAuthKindLegacyAPIKey:
		filter.AuthKind = kind
	default:
		response.BadRequest(c, "auth_kind must be one of jwt, admin_token, legacy_api_key")
		return
	}

	var err error
	if filter.StartTime, err = optionalTimeQuery(c, "start_time", false); err != nil {
		response.BadRequest(c, "start_time must be RFC 3339 or YYYY-MM-DD")
		return
	}
	if filter.EndTime, err = optionalTimeQuery(c, "end_time", true); err != nil {
		response.BadRequest(c, "end_time must be RFC 3339 or YYYY-MM-DD")
		return
	}
	if filter.StartTime != nil && filter.EndTime != nil && filter.StartTime.After(*filter.EndTime) {
		response.BadRequest(c, "start_time must not be after end_time")
		return
	}

	filter.RoutePrefix = c.Query("route")
	filter.TargetType = c.Query("target_type")
	filter.TargetID = c.Query("target_id")
	filter.Method = c.Query("method")

	list, err := h.repo.List(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, list.Logs, int64(list.Total), list.Page, list.PageSize)
}

// optionalInt64Query reads a positive integer query parameter. ok is false
// (and the 400 already written) when the value is present but invalid.
func optionalInt64Query(c *gin.Context, name string) (*int64, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		response.BadRequest(c, name+" must be a positive integer")
		return nil, false
	}
	return &value, true
}

// optionalStatusQuery reads an HTTP status code (100-599).
func optionalStatusQuery(c *gin.Context, name string) (*int, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 100 || value > 599 {
		response.BadRequest(c, name+" must be an HTTP status code between 100 and 599")
		return nil, false
	}
	return &value, true
}

// optionalTimeQuery parses RFC 3339, or a bare date. A bare end date means the
// end of that day so "end_time=2026-10-01" includes October 1st.
func optionalTimeQuery(c *gin.Context, name string, endOfDay bool) (*time.Time, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return &parsed, nil
	}
	day, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, err
	}
	if endOfDay {
		day = day.Add(24*time.Hour - time.Nanosecond)
	}
	return &day, nil
}
