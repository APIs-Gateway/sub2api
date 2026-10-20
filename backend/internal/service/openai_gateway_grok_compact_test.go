//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestBuildGrokCompactRequestBodyUsesResponsesCompactionTurn in
// openai_gateway_grok_test.go already covers the array-input happy path and
// the tools -> tool_choice=none behavior. This only adds what that test
// doesn't: empty-tools, and the two error branches.
func TestBuildGrokCompactRequestBody_EdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("string input becomes a message", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"input":"hello there"}`)
		out, err := buildGrokCompactRequestBody(body)
		require.NoError(t, err)
		require.True(t, json.Valid(out))
		require.Equal(t, "message", gjson.GetBytes(out, "input.0.type").String())
		require.Equal(t, "hello there", gjson.GetBytes(out, "input.0.content.0.text").String())
	})

	t.Run("empty tools array does not force tool_choice", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"input":"hi","tools":[]}`)
		out, err := buildGrokCompactRequestBody(body)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(out, "tool_choice").Exists())
	})

	t.Run("invalid outer JSON errors", func(t *testing.T) {
		t.Parallel()
		_, err := buildGrokCompactRequestBody([]byte("not-json"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "decode compact request")
	})

	t.Run("invalid input shape errors", func(t *testing.T) {
		t.Parallel()
		_, err := buildGrokCompactRequestBody([]byte(`{"input":42}`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "compact input must be")
	})
}

func TestNormalizeGrokCompactInput(t *testing.T) {
	t.Parallel()

	t.Run("nil becomes an empty array", func(t *testing.T) {
		t.Parallel()
		out, err := normalizeGrokCompactInput(nil)
		require.NoError(t, err)
		require.Equal(t, []any{}, out)
	})

	t.Run("array passes through unchanged", func(t *testing.T) {
		t.Parallel()
		in := []any{map[string]any{"type": "message"}}
		out, err := normalizeGrokCompactInput(in)
		require.NoError(t, err)
		require.Equal(t, in, out)
	})

	t.Run("string is wrapped as a user message", func(t *testing.T) {
		t.Parallel()
		out, err := normalizeGrokCompactInput("hello")
		require.NoError(t, err)
		require.Len(t, out, 1)
		item := out[0].(map[string]any)
		require.Equal(t, "message", item["type"])
		require.Equal(t, "user", item["role"])
	})

	t.Run("single object is wrapped in an array", func(t *testing.T) {
		t.Parallel()
		obj := map[string]any{"type": "message", "role": "user"}
		out, err := normalizeGrokCompactInput(obj)
		require.NoError(t, err)
		require.Equal(t, []any{obj}, out)
	})

	t.Run("unsupported shape errors", func(t *testing.T) {
		t.Parallel()
		_, err := normalizeGrokCompactInput(42)
		require.Error(t, err)
	})
}

func TestConvertOpenAICompactInputsForGrok(t *testing.T) {
	t.Parallel()

	t.Run("no input array returns body unchanged", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"foo":"bar"}`)
		out, err := convertOpenAICompactInputsForGrok(body)
		require.NoError(t, err)
		require.Equal(t, body, out)
	})

	t.Run("no compaction items returns body unchanged", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"input":[{"type":"message","role":"user"}]}`)
		out, err := convertOpenAICompactInputsForGrok(body)
		require.NoError(t, err)
		require.Equal(t, body, out)
	})

	t.Run("compaction item with encrypted content and summary is converted", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"input":[
			{"type":"compaction","encrypted_content":"abc123","summary":[{"type":"summary_text","text":"the summary"}]}
		]}`)
		out, err := convertOpenAICompactInputsForGrok(body)
		require.NoError(t, err)
		require.Equal(t, "reasoning", gjson.GetBytes(out, "input.0.type").String())
		require.Equal(t, "abc123", gjson.GetBytes(out, "input.0.encrypted_content").String())
		require.Equal(t, "message", gjson.GetBytes(out, "input.1.type").String())
		require.Contains(t, gjson.GetBytes(out, "input.1.content.0.text").String(), "the summary")
	})

	t.Run("compaction_summary item without encrypted content only emits the summary message", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"input":[
			{"type":"compaction_summary","summary":[{"type":"summary_text","text":"only text"}]}
		]}`)
		out, err := convertOpenAICompactInputsForGrok(body)
		require.NoError(t, err)
		items := gjson.GetBytes(out, "input").Array()
		require.Len(t, items, 1)
		require.Equal(t, "message", items[0].Get("type").String())
	})

	t.Run("compaction item with neither field is dropped entirely", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"input":[
			{"type":"compaction","encrypted_content":"","summary":[]},
			{"type":"message","role":"user"}
		]}`)
		out, err := convertOpenAICompactInputsForGrok(body)
		require.NoError(t, err)
		items := gjson.GetBytes(out, "input").Array()
		require.Len(t, items, 1)
		require.Equal(t, "message", items[0].Get("type").String())
	})

	t.Run("invalid JSON errors", func(t *testing.T) {
		t.Parallel()
		_, err := convertOpenAICompactInputsForGrok([]byte("not-json"))
		require.Error(t, err)
	})
}

// TestConvertGrokResponseToOpenAICompact and
// TestConvertGrokResponseToOpenAICompactRequiresEncryptedContent in
// openai_gateway_grok_test.go already cover the reasoning+message happy path
// and the "output present but no reasoning" error. This adds the branches
// those don't reach: no summary content, no output key at all, and decode
// failure.
func TestConvertGrokResponseToOpenAICompact_EdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("reasoning without any message content omits the summary field", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"output":[{"type":"reasoning","encrypted_content":"enc-only"}]}`)
		out, err := convertGrokResponseToOpenAICompact(body)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(out, "output.0.summary").Exists())
	})

	t.Run("missing output key entirely errors", func(t *testing.T) {
		t.Parallel()
		_, err := convertGrokResponseToOpenAICompact([]byte(`{}`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "no output array")
	})

	t.Run("invalid JSON errors", func(t *testing.T) {
		t.Parallel()
		_, err := convertGrokResponseToOpenAICompact([]byte("not-json"))
		require.Error(t, err)
	})
}

func TestCompactSummaryText(t *testing.T) {
	t.Parallel()

	require.Equal(t, "", compactSummaryText("not-an-array"))
	require.Equal(t, "", compactSummaryText([]any{}))
	require.Equal(t, "a\nb", compactSummaryText([]any{
		map[string]any{"text": "a"},
		map[string]any{"text": "  "},
		map[string]any{"text": "b"},
		"not-a-map",
	}))
}

func TestIsOpenAICompactionType(t *testing.T) {
	t.Parallel()

	require.True(t, isOpenAICompactionType("compaction"))
	require.True(t, isOpenAICompactionType(" compaction_summary "))
	require.False(t, isOpenAICompactionType("message"))
	require.False(t, isOpenAICompactionType(""))
}

func TestStringValue(t *testing.T) {
	t.Parallel()

	require.Equal(t, "hi", stringValue("hi"))
	require.Equal(t, "", stringValue(42))
	require.Equal(t, "", stringValue(nil))
}
