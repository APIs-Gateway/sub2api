//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestDiffSettings_DetectsGlobalPlatformQuotaChange(t *testing.T) {
	five := 5.0
	ten := 10.0
	before := &service.SystemSettings{
		DefaultPlatformQuotas: map[string]*service.DefaultPlatformQuotaSetting{
			"anthropic": {DailyLimitUSD: &five},
		},
	}
	after := &service.SystemSettings{
		DefaultPlatformQuotas: map[string]*service.DefaultPlatformQuotaSetting{
			"anthropic": {DailyLimitUSD: &ten},
		},
	}

	changed := diffSettings(before, after, nil, nil, UpdateSettingsRequest{})
	found := false
	for _, key := range changed {
		if key == service.SettingKeyDefaultPlatformQuotas {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected change detection for default platform quotas, got %v", changed)
	}
}

func TestDiffSettings_NoChangeWhenEqual(t *testing.T) {
	five := 5.0
	before := &service.SystemSettings{
		DefaultPlatformQuotas: map[string]*service.DefaultPlatformQuotaSetting{
			"anthropic": {DailyLimitUSD: &five},
		},
	}
	after := &service.SystemSettings{
		DefaultPlatformQuotas: map[string]*service.DefaultPlatformQuotaSetting{
			"anthropic": {DailyLimitUSD: &five},
		},
	}

	changed := diffSettings(before, after, nil, nil, UpdateSettingsRequest{})
	for _, key := range changed {
		if key == service.SettingKeyDefaultPlatformQuotas {
			t.Error("equal values should not be detected as changed")
		}
	}
}

func TestResolveSessionBindingEnabledPreservesOmission(t *testing.T) {
	requested := false
	require.True(t, resolveSessionBindingEnabled(true, nil))
	require.False(t, resolveSessionBindingEnabled(true, &requested))
}

func TestDiffSettingsDetectsSessionBindingChange(t *testing.T) {
	changed := diffSettings(
		&service.SystemSettings{SessionBindingEnabled: true},
		&service.SystemSettings{SessionBindingEnabled: false},
		nil,
		nil,
		UpdateSettingsRequest{},
	)
	require.Contains(t, changed, service.SettingKeySessionBindingEnabled)
}

func TestDiffSettingsDetectsStepUpChange(t *testing.T) {
	changed := diffSettings(
		&service.SystemSettings{StepUpEnabled: false},
		&service.SystemSettings{StepUpEnabled: true},
		nil,
		nil,
		UpdateSettingsRequest{},
	)
	require.Contains(t, changed, service.SettingKeyStepUpEnabled)
}

func TestUpdateSettingsSecuritySwitchTransitions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newHandler := func(values map[string]string) (*SettingHandler, *settingHandlerRepoStub) {
		repo := &settingHandlerRepoStub{values: values}
		svc := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
		return NewSettingHandler(svc, nil, nil, nil, nil, nil, nil), repo
	}
	doUpdate := func(handler *SettingHandler, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		handler.UpdateSettings(c)
		return rec
	}

	handler, repo := newHandler(map[string]string{
		service.SettingKeyStepUpEnabled:         "true",
		service.SettingKeySessionBindingEnabled: "true",
	})
	rec := doUpdate(handler, `{"registration_enabled":true}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", repo.values[service.SettingKeyStepUpEnabled])
	require.Equal(t, "true", repo.values[service.SettingKeySessionBindingEnabled])

	handler, repo = newHandler(map[string]string{})
	rec = doUpdate(handler, `{"step_up_enabled":true}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_ENABLE_REQUIRES_TOTP")
	require.NotEqual(t, "true", repo.values[service.SettingKeyStepUpEnabled])

	handler, repo = newHandler(map[string]string{service.SettingKeyStepUpEnabled: "true"})
	rec = doUpdate(handler, `{"step_up_enabled":false}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "true", repo.values[service.SettingKeyStepUpEnabled])
}

type settingStepUpActorRepoStub struct {
	service.UserRepository
	user *service.User
	err  error
}

func (s settingStepUpActorRepoStub) GetByID(context.Context, int64) (*service.User, error) {
	return s.user, s.err
}

func (s settingStepUpActorRepoStub) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

func TestEnsureActorTotpForStepUpCoversActorPreconditions(t *testing.T) {
	newHandler := func(actor *service.User, actorErr error) *SettingHandler {
		repo := &settingHandlerRepoStub{values: map[string]string{}}
		settingService := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
		userService := service.NewUserService(&settingStepUpActorRepoStub{user: actor, err: actorErr}, nil, nil, nil)
		h := NewSettingHandler(settingService, nil, nil, nil, nil, nil, nil)
		h.SetStepUpDeps(nil, userService)
		return h
	}
	newContext := func(rec *httptest.ResponseRecorder) *gin.Context {
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", nil)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		return c
	}

	t.Run("admin api key is rejected", func(t *testing.T) {
		h := newHandler(&service.User{ID: 1, TotpEnabled: true}, nil)
		rec := httptest.NewRecorder()
		c := newContext(rec)
		c.Set("auth_method", service.AuditAuthMethodAdminAPIKey)

		require.False(t, h.ensureActorTotpForStepUp(c))
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Contains(t, rec.Body.String(), "STEP_UP_ADMIN_API_KEY_FORBIDDEN")
	})

	t.Run("missing user service fails closed", func(t *testing.T) {
		repo := &settingHandlerRepoStub{values: map[string]string{}}
		h := NewSettingHandler(service.NewSettingService(repo, &config.Config{}), nil, nil, nil, nil, nil, nil)
		rec := httptest.NewRecorder()
		c := newContext(rec)
		h.SetStepUpDeps(nil, nil)

		require.False(t, h.ensureActorTotpForStepUp(c))
		require.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("user lookup error is returned", func(t *testing.T) {
		h := newHandler(nil, errors.New("user lookup failed"))
		rec := httptest.NewRecorder()

		require.False(t, h.ensureActorTotpForStepUp(newContext(rec)))
		require.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("actor without totp is rejected", func(t *testing.T) {
		h := newHandler(&service.User{ID: 1, TotpEnabled: false}, nil)
		rec := httptest.NewRecorder()

		require.False(t, h.ensureActorTotpForStepUp(newContext(rec)))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "STEP_UP_ENABLE_REQUIRES_TOTP")
	})

	t.Run("actor with totp is accepted", func(t *testing.T) {
		h := newHandler(&service.User{ID: 1, TotpEnabled: true}, nil)
		rec := httptest.NewRecorder()

		require.True(t, h.ensureActorTotpForStepUp(newContext(rec)))
	})
}

func TestEqualNullableFloat(t *testing.T) {
	five := 5.0
	five2 := 5.0
	ten := 10.0
	cases := []struct {
		a, b *float64
		want bool
	}{
		{nil, nil, true},
		{&five, nil, false},
		{nil, &five, false},
		{&five, &five2, true},
		{&five, &ten, false},
	}
	for _, c := range cases {
		if got := equalNullableFloat(c.a, c.b); got != c.want {
			t.Errorf("equalNullableFloat(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestEqualPlatformQuotaSettings_DetectsPerWindowChange(t *testing.T) {
	five := 5.0
	ten := 10.0
	before := map[string]*service.DefaultPlatformQuotaSetting{
		"anthropic": {DailyLimitUSD: &five},
	}
	after := map[string]*service.DefaultPlatformQuotaSetting{
		"anthropic": {DailyLimitUSD: &ten},
	}
	if equalPlatformQuotaSettings(before, after) {
		t.Error("expected unequal")
	}
}

func TestAppendAuthSourceDefaultChanges_DetectsPerWindow(t *testing.T) {
	five := 5.0
	ten := 10.0
	before := &service.AuthSourceDefaultSettings{
		LinuxDo: service.ProviderDefaultGrantSettings{
			PlatformQuotas: map[string]*service.DefaultPlatformQuotaSetting{
				"anthropic": {DailyLimitUSD: &five},
			},
		},
	}
	after := &service.AuthSourceDefaultSettings{
		LinuxDo: service.ProviderDefaultGrantSettings{
			PlatformQuotas: map[string]*service.DefaultPlatformQuotaSetting{
				"anthropic": {DailyLimitUSD: &ten},
			},
		},
	}

	changed := appendAuthSourceDefaultChanges([]string{}, before, after)
	// 改动 B5：整体替换语义，审计 log 发单个 JSON key，而非展开 84 个扁平 key。
	key := service.SettingKeyAuthSourcePlatformQuotas("linuxdo")
	found := false
	for _, k := range changed {
		if k == key {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %q in changed, got %v", key, changed)
	}
}

// TestSettingHandler_AuthSourcePlatformQuotas_PutGetRoundTrip 验证 Bug A 修复：
// PUT 发 auth_source_default_email_platform_quotas，GET 能读回相同值（端到端往返）。
func TestSettingHandler_AuthSourcePlatformQuotas_PutGetRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{
		values: map[string]string{
			service.SettingKeyPromoCodeEnabled: "true",
		},
	}
	svc := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	handler := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

	// PUT：发 email platform quota（openai monthly=20）
	putBody := map[string]any{
		"auth_source_default_email_platform_quotas": map[string]any{
			"openai": map[string]any{
				"monthly": 20,
			},
		},
	}
	rawBody, err := json.Marshal(putBody)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", bytes.NewReader(rawBody))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.UpdateSettings(c)
	require.Equal(t, http.StatusOK, rec.Code)

	// 验证 DB 中写入了 JSON key
	jsonKey := service.SettingKeyAuthSourcePlatformQuotas("email")
	require.NotEmpty(t, repo.values[jsonKey], "expected JSON key to be written to DB")

	// GET：验证响应中 auth_source_default_email_platform_quotas.openai.monthly = 20
	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	handler.GetSettings(c2)
	require.Equal(t, http.StatusOK, rec2.Code)

	var resp response.Response
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &resp))
	data, ok := resp.Data.(map[string]any)
	require.True(t, ok)

	emailPQ, ok := data["auth_source_default_email_platform_quotas"].(map[string]any)
	require.True(t, ok, "expected auth_source_default_email_platform_quotas to be a map")
	openaiPQ, ok := emailPQ["openai"].(map[string]any)
	require.True(t, ok, "expected openai entry in email platform quotas")
	monthly, ok := openaiPQ["monthly"].(float64)
	require.True(t, ok, "expected monthly to be float64")
	require.Equal(t, float64(20), monthly, "expected openai monthly=20")
}
