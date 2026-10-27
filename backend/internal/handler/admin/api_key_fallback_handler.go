package admin

import (
	"context"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// keyFallbackAdminService 是管理端回退链接口对编排层的最小依赖，便于测试替换。
type keyFallbackAdminService interface {
	AdminGetChain(ctx context.Context, keyID int64) (*service.KeyFallbackAdminView, error)
	AdminReplaceHiddenChain(ctx context.Context, adminID, keyID int64, head, tail []int64, note string) (*service.KeyFallbackAdminView, error)
	AdminClearHiddenChain(ctx context.Context, adminID, keyID int64) (*service.KeyFallbackAdminView, error)
	AdminRoutesByGroup(ctx context.Context, groupID int64) (*service.KeyFallbackRoutesByGroupView, error)
	AdminReferenceModels(ctx context.Context) map[string]string
	AdminSetReferenceModels(ctx context.Context, models map[string]string) (map[string]string, error)
}

// APIKeyFallbackHandler 提供管理端 Key 级分组回退链接口：查看有效链、读写隐藏链、按分组反查。
//
// 请求体字段名刻意避开 key / token / otp 子串（head、tail、note、group_ids），
// 否则审计日志的脱敏会把整个字段替换成占位符（service.RedactAuditBody）。
type APIKeyFallbackHandler struct {
	svc keyFallbackAdminService
}

// NewAPIKeyFallbackHandler 创建管理端回退链 handler。
func NewAPIKeyFallbackHandler(svc *service.KeyFallbackService) *APIKeyFallbackHandler {
	return &APIKeyFallbackHandler{svc: svc}
}

// ReplaceHiddenFallbackChainRequest 是写隐藏链的请求体。head 与 tail 两个字段都必须出现（空数组表示清空该段）；
// 只给其中一个会被拒绝，避免整体替换时悄悄清空没写的那一段。
type ReplaceHiddenFallbackChainRequest struct {
	// Head 排在 Key 主分组之前的分组，仅管理员可写。
	Head *[]int64 `json:"head"`
	// Tail 排在用户链之后的分组。
	Tail *[]int64 `json:"tail"`
	// Note 有 head 项时必填（用户编辑器上看到的主分组与实际计费分组不一致是刻意的，必须留说明）。
	Note string `json:"note"`
}

// SetKeyEditorReferenceModelsRequest 是保存参考模型的请求体。
// Models 整体替换已保存的覆盖：没出现的平台、值为空串的平台都回到内置默认，所以要保留的平台必须一并提交。
type SetKeyEditorReferenceModelsRequest struct {
	Models map[string]string `json:"models"`
}

// KeyEditorReferenceModelsResponse 是每个平台当前生效的参考模型。
type KeyEditorReferenceModelsResponse struct {
	Models map[string]string `json:"models"`
}

var errFallbackAdminInvalidRequest = infraerrors.BadRequest("FALLBACK_INVALID_REQUEST", "invalid request")

// GetChain 查看某把 Key 的有效链（用户链 + 隐藏链 + dry-run）。
// GET /api/v1/admin/api-keys/:id/fallback-chain
func (h *APIKeyFallbackHandler) GetChain(c *gin.Context) {
	keyID, ok := parseFallbackID(c, "id")
	if !ok {
		return
	}
	view, err := h.svc.AdminGetChain(c.Request.Context(), keyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// ReplaceHiddenChain 整体替换隐藏链。
// PUT /api/v1/admin/api-keys/:id/hidden-fallback-chain   body: {"head":[88],"tail":[],"note":"..."}
func (h *APIKeyFallbackHandler) ReplaceHiddenChain(c *gin.Context) {
	keyID, ok := parseFallbackID(c, "id")
	if !ok {
		return
	}
	var req ReplaceHiddenFallbackChainRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Head == nil || req.Tail == nil {
		response.ErrorFrom(c, errFallbackAdminInvalidRequest)
		return
	}
	view, err := h.svc.AdminReplaceHiddenChain(c.Request.Context(), adminActorID(c), keyID, *req.Head, *req.Tail, req.Note)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// ClearHiddenChain 清空隐藏链（不动用户链）。
// DELETE /api/v1/admin/api-keys/:id/hidden-fallback-chain
func (h *APIKeyFallbackHandler) ClearHiddenChain(c *gin.Context) {
	keyID, ok := parseFallbackID(c, "id")
	if !ok {
		return
	}
	view, err := h.svc.AdminClearHiddenChain(c.Request.Context(), adminActorID(c), keyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// ListRoutesByGroup 反查：哪些 Key 的链里放了这个分组。
// GET /api/v1/admin/groups/:id/fallback-routes
func (h *APIKeyFallbackHandler) ListRoutesByGroup(c *gin.Context) {
	groupID, ok := parseFallbackID(c, "id")
	if !ok {
		return
	}
	view, err := h.svc.AdminRoutesByGroup(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// GetReferenceModels 返回各平台当前生效的参考模型。
// GET /api/v1/admin/key-editor/reference-models
func (h *APIKeyFallbackHandler) GetReferenceModels(c *gin.Context) {
	response.Success(c, KeyEditorReferenceModelsResponse{Models: h.svc.AdminReferenceModels(c.Request.Context())})
}

// SetReferenceModels 保存各平台的参考模型（整体替换，没提交的平台回到内置默认）。
// PUT /api/v1/admin/key-editor/reference-models   body: {"models":{"openai":"gpt-5.6-sol"}}
func (h *APIKeyFallbackHandler) SetReferenceModels(c *gin.Context) {
	var req SetKeyEditorReferenceModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Models == nil {
		response.ErrorFrom(c, errFallbackAdminInvalidRequest)
		return
	}
	models, err := h.svc.AdminSetReferenceModels(c.Request.Context(), req.Models)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, KeyEditorReferenceModelsResponse{Models: models})
}

func parseFallbackID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid ID")
		return 0, false
	}
	return id, true
}

// adminActorID 取当前管理员的用户 ID；旧的全局 admin key 没有用户身份时返回 0（created_by 留空）。
func adminActorID(c *gin.Context) int64 {
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		return subject.UserID
	}
	return 0
}
