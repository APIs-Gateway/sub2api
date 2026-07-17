package middleware

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// NewAPIKeyAuthMiddleware 创建 API Key 认证中间件
func NewAPIKeyAuthMiddleware(apiKeyService *service.APIKeyService, subscriptionService *service.SubscriptionService, billingCacheService *service.BillingCacheService, cfg *config.Config) APIKeyAuthMiddleware {
	return APIKeyAuthMiddleware(apiKeyAuthWithSubscription(apiKeyService, subscriptionService, billingCacheService, cfg))
}

// apiKeyAuthWithSubscription API Key认证中间件（支持订阅验证）
//
// 中间件职责分为两层：
//   - 鉴权（Authentication）：验证 Key 有效性、用户状态、IP 限制 —— 始终执行
//   - 计费执行（Billing Enforcement）：过期/配额/订阅/余额检查 —— skipBilling 时整块跳过
//
// /v1/usage 和 /v1/sub2api/billing 端点只需鉴权，不需要计费执行。
func apiKeyAuthWithSubscription(apiKeyService *service.APIKeyService, subscriptionService *service.SubscriptionService, billingCacheService *service.BillingCacheService, cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		// ── 1. 提取 API Key ──────────────────────────────────────────

		queryKey := strings.TrimSpace(c.Query("key"))
		queryApiKey := strings.TrimSpace(c.Query("api_key"))
		if queryKey != "" || queryApiKey != "" {
			AbortWithError(c, 400, "api_key_in_query_deprecated", "API key in query parameter is deprecated. Please use Authorization header instead.")
			return
		}

		// 尝试从Authorization header中提取API key (Bearer scheme)
		authHeader := c.GetHeader("Authorization")
		var apiKeyString string

		if authHeader != "" {
			// 验证Bearer scheme
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				apiKeyString = strings.TrimSpace(parts[1])
			}
		}

		// 如果Authorization header中没有，尝试从x-api-key header中提取
		if apiKeyString == "" {
			apiKeyString = c.GetHeader("x-api-key")
		}

		// 如果x-api-key header中没有，尝试从x-goog-api-key header中提取（Gemini CLI兼容）
		if apiKeyString == "" {
			apiKeyString = c.GetHeader("x-goog-api-key")
		}

		// 如果所有header都没有API key
		if apiKeyString == "" {
			AbortWithError(c, 401, "API_KEY_REQUIRED", "API key is required in Authorization header (Bearer scheme), x-api-key header, or x-goog-api-key header")
			return
		}

		// ── 2. 验证 Key 存在 ─────────────────────────────────────────

		apiKey, err := apiKeyService.GetByKey(c.Request.Context(), apiKeyString)
		if err != nil {
			if errors.Is(err, service.ErrAPIKeyNotFound) {
				AbortWithError(c, 401, "INVALID_API_KEY", "Invalid API key")
				return
			}
			AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to validate API key")
			return
		}

		// apiKey 已加载（含 User/Group）。即便后续因分组停用/Key 停用/用户停用/
		// IP 限制等早退中断，也让 Ops 错误日志能回退取到 user/group/platform。
		SetOpsFallbackAPIKey(c, apiKey)

		// ── 3. 基础鉴权（始终执行） ─────────────────────────────────

		// disabled / 未知状态 → 无条件拦截（expired 和 quota_exhausted 留给计费阶段）
		if !apiKey.IsActive() &&
			apiKey.Status != service.StatusAPIKeyExpired &&
			apiKey.Status != service.StatusAPIKeyQuotaExhausted {
			AbortWithError(c, 401, "API_KEY_DISABLED", "API key is disabled")
			return
		}

		// 检查 IP 限制（白名单/黑名单）
		// 注意：错误信息故意模糊，避免暴露具体的 IP 限制机制
		if len(apiKey.IPWhitelist) > 0 || len(apiKey.IPBlacklist) > 0 {
			clientIP := ip.GetTrustedClientIP(c)
			if cfg.TrustForwardedIPForAPIKeyACL() {
				clientIP = ip.GetClientIP(c)
			}
			allowed, _ := ip.CheckIPRestrictionWithCompiledRules(clientIP, apiKey.CompiledIPWhitelist, apiKey.CompiledIPBlacklist)
			if !allowed {
				if clientIP == "" {
					clientIP = "unknown"
				}
				service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonIPRestriction)
				AbortWithError(c, 403, "ACCESS_DENIED", fmt.Sprintf("Access denied. Your IP is %s", clientIP))
				return
			}
		}

		// 检查关联的用户
		if apiKey.User == nil {
			AbortWithError(c, 401, "USER_NOT_FOUND", "User associated with API key not found")
			return
		}

		// 检查用户状态
		if !apiKey.User.IsActive() {
			AbortWithError(c, 401, "USER_INACTIVE", "User account is not active")
			return
		}
		if abortIfAPIKeyGroupUnavailable(c, apiKey) {
			return
		}
		if abortIfAPIKeyGroupNotAllowed(c, apiKey) {
			return
		}
		setUserIDContext(c, apiKey.User.ID)

		// ── 4. SimpleMode → early return ─────────────────────────────

		if cfg.RunMode == config.RunModeSimple {
			c.Set(string(ContextKeyAPIKey), apiKey)
			c.Set(string(ContextKeyUser), AuthSubject{
				UserID:      apiKey.User.ID,
				Concurrency: apiKey.User.Concurrency,
			})
			c.Set(string(ContextKeyUserRole), apiKey.User.Role)
			setGroupContext(c, apiKey.Group)
			if !isAPIKeyBillingInfoPath(c.Request.URL.Path) {
				_ = apiKeyService.TouchLastUsed(c.Request.Context(), apiKey.ID)
			}
			c.Next()
			return
		}

		// ── 5. 计费执行（skipBilling 时整块跳过） ────────────────────
		//
		// burn-down 模型：访问改为「纯余额模式」——只要可用余额 > 0 即放行，
		// 不再按分组校验订阅日/周/月限额。订阅额度在开通时已一次性打入 users.balance，
		// 由每日清扣 job 维护；消费时由计费仓储按「订阅优先」归集到各订阅卡。
		//
		// skipBilling: read-only usage endpoints only need authentication.
		skipBilling := isAPIKeyAuthReadOnlyUsagePath(c.Request.URL.Path)
		var subscription *service.UserSubscription

		if !skipBilling {
			// Key 状态检查
			switch apiKey.Status {
			case service.StatusAPIKeyQuotaExhausted:
				AbortWithError(c, 429, "API_KEY_QUOTA_EXHAUSTED", "API key 额度已用完")
				return
			case service.StatusAPIKeyExpired:
				AbortWithError(c, 403, "API_KEY_EXPIRED", "API key 已过期")
				return
			}

			// 运行时过期/配额检查（即使状态是 active，也要检查时间和用量）
			if apiKey.IsExpired() {
				AbortWithError(c, 403, "API_KEY_EXPIRED", "API key 已过期")
				return
			}
			if apiKey.IsQuotaExhausted() {
				AbortWithError(c, 429, "API_KEY_QUOTA_EXHAUSTED", "API key 额度已用完")
				return
			}

			// Keep window maintenance synchronous with admission. The fork's billing
			// model uses one active card per user and card-level frozen limits; group
			// subscription type is intentionally not consulted here.
			if subscriptionService != nil {
				loaded, subErr := subscriptionService.GetActiveUserSubscription(c.Request.Context(), apiKey.User.ID)
				if subErr == nil && loaded != nil {
					subscription = loaded
					needsMaintenance, validateErr := subscriptionService.ValidateAndCheckLimits(subscription, apiKey.Group)
					if needsMaintenance {
						refreshed, maintenanceErr := subscriptionService.EnsureWindowMaintenance(c.Request.Context(), subscription)
						if maintenanceErr != nil {
							AbortWithError(c, 500, "SUBSCRIPTION_MAINTENANCE_FAILED", "Failed to maintain subscription usage windows")
							return
						}
						subscription = refreshed
						_, validateErr = subscriptionService.ValidateAndCheckLimits(subscription, apiKey.Group)
					}
					// Window exhaustion still falls back to wallet under the fork's AdmitWindow
					// contract. Empty-wallet users are rejected here from the fresh snapshot.
					if validateErr != nil && !(isSubscriptionLimitError(validateErr) && apiKey.User.Balance > 0) {
						code := "SUBSCRIPTION_INVALID"
						status := 403
						if isSubscriptionLimitError(validateErr) {
							code = "USAGE_LIMIT_EXCEEDED"
							status = 429
						}
						AbortWithError(c, status, code, validateErr.Error())
						return
					}
				}
			}

			// per-day：不再在中间件按 user.Balance<=0 早拒。新模型套餐额度在卡的 today_remaining、
			// 不在 users.balance（已是纯钱包），「钱包 0 + 有今日套餐额度」的用户应放行。准入由各计费
			// handler 调 BillingCacheService.CheckBillingEligibility（per-day Admit：套餐余额 || 钱包 ||
			// 可透支）权威判定，此处不再做余额闸，避免 Admit 不可达。

			// 公益 key（hvoy/hovy）单 IP 每自然日消费上限：超额则按分组协议输出
			// 标准 error 信封（429）+ 文案，不计费、不转发上游
			// （仅作用于公益 key 名单内的 key；非公益 key 直接放行）。
			if billingCacheService != nil {
				// IP 口径必须与计费累加侧（usage_log.IPAddress，handler 一律用 ip.GetClientIP）
				// 完全一致：公益限额是按【真实客户端 IP】计量，而非安全 ACL 决策，故不走
				// trust_forwarded 开关。否则默认反代部署下预检读到固定反代 IP、计数恒 0，上限失效。
				capClientIP := ip.GetClientIP(c)
				if exceeded, msg := billingCacheService.PublicBenefitIPCapExceeded(c.Request.Context(), apiKey, capClientIP); exceeded {
					// 归类为业务限流（非系统/上游错误），避免污染 ops_error_logs 的故障统计。
					service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
					writePublicBenefitCapError(c, apiKey, msg)
					c.Abort()
					return
				}
			}
		}

		// ── 6. 设置上下文 → Next ─────────────────────────────────────

		if subscription != nil {
			c.Set(string(ContextKeySubscription), subscription)
		}
		c.Set(string(ContextKeyAPIKey), apiKey)
		c.Set(string(ContextKeyUser), AuthSubject{
			UserID:      apiKey.User.ID,
			Concurrency: apiKey.User.Concurrency,
		})
		c.Set(string(ContextKeyUserRole), apiKey.User.Role)
		setGroupContext(c, apiKey.Group)
		if !isAPIKeyBillingInfoPath(c.Request.URL.Path) {
			_ = apiKeyService.TouchLastUsed(c.Request.Context(), apiKey.ID)
		}

		c.Next()
	}
}

func isSubscriptionLimitError(err error) bool {
	return errors.Is(err, service.ErrDailyLimitExceeded) ||
		errors.Is(err, service.ErrWeeklyLimitExceeded) ||
		errors.Is(err, service.ErrMonthlyLimitExceeded)
}

func isAPIKeyAuthReadOnlyUsagePath(path string) bool {
	switch path {
	case "/v1/usage", "/backend-api/wham/usage", "/backend-api/codex/wham/usage", "/v1/sub2api/billing":
		return true
	default:
		return false
	}
}

func isAPIKeyBillingInfoPath(path string) bool {
	return path == "/v1/sub2api/billing"
}

// writePublicBenefitCapError 在公益 key 命中单 IP 每日上限时，按调用方所属分组的
// 协议格式输出标准 error 信封（HTTP 429）。
//
// 必须是结构化 error 而非裸文本：AI 编程客户端把 2xx 当成功响应、会按 JSON/SSE 解析
// body，纯文本会解析失败而把提示文案丢掉；唯有标准 error.message 才能被各客户端原样
// 展示给用户（引导其购买套餐）。OpenAI 分组用 {"error":{...}} 信封，其余（Anthropic
// 及走本中间件的 antigravity 等）用 Anthropic {"type":"error","error":{...}} 信封——
// 两者均含 error.message，主流客户端都能读取展示。
func writePublicBenefitCapError(c *gin.Context, apiKey *service.APIKey, message string) {
	platform := ""
	if apiKey != nil && apiKey.Group != nil {
		platform = apiKey.Group.Platform
	}
	if platform == service.PlatformOpenAI {
		c.JSON(429, gin.H{"error": gin.H{
			"type":    "insufficient_quota",
			"code":    "public_benefit_daily_cap",
			"message": message,
		}})
		return
	}
	c.JSON(429, gin.H{
		"type":  "error",
		"error": gin.H{"type": "rate_limit_error", "message": message},
	})
}

// GetAPIKeyFromContext 从上下文中获取API key
func GetAPIKeyFromContext(c *gin.Context) (*service.APIKey, bool) {
	value, exists := c.Get(string(ContextKeyAPIKey))
	if !exists {
		return nil, false
	}
	apiKey, ok := value.(*service.APIKey)
	return apiKey, ok
}

// SetOpsFallbackAPIKey 记录已加载的 API Key，供 Ops 错误日志在鉴权早退时回退使用。
// 与 ContextKeyAPIKey 区分：写入它不代表请求已通过鉴权，因此不影响 handler、
// 审计日志等对“已鉴权”的判断。
func SetOpsFallbackAPIKey(c *gin.Context, apiKey *service.APIKey) {
	if c == nil || apiKey == nil {
		return
	}
	c.Set(string(ContextKeyOpsFallbackAPIKey), apiKey)
}

// GetOpsFallbackAPIKey 读取 Ops 错误日志专用的回退 API Key。
func GetOpsFallbackAPIKey(c *gin.Context) (*service.APIKey, bool) {
	value, exists := c.Get(string(ContextKeyOpsFallbackAPIKey))
	if !exists {
		return nil, false
	}
	apiKey, ok := value.(*service.APIKey)
	return apiKey, ok
}

// GetSubscriptionFromContext 从上下文中获取订阅信息
func GetSubscriptionFromContext(c *gin.Context) (*service.UserSubscription, bool) {
	value, exists := c.Get(string(ContextKeySubscription))
	if !exists {
		return nil, false
	}
	subscription, ok := value.(*service.UserSubscription)
	return subscription, ok
}

func setGroupContext(c *gin.Context, group *service.Group) {
	if !service.IsGroupContextValid(group) {
		return
	}
	if existing, ok := c.Request.Context().Value(ctxkey.Group).(*service.Group); ok && existing != nil && existing.ID == group.ID && service.IsGroupContextValid(existing) {
		return
	}
	ctx := context.WithValue(c.Request.Context(), ctxkey.Group, group)
	c.Request = c.Request.WithContext(ctx)
}

func setUserIDContext(c *gin.Context, userID int64) {
	if c == nil || userID <= 0 {
		return
	}
	if existing, ok := c.Request.Context().Value(ctxkey.UserID).(int64); ok && existing == userID {
		return
	}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.UserID, userID))
}

func abortIfAPIKeyGroupUnavailable(c *gin.Context, apiKey *service.APIKey) bool {
	code, message, ok := validateAPIKeyGroupAvailable(apiKey)
	if ok {
		return false
	}
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonAPIKeyGroupUnavailable)
	AbortWithError(c, 403, code, message)
	return true
}

func abortIfAPIKeyGroupNotAllowed(c *gin.Context, apiKey *service.APIKey) bool {
	if validateAPIKeyGroupAllowed(apiKey) {
		return false
	}
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonAPIKeyGroupUnavailable)
	AbortWithError(c, 403, "GROUP_NOT_ALLOWED", "API Key 所属专属分组不再允许当前用户使用")
	return true
}

func validateAPIKeyGroupAllowed(apiKey *service.APIKey) bool {
	if apiKey == nil || apiKey.GroupID == nil || apiKey.User == nil || apiKey.Group == nil {
		return true
	}
	// per-day：分组仅管路由、无「订阅型分组」豁免，统一按标准 AllowedGroups/IsExclusive 校验。
	group := apiKey.Group
	return apiKey.User.CanBindGroup(group.ID, group.IsExclusive)
}

func validateAPIKeyGroupAvailable(apiKey *service.APIKey) (string, string, bool) {
	if apiKey == nil || apiKey.GroupID == nil {
		return "", "", true
	}
	group := apiKey.Group
	if group == nil || strings.EqualFold(group.Status, "deleted") {
		return "GROUP_DELETED", "API Key 所属分组已删除", false
	}
	if !group.IsActive() {
		return "GROUP_DISABLED", "API Key 所属分组已停用", false
	}
	return "", "", true
}
