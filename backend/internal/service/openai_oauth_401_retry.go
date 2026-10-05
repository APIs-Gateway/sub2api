package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// The snapshot covers authentication and transport settings, but excludes the
// token fields that the refresh executor is allowed to rotate. It is never logged.
type openAI401Snapshot struct {
	id           int64
	proxyID      int64
	proxyURL     string
	settings     [32]byte
	refreshToken [32]byte
}

func snapshotOpenAI401Account(account *Account) (openAI401Snapshot, bool) {
	if account == nil || !account.IsActive() || !account.IsOpenAIOAuth() ||
		account.IsOpenAIPersonalAccessToken() || account.IsOpenAIAgentIdentity() ||
		strings.TrimSpace(account.GetOpenAIRefreshToken()) == "" {
		return openAI401Snapshot{}, false
	}
	credentials := cloneCredentials(account.Credentials)
	for _, key := range []string{"access_token", "refresh_token", "id_token", "token_type", "expires_at", "expires_in", "_token_version"} {
		delete(credentials, key)
	}
	encoded, err := json.Marshal([]any{credentials, account.Extra})
	if err != nil {
		return openAI401Snapshot{}, false
	}
	snapshot := openAI401Snapshot{id: account.ID, settings: sha256.Sum256(encoded), refreshToken: sha256.Sum256([]byte(account.GetOpenAIRefreshToken()))}
	if account.ProxyID != nil {
		if account.Proxy == nil || account.Proxy.ID != *account.ProxyID || !account.Proxy.IsActive() {
			return openAI401Snapshot{}, false
		}
		snapshot.proxyID = *account.ProxyID
		snapshot.proxyURL = account.Proxy.URL()
	}
	return snapshot, true
}

func (snapshot openAI401Snapshot) matches(account *Account) bool {
	current, ok := snapshotOpenAI401Account(account)
	// A durable access-token winner may have rotated its refresh token too.
	// The selected refresh token is separately enforced before a new grant.
	current.refreshToken = snapshot.refreshToken
	return ok && current == snapshot
}

type openAI401CredentialsCAS interface {
	CompareAndSwapCredentials(context.Context, *Account, map[string]any) (bool, error)
}

// A complete, structured authentication refusal is required. This classifier
// does not mutate a billing lease: an internal retry remains funded by the same
// immutable attempt until its final outcome is known.
func isRecoverableOpenAIHTTP401(status int, body []byte, readErr error) bool {
	if status != http.StatusUnauthorized || readErr != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	payload, err := decodeBillingInflightErrorValue(decoder, 0)
	if err != nil || billingInflightErrorHasUsage(payload) {
		return false
	}
	if _, err = decoder.Token(); err != io.EOF {
		return false
	}
	envelope, ok := payload.(map[string]any)
	if !ok {
		return false
	}
	if outerType, exists := envelope["type"]; exists && outerType != "error" {
		return false
	}
	if detail, ok := envelope["detail"].(string); ok && strings.EqualFold(strings.TrimSpace(detail), "Unauthorized") {
		return false
	}
	if openAI401PermanentOrRequestError(payload) {
		return false
	}

	// Envelope metadata cannot contradict the HTTP status or its nested auth
	// refusal. An unknown/object-valued marker is not a credential proof.
	for _, field := range []string{"code", "status"} {
		value, exists := envelope[field]
		if !exists || value == nil || value == "" {
			continue
		}
		switch value := value.(type) {
		case float64:
			if value != float64(status) {
				return false
			}
		case string:
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "authentication_error", "invalid_api_key", "invalid_authentication", "unauthenticated", "access_token_expired", "token_expired", "error", "invalid_request_error":
			default:
				return false
			}
		default:
			return false
		}
	}
	providerError, ok := envelope["error"].(map[string]any)
	if !ok {
		return false
	}
	proof := false
	for _, field := range []string{"type", "code", "status"} {
		value, exists := providerError[field]
		if !exists || value == nil || value == "" {
			continue
		}
		if number, ok := value.(float64); ok {
			if field != "code" || number != float64(status) {
				return false
			}
			continue
		}
		marker, ok := value.(string)
		if !ok {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(marker)) {
		case "authentication_error", "invalid_api_key", "invalid_authentication", "unauthenticated", "access_token_expired", "token_expired":
			proof = true
		case "error", "invalid_request_error":
		default:
			return false
		}
	}
	return proof
}

func openAI401PermanentOrRequestError(value any) bool {
	switch value := value.(type) {
	case string:
		text := strings.ToLower(value)
		for _, marker := range []string{"token_invalidated", "token_revoked", "insufficient_scope", "missing_scope", "missing scopes:", "missing scope", "insufficient scope", "has been invalidated", "has been revoked", "refresh token expired", "account deactivated", "account disabled", "cloudflare", "error 1010", "model_not_found", "model_not_available", "unsupported_model", "invalid_model"} {
			if strings.Contains(text, marker) {
				return true
			}
		}
		return strings.Contains(text, "model") && (strings.Contains(text, "not found") || strings.Contains(text, "does not exist") || strings.Contains(text, "unknown model"))
	case map[string]any:
		for _, child := range value {
			if openAI401PermanentOrRequestError(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if openAI401PermanentOrRequestError(child) {
				return true
			}
		}
	}
	return false
}

func (s *OpenAIGatewayService) tryRefreshOpenAIHTTP401(ctx context.Context, c *gin.Context, account *Account, snapshot openAI401Snapshot, status int, body []byte, readErr error, rejectedToken string) (string, bool) {
	if s == nil || s.openAITokenProvider == nil || ctx.Err() != nil || c == nil || c.Writer.Written() ||
		s.isAgentIdentityAccount(ctx, account) || !snapshot.matches(account) || !isRecoverableOpenAIHTTP401(status, body, readErr) {
		return "", false
	}
	p := s.openAITokenProvider
	if p.refreshAPI == nil || p.executor == nil || p.accountRepo == nil || strings.TrimSpace(rejectedToken) == "" {
		return "", false
	}
	token, err := p.refreshAPI.refreshRejectedOpenAIToken(ctx, snapshot, p.executor, rejectedToken)
	if err != nil || ctx.Err() != nil {
		return "", false
	}
	return token, true
}

// Unlike normal expiry refresh, this narrow recovery fails closed on lock/DB/
// credential/cache errors and never falls back to a stale full-account Update.
// It shares both existing lock namespaces with every other refresh caller.
func (api *OAuthRefreshAPI) refreshRejectedOpenAIToken(ctx context.Context, snapshot openAI401Snapshot, executor OAuthRefreshExecutor, rejectedToken string) (string, error) {
	if api == nil || api.accountRepo == nil || api.tokenCache == nil || executor == nil {
		return "", errors.New("OAuth rejection recovery unavailable")
	}
	updater, ok := api.accountRepo.(openAI401CredentialsCAS)
	if !ok {
		return "", errors.New("OAuth credential updater unavailable")
	}
	budget := 10 * time.Second
	if api.lockTTL/2 < budget {
		budget = api.lockTTL / 2
	}
	recoveryDeadline := time.Now().Add(budget)
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	callerCtx := ctx
	key := executor.CacheKey(&Account{ID: snapshot.id, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
	local := api.getLocalLock(key)
	for !local.TryLock() {
		if err := waitOpenAI401Recovery(ctx); err != nil {
			return "", err
		}
	}
	defer local.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		acquired, err := api.tokenCache.AcquireRefreshLock(ctx, key, api.lockTTL)
		if err != nil {
			return "", err
		}
		if acquired {
			defer func() {
				releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
				defer releaseCancel()
				_ = api.tokenCache.ReleaseRefreshLock(releaseCtx, key)
			}()
			break
		}
		if err := waitOpenAI401Recovery(ctx); err != nil {
			return "", err
		}
	}
	read := func() (*Account, error) {
		fresh, err := api.accountRepo.GetByID(ctx, snapshot.id)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !snapshot.matches(fresh) || !executor.CanRefresh(fresh) {
			return nil, errors.New("OAuth account changed during rejection recovery")
		}
		return fresh, nil
	}
	fresh, err := read()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(fresh.GetOpenAIAccessToken()) == strings.TrimSpace(rejectedToken) {
		usedRefreshToken := fresh.GetOpenAIRefreshToken()
		if sha256.Sum256([]byte(usedRefreshToken)) != snapshot.refreshToken {
			return "", errors.New("selected OAuth refresh credentials changed before recovery")
		}
		credentials, refreshErr := executor.Refresh(ctx, fresh)
		if refreshErr != nil {
			return "", refreshErr
		}
		// The issuer may already have invalidated the old refresh token. Once
		// a grant succeeds, persist its rotation even if its caller cancels;
		// the original lock budget still bounds every durable operation.
		persistCtx, persistCancel := context.WithDeadline(context.WithoutCancel(callerCtx), recoveryDeadline)
		defer persistCancel()
		ctx = persistCtx
		current, readErr := read()
		if readErr != nil {
			return "", readErr
		}
		if strings.TrimSpace(current.GetOpenAIAccessToken()) == strings.TrimSpace(rejectedToken) {
			if current.GetOpenAIRefreshToken() != usedRefreshToken {
				return "", errors.New("OAuth refresh credentials changed during recovery")
			}
			candidate := *current
			candidate.Credentials = cloneCredentials(credentials)
			if !snapshot.matches(&candidate) || strings.TrimSpace(candidate.GetOpenAIAccessToken()) == "" || strings.TrimSpace(candidate.GetOpenAIAccessToken()) == strings.TrimSpace(rejectedToken) {
				return "", errors.New("OAuth refresh did not replace rejected credentials safely")
			}
			candidate.Credentials["_token_version"] = time.Now().UnixMilli()
			if err := ctx.Err(); err != nil {
				return "", err
			}
			swapped, err := updater.CompareAndSwapCredentials(ctx, current, candidate.Credentials)
			if err != nil {
				return "", err
			}
			if !swapped {
				return "", errors.New("OAuth credentials changed before conditional persistence")
			}
		}
		fresh, err = read()
		if err != nil {
			return "", err
		}
	}
	token := strings.TrimSpace(fresh.GetOpenAIAccessToken())
	if token == "" || token == strings.TrimSpace(rejectedToken) {
		return "", errors.New("OAuth durable token did not change")
	}
	if err := api.tokenCache.DeleteAccessToken(ctx, key); err != nil {
		return "", err
	}
	if err := callerCtx.Err(); err != nil {
		return "", err
	}
	return token, nil
}

func waitOpenAI401Recovery(ctx context.Context) error {
	timer := time.NewTimer(openAILockInitialWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
