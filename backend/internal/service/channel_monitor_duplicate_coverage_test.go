package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorDuplicateCloneHelpersHandleNilAndCopies(t *testing.T) {
	require.Nil(t, cloneInt64Pointer(nil))
	originalID := int64(9)
	clonedID := cloneInt64Pointer(&originalID)
	require.NotNil(t, clonedID)
	*clonedID = 10
	require.Equal(t, int64(9), originalID)

	require.Empty(t, cloneChannelMonitorHeaders(nil))
	originalHeaders := map[string]string{"User-Agent": "Codex"}
	clonedHeaders := cloneChannelMonitorHeaders(originalHeaders)
	clonedHeaders["User-Agent"] = "changed"
	require.Equal(t, "Codex", originalHeaders["User-Agent"])

	originalBody := map[string]any{
		"metadata": map[string]any{"source": "original"},
	}
	clonedBody, err := cloneChannelMonitorJSONMap(originalBody)
	require.NoError(t, err)
	clonedBody["metadata"].(map[string]any)["source"] = "changed"
	require.Equal(t, "original", originalBody["metadata"].(map[string]any)["source"])
	nilBody, err := cloneChannelMonitorJSONMap(nil)
	require.NoError(t, err)
	require.Nil(t, nilBody)
}
