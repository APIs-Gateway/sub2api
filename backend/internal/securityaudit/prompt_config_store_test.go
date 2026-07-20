package securityaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type configTestSettings struct {
	values map[string]string
	err    error
}

func (s *configTestSettings) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}
func (s *configTestSettings) GetValue(context.Context, string) (string, error) {
	return "", service.ErrSettingNotFound
}
func (s *configTestSettings) Set(context.Context, string, string) error { return nil }
func (s *configTestSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	values := make(map[string]string, len(s.values))
	for key, value := range s.values {
		values[key] = value
	}
	return values, nil
}
func (s *configTestSettings) SetMultiple(context.Context, map[string]string) error { return nil }
func (s *configTestSettings) GetAll(context.Context) (map[string]string, error)    { return nil, nil }
func (s *configTestSettings) Delete(context.Context, string) error                 { return nil }

type fixedTestClock struct{ now time.Time }

func (c fixedTestClock) Now() time.Time { return c.now }

func enabledStorageJSON(t *testing.T, blocking bool) string {
	t.Helper()
	cfg := DefaultStorageConfig()
	cfg.Enabled = true
	cfg.BlockingEnabled = blocking
	cfg.Endpoints = []StorageEndpoint{{ID: "guard", Name: "Guard", Protocol: "openai_compatible", BaseURL: "http://127.0.0.1:18080", Model: DefaultGuardModel, TimeoutMS: 1000, InputLimit: 1024, Enabled: true}}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	return string(raw)
}

func TestConfigManagerReloadRuntimeAndFailClosed(t *testing.T) {
	clock := fixedTestClock{now: time.Unix(1700000000, 0).UTC()}
	settings := &configTestSettings{values: map[string]string{
		SettingKeyPromptAuditConfig: enabledStorageJSON(t, false), SettingKeyRiskControl: "true",
	}}
	manager := &ConfigManager{settings: settings, encryptor: prefixEncryptor{}, clock: clock}
	require.NoError(t, manager.Reload(context.Background()))
	active, ok := manager.Active()
	require.True(t, ok)
	require.Equal(t, ModeAsync, active.EffectiveMode())
	expected, activeVersion, loadedAt, loadError := manager.RuntimeState()
	require.Equal(t, int64(1), expected)
	require.Equal(t, int64(1), activeVersion)
	require.Equal(t, clock.now, *loadedAt)
	require.Empty(t, loadError)
	require.False(t, manager.BlockingActivationDegraded())

	settings.err = errors.New("settings unavailable")
	require.Error(t, manager.Reload(context.Background()))
	require.False(t, manager.BlockingActivationDegraded())
	require.Equal(t, ModeAsync, manager.EffectiveMode())
	_, _, _, loadError = manager.RuntimeState()
	require.Equal(t, "Prompt Audit configuration could not be loaded", loadError)

	cold := &ConfigManager{settings: &configTestSettings{err: errors.New("cold start")}, clock: clock}
	require.Error(t, cold.Reload(context.Background()))
	require.Equal(t, ModeOff, cold.EffectiveMode())
}

func TestConfigManagerStartupLoadFailureDoesNotBlockWhenBlockingNotIntended(t *testing.T) {
	// Settings unavailable and no prior blocking intent: stay ModeOff so the
	// gateway remains usable and admins can still disable/configure Prompt Audit.
	manager := NewConfigManager(nil, &configTestSettings{err: errors.New("settings unavailable")}, nil, prefixEncryptor{})
	err := manager.Start(context.Background())
	require.Error(t, err)
	require.True(t, manager.configUntrusted.Load())
	require.False(t, manager.BlockingActivationDegraded())
	require.Equal(t, ModeOff, manager.EffectiveMode())

	service := &PromptService{config: manager, evaluator: NewGuardEvaluator(nil, nil, nil)}
	decision, evalErr := service.Evaluate(context.Background(), Request{
		Protocol: "openai_chat_completions",
		Body:     []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	})
	require.NoError(t, evalErr)
	require.NotNil(t, decision)
	require.Equal(t, DecisionAllow, decision.Kind)
	require.NoError(t, manager.Shutdown(context.Background()))
}

func TestConfigManagerStartupLoadFailureFailsClosedWhenBlockingIntended(t *testing.T) {
	manager := NewConfigManager(nil, &configTestSettings{err: errors.New("settings unavailable")}, nil, prefixEncryptor{})
	// Simulate intent observed before a later load failure (e.g. decrypt error).
	manager.observeExpectedState(`{"enabled":true,"blocking_enabled":true,"config_version":3}`, true)
	manager.markConfigUntrusted()
	require.True(t, manager.BlockingActivationDegraded())
	require.Equal(t, ModeBlocking, manager.EffectiveMode())

	service := &PromptService{config: manager, evaluator: NewGuardEvaluator(nil, nil, nil)}
	decision, err := service.Evaluate(context.Background(), Request{
		Protocol: "openai_chat_completions",
		Body:     []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	})
	require.Error(t, err)
	require.Nil(t, decision)
	var guardErr *GuardError
	require.ErrorAs(t, err, &guardErr)
	require.Equal(t, ErrorCodeUnavailable, guardErr.Code)

	require.NoError(t, manager.Shutdown(context.Background()))
}

func TestConfigManagerUntrustedClearsOnSuccessfulDisable(t *testing.T) {
	// After a degraded fail-closed period, saving disabled config must restore ModeOff.
	manager := &ConfigManager{encryptor: prefixEncryptor{}, clock: fixedTestClock{}}
	manager.observeExpectedState(`{"enabled":true,"blocking_enabled":true,"config_version":5}`, true)
	manager.markConfigUntrusted()
	require.Equal(t, ModeBlocking, manager.EffectiveMode())

	// Install a trusted disabled snapshot the same way Save does after commit.
	disabled := DefaultStorageConfig()
	disabled.ConfigVersion = 6
	disabled.Enabled = false
	disabled.BlockingEnabled = false
	active, err := ActiveFromStorage(disabled, true, manager.encryptor)
	require.NoError(t, err)
	manager.expected.Store(disabled.ConfigVersion)
	manager.expectedBlocking.Store(false)
	manager.snapshot.Store(&activeConfigSnapshot{storage: disabled, active: active, loadedAt: manager.clock.Now()})
	manager.configUntrusted.Store(false)

	require.False(t, manager.BlockingActivationDegraded())
	require.Equal(t, ModeOff, manager.EffectiveMode())

	service := &PromptService{config: manager, evaluator: NewGuardEvaluator(nil, nil, nil)}
	decision, evalErr := service.Evaluate(context.Background(), Request{
		Protocol: "openai_chat_completions",
		Body:     []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	})
	require.NoError(t, evalErr)
	require.Equal(t, DecisionAllow, decision.Kind)

	require.NoError(t, manager.Shutdown(context.Background()))
}

func TestConfigManagerUntrustedWithoutBlockingDoesNotForceBlockingMode(t *testing.T) {
	manager := &ConfigManager{}
	manager.observeExpectedState(`{"enabled":true,"blocking_enabled":false,"config_version":2}`, true)
	manager.markConfigUntrusted()
	require.False(t, manager.expectedBlocking.Load())
	require.False(t, manager.BlockingActivationDegraded())
	require.Equal(t, ModeOff, manager.EffectiveMode(), "async intent + untrusted must not force blocking unavailable")
}

func TestConfigManagerBlockingIntentFailsClosedUntilActivation(t *testing.T) {
	manager := &ConfigManager{}
	manager.observeExpectedState(`{"enabled":true,"blocking_enabled":false,"config_version":42}`, true)
	require.Equal(t, ModeOff, manager.EffectiveMode())
	require.False(t, manager.BlockingActivationDegraded())

	manager.observeExpectedState(`{"enabled":true,"blocking_enabled":true,"config_version":43}`, false)
	require.Equal(t, ModeOff, manager.EffectiveMode())

	manager.observeExpectedState(`{"enabled":true,"blocking_enabled":true,"config_version":44}`, true)
	require.Equal(t, ModeBlocking, manager.EffectiveMode())
	require.True(t, manager.BlockingActivationDegraded())

	manager.observeExpectedState(`{"enabled":true`, true)
	require.Equal(t, ModeBlocking, manager.EffectiveMode())
}

func TestConfigManagerStaleWeakerSnapshotFailsClosed(t *testing.T) {
	manager := &ConfigManager{}
	manager.snapshot.Store(&activeConfigSnapshot{
		active:  ActiveConfig{RiskControlEnabled: true, Enabled: true, BlockingEnabled: false, ConfigVersion: 1},
		storage: DefaultStorageConfig(), loadedAt: fixedTestClock{}.Now(),
	})
	manager.expected.Store(2)
	manager.expectedBlocking.Store(true)

	require.True(t, manager.BlockingActivationDegraded())
	require.Equal(t, ModeBlocking, manager.EffectiveMode())
}

func TestConfigManagerLifecycleAndHelpers(t *testing.T) {
	clock := fixedTestClock{now: time.Unix(1700000000, 0).UTC()}
	settings := &configTestSettings{values: map[string]string{
		SettingKeyPromptAuditConfig: enabledStorageJSON(t, false), SettingKeyRiskControl: "true",
	}}
	manager := &ConfigManager{settings: settings, encryptor: prefixEncryptor{}, clock: clock}
	manager.observeExpectedState("", true)
	expected, _, _, _ := manager.RuntimeState()
	require.Equal(t, int64(1), expected)
	manager.observeExpectedState(`{"enabled":true,"blocking_enabled":true,"config_version":9}`, true)
	expected, _, _, _ = manager.RuntimeState()
	require.Equal(t, int64(9), expected)
	manager.observeExpectedState("not json", true)
	require.NoError(t, manager.Start(context.Background()))
	require.NoError(t, manager.Start(context.Background()))
	require.NoError(t, manager.Shutdown(context.Background()))

	var nilManager *ConfigManager
	require.Error(t, nilManager.Start(context.Background()))
	require.NoError(t, nilManager.Shutdown(context.Background()))
	require.Error(t, nilManager.Reload(context.Background()))
	require.Equal(t, ModeOff, nilManager.EffectiveMode())
	require.False(t, func() bool { _, ok := nilManager.Active(); return ok }())
	public := nilManager.Public()
	require.Equal(t, ModeOff, public.EffectiveMode)
	expected, activeVersion, loadedAt, loadError := nilManager.RuntimeState()
	require.Equal(t, int64(1), expected)
	require.Zero(t, activeVersion)
	require.Nil(t, loadedAt)
	require.Equal(t, "config_manager_unavailable", loadError)
}

func TestConfigManagerBuildNextStoragePreservesAndChangesTokens(t *testing.T) {
	manager := &ConfigManager{encryptor: prefixEncryptor{}, clock: fixedTestClock{now: time.Unix(1700000000, 0).UTC()}}
	current := DefaultStorageConfig()
	current.Endpoints = []StorageEndpoint{{ID: "guard-one", TokenCiphertext: "enc:old"}}

	next, err := manager.buildNextStorage(current, promptAuditUpdateRequest(1, 2, "new-token"), 42)
	require.NoError(t, err)
	require.Equal(t, "enc:new-token", next.Endpoints[0].TokenCiphertext)

	clear := promptAuditUpdateRequest(1, 2, "")
	clear.Endpoints[0].ClearToken = true
	next, err = manager.buildNextStorage(current, clear, 42)
	require.NoError(t, err)
	require.Empty(t, next.Endpoints[0].TokenCiphertext)

	preserve := promptAuditUpdateRequest(1, 2, "")
	next, err = manager.buildNextStorage(current, preserve, 42)
	require.NoError(t, err)
	require.Equal(t, "enc:old", next.Endpoints[0].TokenCiphertext)
}

func TestConfigManagerSaveFailurePaths(t *testing.T) {
	validRequest := promptAuditUpdateRequest(1, 1, "")
	validRaw := enabledStorageJSON(t, false)

	tests := []struct {
		name   string
		setup  func(sqlmock.Sqlmock)
		mutate func(*UpdateConfigRequest)
		check  func(*testing.T, error)
	}{
		{
			name: "begin failure",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin().WillReturnError(errors.New("begin failed"))
			},
		},
		{
			name: "read failure",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT value FROM settings").WithArgs(SettingKeyPromptAuditConfig).WillReturnError(errors.New("read failed"))
				mock.ExpectRollback()
			},
		},
		{
			name: "malformed stored config",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT value FROM settings").WithArgs(SettingKeyPromptAuditConfig).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("not-json"))
				mock.ExpectRollback()
			},
		},
		{
			name:   "version conflict",
			mutate: func(req *UpdateConfigRequest) { req.ExpectedConfigVersion = 2 },
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT value FROM settings").WithArgs(SettingKeyPromptAuditConfig).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(validRaw))
				mock.ExpectRollback()
			},
			check: func(t *testing.T, err error) {
				require.Error(t, err)
				require.Equal(t, ErrorCodeConfigConflict, infraerrors.Reason(err))
			},
		},
		{
			name:   "invalid update",
			mutate: func(req *UpdateConfigRequest) { req.Strategy = "round_robin" },
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT value FROM settings").WithArgs(SettingKeyPromptAuditConfig).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(validRaw))
				mock.ExpectRollback()
			},
		},
		{
			name: "compare and swap update failure",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT value FROM settings").WithArgs(SettingKeyPromptAuditConfig).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(validRaw))
				mock.ExpectExec("UPDATE settings SET value=").WithArgs(sqlmock.AnyArg(), SettingKeyPromptAuditConfig, validRaw).WillReturnError(errors.New("update failed"))
				mock.ExpectRollback()
			},
		},
		{
			name: "compare and swap conflict",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT value FROM settings").WithArgs(SettingKeyPromptAuditConfig).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(validRaw))
				mock.ExpectExec("UPDATE settings SET value=").WithArgs(sqlmock.AnyArg(), SettingKeyPromptAuditConfig, validRaw).WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectRollback()
			},
			check: func(t *testing.T, err error) {
				require.Equal(t, ErrorCodeConfigConflict, infraerrors.Reason(err))
			},
		},
		{
			name: "insert failure",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT value FROM settings").WithArgs(SettingKeyPromptAuditConfig).WillReturnError(sql.ErrNoRows)
				mock.ExpectExec("INSERT INTO settings").WithArgs(SettingKeyPromptAuditConfig, sqlmock.AnyArg()).WillReturnError(errors.New("insert failed"))
				mock.ExpectRollback()
			},
		},
		{
			name: "concurrent insert conflict",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT value FROM settings").WithArgs(SettingKeyPromptAuditConfig).WillReturnError(sql.ErrNoRows)
				mock.ExpectExec("INSERT INTO settings").WithArgs(SettingKeyPromptAuditConfig, sqlmock.AnyArg()).WillReturnError(errors.New("duplicate key"))
				mock.ExpectRollback()
			},
			check: func(t *testing.T, err error) {
				require.Equal(t, ErrorCodeConfigConflict, infraerrors.Reason(err))
			},
		},
		{
			name: "commit failure",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT value FROM settings").WithArgs(SettingKeyPromptAuditConfig).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(validRaw))
				mock.ExpectExec("UPDATE settings SET value=").WithArgs(sqlmock.AnyArg(), SettingKeyPromptAuditConfig, validRaw).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock := newPromptStorageSQLMock(t)
			manager := NewConfigManager(db, &configTestSettings{values: map[string]string{SettingKeyRiskControl: "true"}}, nil, prefixEncryptor{})
			tt.setup(mock)
			request := validRequest
			if tt.mutate != nil {
				tt.mutate(&request)
			}
			_, err := manager.Save(context.Background(), request, 9)
			if tt.check != nil {
				tt.check(t, err)
			} else {
				require.Error(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestConfigManagerSaveAndPublicGuards(t *testing.T) {
	_, err := (&ConfigManager{}).Save(context.Background(), UpdateConfigRequest{}, 1)
	require.Error(t, err)
	_, err = (&ConfigManager{db: nil, encryptor: prefixEncryptor{}}).Save(context.Background(), UpdateConfigRequest{ExpectedConfigVersion: 1}, 1)
	require.Error(t, err)
	_, err = (&ConfigManager{db: &sql.DB{}, encryptor: prefixEncryptor{}}).Save(context.Background(), UpdateConfigRequest{ExpectedConfigVersion: 0}, 1)
	require.Error(t, err)

	db, mock := newPromptStorageSQLMock(t)
	settings := &configTestSettings{values: map[string]string{SettingKeyRiskControl: "true"}}
	manager := NewConfigManager(db, settings, nil, prefixEncryptor{})
	manager.clock = fixedTestClock{now: time.Unix(1700000000, 0).UTC()}
	req := promptAuditUpdateRequest(1, 1, "save-token")
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT value FROM settings").WithArgs(SettingKeyPromptAuditConfig).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO settings").WithArgs(SettingKeyPromptAuditConfig, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	public, err := manager.Save(context.Background(), req, 9)
	require.NoError(t, err)
	require.Equal(t, int64(2), public.ConfigVersion)
	require.True(t, public.Endpoints[0].HasToken)
	active, ok := manager.Active()
	require.True(t, ok)
	require.Equal(t, int64(2), active.ConfigVersion)
	expected, activeVersion, _, _ := manager.RuntimeState()
	require.Equal(t, int64(2), expected)
	require.Equal(t, int64(2), activeVersion)
	require.Equal(t, "save-token", mustDecryptForTest(t, manager, public.Endpoints[0].ID))

	plain, err := manager.Encrypt("value")
	require.NoError(t, err)
	require.Equal(t, "enc:value", plain)
	decoded, err := manager.Decrypt(plain)
	require.NoError(t, err)
	require.Equal(t, "value", decoded)
	require.Equal(t, ModeOff, (&ConfigManager{}).Public().EffectiveMode)

	var output strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	LogError(EventConfigReloadDegraded, map[string]any{"status": "failed"})
	require.Contains(t, output.String(), EventConfigReloadDegraded)
}

func TestConfigManagerSaveUsesPortableCASOnSQLite(t *testing.T) {
	db := newPromptStorageSQLite(t)
	_, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	manager := NewConfigManager(db, &configTestSettings{values: map[string]string{SettingKeyRiskControl: "true"}}, nil, prefixEncryptor{})

	first, err := manager.Save(context.Background(), promptAuditUpdateRequest(1, 1, "first-token"), 9)
	require.NoError(t, err)
	require.Equal(t, int64(2), first.ConfigVersion)

	second, err := manager.Save(context.Background(), promptAuditUpdateRequest(2, 2, "second-token"), 9)
	require.NoError(t, err)
	require.Equal(t, int64(3), second.ConfigVersion)

	_, err = manager.Save(context.Background(), promptAuditUpdateRequest(2, 3, "stale-token"), 9)
	require.Equal(t, ErrorCodeConfigConflict, infraerrors.Reason(err))
}

func mustDecryptForTest(t *testing.T, manager *ConfigManager, endpointID string) string {
	t.Helper()
	active, ok := manager.Active()
	require.True(t, ok)
	for _, endpoint := range active.Endpoints {
		if endpoint.ID == endpointID {
			return endpoint.Token
		}
	}
	return ""
}
