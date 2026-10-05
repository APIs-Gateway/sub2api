package antigravity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestThinkingSignatureWireClaudeBlocks(t *testing.T) {
	for _, block := range []ClaudeContentItem{{Type: "thinking"}, {Type: "thinking", Thinking: "plan"}, {Type: "thinking", Signature: "signed"}, {Type: "thinking", Thinking: "plan", Signature: "signed"}} {
		wire, err := json.Marshal(block)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(wire, &fields))
		require.Contains(t, fields, "thinking")
		require.Contains(t, fields, "signature")
		var signature string
		require.NoError(t, json.Unmarshal(fields["signature"], &signature))
		require.Equal(t, block.Signature, signature)
	}
	for _, block := range []ClaudeContentItem{{Type: "text", Text: "answer"}, {Type: "tool_use", ID: "call_1", Name: "lookup", Input: map[string]any{}}} {
		wire, err := json.Marshal(block)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(wire, &fields))
		require.NotContains(t, fields, "thinking")
		require.NotContains(t, fields, "signature")
	}
}

func TestThinkingSignatureWireGeminiBufferedAndStreamed(t *testing.T) {
	for _, parts := range []string{
		`[{"text":"plan","thought":true}]`,
		`[{"text":"plan","thought":true,"thoughtSignature":"signed"}]`,
		`[{"thought":true,"thoughtSignature":"signed"}]`,
		`[{"text":"answer","thoughtSignature":"signed"}]`,
	} {
		t.Run(parts, func(t *testing.T) {
			payload := `{"response":{"candidates":[{"content":{"parts":` + parts + `},"finishReason":"STOP"}]}}`
			wire, _, err := TransformGeminiToClaude([]byte(payload), "gemini-3.8-flash")
			require.NoError(t, err)
			var response struct {
				Content []map[string]json.RawMessage `json:"content"`
			}
			require.NoError(t, json.Unmarshal(wire, &response))
			starts := 0
			for _, block := range response.Content {
				if string(block["type"]) == `"thinking"` {
					require.Contains(t, block, "thinking")
					require.Contains(t, block, "signature")
					var signature string
					require.NoError(t, json.Unmarshal(block["signature"], &signature))
					if strings.Contains(parts, "signed") {
						require.Equal(t, "signed", signature)
					} else {
						require.Empty(t, signature)
					}
					starts++
				}
			}
			require.Equal(t, 1, starts, string(wire))
			processor := NewStreamingProcessor("gemini-3.8-flash")
			output := processor.ProcessLine("data: " + payload)
			tail, _ := processor.Finish()
			output = append(output, tail...)
			starts = 0
			signatures := 0
			for _, line := range strings.Split(string(output), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var event struct {
					Type         string                     `json:"type"`
					ContentBlock map[string]json.RawMessage `json:"content_block"`
					Delta        map[string]json.RawMessage `json:"delta"`
				}
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
				if event.Type == "content_block_start" && string(event.ContentBlock["type"]) == `"thinking"` {
					require.Contains(t, event.ContentBlock, "signature")
					require.JSONEq(t, `""`, string(event.ContentBlock["signature"]))
					starts++
				}
				if string(event.Delta["type"]) == `"signature_delta"` {
					require.JSONEq(t, `"signed"`, string(event.Delta["signature"]))
					signatures++
				}
			}
			require.Equal(t, 1, starts, string(output))
			if strings.Contains(parts, "signed") {
				require.Equal(t, 1, signatures)
			} else {
				require.Zero(t, signatures)
			}
		})
	}
}
