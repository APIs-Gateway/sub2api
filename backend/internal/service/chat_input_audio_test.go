package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatInputAudio_DocumentScope(t *testing.T) {
	for _, tc := range []struct {
		name, media, sourceType, data string
		inline                        bool
	}{
		{"audio", "audio/wav", "base64", "aA==", true},
		{"pdf_keeps_original_fallback", "application/pdf", "base64", "aA==", false},
		{"url_keeps_original_fallback", "audio/wav", "url", "aA==", false},
		{"empty_keeps_original_fallback", "audio/wav", "base64", "", false},
		{"missing_source_keeps_original_fallback", "", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block := map[string]any{"type": "document"}
			if tc.sourceType != "" {
				block["source"] = map[string]any{"type": tc.sourceType, "media_type": tc.media, "data": tc.data}
			}
			messages := []any{map[string]any{"role": "user", "content": []any{block}}}
			got, err := convertClaudeMessagesToGeminiContents(messages, map[string]string{})
			require.NoError(t, err)
			parts := got[0].(map[string]any)["parts"].([]any)
			require.Len(t, parts, 1)
			part := parts[0].(map[string]any)
			if tc.inline {
				require.Equal(t, map[string]any{"mimeType": tc.media, "data": tc.data}, part["inlineData"])
			} else {
				encoded, err := json.Marshal(block)
				require.NoError(t, err)
				require.Equal(t, string(encoded), part["text"])
			}
		})
	}
}
