package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func newCodexReauthTestAccount(accessToken string, extraCreds map[string]any) service.Account {
	creds := map[string]any{
		"chatgpt_account_id": "workspace-1",
		"chatgpt_user_id":    "user-1",
		"access_token":       accessToken,
		"model_mapping":      map[string]any{"gpt-5.5": "gpt-5.5"},
	}
	for k, v := range extraCreds {
		creds[k] = v
	}
	return service.Account{
		ID:          10,
		Name:        "existing",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusError,
		Credentials: creds,
	}
}

func buildCodexAuthJSON(t *testing.T, accessToken, refreshToken string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"access_token":  accessToken,
			"refresh_token": refreshToken,
			"account_id":    "workspace-1",
		},
		"last_refresh": time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("marshal auth.json: %v", err)
	}
	return string(raw)
}

func TestReauthCodexSessionReplacesCredentialsOfTargetAccountOnly(t *testing.T) {
	oldToken := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(time.Hour))
	existing := newCodexReauthTestAccount(oldToken, map[string]any{"refresh_token": "rt-old", "client_id": "old-client"})
	svc := newCodexImportMemoryAdminService([]service.Account{existing})
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	newToken := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(10*24*time.Hour))
	result, err := handler.reauthCodexSession(context.Background(), &existing, buildCodexAuthJSON(t, newToken, "rt-new"))
	if err != nil {
		t.Fatalf("reauthCodexSession error = %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if len(svc.createdAccounts) != 0 {
		t.Fatalf("created accounts = %d, want 0", len(svc.createdAccounts))
	}
	if len(svc.updatedAccounts) != 1 || svc.updatedAccounts[0].id != 10 {
		t.Fatalf("updated accounts = %+v, want only account 10", svc.updatedAccounts)
	}
	input := svc.updatedAccounts[0].input
	if got := input.Credentials["access_token"]; got != newToken {
		t.Fatalf("access_token not replaced")
	}
	if got := input.Credentials["refresh_token"]; got != "rt-new" {
		t.Fatalf("refresh_token = %v, want rt-new", got)
	}
	if _, ok := input.Credentials["model_mapping"]; !ok {
		t.Fatal("model_mapping should be preserved")
	}
	if _, ok := input.Credentials["expires_at"]; !ok {
		t.Fatal("expires_at should be derived from the access token")
	}
	if input.Concurrency != nil || input.Priority != nil || input.GroupIDs != nil || input.ProxyID != nil || input.RateMultiplier != nil || input.LoadFactor != nil || input.Extra != nil || input.Status != "" {
		t.Fatalf("scheduling fields must not be touched: %+v", input)
	}
	if input.ExpiresAt != nil || input.AutoPauseOnExpired != nil {
		t.Fatalf("account expiry must not be set when refresh_token is present: %+v", input)
	}
	if result.Account.ID != existing.ID {
		t.Fatalf("returned account ID = %d, want %d", result.Account.ID, existing.ID)
	}
}

func TestReauthCodexSessionRejectsDifferentUser(t *testing.T) {
	oldToken := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(time.Hour))
	existing := newCodexReauthTestAccount(oldToken, nil)
	svc := newCodexImportMemoryAdminService([]service.Account{existing})
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	otherToken := buildCodexAccessToken(t, "workspace-1", "user-2", time.Now().Add(time.Hour))
	_, err := handler.reauthCodexSession(context.Background(), &existing, buildCodexAuthJSON(t, otherToken, "rt-other"))
	if err == nil || !strings.Contains(err.Error(), "chatgpt_user_id") {
		t.Fatalf("err = %v, want chatgpt_user_id mismatch", err)
	}
	if len(svc.updatedAccounts) != 0 {
		t.Fatalf("updated accounts = %d, want 0", len(svc.updatedAccounts))
	}
}

func TestReauthCodexSessionRejectsConflictingJSONAndTokenIdentity(t *testing.T) {
	oldToken := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(time.Hour))
	existing := newCodexReauthTestAccount(oldToken, nil)
	svc := newCodexImportMemoryAdminService([]service.Account{existing})
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	// A Team workspace ID is shared; JSON metadata must not hide a different JWT user.
	otherToken := buildCodexAccessToken(t, "workspace-1", "user-2", time.Now().Add(time.Hour))
	content, _ := json.Marshal(map[string]any{
		"access_token": otherToken,
		"refresh_token": "rt-other",
		"chatgpt_account_id": "workspace-1",
		"chatgpt_user_id": "user-1",
	})
	_, err := handler.reauthCodexSession(context.Background(), &existing, string(content))
	if err == nil || !strings.Contains(err.Error(), "accessToken") {
		t.Fatalf("err = %v, want JSON/JWT identity mismatch", err)
	}
	if len(svc.updatedAccounts) != 0 {
		t.Fatalf("updated accounts = %d, want 0", len(svc.updatedAccounts))
	}
}

func TestReauthCodexSessionRequiresBothStoredJWTIdentities(t *testing.T) {
	oldToken := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(time.Hour))
	existing := newCodexReauthTestAccount(oldToken, nil)
	svc := newCodexImportMemoryAdminService([]service.Account{existing})
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	// Raw JSON claims a matching user, but the JWT proves only the workspace.
	token := buildCodexImportTestJWT(t, time.Now().Add(time.Hour), map[string]any{
		"sub": "",
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace-1"},
	})
	content, _ := json.Marshal(map[string]any{
		"access_token": token,
		"refresh_token": "rt-new",
		"chatgpt_account_id": "workspace-1",
		"chatgpt_user_id": "user-1",
	})
	_, err := handler.reauthCodexSession(context.Background(), &existing, string(content))
	if err == nil || !strings.Contains(err.Error(), "chatgpt_user_id") {
		t.Fatalf("err = %v, want missing verified user ID", err)
	}
	if len(svc.updatedAccounts) != 0 {
		t.Fatalf("updated accounts = %d, want 0", len(svc.updatedAccounts))
	}
}

func TestReauthCodexSessionRejectsMultipleEntries(t *testing.T) {
	oldToken := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(time.Hour))
	existing := newCodexReauthTestAccount(oldToken, nil)
	svc := newCodexImportMemoryAdminService([]service.Account{existing})
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	a := buildCodexAuthJSON(t, buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(time.Hour)), "rt-a")
	b := buildCodexAuthJSON(t, buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(2*time.Hour)), "rt-b")
	_, err := handler.reauthCodexSession(context.Background(), &existing, "["+a+","+b+"]")
	if err == nil {
		t.Fatal("expected error for multiple entries")
	}
	if len(svc.updatedAccounts) != 0 {
		t.Fatalf("updated accounts = %d, want 0", len(svc.updatedAccounts))
	}
}

func TestReauthCodexSessionAccessTokenOnlyKeepsExistingRefreshToken(t *testing.T) {
	oldToken := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(time.Hour))
	existing := newCodexReauthTestAccount(oldToken, map[string]any{"refresh_token": "rt-old", "client_id": "old-client"})
	svc := newCodexImportMemoryAdminService([]service.Account{existing})
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	newToken := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(2*time.Hour))
	result, err := handler.reauthCodexSession(context.Background(), &existing, newToken)
	if err != nil {
		t.Fatalf("reauthCodexSession error = %v", err)
	}
	input := svc.updatedAccounts[0].input
	if got := input.Credentials["refresh_token"]; got != "rt-old" {
		t.Fatalf("refresh_token = %v, want rt-old", got)
	}
	if got := input.Credentials["client_id"]; got != "old-client" {
		t.Fatalf("client_id = %v, want old-client", got)
	}
	if input.ExpiresAt != nil || input.AutoPauseOnExpired != nil {
		t.Fatalf("account expiry must stay untouched when refresh_token is preserved: %+v", input)
	}
	if !strings.Contains(strings.Join(result.Warnings, " "), "已被撤销") {
		t.Fatalf("warnings = %v, want revoked refresh token warning", result.Warnings)
	}
}

type codexReauthRecordingService struct {
	*codexImportMemoryAdminService
	clearCalls int
	extra      map[string]any
}

func (s *codexReauthRecordingService) UpdateAccount(ctx context.Context, id int64, input *service.UpdateAccountInput) (*service.Account, error) {
	account, err := s.codexImportMemoryAdminService.UpdateAccount(ctx, id, input)
	if err != nil {
		return nil, err
	}
	if input.ExpiresAt != nil {
		if *input.ExpiresAt <= 0 {
			account.ExpiresAt = nil
		} else {
			expiresAt := time.Unix(*input.ExpiresAt, 0)
			account.ExpiresAt = &expiresAt
		}
	}
	if input.AutoPauseOnExpired != nil {
		account.AutoPauseOnExpired = *input.AutoPauseOnExpired
	}
	return account, nil
}

func (s *codexReauthRecordingService) UpdateAccountExtra(_ context.Context, _ int64, updates map[string]any) error {
	s.extra = updates
	return nil
}

func (s *codexReauthRecordingService) ClearAccountError(_ context.Context, id int64) (*service.Account, error) {
	s.clearCalls++
	for index := range s.accounts {
		if s.accounts[index].ID == id {
			s.accounts[index].Status = service.StatusActive
			return &s.accounts[index], nil
		}
	}
	return nil, nil
}

type codexReauthTokenInvalidator struct {
	accountID int64
	calls     int
}

func (i *codexReauthTokenInvalidator) InvalidateToken(_ context.Context, account *service.Account) error {
	i.calls++
	i.accountID = account.ID
	return nil
}

func TestReauthCodexSessionClearsErrorAndInvalidatesTokenCache(t *testing.T) {
	oldToken := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(time.Hour))
	existing := newCodexReauthTestAccount(oldToken, map[string]any{"refresh_token": "rt-old"})
	existing.Extra = map[string]any{"base_rpm": 12, "quota_used": 7}
	svc := &codexReauthRecordingService{codexImportMemoryAdminService: newCodexImportMemoryAdminService([]service.Account{existing})}
	cache := &codexReauthTokenInvalidator{}
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, cache)

	newToken := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(2*time.Hour))
	result, err := handler.reauthCodexSession(context.Background(), &existing, buildCodexAuthJSON(t, newToken, "rt-new"))
	if err != nil {
		t.Fatalf("reauthCodexSession error = %v", err)
	}
	if svc.clearCalls != 1 || result.Account.Status != service.StatusActive {
		t.Fatalf("clear calls = %d, status = %s; want one clear and active", svc.clearCalls, result.Account.Status)
	}
	if cache.calls != 1 || cache.accountID != existing.ID {
		t.Fatalf("cache invalidations = %d for account %d", cache.calls, cache.accountID)
	}
	if svc.extra["import_source"] != "codex_session" || svc.updatedAccounts[0].input.Extra != nil {
		t.Fatalf("extra must be merged by key; updates = %v, update input = %+v", svc.extra, svc.updatedAccounts[0].input)
	}
}

func TestReauthCodexSessionClearsOnlyAutoTokenExpiryAfterNewRefreshToken(t *testing.T) {
	oldExpiry := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	oldToken := buildCodexAccessToken(t, "workspace-1", "user-1", oldExpiry)
	existing := newCodexReauthTestAccount(oldToken, map[string]any{"expires_at": oldExpiry.Format(time.RFC3339)})
	existing.ExpiresAt = &oldExpiry
	existing.AutoPauseOnExpired = true
	existing.Schedulable = true
	svc := &codexReauthRecordingService{codexImportMemoryAdminService: newCodexImportMemoryAdminService([]service.Account{existing})}
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	newToken := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(time.Hour))
	_, err := handler.reauthCodexSession(context.Background(), &existing, buildCodexAuthJSON(t, newToken, "rt-new"))
	if err != nil {
		t.Fatalf("reauthCodexSession error = %v", err)
	}
	input := svc.updatedAccounts[0].input
	if input.ExpiresAt == nil || *input.ExpiresAt != 0 || input.AutoPauseOnExpired == nil || *input.AutoPauseOnExpired {
		t.Fatalf("obsolete AT-only expiry was not cleared: %+v", input)
	}
	if !svc.accounts[0].IsSchedulable() {
		t.Fatalf("account remains unschedulable after fresh refresh token: %+v", svc.accounts[0])
	}

	// A separate administrator expiry must remain untouched.
	manualExpiry := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	existing.ExpiresAt = &manualExpiry
	svc = &codexReauthRecordingService{codexImportMemoryAdminService: newCodexImportMemoryAdminService([]service.Account{existing})}
	handler = NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	_, err = handler.reauthCodexSession(context.Background(), &existing, buildCodexAuthJSON(t, newToken, "rt-new"))
	if err != nil {
		t.Fatalf("reauthCodexSession with manual expiry error = %v", err)
	}
	input = svc.updatedAccounts[0].input
	if input.ExpiresAt != nil || input.AutoPauseOnExpired != nil || svc.accounts[0].ExpiresAt == nil || !svc.accounts[0].ExpiresAt.Equal(manualExpiry) {
		t.Fatalf("administrator expiry should be preserved: %+v", input)
	}
}
