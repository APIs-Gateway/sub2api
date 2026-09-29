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

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func commandCodeNativeAccount(baseURL string) *Account {
	return &Account{
		ID: 7645, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "test-key", "base_url": baseURL},
		Extra:       openAIResponsesSupportedTestExtra(),
	}
}

func commandCodeClientToolsContext(body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, recorder
}

func TestCommandCodeResponsesBaseURLRequiresExactHTTPSHost(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://api.commandcode.ai/provider/v1", true},
		{"HTTPS://API.COMMANDCODE.AI:443/provider/v1", true},
		{"http://api.commandcode.ai/provider/v1", false},
		{"https://api.commandcode.ai:8443/provider/v1", false},
		{"https://api.commandcode.ai.evil.example/provider/v1", false},
		{"https://user@api.commandcode.ai/provider/v1", false},
		{"https://api.openai.com/v1", false},
		{"not a url", false},
	} {
		require.Equal(t, tc.want, isOfficialCommandCodeResponsesBaseURL(tc.url), tc.url)
	}
}

func TestAdaptCommandCodeResponsesHistoryOnlyDoesNotInventTools(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tool_choice":{"type":"tool_search"},"input":[
		{"type":"tool_search_call","id":"tsc_1","call_id":"call_1","arguments":{"query":"repo"},"execution":"client"},
		{"type":"tool_search_output","id":"tso_1","call_id":"call_1","output":{"groups":["repo"]},"execution":"client"},
		{"type":"custom_tool_call_output","call_id":"custom_1","output":"untouched"}
	]}`)
	adapted, mapping, err := adaptCommandCodeResponsesClientTools(body)
	require.NoError(t, err)
	require.True(t, mapping.ToolSearch)
	require.False(t, gjson.GetBytes(adapted, "tools").Exists())
	require.Equal(t, "function_call", gjson.GetBytes(adapted, "input.0.type").String())
	require.Equal(t, "tool_search", gjson.GetBytes(adapted, "input.0.name").String())
	require.JSONEq(t, `{"query":"repo"}`, gjson.GetBytes(adapted, "input.0.arguments").String())
	require.Equal(t, "function_call_output", gjson.GetBytes(adapted, "input.1.type").String())
	require.JSONEq(t, `{"groups":["repo"]}`, gjson.GetBytes(adapted, "input.1.output").String())
	require.Equal(t, "custom_tool_call_output", gjson.GetBytes(adapted, "input.2.type").String())
	require.Equal(t, "function", gjson.GetBytes(adapted, "tool_choice.type").String())
	require.Equal(t, "tool_search", gjson.GetBytes(adapted, "tool_choice.name").String())
}

func TestAdaptCommandCodeResponsesHistoryOnlyRejectsMalformedOutput(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"input":[{"type":"tool_search_output","output":"x"}]}`),
		[]byte(`{"input":[{"type":"tool_search_output","call_id":"call_1"}]}`),
	} {
		adapted, mapping, err := adaptCommandCodeResponsesClientTools(body)
		require.Error(t, err)
		require.Equal(t, body, adapted)
		require.Empty(t, mapping)
	}
}

func TestAdaptCommandCodeResponsesNoToolsNoHistoryIsNoop(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"continue"}`)
	adapted, mapping, err := adaptCommandCodeResponsesClientTools(body)
	require.NoError(t, err)
	require.Equal(t, body, adapted)
	require.Empty(t, mapping)

	// An explicitly present tools field must not inherit a previous turn's
	// tool_search mapping, even when that field has no usable declarations.
	withTools := []byte(`{"tools":null,"input":[{"type":"tool_search_call","call_id":"c1"}]}`)
	adapted, mapping, err = adaptCommandCodeResponsesClientTools(withTools)
	require.NoError(t, err)
	require.Equal(t, withTools, adapted)
	require.Empty(t, mapping)
	emptyTools := []byte(`{"tools":[],"input":[{"type":"tool_search_call","call_id":"c1"}]}`)
	adapted, mapping, err = adaptCommandCodeResponsesClientTools(emptyTools)
	require.NoError(t, err)
	require.Equal(t, emptyTools, adapted)
	require.Empty(t, mapping)
}

func TestForwardCommandCodeNativeResponsesLowersTopLevelToolSearch(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","stream":false,"tools":[{"type":"tool_search"},{"type":"function","name":"deferred_func","parameters":{"type":"object"},"defer_loading":true}],"input":[{"type":"tool_search_call","id":"tsc_1","call_id":"call_1","arguments":{"query":"repo"},"execution":"client"},{"type":"tool_search_output","id":"tso_1","call_id":"call_1","output":{"groups":["repo"]},"execution":"client"}]}`)
	c, recorder := commandCodeClientToolsContext(body)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_search","name":"tool_search","arguments":"{\"query\":\"repo\"}"}],"usage":{"input_tokens":2,"output_tokens":3}}`)),
	}}
	svc := openAIClientToolsTestService(upstream)
	result, err := svc.Forward(context.Background(), c, commandCodeNativeAccount("https://api.commandcode.ai/provider/v1"), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "gpt-5.4", result.BillingModel)
	require.Equal(t, "https://api.commandcode.ai/provider/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "tool_search", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "tools.1.defer_loading").Exists())
	require.Equal(t, "function_call", gjson.GetBytes(upstream.lastBody, "input.0.type").String())
	require.Equal(t, "function_call_output", gjson.GetBytes(upstream.lastBody, "input.1.type").String())
	require.Equal(t, "tool_search_call", gjson.Get(recorder.Body.String(), "output.0.type").String())
	require.Equal(t, "client", gjson.Get(recorder.Body.String(), "output.0.execution").String())
	require.JSONEq(t, `{"query":"repo"}`, gjson.Get(recorder.Body.String(), "output.0.arguments").Raw)
}

func TestForwardCommandCodeNativeResponsesRestoresHistoryOnlyReply(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","stream":false,"input":[{"type":"tool_search_call","call_id":"call_1","arguments":{"query":"repo"},"execution":"client"},{"type":"tool_search_output","call_id":"call_1","tools":[{"type":"function","name":"found"}],"execution":"client"}]}`)
	c, recorder := commandCodeClientToolsContext(body)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp_2","status":"completed","output":[{"type":"function_call","id":"fc_2","call_id":"call_search","name":"tool_search","arguments":"{\"query\":\"next\"}"}],"usage":{"input_tokens":2,"output_tokens":3}}`)),
	}}
	svc := openAIClientToolsTestService(upstream)
	result, err := svc.Forward(context.Background(), c, commandCodeNativeAccount("https://api.commandcode.ai/provider/v1"), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, gjson.GetBytes(upstream.lastBody, "tools").Exists())
	require.Equal(t, "function_call", gjson.GetBytes(upstream.lastBody, "input.0.type").String())
	require.Equal(t, "function_call_output", gjson.GetBytes(upstream.lastBody, "input.1.type").String())
	require.JSONEq(t, `[{"type":"function","name":"found"}]`, gjson.GetBytes(upstream.lastBody, "input.1.output").String())
	require.Equal(t, "tool_search_call", gjson.Get(recorder.Body.String(), "output.0.type").String())
}

func TestForwardCommandCodeNativeResponsesRestoresStreamingToolSearch(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","stream":true,"tools":[{"type":"tool_search"}],"input":"find tools"}`)
	sse := strings.Join([]string{
		`data: {"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_search","name":"tool_search","status":"in_progress"}}`,
		`data: {"type":"response.function_call_arguments.done","sequence_number":1,"item_id":"fc_1","call_id":"call_search","name":"tool_search","arguments":"{\"query\":\"repo\"}"}`,
		`data: {"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_search","name":"tool_search","arguments":"{\"query\":\"repo\"}","status":"completed"}}`,
		`data: {"type":"response.completed","sequence_number":3,"response":{"id":"resp_3","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_search","name":"tool_search","arguments":"{\"query\":\"repo\"}"}],"usage":{"input_tokens":2,"output_tokens":3}}}`,
	}, "\n\n") + "\n\n"
	c, recorder := commandCodeClientToolsContext(body)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}}
	svc := openAIClientToolsTestService(upstream)
	result, err := svc.Forward(context.Background(), c, commandCodeNativeAccount("https://api.commandcode.ai/provider/v1"), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Contains(t, recorder.Body.String(), `"type":"tool_search_call"`)
	require.Contains(t, recorder.Body.String(), `"execution":"client"`)
	require.NotContains(t, recorder.Body.String(), `"name":"tool_search"`)
}

func TestForwardCommandCodeNativeResponsesPreservesOtherAPIKeyAndNamespaces(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		body    []byte
	}{
		{"official OpenAI", "https://api.openai.com/v1", []byte(`{"model":"gpt-5.4","tools":[{"type":"tool_search"}],"input":"hi"}`)},
		{"other API key", "https://api.deepseek.com/v1", []byte(`{"model":"gpt-5.4","tools":[{"type":"tool_search"}],"input":"hi"}`)},
		{"similar host", "https://api.commandcode.ai.evil.example/provider/v1", []byte(`{"model":"gpt-5.4","tools":[{"type":"tool_search"}],"input":"hi"}`)},
		{"Command Code namespace", "https://api.commandcode.ai/provider/v1", []byte(`{"model":"gpt-5.4","tools":[{"type":"namespace","name":"code","tools":[{"type":"function","name":"run"}]},{"type":"tool_search"}],"input":"hi"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := commandCodeClientToolsContext(tc.body)
			// A previous Command Code attempt can leave a real mapping on this
			// context. Forward must clear it before trying another account.
			c.Set(openAIResponsesClientToolMappingContextKey, apicompat.ResponsesClientToolMapping{ToolSearch: true})
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"resp_other","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
			}}
			svc := openAIClientToolsTestService(upstream)
			result, err := svc.Forward(context.Background(), c, commandCodeNativeAccount(tc.baseURL), tc.body)
			require.NoError(t, err)
			require.NotNil(t, result)
			if tc.name == "Command Code namespace" {
				require.Equal(t, "namespace", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
				require.Equal(t, "tool_search", gjson.GetBytes(upstream.lastBody, "tools.1.type").String())
			} else {
				require.Equal(t, "tool_search", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
			}
			_, mapped := openAIResponsesClientToolMapping(c)
			require.False(t, mapped)
		})
	}
}

func TestForwardCommandCodePassthroughKeepsExistingClientToolAdapter(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","stream":false,"tools":[{"type":"tool_search"}],"input":"find"}`)
	c, recorder := commandCodeClientToolsContext(body)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp_passthrough","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"c1","name":"tool_search","arguments":"{\"query\":\"repo\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}}
	svc := openAIClientToolsTestService(upstream)
	account := commandCodeNativeAccount("https://api.commandcode.ai/provider/v1")
	account.Extra["openai_passthrough"] = true
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "tool_search_call", gjson.Get(recorder.Body.String(), "output.0.type").String())
}

func TestForwardCommandCodeForceChatCompletionsBypassesNativeAdapter(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","stream":false,"tools":[{"type":"tool_search"}],"input":"find"}`)
	c, recorder := commandCodeClientToolsContext(body)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"chatcmpl_force","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_search","type":"function","function":{"name":"tool_search","arguments":"{\"query\":\"repo\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)),
	}}
	svc := openAIClientToolsTestService(upstream)
	account := commandCodeNativeAccount("https://api.commandcode.ai/provider/v1")
	account.Extra = map[string]any{openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions)}
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "/provider/v1/chat/completions", upstream.lastReq.URL.Path)
	require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "tool_search_call", gjson.Get(recorder.Body.String(), "output.0.type").String())
}
