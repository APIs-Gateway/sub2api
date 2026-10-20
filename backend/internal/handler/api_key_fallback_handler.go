package handler

import (
	"context"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// keyFallbackUserService 是用户端回退链接口对编排层的最小依赖，便于测试替换。
type keyFallbackUserService interface {
	GetUserChain(ctx context.Context, userID, keyID int64, modelOverride string) (*service.KeyFallbackUserView, error)
	ReplaceUserChain(ctx context.Context, userID, keyID int64, groupIDs []int64, modelOverride string) (*service.KeyFallbackUserView, error)
	ListUserChains(ctx context.Context, userID int64) (*service.KeyFallbackOverview, error)
}

// APIKeyFallbackHandler 提供用户端 Key 级分组回退链接口。
//
// 所有接口都只面向「当前登录用户自己的 Key」：归属校验在编排层，别人的 Key 与不存在的 Key
// 返回同一个 404（防 IDOR）。响应只由用户端视图类型构造，不含任何管理员隐藏链信息。
type APIKeyFallbackHandler struct {
	svc keyFallbackUserService
}

// NewAPIKeyFallbackHandler 创建用户端回退链 handler。
func NewAPIKeyFallbackHandler(svc *service.KeyFallbackService) *APIKeyFallbackHandler {
	return &APIKeyFallbackHandler{svc: svc}
}

// ReplaceFallbackChainRequest 是整体替换用户链的请求体。
type ReplaceFallbackChainRequest struct {
	// GroupIDs 只含兜底项（不含主分组），按顺序即位置；空数组表示清空；缺省或 null 视为错误，避免误清空。
	GroupIDs []int64 `json:"group_ids"`
}

// ErrFallbackInvalidRequest 请求体不合法。
var ErrFallbackInvalidRequest = infraerrors.BadRequest("FALLBACK_INVALID_REQUEST", "invalid request")

// GetChain 返回某把 Key 的回退链。
// GET /api/v1/keys/:id/fallback-chain?model=gpt-5.5
func (h *APIKeyFallbackHandler) GetChain(c *gin.Context) {
	subject, keyID, ok := h.subjectAndKeyID(c)
	if !ok {
		return
	}
	view, err := h.svc.GetUserChain(c.Request.Context(), subject, keyID, c.Query("model"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// ListChains 返回当前用户全部 Key 的回退链摘要，按平台分区。
// GET /api/v1/keys/fallback-chains
func (h *APIKeyFallbackHandler) ListChains(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	view, err := h.svc.ListUserChains(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// ReplaceChain 整体替换某把 Key 的回退链。
// PUT /api/v1/keys/:id/fallback-chain   body: {"group_ids":[21,30]}
func (h *APIKeyFallbackHandler) ReplaceChain(c *gin.Context) {
	subject, keyID, ok := h.subjectAndKeyID(c)
	if !ok {
		return
	}
	var req ReplaceFallbackChainRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.GroupIDs == nil {
		response.ErrorFrom(c, ErrFallbackInvalidRequest)
		return
	}
	view, err := h.svc.ReplaceUserChain(c.Request.Context(), subject, keyID, req.GroupIDs, c.Query("model"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// subjectAndKeyID 取当前用户 ID 与路径里的 Key ID。失败时已写响应。
func (h *APIKeyFallbackHandler) subjectAndKeyID(c *gin.Context) (userID, keyID int64, ok bool) {
	subject, found := middleware2.GetAuthSubjectFromContext(c)
	if !found {
		response.Unauthorized(c, "User not authenticated")
		return 0, 0, false
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		// 与「不存在」同一个响应，不区分原因。
		response.ErrorFrom(c, service.ErrAPIKeyNotFound)
		return 0, 0, false
	}
	return subject.UserID, id, true
}
