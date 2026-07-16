//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBuildSystemSettingsUpdatesPersistsSessionBinding(t *testing.T) {
	svc := NewSettingService(&settingUpdateRepoStub{}, nil)

	updates, err := svc.buildSystemSettingsUpdates(context.Background(), &SystemSettings{SessionBindingEnabled: true})
	require.NoError(t, err)
	require.Equal(t, "true", updates[SettingKeySessionBindingEnabled])
}

func TestParseSettingsSessionBindingDefaultsToEnabled(t *testing.T) {
	svc := NewSettingService(&settingRepoStub{values: map[string]string{}}, &config.Config{})

	require.True(t, svc.parseSettings(map[string]string{}).SessionBindingEnabled)
	require.True(t, svc.parseSettings(map[string]string{
		SettingKeySessionBindingEnabled: "true",
	}).SessionBindingEnabled)
	require.False(t, svc.parseSettings(map[string]string{
		SettingKeySessionBindingEnabled: "false",
	}).SessionBindingEnabled)
}

func TestIsSessionBindingEnabledDefaultsToEnabled(t *testing.T) {
	ctx := context.Background()

	require.True(t, NewSettingService(&settingRepoStub{values: map[string]string{}}, nil).IsSessionBindingEnabled(ctx))
	require.True(t, NewSettingService(&settingRepoStub{values: map[string]string{
		SettingKeySessionBindingEnabled: "unexpected",
	}}, nil).IsSessionBindingEnabled(ctx))
	require.False(t, NewSettingService(&settingRepoStub{values: map[string]string{
		SettingKeySessionBindingEnabled: "false",
	}}, nil).IsSessionBindingEnabled(ctx))
	require.True(t, NewSettingService(&settingRepoStub{err: ErrSettingNotFound}, nil).IsSessionBindingEnabled(ctx))
}
