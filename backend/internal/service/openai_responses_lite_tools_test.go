//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIResponsesLiteTools_MovesNamespacesAndKeepsSupportedTools(t *testing.T) {
	reqBody := map[string]any{
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
			map[string]any{"type": "custom", "name": "exec"},
			map[string]any{"type": "tool_search"},
			map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{
				map[string]any{"type": "function", "name": "spawn_agent"},
			}},
		},
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hello"},
			map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{
				map[string]any{"type": "namespace", "name": "image_gen"},
				map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{
					map[string]any{"type": "function", "name": "spawn_agent"},
				}},
			}},
		},
		"tool_choice": map[string]any{"type": "namespace", "name": "collaboration"},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	tools := reqBody["tools"].([]any)
	require.Len(t, tools, 3)
	require.Equal(t, "function", tools[0].(map[string]any)["type"])
	require.Equal(t, "custom", tools[1].(map[string]any)["type"])
	require.Equal(t, "tool_search", tools[2].(map[string]any)["type"])
	input := reqBody["input"].([]any)
	require.Len(t, input, 2)
	additional := input[1].(map[string]any)["tools"].([]any)
	require.Len(t, additional, 2)
	require.Equal(t, "image_gen", additional[0].(map[string]any)["name"])
	require.Equal(t, "collaboration", additional[1].(map[string]any)["name"])
}

func TestNormalizeOpenAIResponsesLiteTools_ConvertsStringInputAndDeduplicates(t *testing.T) {
	reqBody := map[string]any{
		"input": "hello",
		"tools": []any{map[string]any{
			"type": "namespace",
			"name": "collaboration",
		}},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.NotContains(t, reqBody, "tools")
	input := reqBody["input"].([]any)
	require.Len(t, input, 2)
	require.Equal(t, "message", input[0].(map[string]any)["type"])
	require.Equal(t, "hello", input[0].(map[string]any)["content"])
	require.Equal(t, "additional_tools", input[1].(map[string]any)["type"])
}

func TestNormalizeOpenAIResponsesLiteTools_AddsAdditionalToolsForMissingInput(t *testing.T) {
	reqBody := map[string]any{
		"tools": []any{map[string]any{"type": "namespace", "name": "collaboration"}},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "additional_tools", reqBody["input"].([]any)[0].(map[string]any)["type"])
}

func TestNormalizeOpenAIResponsesLiteTools_RejectsUnsupportedToolsWithoutMutation(t *testing.T) {
	reqBody := map[string]any{"tools": []any{
		map[string]any{"type": "function", "name": "shell"},
		map[string]any{"type": "image_generation"},
	}}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.ErrorContains(t, err, `top-level tool type "image_generation"`)
	require.False(t, changed)
	require.Len(t, reqBody["tools"], 2)
}

func TestNormalizeOpenAIResponsesLiteTools_RejectsConflictingAdditionalTool(t *testing.T) {
	reqBody := map[string]any{
		"tools": []any{map[string]any{
			"type": "namespace", "name": "collaboration",
			"tools": []any{map[string]any{"type": "function", "name": "spawn_agent"}},
		}},
		"input": []any{map[string]any{
			"type": "additional_tools",
			"tools": []any{map[string]any{
				"type": "namespace", "name": "collaboration",
				"tools": []any{map[string]any{"type": "function", "name": "send_message"}},
			}},
		}},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.ErrorContains(t, err, "conflicts with migrated")
	require.False(t, changed)
	require.Len(t, reqBody["tools"], 1)
}

func TestNormalizeOpenAIResponsesLiteTools_EnsuresReasoningContext(t *testing.T) {
	tests := []struct {
		name      string
		reasoning any
	}{
		{name: "missing"},
		{name: "missing context", reasoning: map[string]any{"effort": "high"}},
		{name: "wrong context", reasoning: map[string]any{"effort": "medium", "context": "current_turn"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqBody := map[string]any{"input": "hello"}
			if tt.reasoning != nil {
				reqBody["reasoning"] = tt.reasoning
			}

			changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

			require.NoError(t, err)
			require.True(t, changed)
			reasoning := reqBody["reasoning"].(map[string]any)
			require.Equal(t, "all_turns", reasoning["context"])
			if tt.reasoning != nil {
				require.Equal(t, tt.reasoning.(map[string]any)["effort"], reasoning["effort"])
			}
		})
	}
}

func TestNormalizeOpenAIResponsesLiteTools_RejectsNonObjectReasoning(t *testing.T) {
	reqBody := map[string]any{"reasoning": "high"}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.ErrorContains(t, err, "reasoning to be an object")
	require.False(t, changed)
	require.Equal(t, "high", reqBody["reasoning"])
}

func TestNormalizeOpenAIResponsesLiteTools_ValidationAndNoOpCases(t *testing.T) {
	tests := []struct {
		name    string
		reqBody map[string]any
		wantErr string
		changed bool
	}{
		{name: "nil request"},
		{name: "tools must be array", reqBody: map[string]any{"tools": "invalid"}, wantErr: "tools to be an array"},
		{name: "empty string tool", reqBody: map[string]any{"tools": []any{"  "}}, wantErr: "must not be empty"},
		{name: "tool must be object", reqBody: map[string]any{"tools": []any{42}}, wantErr: "must be an object"},
		{name: "tool type required", reqBody: map[string]any{"tools": []any{map[string]any{}}}, wantErr: "missing type"},
		{
			name: "supported top level tools keep all turns",
			reqBody: map[string]any{
				"reasoning": map[string]any{"context": "all_turns"},
				"tools": []any{
					map[string]any{"type": "function", "name": "shell"},
					map[string]any{"type": "custom", "name": "exec"},
					map[string]any{"type": "tool_search"},
					"custom shorthand",
				},
			},
			changed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed, err := normalizeOpenAIResponsesLiteTools(tt.reqBody)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.False(t, changed)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.changed, changed)
		})
	}
}

func TestNormalizeOpenAIResponsesLiteTools_RejectsInvalidAdditionalToolsInput(t *testing.T) {
	tests := []struct {
		name  string
		input any
	}{
		{name: "input object", input: map[string]any{"type": "message"}},
		{name: "additional tools not array", input: []any{map[string]any{"type": "additional_tools", "tools": "invalid"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqBody := map[string]any{
				"input": tt.input,
				"tools": []any{map[string]any{"type": "namespace", "name": "collaboration"}},
			}
			changed, err := normalizeOpenAIResponsesLiteTools(reqBody)
			require.Error(t, err)
			require.False(t, changed)
		})
	}
}

func TestNormalizeOpenAIResponsesLiteTools_RejectsConflictingExistingDefinitions(t *testing.T) {
	reqBody := map[string]any{
		"input": []any{
			map[string]any{"type": "additional_tools", "tools": []any{
				map[string]any{"type": "namespace", "name": "collaboration", "description": "first"},
			}},
			map[string]any{"type": "additional_tools", "tools": []any{
				map[string]any{"type": "namespace", "name": "collaboration", "description": "second"},
			}},
		},
		"tools": []any{map[string]any{"type": "namespace", "name": "new_namespace"}},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.ErrorContains(t, err, "contains conflicting definitions")
	require.False(t, changed)
}

func TestNormalizeOpenAIResponsesLiteToolIdentityHandlesUnnamedTools(t *testing.T) {
	merged, err := mergeOpenAIResponsesLiteAdditionalTools(
		[]any{"string tool", map[string]any{"type": "function"}},
		[]any{map[string]any{"type": "namespace"}},
	)

	require.NoError(t, err)
	require.Len(t, merged, 3)
	require.Empty(t, openAIResponsesLiteToolIdentity("string tool"))
	require.Empty(t, openAIResponsesLiteToolIdentity(map[string]any{"type": "function"}))
	require.Empty(t, openAIResponsesLiteToolIdentity(map[string]any{"name": "unnamed_type"}))
}

func TestNormalizeOpenAIResponsesLiteToolsPayloadRejectsInvalidJSONAndKeepsNoOp(t *testing.T) {
	invalid := []byte(`{"tools":[`)
	updated, changed, err := normalizeOpenAIResponsesLiteToolsPayload(invalid)
	require.Error(t, err)
	require.False(t, changed)
	require.Equal(t, invalid, updated)

	noOp := []byte(`{"reasoning":{"context":"all_turns"},"tools":[{"type":"function","name":"shell"}]}`)
	updated, changed, err = normalizeOpenAIResponsesLiteToolsPayload(noOp)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, noOp, updated)
}

func TestNormalizeOpenAIResponsesLiteToolsPayload_PreservesResponseCreateShape(t *testing.T) {
	body := []byte(`{
		"type":"response.create",
		"model":"gpt-5.6-terra",
		"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},
		"input":[{"type":"message","role":"user","content":"hello"}],
		"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}],
		"tool_choice":{"type":"namespace","name":"collaboration"}
	}`)

	updated, changed, err := normalizeOpenAIResponsesLiteToolsPayload(body)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "response.create", gjson.GetBytes(updated, "type").String())
	require.False(t, gjson.GetBytes(updated, "tools").Exists())
	require.Equal(t, "collaboration", gjson.GetBytes(updated, `input.#(type=="additional_tools").tools.0.name`).String())
	require.Equal(t, "namespace", gjson.GetBytes(updated, "tool_choice.type").String())
	require.Equal(t, "all_turns", gjson.GetBytes(updated, "reasoning.context").String())
}

func TestApplyCodexOAuthTransform_PreservesLiteNamespaceToolChoice(t *testing.T) {
	reqBody := map[string]any{
		"input": []any{map[string]any{
			"type":  "additional_tools",
			"tools": []any{map[string]any{"type": "namespace", "name": "collaboration"}},
		}},
		"tool_choice": map[string]any{"type": "namespace", "name": "collaboration"},
	}

	applyCodexOAuthTransform(reqBody, true, false)

	require.Equal(t, map[string]any{"type": "namespace", "name": "collaboration"}, reqBody["tool_choice"])
}

func TestOpenAIGatewayServiceForward_NormalizesResponsesLiteToolsForOAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
	c.Request.Header.Set(responsesLiteHeader, "true")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_lite\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n" +
				"data: [DONE]\n\n",
		)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID: 501, Name: "responses-lite", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Concurrency: 1, Status: StatusActive, Schedulable: true, RateMultiplier: f64p(1),
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-account"},
	}
	body := []byte(`{
		"model":"gpt-5.6-terra","stream":true,"instructions":"test",
		"tools":[
			{"type":"function","name":"shell","parameters":{"type":"object"}},
			{"type":"custom","name":"exec"},
			{"type":"tool_search"},
			{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]}
		],
		"input":[{"type":"message","role":"user","content":"hello"}],
		"tool_choice":{"type":"namespace","name":"collaboration"}
	}`)

	result, err := svc.Forward(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "true", upstream.lastReq.Header.Get(responsesLiteHeader))
	require.False(t, gjson.GetBytes(upstream.lastBody, `tools.#(type=="namespace")`).Exists())
	require.Equal(t, "collaboration", gjson.GetBytes(upstream.lastBody, `input.#(type=="additional_tools").tools.0.name`).String())
	require.Equal(t, "namespace", gjson.GetBytes(upstream.lastBody, "tool_choice.type").String())
	require.Equal(t, "all_turns", gjson.GetBytes(upstream.lastBody, "reasoning.context").String())
}
