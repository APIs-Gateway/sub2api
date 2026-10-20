package securityaudit

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCoreConfigMetadataAndEndpointSelection(t *testing.T) {
	cfg := ActiveConfig{
		AllGroups: false,
		GroupIDs:  []int64{1, 4},
		Endpoints: []ActiveEndpoint{
			{ID: "disabled", Enabled: false},
			{ID: "enabled", Enabled: true},
		},
	}
	groupOne, groupTwo, groupMissing := int64(1), int64(2), int64(0)
	require.True(t, cfg.IncludesGroup(&groupOne))
	require.False(t, cfg.IncludesGroup(&groupTwo))
	require.False(t, cfg.IncludesGroup(&groupMissing))
	require.False(t, cfg.IncludesGroup(nil))
	require.Equal(t, []ActiveEndpoint{{ID: "enabled", Enabled: true}}, cfg.EnabledEndpoints())

	storage := DefaultStorageConfig()
	storage.GroupIDs = []int64{4, 1}
	storage.Scanners = []string{"PII", "jailbreak", "unknown"}
	storage.Endpoints = []StorageEndpoint{{ID: "one"}}
	summary := changeSummary(storage)
	require.Contains(t, summary, `"endpoint_count":1`)
	require.Contains(t, summary, `"group_count":2`)
	require.Len(t, ScannerDefinitions(), len(AllScannerIDs))
}

func TestCoreSecurityAndLegacyHelpers(t *testing.T) {
	modelsURL, err := ModelsURL("https://guard.example.com/v1/")
	require.NoError(t, err)
	require.Equal(t, "https://guard.example.com/v1/models", modelsURL)

	wrapped := errors.New("wrapped")
	guardErr := &GuardError{Code: ErrorCodeUnavailable, Cause: wrapped}
	require.Equal(t, ErrorCodeUnavailable, guardErr.Error())
	require.ErrorIs(t, guardErr, wrapped)

	adapter := NewLegacyModerationAdapter(nil)
	decision, err := adapter.Check(context.Background(), Request{})
	require.NoError(t, err)
	require.Nil(t, decision)
}

func TestCoreIssueSummariesAndRequestClone(t *testing.T) {
	groupID := int64(7)
	req := Request{Body: []byte("body"), GroupID: &groupID}
	clone := req.Clone()
	clone.Body[0] = 'B'
	*clone.GroupID = 8
	require.Equal(t, []byte("body"), req.Body)
	require.Equal(t, int64(7), *req.GroupID)

	result := NormalizedResult{
		RiskLevel:       RiskHigh,
		Action:          ActionWarn,
		Categories:      []string{"pii"},
		ScannerEvidence: map[string]string{"pii": "email@example.com"},
		ScannerScores:   map[string]float64{"pii": 0.9},
	}
	summaries := BuildIssueSummaries(result)
	require.Len(t, summaries, 1)
	require.Equal(t, "pii", summaries[0].Category)
	require.Equal(t, "警告", summaries[0].ActionLabel)

	unknown := BuildIssueSummaries(NormalizedResult{UnknownCategories: []string{"future-risk"}, RiskLevel: RiskCritical, Action: ActionBlock})
	require.Len(t, unknown, 1)
	require.Equal(t, "unknown_unsafe", unknown[0].ScannerID)
}
