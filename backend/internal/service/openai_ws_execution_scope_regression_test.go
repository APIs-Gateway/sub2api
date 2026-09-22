package service

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResolveOpenAIWSClientThreadIDPriorityAndFallbacks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name    string
		headers map[string]string
		body    string
		want    string
	}{
		{
			name: "thread header wins",
			headers: map[string]string{
				openAIWSThreadIDHeader:     " thread ",
				openAIWSTurnMetadataHeader: `{"thread_id":"metadata"}`,
				openAIWSWindowIDHeader:     "window:0",
			},
			body: `{"client_metadata":{"thread_id":"body"}}`,
			want: "thread",
		},
		{
			name:    "metadata fallback",
			headers: map[string]string{openAIWSTurnMetadataHeader: `{"thread_id":"metadata"}`},
			want:    "metadata",
		},
		{
			name:    "window fallback",
			headers: map[string]string{openAIWSWindowIDHeader: "window:0"},
			want:    "window",
		},
		{
			name: "body metadata fallback",
			body: `{"client_metadata":{"x-codex-turn-metadata":"{\"thread_id\":\"embedded\"}"}}`,
			want: "embedded",
		},
		{
			name:    "malformed metadata is ignored",
			headers: map[string]string{openAIWSTurnMetadataHeader: "not-json"},
			body:    `{}`,
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, resolveOpenAIWSClientThreadID(newOpenAIWSExecutionScopeContext(tc.headers), []byte(tc.body)))
		})
	}
	require.Equal(t, "body", resolveOpenAIWSClientThreadID(nil, []byte(`{"client_metadata":{"thread_id":"body"}}`)))
}

func TestResolveOpenAIWSExecutionLaneAndScopeFallbacks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name    string
		headers map[string]string
		body    string
		want    string
	}{
		{name: "turn stays main lane", headers: map[string]string{openAIWSTurnMetadataHeader: `{"thread_id":"t","request_kind":"turn"}`}},
		{name: "prewarm stays main lane", headers: map[string]string{openAIWSTurnMetadataHeader: `{"thread_id":"t","request_kind":"prewarm"}`}},
		{name: "compaction stays main lane", headers: map[string]string{openAIWSTurnMetadataHeader: `{"thread_id":"t","request_kind":"compaction"}`}},
		{name: "unknown kind is isolated", headers: map[string]string{openAIWSTurnMetadataHeader: `{"thread_id":"t","request_kind":" Memory "}`}, want: "kind=memory"},
		{name: "body metadata is used", body: `{"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"memory\"}"}}`, want: "kind=memory"},
		{name: "header takes precedence over body", headers: map[string]string{openAIWSTurnMetadataHeader: `{"thread_id":"t","request_kind":"turn"}`}, body: `{"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"memory\"}"}}`},
		{name: "subagent without metadata is isolated", headers: map[string]string{openAIWSSubagentHeader: "Guardian"}, want: "subagent=guardian"},
		{name: "body subagent is isolated", body: `{"client_metadata":{"x-openai-subagent":"guardian"}}`, want: "subagent=guardian"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, resolveOpenAIWSExecutionLane(newOpenAIWSExecutionScopeContext(tc.headers), []byte(tc.body)))
		})
	}

	body := []byte(`{"type":"response.create","input":"hello"}`)
	sessionHeaders := map[string]string{"session_id": "session-a"}
	bySession, threadID := resolveOpenAIWSExecutionScope(newOpenAIWSExecutionScopeContext(sessionHeaders), body, 11)
	require.NotEmpty(t, bySession)
	require.Empty(t, threadID)
	require.Equal(t, bySession, executionScopeForTest(sessionHeaders, []byte(`{"input":"changed"}`), 11))
	require.NotEqual(t, bySession, executionScopeForTest(sessionHeaders, body, 12))
	require.NotEqual(t, bySession, executionScopeForTest(map[string]string{"session_id": "session-a", openAIWSThreadIDHeader: "thread-a"}, body, 11))
	require.Empty(t, executionScopeForTest(nil, body, 11))
	require.Empty(t, resolveOpenAIWSClientThreadID(newOpenAIWSExecutionScopeContext(nil), nil))
	require.Empty(t, openAIWSExecutionTurnMetadata(newOpenAIWSExecutionScopeContext(nil), nil))
	require.Empty(t, openAIWSExecutionSubagent(nil, nil))
	require.Equal(t, "openai_ws_exec:11|thread=t|kind=memory", openAIWSExecutionScopeSeed(11, "thread", "t", "kind=memory"))
}
