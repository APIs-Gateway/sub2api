package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type panelRateLimitSettingHandlerRepoStub struct {
	values map[string]string
}

func (s *panelRateLimitSettingHandlerRepoStub) Get(_ context.Context, key string) (*service.Setting, error) {
	value, ok := s.values[key]
	if !ok {
		return nil, service.ErrSettingNotFound
	}
	return &service.Setting{Key: key, Value: value}, nil
}

func (s *panelRateLimitSettingHandlerRepoStub) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *panelRateLimitSettingHandlerRepoStub) Set(_ context.Context, key, value string) error {
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[key] = value
	return nil
}

func (s *panelRateLimitSettingHandlerRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (s *panelRateLimitSettingHandlerRepoStub) SetMultiple(_ context.Context, settings map[string]string) error {
	for key, value := range settings {
		if err := s.Set(context.Background(), key, value); err != nil {
			return err
		}
	}
	return nil
}

func (s *panelRateLimitSettingHandlerRepoStub) GetAll(_ context.Context) (map[string]string, error) {
	values := make(map[string]string, len(s.values))
	for key, value := range s.values {
		values[key] = value
	}
	return values, nil
}

func (s *panelRateLimitSettingHandlerRepoStub) Delete(_ context.Context, key string) error {
	delete(s.values, key)
	return nil
}

func TestSettingHandlerPanelRateLimitSettingsRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &panelRateLimitSettingHandlerRepoStub{values: map[string]string{}}
	handler := NewSettingHandler(service.NewSettingService(repo, &config.Config{}), nil, nil, nil, nil, nil, nil)

	updateRecorder := httptest.NewRecorder()
	updateContext, _ := gin.CreateTestContext(updateRecorder)
	updateContext.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/panel-rate-limit", bytes.NewBufferString(`{"enabled":true,"user_rpm":120,"heavy_rpm":30,"exempt_admin":false,"public_ip_rpm":60}`))
	updateContext.Request.Header.Set("Content-Type", "application/json")
	handler.UpdatePanelRateLimitSettings(updateContext)

	require.Equal(t, http.StatusOK, updateRecorder.Code)
	require.Contains(t, repo.values, service.SettingKeyPanelRateLimitSettings)
	require.Contains(t, updateRecorder.Body.String(), `"user_rpm":120`)
	require.Contains(t, updateRecorder.Body.String(), `"exempt_admin":false`)

	getRecorder := httptest.NewRecorder()
	getContext, _ := gin.CreateTestContext(getRecorder)
	getContext.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/panel-rate-limit", nil)
	handler.GetPanelRateLimitSettings(getContext)

	require.Equal(t, http.StatusOK, getRecorder.Code)
	require.Contains(t, getRecorder.Body.String(), `"heavy_rpm":30`)
	require.Contains(t, getRecorder.Body.String(), `"public_ip_rpm":60`)
}

func TestSettingHandlerPanelRateLimitSettingsRejectsInvalidRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &panelRateLimitSettingHandlerRepoStub{values: map[string]string{}}
	handler := NewSettingHandler(service.NewSettingService(repo, &config.Config{}), nil, nil, nil, nil, nil, nil)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/panel-rate-limit", bytes.NewBufferString(`{"enabled":true,"user_rpm":-1}`))
	context.Request.Header.Set("Content-Type", "application/json")
	handler.UpdatePanelRateLimitSettings(context)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.NotContains(t, repo.values, service.SettingKeyPanelRateLimitSettings)
}

func TestSettingHandlerPanelRateLimitSettingsRejectsMalformedJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &panelRateLimitSettingHandlerRepoStub{values: map[string]string{}}
	handler := NewSettingHandler(service.NewSettingService(repo, &config.Config{}), nil, nil, nil, nil, nil, nil)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/panel-rate-limit", bytes.NewBufferString(`{"enabled":`))
	context.Request.Header.Set("Content-Type", "application/json")
	handler.UpdatePanelRateLimitSettings(context)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.NotContains(t, repo.values, service.SettingKeyPanelRateLimitSettings)
}
