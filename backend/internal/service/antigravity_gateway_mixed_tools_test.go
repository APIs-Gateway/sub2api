package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func mixedToolsSlice(t *testing.T, v any) []any {
	t.Helper()
	out, ok := v.([]any)
	require.True(t, ok, "expected JSON array, got %T", v)
	return out
}

func mixedToolsMap(t *testing.T, v any) map[string]any {
	t.Helper()
	out, ok := v.(map[string]any)
	require.True(t, ok, "expected JSON object, got %T", v)
	return out
}

func TestEnableMixedGeminiToolInvocations(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		unchanged bool
		check     func(t *testing.T, request map[string]any)
	}{
		{
			name: "mixed function and googleSearch drops built-in and flag",
			body: `{"tools":[{"functionDeclarations":[{"name":"shell"}]},{"googleSearch":{}}],"toolConfig":{"includeServerSideToolInvocations":true,"functionCallingConfig":{"mode":"VALIDATED"}}}`,
			check: func(t *testing.T, request map[string]any) {
				tools := mixedToolsSlice(t, request["tools"])
				require.Len(t, tools, 1)
				require.Contains(t, mixedToolsMap(t, tools[0]), "functionDeclarations")
				toolConfig := mixedToolsMap(t, request["toolConfig"])
				require.NotContains(t, toolConfig, "includeServerSideToolInvocations")
				require.Contains(t, toolConfig, "functionCallingConfig")
			},
		},
		{
			name: "codeExecution and snake_case flag are removed and empty toolConfig dropped",
			body: `{"tools":[{"functionDeclarations":[{"name":"shell"}],"codeExecution":{}}],"toolConfig":{"include_server_side_tool_invocations":true}}`,
			check: func(t *testing.T, request map[string]any) {
				tools := mixedToolsSlice(t, request["tools"])
				require.Len(t, tools, 1)
				tool := mixedToolsMap(t, tools[0])
				require.Contains(t, tool, "functionDeclarations")
				require.NotContains(t, tool, "codeExecution")
				require.NotContains(t, request, "toolConfig")
			},
		},
		{
			name: "non-object tool entries are preserved",
			body: `{"tools":["opaque",{"functionDeclarations":[{"name":"shell"}]},{"googleSearch":{}}]}`,
			check: func(t *testing.T, request map[string]any) {
				tools := mixedToolsSlice(t, request["tools"])
				require.Len(t, tools, 2)
				require.Equal(t, "opaque", tools[0])
			},
		},
		{name: "google search only is unchanged", body: `{"tools":[{"googleSearch":{}}],"toolConfig":{"includeServerSideToolInvocations":true}}`, unchanged: true},
		{name: "functions only is unchanged", body: `{"tools":[{"functionDeclarations":[{"name":"shell"}]}]}`, unchanged: true},
		{name: "empty functionDeclarations is unchanged", body: `{"tools":[{"functionDeclarations":[]},{"googleSearch":{}}]}`, unchanged: true},
		{name: "no tools is unchanged", body: `{"contents":[]}`, unchanged: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := enableMixedGeminiToolInvocations([]byte(tt.body))
			require.NoError(t, err)
			if tt.unchanged {
				require.Equal(t, tt.body, string(out))
				return
			}
			var request map[string]any
			require.NoError(t, json.Unmarshal(out, &request))
			tt.check(t, request)
		})
	}

	_, err := enableMixedGeminiToolInvocations([]byte(`not json`))
	require.Error(t, err)
}

func TestAntigravityGatewayService_ForwardGemini_StripsBuiltinsWhenClientFunctionsPresent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"tools":[{"functionDeclarations":[{"name":"get_weather","parameters":{"type":"object","additionalProperties":false}}]},{"googleSearch":{}}],"toolConfig":{"includeServerSideToolInvocations":true}}`)
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/antigravity/v1beta/models/gemini-2.5-flash:streamGenerateContent", bytes.NewReader(body))

	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{}}}\n\n")),
	}}}
	svc := &AntigravityGatewayService{
		settingService: NewSettingService(&antigravitySettingRepoStub{}, &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}),
		tokenProvider:  &AntigravityTokenProvider{},
		httpUpstream:   upstream,
	}
	account := &Account{
		ID: 103, Name: "native-gemini", Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{"access_token": "token", "project_id": "project-103", "model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"}},
	}

	result, err := svc.ForwardGemini(context.Background(), c, account, "gemini-2.5-flash", "streamGenerateContent", true, body, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requestBodies, 1)

	var wrapped map[string]any
	require.NoError(t, json.Unmarshal(upstream.requestBodies[0], &wrapped))
	request, ok := wrapped["request"].(map[string]any)
	require.True(t, ok)
	tools, ok := request["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	require.Contains(t, tool, "functionDeclarations")
	require.NotContains(t, tool, "googleSearch")
	if toolConfig, exists := request["toolConfig"].(map[string]any); exists {
		require.NotContains(t, toolConfig, "includeServerSideToolInvocations")
	}
}
