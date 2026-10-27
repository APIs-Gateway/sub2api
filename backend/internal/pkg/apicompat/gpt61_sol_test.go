package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGPT61SolChatBridgeKeepsEffortAndDropsSampling(t *testing.T) {
	sampling := 0.7
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		out, err := ChatCompletionsToResponses(&ChatCompletionsRequest{
			Model:           "gpt-6.1-sol",
			ReasoningEffort: effort,
			Temperature:     &sampling,
			TopP:            &sampling,
			Messages:        []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		})
		require.NoError(t, err, effort)
		require.Nil(t, out.Temperature, effort)
		require.Nil(t, out.TopP, effort)
		require.Equal(t, effort, out.Reasoning.Effort)
	}
}

func TestGPT61SolBridgesRejectDisabledReasoning(t *testing.T) {
	for _, effort := range []string{"none", "minimal"} {
		_, err := ChatCompletionsToResponses(&ChatCompletionsRequest{
			Model:           "gpt-6.1-sol",
			ReasoningEffort: effort,
			Messages:        []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		})
		require.Error(t, err, effort)
	}

	_, err := AnthropicToResponses(&AnthropicRequest{
		Model:     "gpt-6.1-sol",
		MaxTokens: 100,
		Thinking:  &AnthropicThinking{Type: "disabled"},
		Messages:  []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	})
	require.Error(t, err)

	// GPT-6 Sol still accepts disabled reasoning.
	_, err = AnthropicToResponses(&AnthropicRequest{
		Model:     "gpt-6-sol",
		MaxTokens: 100,
		Thinking:  &AnthropicThinking{Type: "disabled"},
		Messages:  []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	})
	require.NoError(t, err)
}
