package securityaudit

import (
	"encoding/json"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type prefixEncryptor struct{}

func (prefixEncryptor) Encrypt(value string) (string, error) { return "enc:" + value, nil }
func (prefixEncryptor) Decrypt(value string) (string, error) { return value[4:], nil }

func promptAuditUpdateRequest(version int64, workerCount int, token string) UpdateConfigRequest {
	return UpdateConfigRequest{
		ExpectedConfigVersion: version,
		Enabled:               true,
		Strategy:              "priority",
		WorkerCount:           workerCount,
		QueueCapacity:         64,
		Scanners:              []string{"pii", "jailbreak"},
		AllGroups:             true,
		Endpoints: []UpdateEndpoint{{
			ID: "guard-one", Name: "Guard One", Protocol: "openai_compatible",
			BaseURL: "http://127.0.0.1:18080", Token: token, TimeoutMS: 1000,
			InputLimit: 1024, Enabled: true,
		}},
	}
}

func TestDefaultConfigIsOff(t *testing.T) {
	storage, err := ParseStorageConfig("")
	require.NoError(t, err)
	require.False(t, storage.Enabled)
	active, err := ActiveFromStorage(storage, true, prefixEncryptor{})
	require.NoError(t, err)
	require.Equal(t, ModeOff, active.EffectiveMode())
	require.Equal(t, AllScannerIDs, storage.Scanners)
	publicJSON, err := json.Marshal(PublicFromStorage(storage, true, nil))
	require.NoError(t, err)
	require.Contains(t, string(publicJSON), `"group_ids":[]`)
	require.Contains(t, string(publicJSON), `"endpoints":[]`)
}

func TestConfigRejectsBlockingWithoutAudit(t *testing.T) {
	storage := DefaultStorageConfig()
	storage.BlockingEnabled = true
	require.Error(t, validateStorageConfig(storage))
}

func TestPublicConfigNeverMarshalsToken(t *testing.T) {
	storage := DefaultStorageConfig()
	storage.Endpoints = []StorageEndpoint{{ID: "one", Name: "One", Protocol: "openai_compatible", BaseURL: "http://127.0.0.1:8080", Model: DefaultGuardModel, TokenCiphertext: "GUARD_TOKEN_CANARY_SECRET", TimeoutMS: 1000, InputLimit: 1000, Enabled: true}}
	public := PublicFromStorage(storage, true, nil)
	raw, err := json.Marshal(public)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "GUARD_TOKEN_CANARY_SECRET")
	require.NotContains(t, string(raw), "ciphertext")
	require.True(t, public.Endpoints[0].HasToken)
}

func TestEffectiveModeTruthTable(t *testing.T) {
	tests := []struct {
		risk, enabled, blocking bool
		want                    Mode
	}{
		{false, false, false, ModeOff}, {false, true, true, ModeOff}, {true, false, false, ModeOff},
		{true, true, false, ModeAsync}, {true, true, true, ModeBlocking},
	}
	for _, tt := range tests {
		cfg := ActiveConfig{RiskControlEnabled: tt.risk, Enabled: tt.enabled, BlockingEnabled: tt.blocking}
		require.Equal(t, tt.want, cfg.EffectiveMode())
	}
}

func TestParseLegacyConfigDefaultsMissingFieldsWithoutEnablingBlocking(t *testing.T) {
	storage, err := ParseStorageConfig(`{"enabled":false,"config_version":9}`)
	require.NoError(t, err)
	require.False(t, storage.BlockingEnabled)
	require.Equal(t, "priority", storage.Strategy)
	require.Equal(t, DefaultWorkerCount, storage.WorkerCount)
	require.Equal(t, DefaultQueueCapacity, storage.QueueCapacity)
	require.Equal(t, AllScannerIDs, storage.Scanners)
	require.True(t, storage.AllGroups)
}

func TestUpdateConfigStrictBoundsAndKnownValues(t *testing.T) {
	valid := promptAuditUpdateRequest(1, 1, "")
	require.NoError(t, validateUpdateConfigRequest(valid))

	tests := []struct {
		name   string
		mutate func(*UpdateConfigRequest)
		reason string
	}{
		{name: "strategy", mutate: func(req *UpdateConfigRequest) { req.Strategy = "round_robin" }, reason: "prompt_audit_invalid_strategy"},
		{name: "worker low", mutate: func(req *UpdateConfigRequest) { req.WorkerCount = 0 }, reason: "prompt_audit_invalid_worker_count"},
		{name: "worker high", mutate: func(req *UpdateConfigRequest) { req.WorkerCount = MaxWorkerCount + 1 }, reason: "prompt_audit_invalid_worker_count"},
		{name: "capacity low", mutate: func(req *UpdateConfigRequest) { req.QueueCapacity = 0 }, reason: "prompt_audit_invalid_queue_capacity"},
		{name: "capacity high", mutate: func(req *UpdateConfigRequest) { req.QueueCapacity = MaxQueueCapacity + 1 }, reason: "prompt_audit_invalid_queue_capacity"},
		{name: "unknown scanner", mutate: func(req *UpdateConfigRequest) { req.Scanners = []string{"made_up"} }, reason: "prompt_audit_invalid_scanner"},
		{name: "group required", mutate: func(req *UpdateConfigRequest) { req.AllGroups = false; req.GroupIDs = nil }, reason: "prompt_audit_groups_required"},
		{name: "group positive", mutate: func(req *UpdateConfigRequest) { req.AllGroups = false; req.GroupIDs = []int64{0} }, reason: "prompt_audit_invalid_group"},
		{name: "timeout low", mutate: func(req *UpdateConfigRequest) { req.Endpoints[0].TimeoutMS = MinTimeoutMS - 1 }, reason: "prompt_audit_invalid_timeout"},
		{name: "timeout high", mutate: func(req *UpdateConfigRequest) { req.Endpoints[0].TimeoutMS = MaxTimeoutMS + 1 }, reason: "prompt_audit_invalid_timeout"},
		{name: "input low", mutate: func(req *UpdateConfigRequest) { req.Endpoints[0].InputLimit = MinInputLimit - 1 }, reason: "prompt_audit_invalid_input_limit"},
		{name: "input high", mutate: func(req *UpdateConfigRequest) { req.Endpoints[0].InputLimit = MaxInputLimit + 1 }, reason: "prompt_audit_invalid_input_limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := valid
			req.Scanners = append([]string(nil), valid.Scanners...)
			req.GroupIDs = append([]int64(nil), valid.GroupIDs...)
			req.Endpoints = append([]UpdateEndpoint(nil), valid.Endpoints...)
			tt.mutate(&req)
			err := validateUpdateConfigRequest(req)
			require.Error(t, err)
			require.Equal(t, tt.reason, infraerrors.Reason(err))
		})
	}
}
