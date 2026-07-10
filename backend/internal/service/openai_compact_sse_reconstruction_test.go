package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newCompactSSEReconstructionContext(t *testing.T, path string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c
}

func TestSupplementCompactionItemFromSSE(t *testing.T) {
	finalResponse := []byte(`{"id":"resp_1","output":[{"id":"msg_1","type":"message"}]}`)
	doneEvent := `data: {"type":"response.output_item.done","item":{"id":"cmp_done","type":"compaction","encrypted_content":"done-value"}}\n\n`
	addedEvent := `data: {"type":"response.output_item.added","item":{"id":"cmp_added","type":"compaction_summary","encrypted_content":"added-value"}}\n\n`

	t.Run("done event appends raw compact item", func(t *testing.T) {
		got := supplementCompactionItemFromSSE(newCompactSSEReconstructionContext(t, "/v1/responses/compact"), finalResponse, doneEvent)
		require.Len(t, gjson.GetBytes(got, "output").Array(), 2)
		require.Equal(t, "done-value", gjson.GetBytes(got, "output.1.encrypted_content").String())
	})

	t.Run("added event is fallback", func(t *testing.T) {
		got := supplementCompactionItemFromSSE(newCompactSSEReconstructionContext(t, "/v1/responses/compact"), finalResponse, addedEvent)
		require.Equal(t, "compaction_summary", gjson.GetBytes(got, "output.1.type").String())
		require.Equal(t, "added-value", gjson.GetBytes(got, "output.1.encrypted_content").String())
	})

	t.Run("non compact path and already complete output are unchanged", func(t *testing.T) {
		nonCompact := supplementCompactionItemFromSSE(newCompactSSEReconstructionContext(t, "/v1/responses"), finalResponse, doneEvent)
		require.Equal(t, string(finalResponse), string(nonCompact))

		alreadyComplete := []byte(`{"output":[{"id":"cmp_existing","type":"compaction"}]}`)
		got := supplementCompactionItemFromSSE(newCompactSSEReconstructionContext(t, "/v1/responses/compact"), alreadyComplete, doneEvent)
		require.Equal(t, string(alreadyComplete), string(got))
	})
}

func TestCollectRawResponsesOutputItemsFromSSEKeepsCompactFallback(t *testing.T) {
	body := "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\"}}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"cmp_1\",\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}}\n\n"

	output, ok := collectRawResponsesOutputItemsFromSSE(body)
	require.True(t, ok)
	require.Len(t, gjson.ParseBytes(output).Array(), 2)
	require.Equal(t, "message", gjson.GetBytes(output, "0.type").String())
	require.Equal(t, "compaction", gjson.GetBytes(output, "1.type").String())
	require.Equal(t, "opaque", gjson.GetBytes(output, "1.encrypted_content").String())
}
