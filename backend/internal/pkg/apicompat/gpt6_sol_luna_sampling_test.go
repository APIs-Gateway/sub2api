package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// GPT-6 Sol/Luna 是推理模型：默认剔除 temperature/top_p；reasoning_effort=none 时保留。
func TestGPT6SolLunaChatSamplingParameters(t *testing.T) {
	temperature := 0.7
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna", "openai/gpt-6-sol-max"} {
		for _, effort := range []string{"", "medium", "max", "none"} {
			out, err := ChatCompletionsToResponses(&ChatCompletionsRequest{
				Model:           model,
				ReasoningEffort: effort,
				Temperature:     &temperature,
				TopP:            &temperature,
				Messages:        []ChatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
			})
			require.NoError(t, err)
			if effort == "none" {
				require.NotNil(t, out.Temperature, model)
				require.NotNil(t, out.TopP, model)
			} else {
				require.Nil(t, out.Temperature, model+"/"+effort)
				require.Nil(t, out.TopP, model+"/"+effort)
			}
		}
	}
	// Astra 和公开别名 gpt-6 不支持自定义采样参数，即使客户端送出 none。
	for _, model := range []string{"gpt-6-astra", "gpt-6"} {
		for _, effort := range []string{"", "none"} {
			out, err := ChatCompletionsToResponses(&ChatCompletionsRequest{
				Model:           model,
				ReasoningEffort: effort,
				Temperature:     &temperature,
				TopP:            &temperature,
				Messages:        []ChatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
			})
			require.NoError(t, err)
			require.Nil(t, out.Temperature, model+"/"+effort)
			require.Nil(t, out.TopP, model+"/"+effort)
		}
	}
	// GPT-4 系列保持采样参数透传。
	for _, model := range []string{"gpt-4o", "gpt-4.1"} {
		out, err := ChatCompletionsToResponses(&ChatCompletionsRequest{
			Model:       model,
			Temperature: &temperature,
			Messages:    []ChatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
		})
		require.NoError(t, err)
		require.NotNil(t, out.Temperature, model)
	}

	anth, err := AnthropicToResponses(&AnthropicRequest{
		Model:       "gpt-6-luna",
		MaxTokens:   100,
		Temperature: &temperature,
		Messages:    []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
	})
	require.NoError(t, err)
	require.Nil(t, anth.Temperature)
}
