package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/imroc/req/v3"
)

// Endpoints used by the OpenAI/ChatGPT/Codex quota query and reset feature.
const (
	chatGPTUsageURL             = "https://chatgpt.com/backend-api/wham/usage"
	chatGPTRateLimitCreditsURL  = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	chatGPTRateLimitResetURL    = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
	openaiQuotaUpstreamTimeout  = 20 * time.Second
	openaiQuotaCodexOriginator  = "Codex Desktop"
	openaiQuotaCodexLanguageTag = "zh-CN"
	openaiQuotaSecFetchSite     = "none"
	openaiQuotaSecFetchMode     = "no-cors"
	openaiQuotaSecFetchDest     = "empty"
)

// OpenAIRateLimitWindow describes a single rate-limit window returned by
// /wham/usage. The upstream returns an explicit `null` window when the slot
// is unused, so consumers should treat a nil pointer as "no data".
type OpenAIRateLimitWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

// OpenAIRateLimit is a rate-limit envelope (primary + optional secondary window).
type OpenAIRateLimit struct {
	Allowed         bool                   `json:"allowed"`
	LimitReached    bool                   `json:"limit_reached"`
	PrimaryWindow   *OpenAIRateLimitWindow `json:"primary_window,omitempty"`
	SecondaryWindow *OpenAIRateLimitWindow `json:"secondary_window,omitempty"`
}

// OpenAIAdditionalRateLimit describes a per-feature rate limit (e.g. Codex Spark).
type OpenAIAdditionalRateLimit struct {
	LimitName      string           `json:"limit_name"`
	MeteredFeature string           `json:"metered_feature"`
	RateLimit      *OpenAIRateLimit `json:"rate_limit,omitempty"`
}

// OpenAIRateLimitResetCredits captures the "available_count" surfaced for the
// rate_limit_reset_credit grant type, which the reset action consumes.
type OpenAIRateLimitResetCredits struct {
	AvailableCount int                                `json:"available_count"`
	Credits        []OpenAIRateLimitResetCreditDetail `json:"credits,omitempty"`
}

// OpenAIRateLimitResetCreditDetail contains only safe reset-credit metadata.
type OpenAIRateLimitResetCreditDetail struct {
	ExpiresAt string `json:"expires_at,omitempty"`
}

// OpenAIQuotaUsage is the typed projection of /wham/usage we expose to the UI.
// Fields not relevant to the quota card are intentionally omitted to keep the
// surface narrow; full upstream payload preservation is unnecessary.
type OpenAIQuotaUsage struct {
	UserID                string                       `json:"user_id,omitempty"`
	AccountID             string                       `json:"account_id,omitempty"`
	Email                 string                       `json:"email,omitempty"`
	PlanType              string                       `json:"plan_type,omitempty"`
	RateLimit             *OpenAIRateLimit             `json:"rate_limit,omitempty"`
	AdditionalRateLimits  []OpenAIAdditionalRateLimit  `json:"additional_rate_limits,omitempty"`
	RateLimitResetCredits *OpenAIRateLimitResetCredits `json:"rate_limit_reset_credits,omitempty"`
	FetchedAt             int64                        `json:"fetched_at"`
}

// OpenAIQuotaResetCredit captures the redeemed credit metadata returned by the
// reset endpoint.
type OpenAIQuotaResetCredit struct {
	ID              string `json:"id,omitempty"`
	ResetType       string `json:"reset_type,omitempty"`
	Status          string `json:"status,omitempty"`
	GrantedAt       string `json:"granted_at,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	RedeemStartedAt string `json:"redeem_started_at,omitempty"`
	RedeemedAt      string `json:"redeemed_at,omitempty"`
}

// OpenAIQuotaResetResult is the typed projection of /wham/rate-limit-reset-credits/consume.
// The inner Credit also carries `redeemed_at` (RFC3339 string); we deliberately do
// NOT add a top-level redeemed_at to avoid ambiguity with the nested field.
type OpenAIQuotaResetResult struct {
	Code         string                  `json:"code"`
	Credit       *OpenAIQuotaResetCredit `json:"credit,omitempty"`
	WindowsReset int                     `json:"windows_reset"`
}

// OpenAIQuotaService queries and consumes ChatGPT/Codex rate-limit reset credits
// for OpenAI OAuth accounts. It reuses the privacy client factory so all calls
// flow through the impersonated HTTP client (Cloudflare-friendly TLS fingerprint).
type OpenAIQuotaService struct {
	accountRepo          AccountRepository
	proxyRepo            ProxyRepository
	tokenProvider        *OpenAITokenProvider
	privacyClientFactory PrivacyClientFactory
}

// NewOpenAIQuotaService constructs a quota service. token provider is required —
// it ensures we always invoke upstream with a valid (refreshed-if-needed)
// access_token, sharing the same refresh/locking machinery used by the gateway.
func NewOpenAIQuotaService(
	accountRepo AccountRepository,
	proxyRepo ProxyRepository,
	tokenProvider *OpenAITokenProvider,
	privacyClientFactory PrivacyClientFactory,
) *OpenAIQuotaService {
	return &OpenAIQuotaService{
		accountRepo:          accountRepo,
		proxyRepo:            proxyRepo,
		tokenProvider:        tokenProvider,
		privacyClientFactory: privacyClientFactory,
	}
}

// QueryUsage fetches the latest rate-limit/usage snapshot for the given OpenAI
// OAuth account. Returns infraerrors so the handler layer can map them to
// stable error codes / HTTP statuses.
func (s *OpenAIQuotaService) QueryUsage(ctx context.Context, accountID int64) (*OpenAIQuotaUsage, error) {
	accessToken, chatGPTAccountID, proxyURL, err := s.prepareUpstreamCall(ctx, accountID)
	if err != nil {
		return nil, err
	}

	client, err := s.privacyClientFactory(proxyURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_CLIENT_ERROR", "failed to build upstream client: %v", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, openaiQuotaUpstreamTimeout)
	defer cancel()

	agentIdentity := s.isAgentIdentityAccount(ctx, accountID)
	var payload OpenAIQuotaUsage
	for recovered := false; ; {
		quotaHeaders, expectedTaskID, headerErr := s.buildCodexQuotaHeaders(callCtx, accountID, accessToken, chatGPTAccountID)
		if headerErr != nil {
			return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "failed to build upstream authentication: %v", headerErr)
		}
		resp, requestErr := client.R().
			SetContext(callCtx).
			SetHeaders(quotaHeaders).
			SetSuccessResult(&payload).
			Get(chatGPTUsageURL)
		if requestErr != nil {
			return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_REQUEST_FAILED", "upstream request failed: %v", requestErr)
		}
		if !resp.IsSuccessState() {
			body := []byte(resp.String())
			if agentIdentity && !recovered && isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, body) {
				recovered = true
				if recoverErr := s.recoverAgentIdentityTask(ctx, accountID, expectedTaskID); recoverErr != nil {
					return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "agent identity task recovery failed: %v", recoverErr)
				}
				continue
			}
			status := resp.StatusCode
			bodyText := truncate(s.redactQuotaErrorBody(ctx, accountID, string(body)), 240)
			slog.Warn("openai_quota_query_failed", "account_id", accountID, "status", status, "body", bodyText)
			return nil, infraerrors.Newf(mapUpstreamStatus(status), "OPENAI_QUOTA_UPSTREAM_ERROR", "upstream returned %d: %s", status, bodyText)
		}
		break
	}

	payload.FetchedAt = time.Now().Unix()
	details := s.queryResetCreditDetailsForAccount(callCtx, client, accessToken, chatGPTAccountID, accountID)
	applyOpenAIRateLimitResetCreditDetails(&payload, details)
	return &payload, nil
}

func applyOpenAIRateLimitResetCreditDetails(payload *OpenAIQuotaUsage, details *openAIRateLimitResetCreditDetails) {
	if payload == nil || details == nil || (details.AvailableCount == nil && !details.CreditListPresent) {
		return
	}
	if payload.RateLimitResetCredits == nil {
		payload.RateLimitResetCredits = &OpenAIRateLimitResetCredits{}
	}
	if details.CreditListPresent {
		payload.RateLimitResetCredits.Credits = details.Credits
	}
	switch {
	case details.AvailableCount != nil:
		payload.RateLimitResetCredits.AvailableCount = *details.AvailableCount
	case details.CreditListPresent:
		payload.RateLimitResetCredits.AvailableCount = details.AvailableCreditCount
	}
}

func (s *OpenAIQuotaService) queryResetCreditDetails(ctx context.Context, client *req.Client, accessToken, chatGPTAccountID string, accountID int64) *openAIRateLimitResetCreditDetails {
	return s.queryResetCreditDetailsForAccount(ctx, client, accessToken, chatGPTAccountID, accountID)
}

func (s *OpenAIQuotaService) queryResetCreditDetailsForAccount(ctx context.Context, client *req.Client, accessToken, chatGPTAccountID string, accountID int64) *openAIRateLimitResetCreditDetails {
	quotaHeaders, _, headerErr := s.buildCodexQuotaHeaders(ctx, accountID, accessToken, chatGPTAccountID)
	if headerErr != nil {
		slog.Warn("openai_quota_reset_credit_details_auth_failed", "account_id", accountID, "error", headerErr)
		return nil
	}
	resp, err := client.R().
		SetContext(ctx).
		SetHeaders(quotaHeaders).
		Get(chatGPTRateLimitCreditsURL)
	if err != nil {
		slog.Warn("openai_quota_reset_credit_details_failed", "account_id", accountID, "error", err)
		return nil
	}
	if !resp.IsSuccessState() {
		slog.Warn("openai_quota_reset_credit_details_failed", "account_id", accountID, "status", resp.StatusCode)
		return nil
	}

	details, err := parseOpenAIRateLimitResetCreditDetails(resp.Bytes())
	if err != nil {
		slog.Warn("openai_quota_reset_credit_details_parse_failed", "account_id", accountID, "error", err)
		if details.AvailableCount == nil {
			return nil
		}
	}
	if details.AvailableCount == nil && !details.CreditListPresent {
		return nil
	}
	return &details
}

// ResetCredit consumes one rate_limit_reset_credit for the given OpenAI account.
// The redeem_request_id is auto-generated (uuid-like) — upstream uses it for
// idempotency. Returns the consumed credit metadata so the UI can refresh.
func (s *OpenAIQuotaService) ResetCredit(ctx context.Context, accountID int64) (*OpenAIQuotaResetResult, error) {
	accessToken, chatGPTAccountID, proxyURL, err := s.prepareUpstreamCall(ctx, accountID)
	if err != nil {
		return nil, err
	}

	redeemRequestID, err := generateRedeemRequestID()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_QUOTA_REDEEM_ID_FAILED", "failed to generate redeem id: %v", err)
	}

	client, err := s.privacyClientFactory(proxyURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_CLIENT_ERROR", "failed to build upstream client: %v", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, openaiQuotaUpstreamTimeout)
	defer cancel()

	agentIdentity := s.isAgentIdentityAccount(ctx, accountID)
	var payload OpenAIQuotaResetResult
	for recovered := false; ; {
		headers, expectedTaskID, headerErr := s.buildCodexQuotaHeaders(callCtx, accountID, accessToken, chatGPTAccountID)
		if headerErr != nil {
			return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "failed to build upstream authentication: %v", headerErr)
		}
		headers["content-type"] = "application/json"
		resp, requestErr := client.R().
			SetContext(callCtx).
			SetHeaders(headers).
			SetBody(map[string]string{"redeem_request_id": redeemRequestID}).
			SetSuccessResult(&payload).
			Post(chatGPTRateLimitResetURL)
		if requestErr != nil {
			return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_RESET_REQUEST_FAILED", "upstream request failed: %v", requestErr)
		}
		if !resp.IsSuccessState() {
			body := []byte(resp.String())
			if agentIdentity && !recovered && isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, body) {
				recovered = true
				if recoverErr := s.recoverAgentIdentityTask(ctx, accountID, expectedTaskID); recoverErr != nil {
					return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "agent identity task recovery failed: %v", recoverErr)
				}
				continue
			}
			status := resp.StatusCode
			bodyText := truncate(s.redactQuotaErrorBody(ctx, accountID, string(body)), 240)
			slog.Warn("openai_quota_reset_failed", "account_id", accountID, "status", status, "body", bodyText)
			return nil, infraerrors.Newf(mapUpstreamStatus(status), "OPENAI_QUOTA_RESET_UPSTREAM_ERROR", "upstream returned %d: %s", status, bodyText)
		}
		break
	}

	slog.Info("openai_quota_reset_success",
		"account_id", accountID,
		"code", payload.Code,
		"windows_reset", payload.WindowsReset,
	)
	return &payload, nil
}

// prepareUpstreamCall loads the account, validates it, obtains a fresh access
// token via the shared TokenProvider, and resolves the chatgpt-account-id and
// proxy URL. Centralized so QueryUsage / ResetCredit share validation.
func (s *OpenAIQuotaService) prepareUpstreamCall(ctx context.Context, accountID int64) (accessToken, chatGPTAccountID, proxyURL string, err error) {
	if s == nil || s.accountRepo == nil || s.privacyClientFactory == nil {
		return "", "", "", infraerrors.New(http.StatusInternalServerError, "OPENAI_QUOTA_NOT_CONFIGURED", "openai quota service is not configured")
	}

	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return "", "", "", infraerrors.Newf(http.StatusNotFound, "OPENAI_QUOTA_ACCOUNT_NOT_FOUND", "account not found: %v", err)
	}
	if account == nil {
		return "", "", "", infraerrors.New(http.StatusNotFound, "OPENAI_QUOTA_ACCOUNT_NOT_FOUND", "account not found")
	}
	if account.Platform != PlatformOpenAI {
		return "", "", "", infraerrors.New(http.StatusBadRequest, "OPENAI_QUOTA_INVALID_PLATFORM", "account is not an OpenAI account")
	}
	if account.Type != AccountTypeOAuth {
		return "", "", "", infraerrors.New(http.StatusBadRequest, "OPENAI_QUOTA_INVALID_TYPE", "account is not an OAuth account")
	}

	chatGPTAccountID = strings.TrimSpace(account.GetCredential("chatgpt_account_id"))
	if chatGPTAccountID == "" {
		// Fall back to organization_id — some legacy accounts only persisted poid.
		chatGPTAccountID = strings.TrimSpace(account.GetCredential("organization_id"))
	}
	if chatGPTAccountID == "" {
		return "", "", "", infraerrors.New(http.StatusBadRequest, "OPENAI_QUOTA_MISSING_ACCOUNT_ID", "chatgpt_account_id is missing; please re-authorize this account")
	}

	if !account.IsOpenAIAgentIdentity() {
		if s.tokenProvider == nil {
			return "", "", "", infraerrors.New(http.StatusInternalServerError, "OPENAI_QUOTA_NOT_CONFIGURED", "openai quota service is not configured")
		}
		accessToken, err = s.tokenProvider.GetAccessToken(ctx, account)
		if err != nil {
			return "", "", "", infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_TOKEN_UNAVAILABLE", "failed to acquire access token: %v", err)
		}
		if strings.TrimSpace(accessToken) == "" {
			return "", "", "", infraerrors.New(http.StatusBadGateway, "OPENAI_QUOTA_TOKEN_UNAVAILABLE", "access token is empty")
		}
	}

	// account.Proxy is eager-loaded by accountRepo.GetByID (see
	// repository.accountsToService), so we can read the proxy URL directly
	// instead of round-tripping the DB again. Fall back to proxyRepo only
	// when Proxy isn't pre-populated (defensive — e.g. callers that built
	// the Account by hand).
	if account.ProxyID != nil {
		switch {
		case account.Proxy != nil:
			proxyURL = account.Proxy.URL()
		case s.proxyRepo != nil:
			if proxy, perr := s.proxyRepo.GetByID(ctx, *account.ProxyID); perr == nil && proxy != nil {
				proxyURL = proxy.URL()
			}
		}
	}

	return accessToken, chatGPTAccountID, proxyURL, nil
}

func (s *OpenAIQuotaService) isAgentIdentityAccount(ctx context.Context, accountID int64) bool {
	if s == nil || s.accountRepo == nil {
		return false
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	return err == nil && account != nil && account.IsOpenAIAgentIdentity()
}

func (s *OpenAIQuotaService) buildCodexQuotaHeaders(ctx context.Context, accountID int64, accessToken, chatGPTAccountID string) (map[string]string, string, error) {
	headers := buildCodexCommonHeaders(accessToken, chatGPTAccountID)
	if s == nil || s.accountRepo == nil {
		return headers, "", nil
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		if strings.TrimSpace(accessToken) == "" {
			return nil, "", fmt.Errorf("agent identity account credentials are unavailable")
		}
		return headers, "", nil
	}
	if !account.IsOpenAIAgentIdentity() {
		return headers, "", nil
	}
	if err := EnsureOpenAIAgentIdentityTask(ctx, s.accountRepo, account, ""); err != nil {
		return nil, "", err
	}
	key, err := agentIdentityKeyFromAccount(account)
	if err != nil {
		return nil, "", err
	}
	assertion, err := buildAgentAssertion(key, time.Now())
	if err != nil {
		return nil, "", err
	}
	headers["authorization"] = assertion
	return headers, key.taskID, nil
}

func (s *OpenAIQuotaService) recoverAgentIdentityTask(ctx context.Context, accountID int64, expectedTaskID string) error {
	if s == nil || s.accountRepo == nil {
		return fmt.Errorf("account repository is unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return err
	}
	if account == nil || !account.IsOpenAIAgentIdentity() {
		return nil
	}
	return EnsureOpenAIAgentIdentityTask(ctx, s.accountRepo, account, expectedTaskID)
}

func (s *OpenAIQuotaService) redactQuotaErrorBody(ctx context.Context, accountID int64, body string) string {
	if s == nil || s.accountRepo == nil {
		return body
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return body
	}
	return string(redactAgentIdentitySensitiveBodyForAccount(account, []byte(body)))
}

// buildCodexCommonHeaders sets the request headers expected by the chatgpt.com
// backend so calls succeed past Cloudflare/WASM checks.
func buildCodexCommonHeaders(accessToken, chatGPTAccountID string) map[string]string {
	return map[string]string{
		"authorization":      "Bearer " + accessToken,
		"chatgpt-account-id": chatGPTAccountID,
		"oai-language":       openaiQuotaCodexLanguageTag,
		"originator":         openaiQuotaCodexOriginator,
		"accept":             "application/json",
		"sec-fetch-site":     openaiQuotaSecFetchSite,
		"sec-fetch-mode":     openaiQuotaSecFetchMode,
		"sec-fetch-dest":     openaiQuotaSecFetchDest,
		"priority":           "u=4, i",
	}
}

// generateRedeemRequestID produces a UUID-v4-shaped string without pulling in a
// new dependency. ChatGPT uses this as an idempotency key for the consume call.
func generateRedeemRequestID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// Set version (4) and variant (RFC 4122) bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexStr := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexStr[0:8], hexStr[8:12], hexStr[12:16], hexStr[16:20], hexStr[20:]), nil
}

// mapUpstreamStatus collapses upstream HTTP statuses into a stable set we
// surface from the admin handler. 4xx upstream errors are surfaced as 502
// (BadGateway) so callers can distinguish "your input is bad" (400) from
// "upstream said no" (502); 401/403 are bubbled directly to hint at re-auth.
func mapUpstreamStatus(status int) int {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return status
	case status == http.StatusTooManyRequests:
		return http.StatusTooManyRequests
	case status >= 400 && status < 500:
		return http.StatusBadGateway
	case status >= 500:
		return http.StatusBadGateway
	default:
		return http.StatusBadGateway
	}
}
