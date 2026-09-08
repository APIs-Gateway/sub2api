//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestOmittedSettingKeys_Has(t *testing.T) {
	var nilSet OmittedSettingKeys
	require.False(t, nilSet.Has(SettingKeyRegistrationEnabled), "nil set must report every key as not omitted")

	set := NewOmittedSettingKeys(SettingKeyRegistrationEnabled, "", SettingKeySiteName)
	require.True(t, set.Has(SettingKeyRegistrationEnabled))
	require.True(t, set.Has(SettingKeySiteName))
	require.False(t, set.Has(SettingKeyEmailVerifyEnabled))
	require.Len(t, set, 2, "empty-string keys must be skipped")
}

// TestUpdateSettingsWithAuthSourceDefaultsOmitting_PreservesOmittedField covers the
// core scenario from upstream #4868 / local item17: when only some fields are
// updated, a field the request never mentioned must keep its stored value
// instead of being clobbered by *SystemSettings' Go zero value.
func TestUpdateSettingsWithAuthSourceDefaultsOmitting_PreservesOmittedField(t *testing.T) {
	repo := newMockSettingRepo()
	repo.data[SettingKeyRegistrationEnabled] = "true"
	repo.data[SettingKeySiteName] = "Old Site"
	svc := NewSettingService(repo, &config.Config{})

	// Simulates a handler that decoded a request body which only mentioned
	// site_name: RegistrationEnabled decodes to the Go zero value (false)
	// simply because the field was absent, not because anyone asked to
	// disable registration.
	next := &SystemSettings{
		SiteName:            "New Site",
		RegistrationEnabled: false,
	}
	omitted := NewOmittedSettingKeys(SettingKeyRegistrationEnabled)

	err := svc.UpdateSettingsWithAuthSourceDefaultsOmitting(context.Background(), next, &AuthSourceDefaultSettings{}, omitted)
	require.NoError(t, err)

	require.Equal(t, "true", repo.data[SettingKeyRegistrationEnabled], "omitted field must keep its stored value")
	require.Equal(t, "New Site", repo.data[SettingKeySiteName], "provided field must be updated")

	// The merged reload path (used to refresh in-process caches whenever any
	// key was omitted) must also reflect the preserved value.
	after, err := svc.GetAllSettings(context.Background())
	require.NoError(t, err)
	require.True(t, after.RegistrationEnabled, "reloaded settings must keep the preserved value")
	require.Equal(t, "New Site", after.SiteName)
}

// TestUpdateSettingsWithAuthSourceDefaultsOmitting_EmptyOmittedWritesEverything ensures
// an empty/nil omitted set behaves exactly like UpdateSettingsWithAuthSourceDefaults
// (full write), preserving backward compatibility for existing callers.
func TestUpdateSettingsWithAuthSourceDefaultsOmitting_EmptyOmittedWritesEverything(t *testing.T) {
	repo := newMockSettingRepo()
	repo.data[SettingKeyRegistrationEnabled] = "true"
	svc := NewSettingService(repo, &config.Config{})

	next := &SystemSettings{RegistrationEnabled: false, SiteName: "Site"}
	err := svc.UpdateSettingsWithAuthSourceDefaultsOmitting(context.Background(), next, &AuthSourceDefaultSettings{}, nil)
	require.NoError(t, err)
	require.Equal(t, "false", repo.data[SettingKeyRegistrationEnabled], "non-omitted fields must still be written, even to a zero value")
	require.Equal(t, "Site", repo.data[SettingKeySiteName])
}

// TestUpdateSettingsWithAuthSourceDefaultsOmitting_PropagatesBuildError confirms
// validation errors from buildSystemSettingsUpdates still surface and nothing
// gets persisted, regardless of what was marked omitted.
func TestUpdateSettingsWithAuthSourceDefaultsOmitting_PropagatesBuildError(t *testing.T) {
	repo := newMockSettingRepo()
	svc := NewSettingService(repo, &config.Config{})

	next := &SystemSettings{
		RegistrationEmailSuffixWhitelist: []string{"@invalid_domain"},
	}
	omitted := NewOmittedSettingKeys(SettingKeyRegistrationEnabled)
	err := svc.UpdateSettingsWithAuthSourceDefaultsOmitting(context.Background(), next, &AuthSourceDefaultSettings{}, omitted)
	require.Error(t, err)
	require.Equal(t, "INVALID_REGISTRATION_EMAIL_SUFFIX_WHITELIST", infraerrors.Reason(err))
	require.Empty(t, repo.data, "nothing should be persisted when validation fails")
}
