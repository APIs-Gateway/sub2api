package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// LegacyInviteHandler 处理「旧站付费用户领取本站邀请码」。
type LegacyInviteHandler struct {
	legacyInviteService *service.LegacyInviteService
	turnstileService    *service.TurnstileService
}

// NewLegacyInviteHandler 构造领码 handler。
func NewLegacyInviteHandler(
	legacyInviteService *service.LegacyInviteService,
	turnstileService *service.TurnstileService,
) *LegacyInviteHandler {
	return &LegacyInviteHandler{
		legacyInviteService: legacyInviteService,
		turnstileService:    turnstileService,
	}
}

// legacyInviteSendCodeRequest 是发送领码验证码的请求体。
type legacyInviteSendCodeRequest struct {
	Email          string `json:"email" binding:"required"`
	TurnstileToken string `json:"turnstile_token"`
}

// legacyInviteClaimRequest 是领取邀请码的请求体。
type legacyInviteClaimRequest struct {
	Email string `json:"email" binding:"required"`
	Code  string `json:"code" binding:"required"`
}

// GetStatus 返回领码入口是否开放以及门槛金额，供前端决定要不要渲染这个页面。
func (h *LegacyInviteHandler) GetStatus(c *gin.Context) {
	response.Success(c, h.legacyInviteService.Status())
}

// SendCode 往旧站邮箱发送一次性验证码，用于证明邮箱归属。
func (h *LegacyInviteHandler) SendCode(c *gin.Context) {
	var req legacyInviteSendCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// 这个接口能向任意邮箱发信，是典型的滥用面，人机校验不能省。
	// Turnstile 关闭时 VerifyToken 直接返回 nil。
	if err := h.turnstileService.VerifyToken(c.Request.Context(), req.TurnstileToken, ip.GetClientIP(c)); err != nil {
		response.BadRequest(c, "TURNSTILE_VERIFICATION_FAILED")
		return
	}

	if err := h.legacyInviteService.SendClaimCode(c.Request.Context(), req.Email, c.GetHeader("Accept-Language")); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"countdown": 60})
}

// Claim 校验验证码并发放邀请码。
func (h *LegacyInviteHandler) Claim(c *gin.Context) {
	var req legacyInviteClaimRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	result, err := h.legacyInviteService.Claim(c.Request.Context(), req.Email, req.Code, ip.GetClientIP(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}
