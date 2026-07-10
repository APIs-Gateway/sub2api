package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newCompactBridgeTestContext(t *testing.T, markClientStream bool) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	if markClientStream {
		MarkOpenAICompactClientStream(c)
	}
	return c, rec
}

func newCompactBridgeTestService() *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg:           &config.Config{},
		toolCorrector: NewCodexToolCorrector(),
	}
}

func parseCompactBridgeSSE(t *testing.T, body string) [][2]string {
	t.Helper()
	var events [][2]string
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		lines := strings.Split(block, "\n")
		require.Len(t, lines, 2, "SSE event should contain event and data lines: %q", block)
		require.True(t, strings.HasPrefix(lines[0], "event: "))
		require.True(t, strings.HasPrefix(lines[1], "data: "))
		events = append(events, [2]string{
			strings.TrimPrefix(lines[0], "event: "),
			strings.TrimPrefix(lines[1], "data: "),
		})
	}
	return events
}

func TestBuildOpenAICompactSSEPayload_EmitsItemsAndCompleted(t *testing.T) {
	finalResponse := []byte(`{
		"id":"resp_compact_1",
		"output":[
			{"id":"cmp_1","type":"compaction","encrypted_content":"compact-payload","opaque":{"kept":true}},
			{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"done"}]}
		],
		"usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13}
	}`)

	payload, ok := buildOpenAICompactSSEPayload(finalResponse)
	require.True(t, ok)

	events := parseCompactBridgeSSE(t, string(payload))
	require.Len(t, events, 3)
	require.Equal(t, "response.output_item.done", events[0][0])
	require.Equal(t, int64(0), gjson.Get(events[0][1], "output_index").Int())
	require.Equal(t, "compaction", gjson.Get(events[0][1], "item.type").String())
	require.Equal(t, "compact-payload", gjson.Get(events[0][1], "item.encrypted_content").String())
	require.True(t, gjson.Get(events[0][1], "item.opaque.kept").Bool())
	require.Equal(t, int64(1), gjson.Get(events[1][1], "output_index").Int())
	require.Equal(t, "message", gjson.Get(events[1][1], "item.type").String())
	require.Equal(t, "response.completed", events[2][0])
	require.Equal(t, "resp_compact_1", gjson.Get(events[2][1], "response.id").String())
	require.Equal(t, int64(13), gjson.Get(events[2][1], "response.usage.total_tokens").Int())
}

func TestBuildOpenAICompactSSEPayload_RepairsCodexRequiredFields(t *testing.T) {
	payload, ok := buildOpenAICompactSSEPayload([]byte(`{
		"output":[{"type":"compaction","encrypted_content":"x"}],
		"usage":{"prompt_tokens":9,"completion_tokens":4}
	}`))
	require.True(t, ok)

	events := parseCompactBridgeSSE(t, string(payload))
	completed := events[len(events)-1][1]
	id := gjson.Get(completed, "response.id").String()
	require.True(t, strings.HasPrefix(id, "resp_"))
	require.NotEqual(t, "resp_", id)
	require.False(t, gjson.Get(completed, "response.usage").Exists())
}

func TestBuildOpenAICompactSSEPayload_PreservesWellFormedUsageAndRejectsInvalidBodies(t *testing.T) {
	payload, ok := buildOpenAICompactSSEPayload([]byte(`{
		"id":"resp_1",
		"output":[{"type":"compaction","encrypted_content":"x"}],
		"usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13,"input_tokens_details":{"cached_tokens":2}}
	}`))
	require.True(t, ok)
	completed := parseCompactBridgeSSE(t, string(payload))[1][1]
	require.Equal(t, int64(9), gjson.Get(completed, "response.usage.input_tokens").Int())
	require.Equal(t, int64(2), gjson.Get(completed, "response.usage.input_tokens_details.cached_tokens").Int())

	for name, body := range map[string][]byte{
		"empty":     nil,
		"sse":       []byte("data: {\"type\":\"response.completed\"}\n\n"),
		"array":     []byte(`[{"id":"resp_1"}]`),
		"non-json":  []byte("upstream said no"),
		"bare-bool": []byte("true"),
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := buildOpenAICompactSSEPayload(body)
			require.False(t, ok)
		})
	}
}

func TestWriteOpenAICompactSSEBridge_RequiresMarkAndSuccessStatus(t *testing.T) {
	finalResponse := []byte(`{"id":"resp_1","output":[{"type":"compaction","encrypted_content":"x"}]}`)

	c, rec := newCompactBridgeTestContext(t, false)
	require.False(t, writeOpenAICompactSSEBridge(c, http.StatusOK, finalResponse))
	require.Zero(t, rec.Body.Len())

	c, rec = newCompactBridgeTestContext(t, true)
	require.False(t, writeOpenAICompactSSEBridge(c, http.StatusBadGateway, finalResponse))
	require.Zero(t, rec.Body.Len())

	c, rec = newCompactBridgeTestContext(t, true)
	require.False(t, writeOpenAICompactSSEBridge(c, http.StatusOK, []byte("not json")))
	require.Zero(t, rec.Body.Len())

	c, rec = newCompactBridgeTestContext(t, true)
	require.True(t, writeOpenAICompactSSEBridge(c, http.StatusOK, finalResponse))
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), "event: response.completed")
}

func TestOpenAICompactClientStreamHelpersRejectNilAndInvalidMarkers(t *testing.T) {
	MarkOpenAICompactClientStream(nil)
	require.Equal(t, "openai_compact_client_stream", OpenAICompactClientStreamKeyForTest())
	require.False(t, openAICompactClientWantsStream(nil))

	c, _ := newCompactBridgeTestContext(t, false)
	require.False(t, openAICompactClientWantsStream(c))
	c.Set(OpenAICompactClientStreamKeyForTest(), "not-a-bool")
	require.False(t, openAICompactClientWantsStream(c))
}

func TestBuildOpenAICompactSSEPayload_SkipsNonOutputObjectsAndMalformedUsage(t *testing.T) {
	payload, ok := buildOpenAICompactSSEPayload([]byte(`{
		"id":"resp_1",
		"output":[null,{"type":"compaction","encrypted_content":"x"}],
		"usage":null
	}`))
	require.True(t, ok)

	events := parseCompactBridgeSSE(t, string(payload))
	require.Len(t, events, 2)
	require.Equal(t, "compaction", gjson.Get(events[0][1], "item.type").String())
	require.False(t, gjson.Get(events[1][1], "response.usage").Exists())
}

func compactBridgeResponse(id, payload string, input, output int) string {
	return fmt.Sprintf(`{"id":%q,"object":"response","status":"completed","output":[{"id":"cmp_1","type":"compaction","encrypted_content":%q}],"usage":{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d}}`, id, payload, input, output, input+output)
}

func TestHandleNonStreamingResponse_CompactClientStreamBridgesToSSE(t *testing.T) {
	svc := newCompactBridgeTestService()
	c, rec := newCompactBridgeTestContext(t, true)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(compactBridgeResponse("resp_compact_json", "compact-payload", 9, 4))),
	}

	result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, &Account{ID: 1, Type: AccountTypeOAuth}, "gpt-5.5", "gpt-5.5")
	require.NoError(t, err)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	events := parseCompactBridgeSSE(t, rec.Body.String())
	require.Len(t, events, 2)
	require.Equal(t, "compaction", gjson.Get(events[0][1], "item.type").String())
	require.Equal(t, "resp_compact_json", gjson.Get(events[1][1], "response.id").String())
	require.NotNil(t, result)
	require.Equal(t, 9, result.usage.InputTokens)
}

func TestHandleNonStreamingResponse_PathBasedCompactStaysJSON(t *testing.T) {
	svc := newCompactBridgeTestService()
	c, rec := newCompactBridgeTestContext(t, false)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(compactBridgeResponse("resp_compact_json", "compact-payload", 9, 4))),
	}

	_, err := svc.handleNonStreamingResponse(context.Background(), resp, c, &Account{ID: 1, Type: AccountTypeOAuth}, "gpt-5.5", "gpt-5.5")
	require.NoError(t, err)
	require.NotContains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	require.Equal(t, "resp_compact_json", gjson.Get(rec.Body.String(), "id").String())
}

func TestHandleSSEToJSON_CompactClientStreamBridgesToSSE(t *testing.T) {
	svc := newCompactBridgeTestService()
	c, rec := newCompactBridgeTestContext(t, true)
	upstreamSSE := `data: {"type":"response.completed","response":` + compactBridgeResponse("resp_compact_sse", "compact-sse-payload", 3, 2) + `}` + "\n\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}

	result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, &Account{ID: 1, Type: AccountTypeOAuth}, "gpt-5.5", "gpt-5.5")
	require.NoError(t, err)
	events := parseCompactBridgeSSE(t, rec.Body.String())
	require.Equal(t, "compact-sse-payload", gjson.Get(events[0][1], "item.encrypted_content").String())
	require.Equal(t, "resp_compact_sse", gjson.Get(events[1][1], "response.id").String())
	require.NotNil(t, result)
	require.Equal(t, 3, result.usage.InputTokens)
}

func TestHandleNonStreamingResponsePassthrough_CompactClientStreamBridgesToSSE(t *testing.T) {
	svc := newCompactBridgeTestService()
	c, rec := newCompactBridgeTestContext(t, true)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(compactBridgeResponse("resp_compact_pt", "compact-pt-payload", 7, 3))),
	}

	result, err := svc.handleNonStreamingResponsePassthrough(context.Background(), resp, c, "gpt-5.5", "")
	require.NoError(t, err)
	events := parseCompactBridgeSSE(t, rec.Body.String())
	require.Equal(t, "compaction", gjson.Get(events[0][1], "item.type").String())
	require.Equal(t, "resp_compact_pt", gjson.Get(events[1][1], "response.id").String())
	require.NotNil(t, result)
	require.Equal(t, 7, result.usage.InputTokens)
}

func TestHandlePassthroughSSEToJSON_CompactClientStreamBridgesToSSE(t *testing.T) {
	svc := newCompactBridgeTestService()
	c, rec := newCompactBridgeTestContext(t, true)
	upstreamSSE := `data: {"type":"response.completed","response":` + compactBridgeResponse("resp_compact_pt_sse", "compact-pt-sse-payload", 5, 2) + `}` + "\n\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	result, err := svc.handlePassthroughSSEToJSON(resp, c, []byte(upstreamSSE), "gpt-5.5", "")
	require.NoError(t, err)
	events := parseCompactBridgeSSE(t, rec.Body.String())
	require.Equal(t, "compact-pt-sse-payload", gjson.Get(events[0][1], "item.encrypted_content").String())
	require.Equal(t, "resp_compact_pt_sse", gjson.Get(events[1][1], "response.id").String())
	require.NotNil(t, result)
	require.Equal(t, 5, result.usage.InputTokens)
}

func TestReconstructResponseOutputFromSSE_PreservesRawCompactDoneItem(t *testing.T) {
	bodyText := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"id":"cmp_1","type":"compaction","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"kept"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[]}}`,
	}, "\n")

	output, ok := reconstructResponseOutputFromSSE(bodyText)
	require.True(t, ok)
	require.Equal(t, "opaque", gjson.GetBytes(output, "0.encrypted_content").String())
	require.Equal(t, "kept", gjson.GetBytes(output, "0.summary.0.text").String())
}

func TestHandleSSEToJSON_CompactSupplementsMissingCompaction(t *testing.T) {
	svc := newCompactBridgeTestService()
	c, rec := newCompactBridgeTestContext(t, true)
	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"id":"cmp_1","type":"compaction","encrypted_content":"supplement"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"note"}]}]}}`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}

	_, err := svc.handleNonStreamingResponse(context.Background(), resp, c, &Account{ID: 1, Type: AccountTypeOAuth}, "gpt-5.5", "gpt-5.5")
	require.NoError(t, err)
	events := parseCompactBridgeSSE(t, rec.Body.String())
	require.Len(t, events, 3)
	require.Equal(t, "message", gjson.Get(events[0][1], "item.type").String())
	require.Equal(t, "compaction", gjson.Get(events[1][1], "item.type").String())
	require.Equal(t, "supplement", gjson.Get(events[2][1], "response.output.1.encrypted_content").String())
}

func TestOpenAICompactSSEKeepalive_OnlyHeartbeatDoesNotCountAsResponse(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	defer stop()
	value, ok := c.Get(openAICompactSSEKeepaliveKey)
	require.True(t, ok)
	keepalive := value.(*openAICompactSSEKeepalive)
	require.True(t, keepalive.beat())
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))

	require.True(t, writeOpenAICompactSSEBridge(c, http.StatusBadGateway, []byte(`{"error":{"message":"upstream failed"}}`)))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), ": keepalive\n\n")
	require.Contains(t, rec.Body.String(), "event: response.failed\n")
	require.Contains(t, rec.Body.String(), `"message":"upstream failed"`)
}
