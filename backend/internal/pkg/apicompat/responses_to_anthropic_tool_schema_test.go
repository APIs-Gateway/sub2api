package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// flattenRootUnionsJSON runs flattenAnthropicRootUnions on a raw schema and
// returns the re-encoded result for JSONEq comparisons.
func flattenRootUnionsJSON(t *testing.T, raw string) string {
	t.Helper()
	var schema map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &schema))
	flattenAnthropicRootUnions(schema)
	out, err := json.Marshal(schema)
	require.NoError(t, err)
	return string(out)
}

func TestFlattenAnthropicRootUnions_EdgeCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no root union leaves schema untouched",
			in:   `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`,
			want: `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`,
		},
		{
			name: "malformed and empty unions are dropped without adding fields",
			in:   `{"type":"object","oneOf":"bad","anyOf":[]}`,
			want: `{"type":"object"}`,
		},
		{
			name: "malformed root properties are replaced by branch properties",
			in:   `{"type":"object","properties":"x","oneOf":[{"type":"object","properties":{"a":{"type":"string"}}}]}`,
			want: `{"type":"object","properties":{"a":{"type":"string"}}}`,
		},
		{
			name: "non-object JSON branch contributes nothing and voids the required intersection",
			in:   `{"anyOf":["str",{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}]}`,
			want: `{"properties":{"a":{"type":"string"}}}`,
		},
		{
			name: "non-object typed branch is skipped, untyped branch with properties counts as object",
			in:   `{"oneOf":[{"type":"string"},{"properties":{"b":{"type":"integer"}},"required":["b"]}]}`,
			want: `{"properties":{"b":{"type":"integer"}}}`,
		},
		{
			name: "branch with malformed properties keeps its required contribution",
			in:   `{"oneOf":[{"type":"object","properties":5,"required":["c"]},{"type":"object","properties":{"c":{"type":"string"}},"required":["c"]}]}`,
			want: `{"properties":{"c":{"type":"string"}},"required":["c"]}`,
		},
		{
			name: "allOf ignores malformed required and unions the rest",
			in:   `{"allOf":[{"type":"object","properties":{"a":{}},"required":"a"},{"type":"object","properties":{"b":{}},"required":["b"]}]}`,
			want: `{"properties":{"a":{},"b":{}},"required":["b"]}`,
		},
		{
			name: "identical properties stay single, conflicting ones become a nested anyOf",
			in: `{"oneOf":[` +
				`{"type":"object","properties":{"s":{"type":"string"},"n":{"type":"integer"}},"required":["s","n"]},` +
				`{"type":"object","properties":{"s":{ "type" : "string" },"n":{"type":"number"}},"required":["n","s"]},` +
				`{"type":"object","properties":{"s":{"type":"string"}},"required":["s"]}]}`,
			want: `{"properties":{"s":{"type":"string"},"n":{"anyOf":[{"type":"integer"},{"type":"number"}]}},"required":["s"]}`,
		},
		{
			name: "nested branch unions flatten recursively and mixed keywords merge required",
			in: `{"required":["root"],"properties":{"root":{"type":"string"}},` +
				`"oneOf":[{"anyOf":[{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}]},` +
				`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}],` +
				`"allOf":[{"type":"object","properties":{"y":{"type":"boolean"}},"required":["y"]}]}`,
			want: `{"required":["x","y","root"],"properties":{"root":{"type":"string"},"x":{"type":"string"},"y":{"type":"boolean"}}}`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.JSONEq(t, tc.want, flattenRootUnionsJSON(t, tc.in))
		})
	}
}

func TestAnthropicSchemaJSONEqual(t *testing.T) {
	t.Parallel()

	require.True(t, anthropicSchemaJSONEqual(json.RawMessage(`{"type":"string"}`), json.RawMessage(`{ "type" : "string" }`)))
	require.False(t, anthropicSchemaJSONEqual(json.RawMessage(`{"type":"string"}`), json.RawMessage(`{"type":"number"}`)))
	require.False(t, anthropicSchemaJSONEqual(json.RawMessage(`{`), json.RawMessage(`{}`)))
	require.False(t, anthropicSchemaJSONEqual(json.RawMessage(`{}`), json.RawMessage(`{`)))
}

func TestIntersectAnthropicRequiredLists(t *testing.T) {
	t.Parallel()

	require.Nil(t, intersectAnthropicRequiredLists(nil))
	require.Equal(t, []string{"a", "b"}, intersectAnthropicRequiredLists([][]string{{"a", "b", "c"}, {"b", "a"}}))
	require.Nil(t, intersectAnthropicRequiredLists([][]string{{"a"}, {"b"}, {"a"}}))
}
