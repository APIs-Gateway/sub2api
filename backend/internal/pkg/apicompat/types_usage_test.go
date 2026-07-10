package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesUsageUnmarshal_CacheWriteCompatibility(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{
			name: "input details cache write zero wins",
			body: `{"input_tokens":3,"output_tokens":5,"cache_creation_input_tokens":7,"input_tokens_details":{"cache_write_tokens":0}}`,
			want: 0,
		},
		{
			name: "prompt details cache write wins",
			body: `{"prompt_tokens":3,"completion_tokens":5,"cache_creation_input_tokens":7,"prompt_tokens_details":{"cache_write_tokens":4}}`,
			want: 4,
		},
		{
			name: "input details cache creation fallback",
			body: `{"input_tokens":3,"output_tokens":5,"input_tokens_details":{"cache_creation_tokens":6}}`,
			want: 6,
		},
		{
			name: "prompt details cache creation fallback",
			body: `{"prompt_tokens":3,"completion_tokens":5,"prompt_tokens_details":{"cache_creation_tokens":5}}`,
			want: 5,
		},
		{
			name: "top level cache write input fallback",
			body: `{"input_tokens":3,"output_tokens":5,"cache_write_input_tokens":3}`,
			want: 3,
		},
		{
			name: "top level cache creation fallback",
			body: `{"input_tokens":3,"output_tokens":5,"cache_creation_tokens":2}`,
			want: 2,
		},
		{
			name: "top level cache write fallback",
			body: `{"input_tokens":3,"output_tokens":5,"cache_write_tokens":1}`,
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var usage ResponsesUsage
			err := json.Unmarshal([]byte(tt.body), &usage)
			require.NoError(t, err)
			require.Equal(t, tt.want, usage.CacheCreationInputTokens)
		})
	}
}
