package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesToAnthropicCustomToolUsesTextInputSchema(t *testing.T) {
	tools := convertResponsesToAnthropicTools([]ResponsesTool{{
		Type: "custom", Name: "apply_patch", Description: "Apply a patch",
	}})
	require.Len(t, tools, 1)
	require.Empty(t, tools[0].Type)
	require.Equal(t, "apply_patch", tools[0].Name)
	require.JSONEq(t, customToolInputSchema, string(tools[0].InputSchema))
}

func TestResponsesToAnthropicCustomToolChoiceBecomesToolChoice(t *testing.T) {
	choice, err := convertResponsesToAnthropicToolChoice(json.RawMessage(`{"type":"custom","name":"exec"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"tool","name":"exec"}`, string(choice))
}

func TestNormalizeAnthropicInputSchemaRequiresObject(t *testing.T) {
	for _, schema := range []json.RawMessage{
		nil,
		json.RawMessage(`null`),
		json.RawMessage(`not-json`),
		json.RawMessage(`{"type":"string"}`),
		json.RawMessage(`{"properties":{"path":{"type":"string"}}}`),
	} {
		got := normalizeAnthropicInputSchema(schema)
		var parsed map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(got, &parsed))
		require.JSONEq(t, `"object"`, string(parsed["type"]))
		require.Contains(t, parsed, "properties")
	}
}
