package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesUsageUnmarshal_NestedCacheWriteZeroWins(t *testing.T) {
	var usage ResponsesUsage
	err := json.Unmarshal([]byte(`{"input_tokens":3,"output_tokens":5,"cache_creation_input_tokens":7,"input_tokens_details":{"cache_write_tokens":0}}`), &usage)
	require.NoError(t, err)
	require.Zero(t, usage.CacheCreationInputTokens)
}
