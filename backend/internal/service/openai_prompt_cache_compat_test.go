package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeGPTPromptCacheHintsModels(t *testing.T) {
	body := []byte(`{"prompt_cache_options":{"mode":"explicit"},"prompt_cache_breakpoint":{"mode":"explicit"},"prompt_cache_key":"keep"}`)
	for _, model := range []string{
		"gpt-5.4", "gpt-5.4-mini", "gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra",
		"gpt-5.6-luna", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna",
		"openai/GPT_6_SOL", "gpt-future-preview",
	} {
		t.Run(model, func(t *testing.T) {
			got, changed := sanitizeGPTPromptCacheHints(body, model)
			require.True(t, changed)
			require.JSONEq(t, `{"prompt_cache_key":"keep"}`, string(got))
		})
	}
}

func TestSanitizeGPTPromptCacheHintsPreservesUnrelatedBodies(t *testing.T) {
	for _, tc := range []struct {
		name, model, body string
	}{
		{"non GPT", "claude-opus-5-5", `{"prompt_cache_options":{"mode":"explicit"}}`},
		{"empty model", "", `{"prompt_cache_options":{"mode":"explicit"}}`},
		{"invalid JSON", "gpt-6-sol", `{invalid`},
		{"JSON array", "gpt-6-sol", `[{"prompt_cache_options":true}]`},
		{"JSON null", "gpt-6-sol", `null`},
		{"already compatible", "gpt-6-sol", ` {"prompt_cache_key":"keep"} `},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := sanitizeGPTPromptCacheHints([]byte(tc.body), tc.model)
			require.False(t, changed)
			require.Equal(t, tc.body, string(got))
		})
	}
}

func TestSanitizeGPTPromptCacheHintsOnlyTouchesProtocolMessages(t *testing.T) {
	body := []byte(`{
		"prompt_cache_options":{"mode":"explicit"},
		"prompt_cache_breakpoint":{"mode":"explicit"},
		"prompt_cache_key":"keep-key",
		"prompt_cache_retention":"24h",
		"input":[
			{"type":"message","role":"user","prompt_cache_breakpoint":true,"content":[{"type":"input_text","text":"hello","prompt_cache_breakpoint":true,"extra":{"number":9007199254740993}}]},
			{"type":"function_call","arguments":"{\"prompt_cache_breakpoint\":true}","prompt_cache_breakpoint":"keep"},
			{"type":"function_call_output","output":{"prompt_cache_breakpoint":"keep"}}
		],
		"tools":[{"type":"function","parameters":{"properties":{"prompt_cache_breakpoint":{"type":"string"}}}}],
		"metadata":{"prompt_cache_breakpoint":"keep","number":9007199254740993},
		"custom":{"prompt_cache_breakpoint":"keep"}
	}`)

	got, changed := sanitizeGPTPromptCacheHints(body, "openai/gpt-6-luna")
	require.True(t, changed)

	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got, &decoded))
	require.NotContains(t, decoded, "prompt_cache_options")
	require.NotContains(t, decoded, "prompt_cache_breakpoint")
	require.JSONEq(t, `"keep-key"`, string(decoded["prompt_cache_key"]))
	require.JSONEq(t, `"24h"`, string(decoded["prompt_cache_retention"]))
	require.JSONEq(t, `{"prompt_cache_breakpoint":"keep","number":9007199254740993}`, string(decoded["metadata"]))
	require.JSONEq(t, `{"prompt_cache_breakpoint":"keep"}`, string(decoded["custom"]))
	require.Contains(t, string(decoded["tools"]), `"prompt_cache_breakpoint"`)
	require.NotContains(t, string(decoded["input"]), `"prompt_cache_breakpoint":true`)
	require.Contains(t, string(decoded["input"]), `\"prompt_cache_breakpoint\":true`)
	require.Contains(t, string(decoded["input"]), `"prompt_cache_breakpoint":"keep"`)
	require.Contains(t, string(decoded["input"]), `9007199254740993`)
}

func TestSanitizeGPTPromptCacheHintsMessagesShape(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","prompt_cache_breakpoint":true,"content":[{"type":"text","text":"hello","prompt_cache_breakpoint":true}]}]}`)
	got, changed := sanitizeGPTPromptCacheHints(body, "gpt-5.4")
	require.True(t, changed)
	require.JSONEq(t, `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`, string(got))
}

func TestSanitizeGPTPromptCacheHintsMixedInputShapes(t *testing.T) {
	body := []byte(`{"input":[42,null,{"type":"function_call","prompt_cache_breakpoint":"keep"},{"role":"user","content":"plain","prompt_cache_breakpoint":true},{"type":"message","content":[null,"literal",{"type":"input_text","text":"hello","prompt_cache_breakpoint":true}]}]}`)
	got, changed := sanitizeGPTPromptCacheHints(body, "gpt-6-sol")
	require.True(t, changed)
	require.JSONEq(t, `{"input":[42,null,{"type":"function_call","prompt_cache_breakpoint":"keep"},{"role":"user","content":"plain"},{"type":"message","content":[null,"literal",{"type":"input_text","text":"hello"}]}]}`, string(got))

	noChanges := []byte(`{"input":[{"type":"function_call","prompt_cache_breakpoint":"keep"},{"role":"user","content":"plain"}]}`)
	got, changed = sanitizeGPTPromptCacheHints(noChanges, "gpt-6-sol")
	require.False(t, changed)
	require.Equal(t, string(noChanges), string(got))
}
