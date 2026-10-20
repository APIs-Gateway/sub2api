package handler

import (
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// CheckinHandler 处理用户每日签到。
type CheckinHandler struct {
	checkinService   *service.CheckinService
	turnstileService *service.TurnstileService
}

// NewCheckinHandler 构造签到 handler。
func NewCheckinHandler(checkinService *service.CheckinService, turnstileService *service.TurnstileService) *CheckinHandler {
	return &CheckinHandler{checkinService: checkinService, turnstileService: turnstileService}
}

// claimRequest 是签到领取请求体；turnstile_token 用于人机校验（Turnstile 关闭时可为空）。
type claimRequest struct {
	TurnstileToken string `json:"turnstile_token"`
}

// GetStatus 返回当前用户的签到状态。
func (h *CheckinHandler) GetStatus(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	status, err := h.checkinService.GetStatus(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

// Claim 领取一次签到（优先基础签到，否则一次额外签到）。
func (h *CheckinHandler) Claim(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	// 请求体可为空（Turnstile 关闭时）；忽略绑定错误，token 默认空串。
	var req claimRequest
	_ = c.ShouldBindJSON(&req)

	// 人机校验：Turnstile 关闭时 VerifyToken 直接返回 nil。
	if err := h.turnstileService.VerifyToken(c.Request.Context(), req.TurnstileToken, ip.GetClientIP(c)); err != nil {
		response.BadRequest(c, "TURNSTILE_VERIFICATION_FAILED")
		return
	}

	result, err := h.checkinService.Claim(c.Request.Context(), subject.UserID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrCheckinDisabled):
			response.BadRequest(c, "CHECKIN_DISABLED")
		case errors.Is(err, service.ErrCheckinAlreadyClaimed):
			response.BadRequest(c, "CHECKIN_ALREADY_CLAIMED")
		case errors.Is(err, service.ErrCheckinNoBonus):
			response.BadRequest(c, "CHECKIN_NO_BONUS")
		case errors.Is(err, service.ErrCheckinNotActiveEnough):
			response.BadRequest(c, "CHECKIN_NOT_ACTIVE_ENOUGH")
		default:
			response.ErrorFrom(c, err)
		}
		return
	}
	response.Success(c, result)
}
