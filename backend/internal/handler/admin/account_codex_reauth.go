package admin

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// CodexSessionReauthRequest 是用 Codex auth.json / session JSON 重新授权单个账号的请求体。
type CodexSessionReauthRequest struct {
	Content string `json:"content" binding:"required"`
}

// CodexSessionReauthResult 返回更新后的账号与导入过程中的提示（如缺少 refresh_token）。
type CodexSessionReauthResult struct {
	Account  AccountWithConcurrency `json:"account"`
	Warnings []string               `json:"warnings,omitempty"`
}

// ReauthCodexSession 用 Codex auth.json / session JSON 重新授权指定的 OpenAI OAuth 账号。
// POST /api/v1/admin/accounts/:id/reauth/codex-session
//
// 与 /import/codex-session 的区别：
//   - 只作用于路径里的账号，不按身份在全量账号里匹配，也不会新建账号；
//   - 导入内容的 chatgpt_account_id / chatgpt_user_id 必须与该账号一致，否则拒绝，
//     避免把别人的凭据写进这个账号；
//   - 只替换凭据，不改并发、优先级、分组、代理等调度配置；
//   - 收尾与 /apply-oauth-credentials 一致：Extra 按键合并、清除错误状态、失效 token 缓存。
func (h *AccountHandler) ReauthCodexSession(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}

	var req CodexSessionReauthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	ctx := c.Request.Context()
	existing, err := h.adminService.GetAccount(ctx, accountID)
	if err != nil {
		response.NotFound(c, "Account not found")
		return
	}
	if existing.Platform != service.PlatformOpenAI || existing.Type != service.AccountTypeOAuth {
		response.ErrorFrom(c, infraerrors.BadRequest("NOT_OPENAI_OAUTH", "only OpenAI OAuth accounts can be re-authorized with a Codex session"))
		return
	}
	if existing.IsOpenAIAgentIdentity() {
		response.ErrorFrom(c, infraerrors.BadRequest("AGENT_IDENTITY_UNSUPPORTED", "agent identity accounts cannot be re-authorized with a Codex session"))
		return
	}

	result, err := h.reauthCodexSession(ctx, existing, req.Content)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *AccountHandler) reauthCodexSession(ctx context.Context, existing *service.Account, content string) (*CodexSessionReauthResult, error) {
	entries, err := parseCodexSessionImportEntries(CodexSessionImportRequest{Content: content})
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_CODEX_SESSION", err.Error())
	}
	if len(entries) != 1 {
		return nil, infraerrors.BadRequest("INVALID_CODEX_SESSION", "重新授权只接受一条 Codex 凭据，当前解析到 "+strconv.Itoa(len(entries))+" 条")
	}

	item, err := normalizeCodexImportEntry(entries[0])
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_CODEX_SESSION", err.Error())
	}
	if item.IsAgentIdentity {
		return nil, infraerrors.BadRequest("AGENT_IDENTITY_UNSUPPORTED", "重新授权不支持 agent identity 凭据")
	}
	if err := checkCodexReauthIdentity(existing, item); err != nil {
		return nil, infraerrors.BadRequest("CODEX_IDENTITY_MISMATCH", err.Error())
	}

	expiresAt, credentialExpiresAt, autoPauseOnExpired, expiryWarnings, err := resolveCodexImportExpiry(CodexSessionImportRequest{}, item)
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_CODEX_SESSION", err.Error())
	}
	warnings := append(append([]string(nil), item.WarningTexts...), expiryWarnings...)
	if credentialExpiresAt != nil {
		item.Credentials["expires_at"] = credentialExpiresAt.Format(time.RFC3339)
	}
	if item.RefreshToken == "" && codexCredentialString(existing.Credentials, "refresh_token") != "" {
		// 与批量导入一致：accessToken-only 的内容不覆盖已有 refresh_token，也不据此设置账号过期。
		warnings = append(warnings, "本次仅导入 accessToken：已保留原 refresh_token，但未验证其是否仍有效；若原 refresh_token 已被撤销，请导入包含新 refresh_token 的 auth.json，否则 accessToken 到期后仍会授权失败")
		expiresAt = nil
		autoPauseOnExpired = nil
	}
	if item.RefreshToken != "" && expiresAt == nil && codexReauthHasAutoTokenExpiry(existing) {
		// A previous accessToken-only import set account expiry to that token's
		// expiry. A fresh refresh_token makes that automatic stop obsolete.
		clearExpiry, disableAutoPause := int64(0), false
		expiresAt = &clearExpiry
		autoPauseOnExpired = &disableAutoPause
	}

	credentials := mergeCodexImportCredentials(existing.Credentials, item.Credentials, item)
	updated, err := h.adminService.UpdateAccount(ctx, existing.ID, &service.UpdateAccountInput{
		Type:               service.AccountTypeOAuth,
		Credentials:        credentials,
		ExpiresAt:          expiresAt,
		AutoPauseOnExpired: autoPauseOnExpired,
	})
	if err != nil {
		return nil, err
	}

	if len(item.Extra) > 0 {
		if extraErr := h.adminService.UpdateAccountExtra(ctx, existing.ID, item.Extra); extraErr != nil {
			slog.Error("reauth_codex_session.update_extra_failed", "account_id", existing.ID, "err", extraErr)
		}
	}
	if cleared, clearErr := h.adminService.ClearAccountError(ctx, existing.ID); clearErr != nil {
		slog.Warn("reauth_codex_session.clear_error_failed", "account_id", existing.ID, "err", clearErr)
	} else if cleared != nil {
		updated = cleared
	}
	if h.tokenCacheInvalidator != nil && updated != nil && updated.IsOAuth() {
		if invalidateErr := h.tokenCacheInvalidator.InvalidateToken(ctx, updated); invalidateErr != nil {
			slog.Warn("reauth_codex_session.invalidate_token_failed", "account_id", existing.ID, "err", invalidateErr)
		}
	}

	return &CodexSessionReauthResult{
		Account:  h.buildAccountResponseWithRuntime(ctx, updated),
		Warnings: warnings,
	}, nil
}

func codexReauthHasAutoTokenExpiry(existing *service.Account) bool {
	if existing == nil || existing.ExpiresAt == nil || !existing.AutoPauseOnExpired ||
		codexCredentialString(existing.Credentials, "refresh_token") != "" {
		return false
	}
	tokenExpiry, err := time.Parse(time.RFC3339Nano, codexCredentialString(existing.Credentials, "expires_at"))
	return err == nil && existing.ExpiresAt.Unix() == tokenExpiry.Unix()
}

// checkCodexReauthIdentity 确认导入凭据与目标账号的所有已有稳定身份一致。
// JSON 中的身份字段不能掩盖 token 中的另一用户或 workspace。
func checkCodexReauthIdentity(existing *service.Account, item *codexImportAccount) error {
	claims, err := decodeCodexJWTClaims(item.AccessToken)
	if err != nil {
		return errors.New("无法解析 accessToken 中的账号身份，不能安全地重新授权指定账号")
	}
	jwtAccountID, jwtUserID := "", strings.TrimSpace(claims.Sub)
	if claims.OpenAIAuth != nil {
		jwtAccountID = strings.TrimSpace(claims.OpenAIAuth.ChatGPTAccountID)
		if userID := strings.TrimSpace(claims.OpenAIAuth.ChatGPTUserID); userID != "" {
			jwtUserID = userID
		} else if userID := strings.TrimSpace(claims.OpenAIAuth.UserID); userID != "" {
			jwtUserID = userID
		}
	}
	if jwtAccountID != "" && item.AccountID != "" && jwtAccountID != strings.TrimSpace(item.AccountID) {
		return errors.New("导入 JSON 的 chatgpt_account_id 与 accessToken 中的账号不一致")
	}
	if jwtUserID != "" && item.UserID != "" && jwtUserID != strings.TrimSpace(item.UserID) {
		return errors.New("导入 JSON 的 chatgpt_user_id 与 accessToken 中的用户不一致")
	}
	if email := strings.TrimSpace(claims.Email); email != "" && item.Email != "" && !strings.EqualFold(email, strings.TrimSpace(item.Email)) {
		return errors.New("导入 JSON 的邮箱与 accessToken 中的邮箱不一致")
	}

	compared := false
	pairs := []struct {
		label    string
		stored   string
		incoming string
	}{
		{"chatgpt_account_id", codexCredentialString(existing.Credentials, "chatgpt_account_id"), jwtAccountID},
		{"chatgpt_user_id", codexCredentialString(existing.Credentials, "chatgpt_user_id"), jwtUserID},
	}
	for _, p := range pairs {
		stored, incoming := strings.TrimSpace(p.stored), strings.TrimSpace(p.incoming)
		if stored == "" {
			continue
		}
		if incoming == "" {
			return errors.New("导入凭据缺少可验证的 " + p.label + "，不能覆盖当前账号")
		}
		if stored != incoming {
			return errors.New("导入凭据的 " + p.label + " 与当前账号不一致（当前 " + stored + "，导入 " + incoming + "），请确认没有选错账号")
		}
		compared = true
	}
	if compared {
		return nil
	}

	storedEmail := strings.TrimSpace(codexCredentialString(existing.Credentials, "email"))
	incomingEmail := strings.TrimSpace(claims.Email)
	if storedEmail != "" && incomingEmail != "" {
		if !strings.EqualFold(storedEmail, incomingEmail) {
			return errors.New("导入凭据的邮箱与当前账号不一致（当前 " + storedEmail + "，导入 " + incomingEmail + "），请确认没有选错账号")
		}
		return nil
	}
	return errors.New("无法确认导入凭据与当前账号属于同一 ChatGPT 用户（缺少 chatgpt_account_id / chatgpt_user_id / email）")
}
