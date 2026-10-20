package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestSupplementUnmappedOpenAIModels(t *testing.T) {
	mapped := Account{
		ID: 2, Platform: PlatformOpenAI,
		Credentials: map[string]any{"model_mapping": map[string]any{"team-coder": "gpt-5.6-sol"}},
	}
	unmappedOpenAI := Account{ID: 1, Platform: PlatformOpenAI}
	unmappedOther := Account{ID: 3, Platform: PlatformAnthropic}

	require.Empty(t, supplementUnmappedOpenAIModels([]Account{unmappedOpenAI}, nil))
	require.Equal(t, []string{"team-coder"}, supplementUnmappedOpenAIModels([]Account{mapped, unmappedOther}, []string{"team-coder"}))

	got := supplementUnmappedOpenAIModels([]Account{mapped, unmappedOpenAI}, []string{"team-coder", "gpt-5.6-sol"})
	require.ElementsMatch(t, append(openai.DefaultModelIDs(), "team-coder"), got)
	require.IsNonDecreasing(t, got)
}
