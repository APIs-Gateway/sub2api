//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 新版 Codex 不发顶层 tools，工具都在 input 里的 additional_tools 项中。
const additionalToolsOnlyBodyTemplate = `{"model":"gpt-5.6","stream":%s,"input":[{"type":"additional_tools","tools":[` +
	`{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec","description":"run"},{"type":"function","name":"wait","parameters":{"type":"object"}},{"type":"function","name":"request_user_input","parameters":{"type":"object"}}]},` +
	`{"type":"namespace","name":"clock","tools":[{"type":"function","name":"sleep","parameters":{"type":"object"}}]},` +
	`{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}},{"type":"function","name":"wait_agent","parameters":{"type":"object"}}]}` +
	`]},{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]}]}`

func additionalToolsOnlyBody(stream bool) []byte {
	s := "false"
	if stream {
		s = "true"
	}
	return []byte(strings.Replace(additionalToolsOnlyBodyTemplate, "%s", s, 1))
}

func newAdditionalToolsContext(body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

func TestForwardResponses_ChatFallbackAdditionalToolsStreamingRestoresNamespace(t *testing.T) {
	body := additionalToolsOnlyBody(true)
	c, rec := newAdditionalToolsContext(body)
	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_at","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"collaboration__spawn_agent","arguments":"{}"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_at","object":"chat.completion.chunk","choices":[{"index":1,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"functions__exec","arguments":"{}"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_at","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	_, err := svc.Forward(context.Background(), c, forceChatResponsesFallbackAccount(), body)
	require.NoError(t, err)
	require.Contains(t, gjson.GetBytes(upstream.lastBody, "tools.#.function.name").Raw, `"functions__exec"`)
	require.Contains(t, gjson.GetBytes(upstream.lastBody, "tools.#.function.name").Raw, `"collaboration__spawn_agent"`)

	out := rec.Body.String()
	var added []gjson.Result
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		ev := gjson.Parse(strings.TrimPrefix(line, "data: "))
		if ev.Get("type").String() == "response.output_item.added" || ev.Get("type").String() == "response.output_item.done" {
			added = append(added, ev.Get("item"))
		}
	}
	require.NotEmpty(t, added)
	var sawSpawn, sawExec bool
	for _, item := range added {
		switch item.Get("name").String() {
		case "spawn_agent":
			sawSpawn = true
			require.Equal(t, "function_call", item.Get("type").String())
			require.Equal(t, "collaboration", item.Get("namespace").String())
		case "exec":
			sawExec = true
			require.Equal(t, "custom_tool_call", item.Get("type").String())
			require.Equal(t, "functions", item.Get("namespace").String())
		default:
			t.Fatalf("unexpected item name restored: %s", item.Raw)
		}
	}
	require.True(t, sawSpawn)
	require.True(t, sawExec)
}

func TestForwardResponses_ChatFallbackAdditionalToolsNonStreamingRestoresNamespace(t *testing.T) {
	body := additionalToolsOnlyBody(false)
	c, rec := newAdditionalToolsContext(body)
	chatJSON := `{"id":"chatcmpl_at_json","object":"chat.completion","model":"gpt-5.6","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[` +
		`{"id":"call_a","type":"function","function":{"name":"collaboration__spawn_agent","arguments":"{}"}},` +
		`{"id":"call_b","type":"function","function":{"name":"functions__exec","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`
	upstream := &httpUpstreamRecorder{resp: upstreamModelMismatchHTTPResponse("application/json", "rid_at_json", chatJSON)}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	_, err := svc.forwardResponsesViaRawChatCompletions(context.Background(), c, forceChatResponsesFallbackAccount(), body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	out := rec.Body.String()
	require.Equal(t, "spawn_agent", gjson.Get(out, `output.#(type=="function_call").name`).String(), out)
	require.Equal(t, "collaboration", gjson.Get(out, `output.#(type=="function_call").namespace`).String(), out)
	require.Equal(t, "exec", gjson.Get(out, `output.#(type=="custom_tool_call").name`).String(), out)
	require.Equal(t, "functions", gjson.Get(out, `output.#(type=="custom_tool_call").namespace`).String(), out)
	require.Equal(t, `{}`, gjson.Get(out, `output.#(type=="function_call").arguments`).String(), out)
}
