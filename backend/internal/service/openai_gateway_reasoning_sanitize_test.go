package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrimOpenAIEncryptedReasoningItems_ContentNull(t *testing.T) {
	reqBody := map[string]any{
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hi"},
			map[string]any{
				"type":              "reasoning",
				"summary":           []any{map[string]any{"type": "summary_text", "text": "thinking"}},
				"content":           nil,
				"encrypted_content": nil,
			},
		},
	}

	require.True(t, trimOpenAIEncryptedReasoningItems(reqBody))
	items, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, items, 2)
	reasoning, ok := items[1].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, reasoning, "content")
	require.NotContains(t, reasoning, "encrypted_content")
	require.Contains(t, reasoning, "summary")
}

func TestTrimOpenAIEncryptedReasoningItems_ContentNullWithoutEncryptedContent(t *testing.T) {
	reqBody := map[string]any{
		"input": []any{
			map[string]any{
				"type":    "reasoning",
				"summary": []any{map[string]any{"type": "summary_text", "text": "thinking"}},
				"content": nil,
			},
		},
	}

	require.True(t, trimOpenAIEncryptedReasoningItems(reqBody))
	items, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	reasoning, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, reasoning, "content")
}

func TestTrimOpenAIEncryptedReasoningItems_PreservesNonNullContentAndNonReasoning(t *testing.T) {
	reqBody := map[string]any{
		"input": []any{
			map[string]any{
				"type":    "reasoning",
				"content": "actual content",
			},
			map[string]any{
				"type":              "message",
				"content":           nil,
				"encrypted_content": "keep this field",
			},
		},
	}

	require.False(t, trimOpenAIEncryptedReasoningItems(reqBody))
	items, ok := reqBody["input"].([]any)
	require.True(t, ok)
	reasoning, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "actual content", reasoning["content"])
	message, ok := items[1].(map[string]any)
	require.True(t, ok)
	require.Nil(t, message["content"])
	require.Equal(t, "keep this field", message["encrypted_content"])
}

func TestTrimOpenAIEncryptedReasoningItems_DropsBareSkeleton(t *testing.T) {
	reqBody := map[string]any{
		"input": []any{
			map[string]any{"type": "reasoning", "content": nil},
		},
	}

	require.True(t, trimOpenAIEncryptedReasoningItems(reqBody))
	require.NotContains(t, reqBody, "input")
}

func TestTrimOpenAIEncryptedReasoningItems_SupportsMapAndTypedSliceInputs(t *testing.T) {
	mapBody := map[string]any{
		"input": map[string]any{
			"type":    "reasoning",
			"summary": "thinking",
			"content": nil,
		},
	}
	require.True(t, trimOpenAIEncryptedReasoningItems(mapBody))
	mapInput, ok := mapBody["input"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, mapInput, "content")

	typedSliceBody := map[string]any{
		"input": []map[string]any{{
			"type":    "reasoning",
			"summary": "thinking",
			"content": nil,
		}},
	}
	require.True(t, trimOpenAIEncryptedReasoningItems(typedSliceBody))
	typedInput, ok := typedSliceBody["input"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, typedInput, 1)
	require.NotContains(t, typedInput[0], "content")
}
