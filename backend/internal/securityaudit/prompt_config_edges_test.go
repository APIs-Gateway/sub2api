package securityaudit

import (
	"errors"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type failingDecryptor struct{ err error }

func (failingDecryptor) Encrypt(value string) (string, error) { return value, nil }
func (f failingDecryptor) Decrypt(string) (string, error)     { return "", f.err }

func TestParseStorageConfigNormalizesEndpointDefaults(t *testing.T) {
	cfg, err := ParseStorageConfig(`{
		"enabled":true,
		"strategy":" ",
		"worker_count":0,
		"queue_capacity":0,
		"scanners":[],
		"all_groups":true,
		"config_version":0,
		"endpoints":[{"id":" one ","name":" Guard ","protocol":" ","base_url":" http://127.0.0.1:18080/ ","model":" ","timeout_ms":0,"input_limit":0,"enabled":true}]
	}`)
	require.NoError(t, err)
	require.Equal(t, int64(1), cfg.ConfigVersion)
	require.Equal(t, "priority", cfg.Strategy)
	require.Equal(t, DefaultWorkerCount, cfg.WorkerCount)
	require.Equal(t, DefaultQueueCapacity, cfg.QueueCapacity)
	require.Equal(t, AllScannerIDs, cfg.Scanners)
	require.Equal(t, StorageEndpoint{
		ID: "one", Name: "Guard", Protocol: "openai_compatible", BaseURL: "http://127.0.0.1:18080/",
		Model: DefaultGuardModel, TimeoutMS: DefaultTimeoutMS, InputLimit: DefaultInputLimit, Enabled: true,
	}, cfg.Endpoints[0])
}

func TestParseStorageConfigRejectsMalformedAndInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		reason string
	}{
		{name: "malformed json", raw: `{"enabled":`, reason: ""},
		{name: "invalid strategy", raw: `{"strategy":"round_robin"}`, reason: "prompt_audit_invalid_strategy"},
		{name: "blocking requires enabled", raw: `{"blocking_enabled":true}`, reason: ErrorCodeRequiresEnabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseStorageConfig(tt.raw)
			require.Error(t, err)
			if tt.reason != "" {
				require.Equal(t, tt.reason, infraerrors.Reason(err))
			}
		})
	}
}

func TestValidateStorageConfigEndpointRules(t *testing.T) {
	valid := DefaultStorageConfig()
	valid.Enabled = true
	valid.Endpoints = []StorageEndpoint{{
		ID: "one", Name: "One", Protocol: "openai_compatible", BaseURL: "http://127.0.0.1:18080",
		TimeoutMS: DefaultTimeoutMS, InputLimit: DefaultInputLimit, Enabled: true,
	}}
	require.NoError(t, validateStorageConfig(valid))

	tests := []struct {
		name   string
		mutate func(*storageConfig)
		reason string
	}{
		{name: "missing endpoint id", mutate: func(cfg *storageConfig) { cfg.Endpoints[0].ID = "" }, reason: "prompt_audit_invalid_endpoint"},
		{name: "duplicate endpoint", mutate: func(cfg *storageConfig) { cfg.Endpoints = append(cfg.Endpoints, cfg.Endpoints[0]) }, reason: "prompt_audit_duplicate_endpoint"},
		{name: "unsupported protocol", mutate: func(cfg *storageConfig) { cfg.Endpoints[0].Protocol = "anthropic" }, reason: "prompt_audit_invalid_endpoint_protocol"},
		{name: "unsafe url", mutate: func(cfg *storageConfig) { cfg.Endpoints[0].BaseURL = "https://metadata.google.internal" }, reason: "prompt_audit_unsafe_base_url"},
		{name: "timeout", mutate: func(cfg *storageConfig) { cfg.Endpoints[0].TimeoutMS = MinTimeoutMS - 1 }, reason: "prompt_audit_invalid_timeout"},
		{name: "input limit", mutate: func(cfg *storageConfig) { cfg.Endpoints[0].InputLimit = MaxInputLimit + 1 }, reason: "prompt_audit_invalid_input_limit"},
		{name: "no enabled endpoint", mutate: func(cfg *storageConfig) { cfg.Endpoints[0].Enabled = false }, reason: "prompt_audit_endpoint_required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			cfg.Endpoints = append([]StorageEndpoint(nil), valid.Endpoints...)
			tt.mutate(&cfg)
			err := validateStorageConfig(cfg)
			require.Error(t, err)
			require.Equal(t, tt.reason, infraerrors.Reason(err))
		})
	}

	groups := valid
	groups.AllGroups = false
	groups.GroupIDs = nil
	require.Equal(t, "prompt_audit_groups_required", infraerrors.Reason(validateStorageConfig(groups)))
	scanners := valid
	scanners.Scanners = nil
	require.Equal(t, "prompt_audit_scanners_required", infraerrors.Reason(validateStorageConfig(scanners)))
}

func TestActiveFromStorageDecryptsAndDegradesInvalidTokens(t *testing.T) {
	cfg := DefaultStorageConfig()
	cfg.Endpoints = []StorageEndpoint{{
		ID: "one", Name: "One", Protocol: "openai_compatible", BaseURL: "http://127.0.0.1:18080",
		TokenCiphertext: "enc:secret", TimeoutMS: 1000, InputLimit: 1024, Enabled: true,
	}}
	active, err := ActiveFromStorage(cfg, true, prefixEncryptor{})
	require.NoError(t, err)
	require.Equal(t, "secret", active.Endpoints[0].Token)

	_, err = ActiveFromStorage(cfg, true, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "encryptor unavailable")

	// A single endpoint's undecryptable token (e.g. after a key rotation) must
	// not take the whole config down (issue #4887): the endpoint is marked
	// TokenInvalid and excluded from runtime use, but ActiveFromStorage still
	// succeeds so admins can see and fix it.
	failure := errors.New("decrypt failed")
	degraded, err := ActiveFromStorage(cfg, true, failingDecryptor{err: failure})
	require.NoError(t, err)
	require.True(t, degraded.Endpoints[0].TokenInvalid)
	require.False(t, degraded.Endpoints[0].Enabled)
	require.Empty(t, degraded.Endpoints[0].Token)
	require.Equal(t, []string{"one"}, degraded.InvalidTokenEndpointIDs())
}

func TestPublicFromStorageMarksInvalidTokenStatus(t *testing.T) {
	cfg := DefaultStorageConfig()
	cfg.Endpoints = []StorageEndpoint{
		{ID: "one", TokenCiphertext: "cipher"},
		{ID: "two", TokenCiphertext: "cipher"},
		{ID: "three"},
	}
	public := PublicFromStorage(cfg, true, []string{"one"})
	require.Equal(t, "invalid", public.Endpoints[0].TokenStatus)
	require.Equal(t, "configured", public.Endpoints[1].TokenStatus)
	require.Equal(t, "missing", public.Endpoints[2].TokenStatus)
}

func TestConfigCanonicalizationAndPublicTokenStatus(t *testing.T) {
	var nilConfig *storageConfig
	normalizeStorageConfig(nilConfig)
	cfg := DefaultStorageConfig()
	cfg.AllGroups = false
	cfg.GroupIDs = []int64{4, 0, 1, 4, -1}
	cfg.Scanners = []string{"PII", "pii", "unknown"}
	cfg.Endpoints = []StorageEndpoint{{ID: "one", TokenCiphertext: "cipher"}, {ID: "two"}}
	normalizeStorageConfig(&cfg)
	require.Equal(t, []int64{1, 4}, cfg.GroupIDs)
	require.Equal(t, []string{"pii"}, cfg.Scanners)
	public := PublicFromStorage(cfg, true, nil)
	require.True(t, public.Endpoints[0].HasToken)
	require.Equal(t, "configured", public.Endpoints[0].TokenStatus)
	require.False(t, public.Endpoints[1].HasToken)
	require.Equal(t, "missing", public.Endpoints[1].TokenStatus)

	allGroups := ActiveConfig{AllGroups: true}
	require.True(t, allGroups.IncludesGroup(nil))
	require.Equal(t, []int64{1, 4}, canonicalInt64s([]int64{4, 0, 1, 4, -1}))
}
