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
			ordinary, err := convertClaudeMessagesToGeminiContents(messages, map[string]string{})
			require.NoError(t, err)
			encoded, err := json.Marshal(block)
			require.NoError(t, err)
			require.Equal(t, []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": string(encoded)}}}}, ordinary, "default Messages route keeps its JSON fallback even for valid audio")
			got, err := convertClaudeMessagesToGeminiContentsForRoute(messages, map[string]string{}, true)
			require.NoError(t, err)
			message, ok := got[0].(map[string]any)
			require.True(t, ok)
			parts, ok := message["parts"].([]any)
			require.True(t, ok)
			require.Len(t, parts, 1)
			part, ok := parts[0].(map[string]any)
			require.True(t, ok)
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

func TestChatInputAudio_DefaultMessagesCannotEnableAudioWithBodyFlags(t *testing.T) {
	for _, role := range []string{"user", "assistant"} {
		for _, media := range []string{"audio/wav", "audio/unknown"} {
			for _, data := range []string{"aA==", "%%%"} {
				t.Run(role+"/"+media+"/"+data, func(t *testing.T) {
					block := map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": media, "data": data}}
					body, err := json.Marshal(map[string]any{"allowValidatedChatAudio": true, "allow_audio": true, "model": "gemini-2.5-flash", "messages": []any{map[string]any{"role": role, "content": []any{block}}}})
					require.NoError(t, err)
					got, err := convertClaudeMessagesToGeminiGenerateContent(body)
					require.NoError(t, err)
					var decoded map[string]any
					require.NoError(t, json.Unmarshal(got, &decoded))
					contents, ok := decoded["contents"].([]any)
					require.True(t, ok)
					message, ok := contents[0].(map[string]any)
					require.True(t, ok)
					parts, ok := message["parts"].([]any)
					require.True(t, ok)
					part, ok := parts[0].(map[string]any)
					require.True(t, ok)
					require.NotContains(t, part, "inlineData")
					encoded, err := json.Marshal(block)
					require.NoError(t, err)
					require.Equal(t, string(encoded), part["text"])
				})
			}
		}
	}
}
