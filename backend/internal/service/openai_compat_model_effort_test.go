package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

// TestForwardAsAnthropic_PreservesMaxForFinalGPT56ResponsesModel in
// openai_compat_model_test.go already drives openAICompatAnthropicReasoningEffort
// end-to-end through the max+GPT-5.6, max+legacy-model, and no-effort branches.
// This adds direct unit coverage for the guard clauses that full request only
// reaches indirectly: a nil request and a request with no OutputConfig at all.
func TestOpenAICompatAnthropicReasoningEffort_GuardClauses(t *testing.T) {
	require.Equal(t, "medium", openAICompatAnthropicReasoningEffort(nil, "gpt-5.6", "medium"))

	reqNoOutputConfig := &apicompat.AnthropicRequest{}
	require.Equal(t, "medium", openAICompatAnthropicReasoningEffort(reqNoOutputConfig, "gpt-5.6", "medium"))

	reqNonMaxEffort := &apicompat.AnthropicRequest{OutputConfig: &apicompat.AnthropicOutputConfig{Effort: "high"}}
	require.Equal(t, "high", openAICompatAnthropicReasoningEffort(reqNonMaxEffort, "gpt-5.6", "high"))

	reqMaxUpperCase := &apicompat.AnthropicRequest{OutputConfig: &apicompat.AnthropicOutputConfig{Effort: "MAX"}}
	require.Equal(t, "max", openAICompatAnthropicReasoningEffort(reqMaxUpperCase, "gpt-5.6", "xhigh"))
}
