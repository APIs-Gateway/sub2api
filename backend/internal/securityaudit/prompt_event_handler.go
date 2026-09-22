package securityaudit

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// PromptEventAdminHandler exposes the event read surface. List responses stay
// redacted; GetEvent may additionally include the unredacted full_prompt on
// an authenticated single-event detail read, but only when store_full_prompts
// is enabled and the repository populated it (see EventRepository.GetEvent).
// Config mutation and prompt probing belong to separate issues. Event
// retention is deliberately limited to the two-step preview/confirm contract
// below; the #485 one-click convenience layer remains out of scope.
type PromptEventAdminHandler struct {
	repository     EventRepository
	clock          Clock
	confirmationMu sync.Mutex
	confirmations  map[string]deleteConfirmation
}

func NewPromptEventAdminHandler(repository EventRepository) *PromptEventAdminHandler {
	return &PromptEventAdminHandler{
		repository:    repository,
		clock:         realClock{},
		confirmations: make(map[string]deleteConfirmation),
	}
}

func (h *PromptEventAdminHandler) ListEvents(c *gin.Context) {
	page, err := positiveIntQuery(c, "page", 1, 0)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	pageSize, err := positiveIntQuery(c, "page_size", 20, 100)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	filter, err := eventFilterFromQuery(c)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if h == nil || h.repository == nil {
		response.ErrorFrom(c, errors.New("prompt audit event repository unavailable"))
		return
	}
	result, err := h.repository.ListEvents(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *PromptEventAdminHandler) GetEvent(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, infraerrors.BadRequest("prompt_audit_invalid_event_id", "事件 ID 无效"))
		return
	}
	if h == nil || h.repository == nil {
		response.ErrorFrom(c, errors.New("prompt audit event repository unavailable"))
		return
	}
	event, err := h.repository.GetEvent(c.Request.Context(), id)
	if errors.Is(err, ErrEventNotFound) {
		response.ErrorFrom(c, infraerrors.NotFound("prompt_audit_event_not_found", "提示词审计事件不存在"))
		return
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, event)
}

type deleteConfirmation struct {
	filterHash    string
	snapshotMaxID int64
	adminID       int64
	expiresAt     time.Time
}

type DeleteByFilterRequest struct {
	Filter            EventFilter `json:"filter"`
	SnapshotMaxID     int64       `json:"snapshot_max_id"`
	FilterHash        string      `json:"filter_hash"`
	ConfirmationToken string      `json:"confirmation_token"`
	Confirm           bool        `json:"confirm"`
}

func (h *PromptEventAdminHandler) DeletePreview(c *gin.Context) {
	var filter EventFilter
	if err := c.ShouldBindJSON(&filter); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("prompt_audit_delete_preview_invalid", "删除预览筛选无效"))
		return
	}
	if h == nil || h.repository == nil {
		response.ErrorFrom(c, errors.New("prompt audit event repository unavailable"))
		return
	}
	preview, err := h.repository.PreviewDelete(c.Request.Context(), filter)
	if errors.Is(err, ErrInvalidDeleteFilter) || errors.Is(err, ErrDeleteNoMatches) {
		response.ErrorFrom(c, infraerrors.BadRequest("prompt_audit_delete_preview_invalid", "删除预览筛选无效或没有匹配事件"))
		return
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	adminID, ok := adminID(c)
	if !ok {
		response.Unauthorized(c, "Admin authentication required")
		return
	}
	if err := h.issueConfirmation(preview, adminID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, preview)
}

func (h *PromptEventAdminHandler) DeleteByFilter(c *gin.Context) {
	var request DeleteByFilterRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("prompt_audit_delete_confirmation_invalid", "删除确认无效或已过期"))
		return
	}
	if h == nil || h.repository == nil {
		response.ErrorFrom(c, errors.New("prompt audit event repository unavailable"))
		return
	}
	adminID, ok := adminID(c)
	if !ok {
		response.Unauthorized(c, "Admin authentication required")
		return
	}
	if err := h.consumeConfirmation(request, adminID); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("prompt_audit_delete_confirmation_invalid", "删除确认无效或已过期"))
		return
	}
	result, err := h.repository.DeleteEventsByFilter(c.Request.Context(), request.Filter, request.SnapshotMaxID, 200)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *PromptEventAdminHandler) issueConfirmation(preview *DeletePreview, adminID int64) error {
	if h == nil || preview == nil || preview.MatchedCount <= 0 || preview.SnapshotMaxID <= 0 {
		return errors.New("prompt audit delete preview unavailable")
	}
	if preview.FilterHash == "" || preview.FilterHash != FilterHash(preview.FilterSummary, preview.SnapshotMaxID) {
		return errors.New("prompt audit delete preview binding invalid")
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return err
	}
	now := h.now()
	token := base64.RawURLEncoding.EncodeToString(bytes)
	confirmation := deleteConfirmation{
		filterHash:    preview.FilterHash,
		snapshotMaxID: preview.SnapshotMaxID,
		adminID:       adminID,
		expiresAt:     now.Add(5 * time.Minute),
	}
	h.confirmationMu.Lock()
	defer h.confirmationMu.Unlock()
	for existing, value := range h.confirmations {
		if !now.Before(value.expiresAt) {
			delete(h.confirmations, existing)
		}
	}
	h.confirmations[token] = confirmation
	preview.ConfirmationToken, preview.ExpiresAt = token, confirmation.expiresAt
	return nil
}

func (h *PromptEventAdminHandler) consumeConfirmation(request DeleteByFilterRequest, adminID int64) error {
	if !request.Confirm || request.SnapshotMaxID <= 0 || strings.TrimSpace(request.FilterHash) == "" || strings.TrimSpace(request.ConfirmationToken) == "" {
		return errors.New("prompt audit confirmation token invalid")
	}
	if err := validateDeleteFilter(request.Filter); err != nil {
		return err
	}
	h.confirmationMu.Lock()
	defer h.confirmationMu.Unlock()
	confirmation, found := h.confirmations[request.ConfirmationToken]
	if !found || !h.now().Before(confirmation.expiresAt) || confirmation.adminID != adminID ||
		confirmation.snapshotMaxID != request.SnapshotMaxID || confirmation.filterHash != request.FilterHash ||
		confirmation.filterHash != FilterHash(request.Filter, request.SnapshotMaxID) {
		return errors.New("prompt audit confirmation token does not match deletion request")
	}
	// Consume before I/O so concurrent requests cannot replay a valid token.
	delete(h.confirmations, request.ConfirmationToken)
	return nil
}

func (h *PromptEventAdminHandler) now() time.Time {
	if h != nil && h.clock != nil {
		return h.clock.Now()
	}
	return realClock{}.Now()
}

// adminID returns the authenticated administrator identity installed by the
// admin route middleware. Callers reject absent and invalid identities so the
// destructive confirmation contract remains fail-closed if route wiring
// changes.
func adminID(c *gin.Context) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		return 0, false
	}
	return subject.UserID, true
}

func eventFilterFromQuery(c *gin.Context) (EventFilter, error) {
	groupID, err := optionalPositiveInt64Query(c, "group_id")
	if err != nil {
		return EventFilter{}, err
	}
	userID, err := optionalPositiveInt64Query(c, "user_id")
	if err != nil {
		return EventFilter{}, err
	}
	apiKeyID, err := optionalPositiveInt64Query(c, "api_key_id")
	if err != nil {
		return EventFilter{}, err
	}
	filter := EventFilter{
		Decision: c.Query("decision"), RiskLevel: c.Query("risk_level"), Endpoint: c.Query("endpoint"),
		GroupID: groupID, UserID: userID, APIKeyID: apiKeyID, RequestID: c.Query("request_id"),
		PromptHash: c.Query("prompt_hash"), Keyword: c.Query("keyword"),
	}
	if value := strings.TrimSpace(c.Query("start_at")); value != "" {
		filter.StartAt = parseTimeQuery(value)
		if filter.StartAt == nil {
			return EventFilter{}, infraerrors.BadRequest("prompt_audit_invalid_time", "开始时间无效")
		}
	}
	if value := strings.TrimSpace(c.Query("end_at")); value != "" {
		filter.EndAt = parseTimeQuery(value)
		if filter.EndAt == nil {
			return EventFilter{}, infraerrors.BadRequest("prompt_audit_invalid_time", "结束时间无效")
		}
	}
	return filter, nil
}

func optionalPositiveInt64Query(c *gin.Context, key string) (*int64, error) {
	value := strings.TrimSpace(c.Query(key))
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return nil, infraerrors.BadRequest("prompt_audit_invalid_filter_id", "事件筛选 ID 无效")
	}
	return &parsed, nil
}

func positiveIntQuery(c *gin.Context, key string, defaultValue, maxValue int) (int, error) {
	value := strings.TrimSpace(c.Query(key))
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 || (maxValue > 0 && parsed > maxValue) {
		return 0, infraerrors.BadRequest("prompt_audit_invalid_pagination", "分页参数无效")
	}
	return parsed, nil
}

func parseTimeQuery(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}
