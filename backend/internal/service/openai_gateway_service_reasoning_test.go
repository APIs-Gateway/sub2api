package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrimOpenAIEncryptedReasoningItems_ContentNull(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hi"},
			map[string]any{
				"type":              "reasoning",
				"summary":           []any{map[string]any{"type": "summary_text", "text": "thinking..."}},
				"content":           nil,
				"encrypted_content": nil,
			},
			map[string]any{"type": "message", "role": "assistant", "content": "Hello!"},
		},
	}

	changed := trimOpenAIEncryptedReasoningItems(reqBody)
	require.True(t, changed)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 3)

	reasoning, ok := input[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "reasoning", reasoning["type"])
	assert.NotNil(t, reasoning["summary"])
	_, hasContent := reasoning["content"]
	assert.False(t, hasContent)
	_, hasEncrypted := reasoning["encrypted_content"]
	assert.False(t, hasEncrypted)
}

func TestTrimOpenAIEncryptedReasoningItems_ContentNullOnly(t *testing.T) {
	reqBody := map[string]any{
		"input": []any{
			map[string]any{
				"type":    "reasoning",
				"summary": []any{map[string]any{"type": "summary_text", "text": "ok"}},
				"content": nil,
			},
		},
	}

	changed := trimOpenAIEncryptedReasoningItems(reqBody)
	require.True(t, changed)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)

	reasoning, ok := input[0].(map[string]any)
	require.True(t, ok)
	_, hasContent := reasoning["content"]
	assert.False(t, hasContent)
}

func TestTrimOpenAIEncryptedReasoningItems_ContentNonNull(t *testing.T) {
	reqBody := map[string]any{
		"input": []any{
			map[string]any{
				"type":    "reasoning",
				"summary": []any{map[string]any{"type": "summary_text", "text": "ok"}},
				"content": "some actual content",
			},
		},
	}

	changed := trimOpenAIEncryptedReasoningItems(reqBody)
	assert.False(t, changed)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	reasoning, ok := input[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "some actual content", reasoning["content"])
}

func TestTrimOpenAIEncryptedReasoningItems_ContentNullDropsBareSkeleton(t *testing.T) {
	reqBody := map[string]any{
		"input": []any{
			map[string]any{"type": "reasoning", "content": nil},
		},
	}

	changed := trimOpenAIEncryptedReasoningItems(reqBody)
	require.True(t, changed)
	_, hasInput := reqBody["input"]
	assert.False(t, hasInput)
}
