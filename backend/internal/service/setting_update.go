package service

import "context"

// OmittedSettingKeys is the set of setting storage keys a partial-update
// caller intentionally left out of its request body.
//
// SystemSettings uses plain (non-pointer) Go types for most legacy fields
// (bool/string/int/...), so buildSystemSettingsUpdates cannot tell "the
// caller explicitly set this to the zero value" apart from "the caller never
// mentioned this field at all" once the request has been decoded into a
// *SystemSettings — both look identical (Go zero value). Left unchecked,
// every admin settings save writes every one of those keys unconditionally,
// so a caller that PATCHes only a handful of fields silently resets every
// other legacy field to its zero value.
//
// The same root cause was already fixed for payment configuration in
// UpdatePaymentConfig (upstream #5133) by making UpdatePaymentConfigRequest
// fully pointer-based, so a nil field means "not provided". SystemSettings
// predates that pattern and carries roughly a hundred legacy plain fields;
// retrofitting every one of them to a pointer (and updating every call site
// that constructs a SystemSettings) is a disproportionate, high-risk
// mechanical rewrite for what upstream (#4868) fixed with a much smaller,
// additive change. OmittedSettingKeys reproduces that approach: the caller
// (typically an HTTP handler that can inspect the raw request body) figures
// out which setting keys were never mentioned, and
// UpdateSettingsWithAuthSourceDefaultsOmitting simply skips persisting those
// keys instead of writing their Go zero value.
type OmittedSettingKeys map[string]struct{}

// Has reports whether key was explicitly omitted from the incoming request.
func (o OmittedSettingKeys) Has(key string) bool {
	if o == nil {
		return false
	}
	_, ok := o[key]
	return ok
}

// UpdateSettingsWithAuthSourceDefaultsOmitting behaves like
// UpdateSettingsWithAuthSourceDefaults, except it does not persist any
// setting key present in omitted.
//
// settings/authDefaults may hold the Go zero value for fields corresponding
// to omitted keys (since the caller never touched them) — those zero values
// still flow through buildSystemSettingsUpdates/buildAuthSourceDefaultUpdates
// like normal (so unrelated validation/normalisation for other fields keeps
// working), but the resulting map entries for omitted keys are dropped
// before the write, so the database keeps its current stored value for
// those keys instead of being clobbered.
func (s *SettingService) UpdateSettingsWithAuthSourceDefaultsOmitting(ctx context.Context, settings *SystemSettings, authDefaults *AuthSourceDefaultSettings, omitted OmittedSettingKeys) error {
	updates, err := s.buildSystemSettingsUpdates(ctx, settings)
	if err != nil {
		return err
	}

	authSourceUpdates, err := s.buildAuthSourceDefaultUpdates(ctx, authDefaults)
	if err != nil {
		return err
	}
	for key, value := range authSourceUpdates {
		updates[key] = value
	}

	for key := range omitted {
		delete(updates, key)
	}

	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return err
	}

	if len(omitted) == 0 {
		s.refreshCachedSettings(settings)
		return nil
	}

	// Some keys were skipped, so settings may hold Go zero values for fields
	// the request never touched. Reload the merged, persisted view before
	// refreshing in-process caches (backend mode, gateway forwarding,
	// version bounds, ...) so those short-TTL caches don't regress the
	// omitted fields to zero.
	merged, err := s.GetAllSettings(ctx)
	if err != nil {
		return err
	}
	s.refreshCachedSettings(merged)
	return nil
}
