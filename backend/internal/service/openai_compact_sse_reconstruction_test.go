package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func newReconstructionCommittedCompactContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	MarkOpenAICompactClientStream(c)
	stop := StartOpenAICompactSSEKeepalive(c, time.Millisecond)
	require.Eventually(t, func() bool {
		return c.Writer.Written()
	}, time.Second, time.Millisecond)
	return c, rec, stop
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

		noItem := supplementCompactionItemFromSSE(newCompactSSEReconstructionContext(t, "/v1/responses/compact"), finalResponse, "data: {\"type\":\"response.completed\"}\n\n")
		require.Equal(t, string(finalResponse), string(noItem))
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

func TestCompactCommittedErrorPathsEmitResponsesFailure(t *testing.T) {
	t.Run("non streaming protocol error", func(t *testing.T) {
		c, rec, stop := newReconstructionCommittedCompactContext(t)
		defer stop()

		svc := &OpenAIGatewayService{}
		err := svc.writeOpenAINonStreamingProtocolError(&http.Response{Header: make(http.Header)}, c, "")
		require.Error(t, err)
		require.Contains(t, err.Error(), "non-streaming openai protocol error")
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), "event: response.failed")
		require.Contains(t, rec.Body.String(), "invalid non-streaming response")
	})

	t.Run("fast policy block", func(t *testing.T) {
		c, rec, stop := newReconstructionCommittedCompactContext(t)
		defer stop()

		writeOpenAIFastPolicyBlockedResponse(c, &OpenAIFastBlockedError{Message: "blocked by policy"})
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), "event: response.failed")
		require.Contains(t, rec.Body.String(), "blocked by policy")
	})
}
