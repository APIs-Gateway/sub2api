package securityaudit

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractPromptSnapshotRejectsInvalidJSONAndDefaultsStage(t *testing.T) {
	_, err := ExtractPromptSnapshot(Request{Protocol: "openai_chat", Body: []byte("{")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "JSON is invalid")
	snapshot, err := ExtractPromptSnapshot(Request{
		RequestID: "req-1",
		Protocol:  "openai_chat",
		Body:      []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	})
	require.NoError(t, err)
	require.Equal(t, "http", snapshot.Stage)
	require.Equal(t, "req-1", snapshot.RequestID)
}

func TestExtractPromptSnapshotProtocolFallbacks(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{name: "unknown chat", body: `{"messages":[{"role":"user","content":"chat fallback"}]}`, want: "chat fallback"},
		{name: "unknown responses", body: `{"instructions":"response fallback","input":"input fallback"}`, want: "input fallback"},
		{name: "unknown gemini", body: `{"contents":[{"role":"user","parts":[{"text":"gemini fallback"}]}]}`, want: "gemini fallback"},
		{name: "unknown media", body: `{"prompt":"media fallback"}`, want: "media fallback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, err := ExtractPromptSnapshot(Request{Protocol: "future_protocol", Body: []byte(tt.body)})
			require.NoError(t, err)
			require.Contains(t, snapshot.ScanText, tt.want)
		})
	}
}

func TestExtractPromptSnapshotResponsesAndInstructionShapes(t *testing.T) {
	tests := []struct {
		name, protocol, body, want string
	}{
		{name: "response create instructions only", protocol: "responses", body: `{"type":"response.create","instructions":"only instructions"}`, want: "only instructions"},
		{name: "response wrapper", protocol: "responses", body: `{"type":"response.create","response":{"instructions":"wrapped instructions","input":"wrapped input"}}`, want: "wrapped input"},
		{name: "response string array", protocol: "responses", body: `{"input":["plain input",{"role":"user","text":"text input"},{"role":"function","text":"ignored"}]}`, want: "text input"},
		{name: "response object rejected role", protocol: "responses", body: `{"input":{"role":"function","content":"ignored"}}`, want: ""},
		{name: "anthropic content objects", protocol: "messages", body: `{"system":[{"type":"text","text":"system list"}],"messages":[{"role":"user","content":{"text":"user object"}}]}`, want: "user object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, err := ExtractPromptSnapshot(Request{Protocol: tt.protocol, Body: []byte(tt.body)})
			if tt.want == "" {
				require.True(t, errors.Is(err, ErrNoPromptText))
				return
			}
			require.NoError(t, err)
			require.Contains(t, snapshot.ScanText, tt.want)
		})
	}
}

func TestPromptSnapshotGeminiInstructionVariantsAndMediaPayloadFiltering(t *testing.T) {
	body := []byte(`{
		"systemInstruction":"system string",
		"system_instruction":{"parts":[{"text":"system snake"}]},
		"requests":[{"systemInstruction":[{"role":"user","parts":[{"text":"system batch"}]}]}],
		"prompt":"keep this",
		"description":"https://example.test/image.png",
		"query":"short query",
		"payload":{"prompt":"` + strings.Repeat("A", 256) + `"}
	}`)
	snapshot, err := ExtractPromptSnapshot(Request{Protocol: "gemini_generate_content", Body: body})
	require.NoError(t, err)
	for _, expected := range []string{"system string", "system snake", "system batch"} {
		require.Contains(t, snapshot.ScanText, expected)
	}
	require.NotContains(t, snapshot.ScanText, "keep this")
	require.NotContains(t, snapshot.ScanText, "example.test")
	require.NotContains(t, snapshot.ScanText, strings.Repeat("A", 100))
}

func TestPromptSnapshotHelpersCoverStructuredContentAndPreviewEdges(t *testing.T) {
	require.Equal(t, "array instruction", extractInstructions([]any{map[string]any{"type": "text", "text": "array instruction"}})[0].text)
	require.Equal(t, "map text", extractInstructions(map[string]any{"text": "map text"})[0].text)
	require.Equal(t, "system map", extractAnthropicSystem(map[string]any{"text": "system map"})[0].text)
	require.Equal(t, "content map", contentTexts(map[string]any{"text": "content map"})[0])
	require.Nil(t, contentTexts(123))
	require.Nil(t, extractResponses(123))
	require.Nil(t, extractGeminiSystemInstruction(123))
	require.False(t, looksLikeMediaPayload(strings.Repeat("A", 255)+"!"))
	require.True(t, looksLikeMediaPayload(strings.Repeat("A", 256)))
	require.Empty(t, BuildPromptPreview("", 0))
	require.Equal(t, "***…", BuildPromptPreview("short", 2))
	require.Equal(t, "", TrimRunes("value", 0))
	require.Equal(t, "val…", TrimRunes("value", 3))
	require.Nil(t, cloneInt64Ptr(nil))
}
