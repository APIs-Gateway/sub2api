package routes

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/middleware"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// RegisterLegacyInviteRoutes 注册「旧站付费用户领取本站邀请码」的公开接口。
//
// 三个接口都不需要登录——领码的人正是因为还没有本站账号才来领码。
// 正因为完全公开，发信和领取都套了 fail-close 的服务端限流：
// Redis 挂掉时宁可拒绝请求，也不能让这两个入口变成无限量的发信器和爆破面。
func RegisterLegacyInviteRoutes(
	v1 *gin.RouterGroup,
	h *handler.Handlers,
	redisClient *redis.Client,
	settingService *service.SettingService,
) {
	rateLimiter := middleware.NewRateLimiter(redisClient)

	group := v1.Group("/legacy-invite")
	group.Use(servermiddleware.BackendModeAuthGuard(settingService))
	{
		// 状态查询无副作用，只回「开没开、门槛多少」，不限流。
		group.GET("/status", h.LegacyInvite.GetStatus)

		group.POST("/send-code", rateLimiter.LimitWithOptions("legacy-invite-send-code", 5, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.LegacyInvite.SendCode)

		group.POST("/claim", rateLimiter.LimitWithOptions("legacy-invite-claim", 10, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.LegacyInvite.Claim)
	}
}
