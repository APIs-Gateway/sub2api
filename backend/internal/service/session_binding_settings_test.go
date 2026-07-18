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

func TestParseSettingsSessionBindingDefaultsToDisabled(t *testing.T) {
	svc := NewSettingService(&settingRepoStub{values: map[string]string{}}, &config.Config{})

	require.False(t, svc.parseSettings(map[string]string{}).SessionBindingEnabled)
	require.True(t, svc.parseSettings(map[string]string{
		SettingKeySessionBindingEnabled: "true",
	}).SessionBindingEnabled)
	require.False(t, svc.parseSettings(map[string]string{
		SettingKeySessionBindingEnabled: "false",
	}).SessionBindingEnabled)
	require.True(t, svc.parseSettings(map[string]string{
		SettingKeyStepUpEnabled: "true",
	}).StepUpEnabled)
	require.False(t, svc.parseSettings(map[string]string{}).StepUpEnabled)
}

func TestIsSessionBindingEnabledDefaultsToDisabled(t *testing.T) {
	ctx := context.Background()

	require.False(t, NewSettingService(&settingRepoStub{values: map[string]string{}}, nil).IsSessionBindingEnabled(ctx))
	require.False(t, NewSettingService(&settingRepoStub{values: map[string]string{
		SettingKeySessionBindingEnabled: "unexpected",
	}}, nil).IsSessionBindingEnabled(ctx))
	require.False(t, NewSettingService(&settingRepoStub{values: map[string]string{
		SettingKeySessionBindingEnabled: "false",
	}}, nil).IsSessionBindingEnabled(ctx))
	require.False(t, NewSettingService(&settingRepoStub{err: ErrSettingNotFound}, nil).IsSessionBindingEnabled(ctx))
}

func TestStepUpSettingDefaultsToDisabledAndPersists(t *testing.T) {
	ctx := context.Background()
	require.False(t, NewSettingService(&settingRepoStub{values: map[string]string{}}, nil).IsStepUpEnabled(ctx))
	require.True(t, NewSettingService(&settingRepoStub{values: map[string]string{
		SettingKeyStepUpEnabled: "true",
	}}, nil).IsStepUpEnabled(ctx))
	require.False(t, NewSettingService(&settingRepoStub{err: ErrSettingNotFound}, nil).IsStepUpEnabled(ctx))

	updates, err := NewSettingService(&settingUpdateRepoStub{}, nil).buildSystemSettingsUpdates(ctx, &SystemSettings{StepUpEnabled: true})
	require.NoError(t, err)
	require.Equal(t, "true", updates[SettingKeyStepUpEnabled])
}
