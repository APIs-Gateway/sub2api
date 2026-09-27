//go:build unit

package antigravity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStreamingProcessor_HasContentExcludesProtocolOnlyEvents(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{"signature only", `{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`, false},
		{"empty thinking with signature", `{"response":{"candidates":[{"content":{"parts":[{"text":"","thought":true,"thoughtSignature":"sig"}]},"finishReason":"STOP"}]}}`, false},
		{"text", `{"response":{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}}`, true},
		{"text with signature", `{"response":{"candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"sig"}]},"finishReason":"STOP"}]}}`, true},
		{"thinking", `{"response":{"candidates":[{"content":{"parts":[{"text":"reason","thought":true}]},"finishReason":"STOP"}]}}`, true},
		{"tool call", `{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]},"finishReason":"STOP"}]}}`, true},
		{"grounding text", `{"response":{"candidates":[{"groundingMetadata":{"webSearchQueries":["query"]},"finishReason":"STOP"}]}}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			processor := NewStreamingProcessor("gemini-3.8-flash")
			output := processor.ProcessLine("data: " + tc.payload)
			require.NotEmpty(t, output)
			require.Equal(t, tc.want, processor.HasContent())
		})
	}
}
