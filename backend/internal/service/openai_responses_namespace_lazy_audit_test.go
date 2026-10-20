package service

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesNamespaceLazySemanticContracts(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		keep     bool
		itemType string
		removed  []string
		retained []string
	}{
		{
			name:     "first_and_last_around_raw_number",
			body:     `{"input":[{"type":"message","namespace":"drop"},9007199254740993,{"type":"message","namespace":null}]}`,
			removed:  []string{"input.0.namespace", "input.2.namespace"},
			retained: []string{"input.1"},
		},
		{
			name:     "kept_call_prefix_and_nested_namespace",
			body:     `{"input":[{"type":"function_call","namespace":"files","name":"read"},{"type":"message","namespace":"drop","content":{"namespace":"nested"}}]}`,
			keep:     true,
			removed:  []string{"input.1.namespace"},
			retained: []string{"input.0", "input.1.content"},
		},
		{
			name:     "mixed_nonobjects_and_middle_removal",
			body:     `{"input":[null,"命名空间",1.234e+15,{"namespace":"drop","content":"keep"},["namespace"],{"content":"tail"}]}`,
			removed:  []string{"input.3.namespace"},
			retained: []string{"input.0", "input.1", "input.2", "input.3.content", "input.4", "input.5"},
		},
		{
			name:     "oftype_removes_only_rejected_type",
			body:     `{"input":[{"type":"mcp_tool_call","namespace":"keep"},{"type":"function_call","namespace":"drop"},{"type":"custom_tool_call","namespace":"keep"}]}`,
			itemType: " FUNCTION_CALL ",
			removed:  []string{"input.1.namespace"},
			retained: []string{"input.0", "input.2"},
		},
		{
			name:     "oftype_decodes_escaped_key_without_generic_prefilter",
			body:     `{"input":[{"type":"function_call","name\u0073pace":"drop","name":"read"},{"type":"custom_tool_call","name\u0073pace":"keep"}]}`,
			itemType: "function_call",
			removed:  []string{"input.0.namespace"},
			retained: []string{"input.0.name", "input.1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			original := append([]byte(nil), body...)
			var out []byte
			var err error
			if tc.itemType != "" {
				out, err = stripOpenAIResponsesInputNamespacesOfType(body, tc.itemType)
			} else {
				out, err = stripOpenAIResponsesInputNamespaces(body, tc.keep)
			}
			require.NoError(t, err)
			require.True(t, gjson.ValidBytes(out))
			require.Equal(t, len(gjson.GetBytes(body, "input").Array()), len(gjson.GetBytes(out, "input").Array()))
			for _, path := range tc.removed {
				require.True(t, gjson.GetBytes(body, path).Exists(), "fixture must contain %s", path)
				require.False(t, gjson.GetBytes(out, path).Exists(), "%s", path)
			}
			for _, path := range tc.retained {
				require.Equal(t, gjson.GetBytes(body, path).Raw, gjson.GetBytes(out, path).Raw, "%s", path)
			}
			require.Equal(t, original, body, "caller input must remain immutable")
		})
	}
}

func TestResponsesNamespaceLazyNoChangeByteExact(t *testing.T) {
	for _, body := range []string{
		`{"input":[],"tools":[{"type":"namespace"}]}`,
		`{"input":[null,42,"namespace",{"content":{"namespace":"nested"}}]}`,
		`{"input":[{"type":"function_call","namespace":"files"}]}`,
		`{"input":"text","namespace":"top"}`,
		`{"input":{"namespace":"object"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			original := []byte(body)
			out, err := stripOpenAIResponsesInputNamespaces(original, true)
			require.NoError(t, err)
			require.Equal(t, original, out)
			out, err = stripOpenAIResponsesInputNamespacesOfType(original, "custom_tool_call")
			require.NoError(t, err)
			require.Equal(t, original, out)
		})
	}
}

// This additional raw-span contract is separate from the old semantic controls.
// OLD may remove the same namespace correctly while normalizing array spacing.
func TestResponsesNamespaceLazyRawSpans(t *testing.T) {
	for _, input := range []string{
		`[ {"namespace":"drop","type":"message"} ]`,
		`[ null, 9007199254740993, {"namespace":"drop","type":"message"} ]`,
		`[ {"namespace":"drop","type":"message"}, "尾部", {"content":{"namespace":"nested"}} ]`,
		`[ {"namespace":"drop","type":"message"}, {"type":"function_call","namespace":"keep"}, {"namespace":"drop","type":"message"} ]`,
	} {
		t.Run(input, func(t *testing.T) {
			body := []byte(" \n{\"prefix\":9007199254740993,\"input\":" + input + ",\"tail\":true} \n")
			original := append([]byte(nil), body...)
			out, err := stripOpenAIResponsesInputNamespaces(body, true)
			require.NoError(t, err)
			require.True(t, gjson.ValidBytes(out))
			want := bytes.ReplaceAll(body, []byte(`"namespace":"drop",`), nil)
			require.Equal(t, want, out)
			require.Equal(t, original, body)
		})
	}
}

func TestResponsesNamespaceLazyMalformedNoPanic(t *testing.T) {
	for _, body := range []string{
		``,
		`{"input":[`,
		`{"input":[{"namespace":"x"}`,
		`{"input":[{"namespace":"x","content":}]}`,
		`{"input":[{"type":"function_call","namespace":"x"}],`,
	} {
		t.Run(body, func(t *testing.T) {
			input := []byte(body)
			original := append([]byte(nil), input...)
			require.NotPanics(t, func() {
				_, _ = stripOpenAIResponsesInputNamespaces(input, false)
				_, _ = stripOpenAIResponsesInputNamespacesOfType(input, "function_call")
			})
			require.True(t, bytes.Equal(original, input))
		})
	}
}
