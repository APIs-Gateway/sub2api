//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type forwardedClientIPSettingsRepoStub struct {
	values  map[string]string
	updates map[string]string
	getErr  error
	setErr  error
}

func (s *forwardedClientIPSettingsRepoStub) Get(context.Context, string) (*Setting, error) {
	panic("unexpected Get call")
}

func (s *forwardedClientIPSettingsRepoStub) GetValue(context.Context, string) (string, error) {
	panic("unexpected GetValue call")
}

func (s *forwardedClientIPSettingsRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

func (s *forwardedClientIPSettingsRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.values, nil
}

func (s *forwardedClientIPSettingsRepoStub) SetMultiple(_ context.Context, settings map[string]string) error {
	s.updates = settings
	return s.setErr
}

func (s *forwardedClientIPSettingsRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *forwardedClientIPSettingsRepoStub) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func TestSettingService_UpdateSettings_ForwardedClientIPHeaders(t *testing.T) {
	repo := &settingUpdateRepoStub{}
	svc := NewSettingService(repo, &config.Config{})

	err := svc.UpdateSettings(context.Background(), &SystemSettings{
		ForwardedClientIPHeaders: []string{" x-cdn-client-ip ", "X-CDN-CLIENT-IP", "True-Client-IP"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"X-Cdn-Client-Ip", "True-Client-Ip"}, svc.cfg.ForwardedClientIPSettings().Headers)
	require.JSONEq(t, `["X-Cdn-Client-Ip","True-Client-Ip"]`, repo.updates[SettingKeyForwardedClientIPHeaders])
}

func TestSettingService_UpdateSettings_RejectsInvalidForwardedClientIPHeader(t *testing.T) {
	repo := &settingUpdateRepoStub{}
	svc := NewSettingService(repo, &config.Config{})

	err := svc.UpdateSettings(context.Background(), &SystemSettings{
		ForwardedClientIPHeaders: []string{"X Invalid"},
	})
	require.Error(t, err)
	require.Equal(t, "INVALID_FORWARDED_CLIENT_IP_HEADERS", infraerrors.Reason(err))
	require.Nil(t, repo.updates)
}

func TestSettingService_ParseSettings_ForwardedClientIPHeaders(t *testing.T) {
	svc := NewSettingService(&settingUpdateRepoStub{}, &config.Config{})

	got := svc.parseSettings(map[string]string{
		SettingKeyAPIKeyACLTrustForwardedIP: "true",
		SettingKeyForwardedClientIPHeaders:  `["x-cdn-client-ip","X-CDN-CLIENT-IP"]`,
	})
	require.True(t, got.APIKeyACLTrustForwardedIP)
	require.Equal(t, []string{"X-Cdn-Client-Ip"}, got.ForwardedClientIPHeaders)

	invalid := svc.parseSettings(map[string]string{
		SettingKeyAPIKeyACLTrustForwardedIP: "true",
		SettingKeyForwardedClientIPHeaders:  `["X Invalid"]`,
	})
	require.False(t, invalid.APIKeyACLTrustForwardedIP)
	require.Empty(t, invalid.ForwardedClientIPHeaders)
	_, err := parseForwardedClientIPHeadersSetting("null")
	require.Error(t, err)
}

func TestSettingService_LoadForwardedClientIPSettings(t *testing.T) {
	cfg := &config.Config{}
	repo := &forwardedClientIPSettingsRepoStub{values: map[string]string{
		SettingKeyAPIKeyACLTrustForwardedIP: "true",
		SettingKeyForwardedClientIPHeaders:  `["X-Cdn-Client-IP"]`,
	}}
	svc := NewSettingService(repo, cfg)

	require.NoError(t, svc.LoadForwardedClientIPSettings(context.Background()))
	require.True(t, cfg.TrustForwardedIPForAPIKeyACL())
	require.Equal(t, []string{"X-Cdn-Client-Ip"}, cfg.ForwardedClientIPSettings().Headers)
	require.Empty(t, repo.updates)

	var decoded []string
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeyForwardedClientIPHeaders]), &decoded))
	require.Equal(t, []string{"X-Cdn-Client-IP"}, decoded)
}

func TestSettingService_LoadForwardedClientIPSettingsMigratesMissingHeaders(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetForwardedClientIPSettings(false, []string{"X-Cdn-Client-IP"})
	repo := &forwardedClientIPSettingsRepoStub{values: map[string]string{
		SettingKeyAPIKeyACLTrustForwardedIP: "true",
	}}
	svc := NewSettingService(repo, cfg)

	require.NoError(t, svc.LoadForwardedClientIPSettings(context.Background()))
	require.True(t, cfg.TrustForwardedIPForAPIKeyACL())
	require.Equal(t, []string{"X-Cdn-Client-IP"}, cfg.ForwardedClientIPSettings().Headers)
	require.JSONEq(t, `["X-Cdn-Client-IP"]`, repo.updates[SettingKeyForwardedClientIPHeaders])
}

func TestSettingService_LoadForwardedClientIPSettingsFailClosedOnReadError(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetForwardedClientIPSettings(true, []string{"X-Cdn-Client-IP"})
	svc := NewSettingService(&forwardedClientIPSettingsRepoStub{getErr: errors.New("database unavailable")}, cfg)

	err := svc.LoadForwardedClientIPSettings(context.Background())
	require.Error(t, err)
	require.False(t, cfg.TrustForwardedIPForAPIKeyACL())
	require.Empty(t, cfg.ForwardedClientIPSettings().Headers)
}

func TestSettingService_LoadForwardedClientIPSettingsReportsInvalidStoredHeaders(t *testing.T) {
	cfg := &config.Config{}
	repo := &forwardedClientIPSettingsRepoStub{values: map[string]string{
		SettingKeyAPIKeyACLTrustForwardedIP: "true",
		SettingKeyForwardedClientIPHeaders:  `["X Invalid"]`,
	}}
	svc := NewSettingService(repo, cfg)

	err := svc.LoadForwardedClientIPSettings(context.Background())
	require.Error(t, err)
	require.False(t, cfg.TrustForwardedIPForAPIKeyACL())
	require.Empty(t, cfg.ForwardedClientIPSettings().Headers)
}

func TestSettingService_LoadForwardedClientIPSettingsKeepsRuntimeOnMigrationError(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetForwardedClientIPSettings(false, []string{"X-Cdn-Client-IP"})
	repo := &forwardedClientIPSettingsRepoStub{
		values: map[string]string{SettingKeyAPIKeyACLTrustForwardedIP: "true"},
		setErr: errors.New("database read-only"),
	}
	svc := NewSettingService(repo, cfg)

	err := svc.LoadForwardedClientIPSettings(context.Background())
	require.Error(t, err)
	require.True(t, cfg.TrustForwardedIPForAPIKeyACL())
	require.Equal(t, []string{"X-Cdn-Client-IP"}, cfg.ForwardedClientIPSettings().Headers)
}
