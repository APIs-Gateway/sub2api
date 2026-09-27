package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// GPT-5 and every later generation are reasoning-only: forwarding temperature or
// top_p makes the Responses API reject the request with "Unsupported parameter".
// The check is keyed on the generation number so a new family (gpt-6-astra and
// whatever follows) does not silently fall into the sampling branch.
func TestIsReasoningModelCoversLaterGenerations(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{"gpt-6-astra", true},
		{"gpt-6", true},
		{"gpt-6.1-mini", true},
		{"gpt-7-whatever", true},
		{"gpt-6-sol", true},
		{"gpt-5.5", true},
		{"gpt-5.2", true},
		{"gpt-5", true},
		{"GPT-6-Astra", true},
		{"  gpt-6-astra  ", true},
		{"gpt-4o", false},
		{"gpt-4.1", false},
		{"gpt-6o", false},
		{"gpt-6foo", false},
		{"gpt-6.1x", false},
		{"gpt-image-1", false},
		{"gpt-audio", false},
		{"claude-opus-4-6", false},
		{"gemini-3.1-pro", false},
		{"", false},
	} {
		if got := isReasoningModel(tc.model); got != tc.want {
			t.Errorf("isReasoningModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestOpenAIModelGeneration(t *testing.T) {
	for _, tc := range []struct {
		model     string
		wantMajor int
		wantOK    bool
	}{
		{"gpt-6-astra", 6, true},
		{"gpt-5.5", 5, true},
		{"gpt-4o", 0, false},
		{"gpt-10-future", 10, true},
		{"gpt-6.1-mini", 6, true},
		{"gpt-6o", 0, false},
		{"gpt-6foo", 0, false},
		{"gpt-6.", 0, false},
		{"gpt-6.1x", 0, false},
		{"gpt-6-", 0, false},
		{"gpt-image-1", 0, false},
		{"claude-opus-4-6", 0, false},
		{"", 0, false},
	} {
		major, ok := openAIModelGeneration(tc.model)
		if major != tc.wantMajor || ok != tc.wantOK {
			t.Errorf("openAIModelGeneration(%q) = (%d, %v), want (%d, %v)",
				tc.model, major, ok, tc.wantMajor, tc.wantOK)
		}
	}
}

func TestAnthropicToResponses_TemperatureStrippedForGPT6Astra(t *testing.T) {
	temp := 0.7
	req := &AnthropicRequest{
		Model:       "gpt-6-astra",
		MaxTokens:   1024,
		Messages:    []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		Temperature: &temp,
		TopP:        &temp,
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	require.Nil(t, resp.Temperature, "gpt-6-astra is reasoning-only: temperature must be stripped")
	require.Nil(t, resp.TopP, "gpt-6-astra is reasoning-only: top_p must be stripped")
}

func TestGPT6AstraSamplingStrippedAcrossCompatibilityConversions(t *testing.T) {
	temp := 0.7
	for _, model := range []string{"gpt-6-astra", "gpt-6"} {
		t.Run(model, func(t *testing.T) {
			anthropicReq := &AnthropicRequest{
				Model:       model,
				MaxTokens:   1024,
				Messages:    []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
				Temperature: &temp,
				TopP:        &temp,
			}
			responsesFromAnthropic, err := AnthropicToResponses(anthropicReq)
			require.NoError(t, err)
			require.Nil(t, responsesFromAnthropic.Temperature)
			require.Nil(t, responsesFromAnthropic.TopP)

			chatFromAnthropic, err := AnthropicToChatCompletionsRequest(anthropicReq)
			require.NoError(t, err)
			require.Nil(t, chatFromAnthropic.Temperature)
			require.Nil(t, chatFromAnthropic.TopP)

			responsesFromChat, err := ChatCompletionsToResponses(&ChatCompletionsRequest{
				Model:       model,
				Messages:    []ChatMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
				Temperature: &temp,
				TopP:        &temp,
			})
			require.NoError(t, err)
			require.Nil(t, responsesFromChat.Temperature)
			require.Nil(t, responsesFromChat.TopP)
		})
	}
}
