package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestForwardAsChatCompletions_ResponsesSupportedFallsBackWhenStructuredInputRequiresString(t *testing.T) {
	for name, rejection := range map[string]string{
		"OpenAI invalid type": `{"error":{"type":"invalid_request_error","code":"invalid_type","param":"input","message":"Invalid type for 'input': expected a string, but got an array instead."}}`,
		"vLLM validation":     structuredVLLMRejection,
	} {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"thinking","thinking":"think"}],"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"done"},{"role":"user","content":"what now"}],"stream":false}`)
			originalBody := append([]byte(nil), body...)
			logs, releaseLogs := captureStructuredLog(t)
			defer releaseLogs()
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{
					StatusCode: http.StatusBadRequest,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body: io.NopCloser(strings.NewReader(
						rejection,
					)),
				},
				{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_raw_fallback"}},
					Body: io.NopCloser(strings.NewReader(
						`{"id":"chatcmpl_fallback","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
					)),
				},
			}}
			svc := &OpenAIGatewayService{
				cfg:          structuredInputRetryTestConfig(),
				httpUpstream: upstream,
			}
			account := structuredInputRetryTestAccount()

			result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.requests, 2)
			require.Equal(t, "/v1/responses", upstream.requests[0].URL.Path)
			require.True(t, gjson.GetBytes(upstream.bodies[0], "input").IsArray())
			require.Equal(t, "/v1/chat/completions", upstream.requests[1].URL.Path)
			require.JSONEq(t, string(body), string(upstream.bodies[1]))
			require.Equal(t, originalBody, body)
			require.Equal(t, "ok", gjson.Get(rec.Body.String(), "choices.0.message.content").String())
			require.True(t, OpenAIStructuredInputRecoveredViaRawChat(c))
			require.True(t, logs.ContainsMessage("structured Responses input rejected"))
			logs.mu.Lock()
			defer logs.mu.Unlock()
			for _, event := range logs.events {
				require.NotContains(t, event.Fields, "upstream_message")
				require.NotContains(t, event.Fields, "upstream_body")
				require.NotContains(t, event.Message, rejection)
			}
		})
	}
}

func TestConvertedResponsesInputStringRejectionIsNarrow(t *testing.T) {
	matching := []byte(`{"error":{"code":"invalid_type","param":"input","message":"Expected a string for input, but got an object instead."}}`)
	require.True(t, isConvertedResponsesInputStringRejection(http.StatusBadRequest, matching, nil))

	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "nested content validation", status: http.StatusBadRequest, body: `{"error":{"message":"'loc':('body','input',0,'content','str'), 'msg':'Input should be a valid string'"}}`},
		{name: "other field validation", status: http.StatusBadRequest, body: `{"error":{"message":"'loc':('body','model','str'), 'msg':'Input should be a valid string'"}}`},
		{name: "generic string validation", status: http.StatusBadRequest, body: `{"error":{"message":"Input should be a valid string"}}`},
		{name: "wrong status", status: http.StatusUnprocessableEntity, body: string(matching)},
		{name: "authentication", status: http.StatusUnauthorized, body: string(matching)},
		{name: "authorization", status: http.StatusForbidden, body: string(matching)},
		{name: "rate limit", status: http.StatusTooManyRequests, body: string(matching)},
		{name: "server error", status: http.StatusInternalServerError, body: string(matching)},
		{name: "wrong parameter", status: http.StatusBadRequest, body: `{"error":{"code":"invalid_type","param":"tools","message":"Expected a string, but got an array instead."}}`},
		{name: "wrong code", status: http.StatusBadRequest, body: `{"error":{"code":"invalid_request_error","param":"input","message":"Expected a string, but got an array instead."}}`},
		{name: "unrelated input error", status: http.StatusBadRequest, body: `{"error":{"code":"invalid_type","param":"input","message":"Input is too long."}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.False(t, isConvertedResponsesInputStringRejection(tc.status, []byte(tc.body), nil))
		})
	}
}

func TestForwardAsChatCompletions_StructuredInputRejectionDoesNotRetryIneligibleRequests(t *testing.T) {
	rejection := `{"error":{"code":"invalid_type","param":"input","message":"Expected a string, but got an array instead."}}`
	for _, test := range []struct {
		name          string
		mode          string
		unknown       bool
		platform      string
		accountType   string
		responsesBody bool
		nativeIngress bool
		status        int
		rejection     string
	}{
		{name: "forced Responses", mode: "force_responses"},
		{name: "forced Responses without probe", mode: "force_responses", unknown: true},
		{name: "unknown support", unknown: true},
		{name: "Responses shaped Chat ingress", responsesBody: true},
		{name: "native Responses ingress", responsesBody: true, nativeIngress: true},
		{name: "OAuth", accountType: AccountTypeOAuth},
		{name: "authentication", status: http.StatusUnauthorized},
		{name: "authorization", status: http.StatusForbidden},
		{name: "rate limit", status: http.StatusTooManyRequests},
		{name: "unrelated validation", rejection: `{"error":{"code":"invalid_type","param":"tools","message":"Expected a string, but got an array instead."}}`},
		{name: "nested validation", rejection: `{"error":{"message":"'loc':('body','input',0,'content','str'), 'msg':'Input should be a valid string'"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
			if test.responsesBody {
				body = []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"hello"}],"stream":false}`)
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			account := structuredInputRetryTestAccount()
			account.Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: true, openai_compat.ExtraKeyResponsesMode: test.mode}
			if test.unknown {
				delete(account.Extra, openai_compat.ExtraKeyResponsesSupported)
			}
			if test.platform != "" {
				account.Platform = test.platform
				account.Credentials["api_protocol"] = APIProtocolResponses
			}
			if test.accountType != "" {
				account.Type = test.accountType
				account.Credentials["access_token"] = "test-token"
			}
			status := test.status
			if status == 0 {
				status = http.StatusBadRequest
			}
			errorBody := rejection
			if test.rejection != "" {
				errorBody = test.rejection
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(errorBody)),
			}}
			svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: upstream}
			var result *OpenAIForwardResult
			var err error
			if test.nativeIngress {
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				result, err = svc.Forward(context.Background(), c, account, body)
			} else {
				result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
			}
			require.Error(t, err)
			require.Nil(t, result)
			require.Len(t, upstream.requests, 1)
			if test.unknown && test.mode != "force_responses" {
				require.Equal(t, "/v1/chat/completions", upstream.requests[0].URL.Path)
			} else {
				require.True(t, strings.HasSuffix(upstream.requests[0].URL.Path, "/responses"))
			}
			var failover *UpstreamFailoverError
			if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests {
				require.ErrorAs(t, err, &failover)
				require.Equal(t, status, failover.StatusCode)
			} else {
				require.False(t, errors.As(err, &failover))
				require.True(t, c.Writer.Written())
			}
		})
	}
}

func TestForwardAsChatCompletions_StructuredInputRetryPreservesStreamingAndMapping(t *testing.T) {
	for _, useAccountMapping := range []bool{false, true} {
		t.Run(fmt.Sprintf("account_mapping=%t", useAccountMapping), func(t *testing.T) {
			body := []byte(`{"model":"client-model","messages":[{"role":"user","content":"hello"}],"stream":true,"stream_options":{"include_usage":false},"vendor_option":"preserve"}`)
			originalBody := append([]byte(nil), body...)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			account := structuredInputRetryTestAccount()
			account.Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: true, openai_compat.ExtraKeyResponsesMode: "auto"}
			mappedModel := "gpt-5.4"
			if useAccountMapping {
				account.Credentials["model_mapping"] = map[string]any{"client-model": mappedModel}
			}
			stream := "data: {\"id\":\"chatcmpl_retry\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4,\"total_tokens\":13}}\n\ndata: [DONE]\n\n"
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_type","param":"input","message":"Expected a string, but got an array instead."}}`))},
				{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))},
			}}
			svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: upstream}
			defaultMappedModel := mappedModel
			if useAccountMapping {
				defaultMappedModel = ""
			}
			result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", defaultMappedModel)
			require.NoError(t, err)
			require.Len(t, upstream.requests, 2)
			for _, outbound := range upstream.bodies {
				require.Equal(t, mappedModel, gjson.GetBytes(outbound, "model").String())
				require.True(t, gjson.GetBytes(outbound, "stream").Bool())
			}
			require.JSONEq(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(upstream.bodies[1], "messages").Raw)
			require.Equal(t, "preserve", gjson.GetBytes(upstream.bodies[1], "vendor_option").String())
			require.True(t, gjson.GetBytes(upstream.bodies[1], "stream_options.include_usage").Bool())
			require.Equal(t, originalBody, body)
			require.True(t, result.Stream)
			require.Equal(t, mappedModel, result.BillingModel)
			require.Equal(t, mappedModel, result.UpstreamModel)
			require.Equal(t, 9, result.Usage.InputTokens)
			require.Equal(t, 4, result.Usage.OutputTokens)
			require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
			require.Contains(t, rec.Body.String(), "data: [DONE]")
		})
	}
}

func TestForwardAsChatCompletions_StructuredInputRetryIsBoundedAndPreservesRawErrors(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests} {
		t.Run(fmt.Sprintf("status=%d", status), func(t *testing.T) {
			rejection := `{"error":{"code":"invalid_type","param":"input","message":"Expected a string, but got an array instead."}}`
			response := func(status int) *http.Response {
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(rejection))}
			}
			body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
			account := structuredInputRetryTestAccount()
			account.Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: true}
			forward := func(retry bool) (*httptest.ResponseRecorder, error) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
				upstream := &httpUpstreamRecorder{responses: []*http.Response{response(status)}}
				if retry {
					upstream.responses = append([]*http.Response{response(http.StatusBadRequest)}, upstream.responses...)
				}
				svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: upstream}
				var result *OpenAIForwardResult
				var err error
				if retry {
					result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
					require.Len(t, upstream.requests, 2)
					require.Equal(t, "/v1/chat/completions", upstream.requests[1].URL.Path)
				} else {
					result, err = svc.forwardAsRawChatCompletions(context.Background(), c, account, body, "")
					require.Len(t, upstream.requests, 1)
				}
				require.Nil(t, result)
				require.Error(t, err)
				return rec, err
			}
			baseline, baselineErr := forward(false)
			retried, retryErr := forward(true)
			require.Equal(t, baseline.Code, retried.Code)
			require.Equal(t, baseline.Body.String(), retried.Body.String())
			require.Equal(t, baselineErr.Error(), retryErr.Error())
			require.IsType(t, baselineErr, retryErr)
		})
	}
}

func structuredInputRetryTestConfig() *config.Config {
	return &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
		Enabled:           false,
		AllowInsecureHTTP: true,
	}}}
}

func structuredInputRetryTestAccount() *Account {
	return &Account{
		ID:          101,
		Name:        "raw-openai-apikey",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://upstream.example"},
		Extra:       map[string]any{openai_compat.ExtraKeyResponsesSupported: true},
	}
}

const structuredInputRejection = `{"error":{"type":"invalid_request_error","code":"invalid_type","param":"input","message":"Invalid type for 'input': expected a string, but got an array instead."}}`
const structuredVLLMRejection = `{"error":{"type":"Bad Request","param":null,"code":400,"message":"2 validation errors: [{'type':'string_type', 'loc':('body','input','str'), 'msg':'Input should be a valid string', 'input':[{'role':'assistant','content':'private user history'}]}, {'type':'list_type','loc':('body','input','list'),'msg':'other diagnostic'}]"}}`

func TestStructuredInputRecovery_StrictCompleteProof(t *testing.T) {
	vllm := func(message string) string {
		b, err := json.Marshal(map[string]any{"error": map[string]any{"message": message}})
		require.NoError(t, err)
		return string(b)
	}
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"standard", structuredInputRejection, true},
		{"vllm", structuredVLLMRejection, true},
		{"single_without_echo", vllm(`1 validation error: [{'loc':('body','input','str'),'msg':'Input should be a valid string'}]`), true},
		{"doublequotes", vllm(`1 validation errors: [{"type":"string_type","loc":("body","input","str"),"msg":"Input should be a valid string","input_value":[]}]`), true},
		{"wrong_root", `[]`, false}, {"null_error", `{"error":null}`, false},
		{"outer_execution", `{"type":"response.failed","error":{"code":"invalid_type","param":"input","message":"Expected a string, but got an array instead."}}`, false},
		{"outer_error", `{"type":"error","error":{"code":"invalid_type","param":"input","message":"Expected a string, but got an array instead."}}`, true},
		{"outer_alias", `{"Error":{"code":"invalid_type"}}`, false},
		{"error_alias", `{"error":{"Message":"Expected a string, but got an array instead.","code":"invalid_type","param":"input"}}`, false},
		{"duplicate", `{"error":{"message":"Expected a string, but got an array instead.","code":"invalid_type","code":"invalid_type","param":"input"}}`, false},
		{"escaped_duplicate", `{"error":{"code":"invalid_type","co\u0064e":"invalid_type","param":"input","message":"Expected a string, but got an array instead."}}`, false},
		{"nested_usage", `{"error":{"code":"invalid_type","param":"input","message":"Expected a string, but got an array instead.","details":{"us\u0061ge":{"input_tokens":10}}}}`, false},
		{"partial_image", `{"error":{"code":"invalid_type","param":"input","message":"Expected a string, but got an array instead."},"output":[{"type":"image_generation_call"}]}`, false},
		{"second_document", structuredInputRejection + ` {"usage":{"input_tokens":10}}`, false},
		{"invalid_json", `{"error":`, false},
		{"conflicting_code", `{"error":{"type":"invalid_type","code":"server_error","param":"input","message":"Expected a string, but got an array instead."}}`, false},
		{"numeric_type", `{"error":{"type":400,"code":"invalid_type","param":"input","message":"Expected a string, but got an array instead."}}`, false},
		{"numeric_code", `{"error":{"code":400,"type":"BadRequestError","message":"1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string'}]"}}`, true},
		{"message_null", `{"error":{"message":null}}`, false}, {"null_param", `{"error":{"param":null}}`, false},
		{"vllm_wrong_param", strings.Replace(structuredVLLMRejection, `"param":null`, `"param":"tools"`, 1), false},
		{"vllm_wrong_type", strings.Replace(structuredVLLMRejection, `"type":"Bad Request"`, `"type":"server_error"`, 1), false},
		{"vllm_wrong_code", strings.Replace(structuredVLLMRejection, `"code":400`, `"code":500`, 1), false},
		{"vllm_alias_param", strings.Replace(structuredVLLMRejection, `"param":null`, `"Param":null`, 1), false},

		{"unknown_field", `{"error":{"unexpected":{},"message":"Expected a string, but got an array instead."}}`, false},
		{"no_header", vllm(`'loc':('body','input','str'),'msg':'Input should be a valid string'`), false},
		{"elided_diagnostics", vllm(`240 validation errors: ... 'loc':('body','input','str'),'msg':'Input should be a valid string'`), false},
		{"quoted_fake", vllm(`2 validation errors: [{'type':'string_type','loc':('body','model','str'),'msg':'bad','input':"'loc':('body','input','str'),'msg':'Input should be a valid string'"}]`), false},
		{"fake_history", vllm(`2 validation errors: [{'input':[{'loc':('body','input','str'),'msg':'Input should be a valid string'}]}]`), false},
		{"input_value_fake", vllm(`1 validation errors: [{'input_value':{'loc':('body','input','str'),'msg':'Input should be a valid string'}}]`), false},
		{"later_diagnostic", vllm(`2 validation errors: [{'loc':('body','model','str'),'msg':'bad','input':'x'},{'loc':('body','input','str'),'msg':'Input should be a valid string'}]`), false},
		{"zero_count", vllm(`0 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string'}]`), false},
		{"large_count", vllm(`1025 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string'}]`), false},
		{"bad_count", vllm(`x validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string'}]`), false},
		{"wrong_header", vllm(`1 problem: [{'loc':('body','input','str'),'msg':'Input should be a valid string'}]`), false},
		{"wrong_diagnostic_type", vllm(`1 validation errors: [{'type':'server_error','loc':('body','input','str'),'msg':'Input should be a valid string'}]`), false},
		{"nested_loc", vllm(`1 validation errors: [{'loc':('body','input',0,'str'),'msg':'Input should be a valid string'}]`), false},
		{"wrong_msg", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'arbitrary rejection'}]`), false},
		{"escaped_key", vllm(`1 validation errors: [{'lo\c':('body','input','str'),'msg':'Input should be a valid string'}]`), false},
		{"unquoted_key", vllm(`1 validation errors: [{loc:('body','input','str'),'msg':'Input should be a valid string'}]`), false},
		{"missing_quote", vllm(`1 validation errors: [{'loc`), false},
		{"unexpected_next_field", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','response':{}}]`), false},
		{"wrong_closing", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string'} trailing`), false},
		{"long_message", vllm(strings.Repeat("x", 32*1024+1)), false},
		{"deep_json", `{"error":{"message":"x","extra":` + strings.Repeat("[", 66) + `0` + strings.Repeat("]", 66) + `}}`, false},
		{"oversize", structuredInputRejection + strings.Repeat(" ", 64*1024), false},
		{"truncated_repr", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':`), false},
		{"duplicate_repr_loc", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':[],'loc':('body','model','str')}]`), false},
		{"escaped_repr_duplicate", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':[],'lo\x63':('body','model','str')}]`), false},
		{"unclosed_echo_quote", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':'unterminated}]`), false},
		{"count_mismatch", vllm(`2 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':[]}]`), false},
		{"complete_dataclass", vllm(`1 validation errors: [{'type':'string_type','loc':('body','input','str'),'msg':'Input should be a valid string','input':[ResponseFunctionToolCall(id='call_1',type='function_call',arguments='{}'),{'role':'assistant','content':'it\'s fine','optional':None,'boolean':True,'n':2}]}]`), true},
		{"missing_value", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':}]`), false},
		{"trailing_repr", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':[]}] []`), false},
		{"echo_literal_types", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':[{},[],(),False,True,None,-2.5,ResponseFunctionToolCall(),ResponseFunctionToolCall('positional',index=0,),],}]`), true},
		{"deep_echo", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':` + strings.Repeat("[", 34) + `0` + strings.Repeat("]", 34) + `}]`), false},
		{"many_echo_nodes", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':[` + strings.Repeat("0,", 4100) + `0]}]`), false},
		{"repr_duplicate_keyword", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':[ResponseFunctionToolCall(id='a',id='b')]}]`), false},
		{"repr_invalid_number", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':.x}]`), false},
		{"repr_bare_identifier", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':unrecognized}]`), false},
		{"repr_invalid_function", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':Fn(id='x'`), false},
		{"repr_missing_comma", vllm(`1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string' 'input':[]}]`), false},
		{"repr_quote_control", vllm("1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':'bad\nquote'}]"), false},
		{"repr_non_map_diagnostic", vllm(`1 validation errors: [None]`), false},
		{"repr_diagnostic_alias", vllm(`1 validation errors: [{'Loc':('body','input','str'),'msg':'Input should be a valid string'}]`), false},
		{"repr_wrong_loc_container", vllm(`1 validation errors: [{'loc':['body','input','str'],'msg':'Input should be a valid string'}]`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isConvertedResponsesInputStringRejection(400, []byte(tc.body), nil))
		})
	}
	require.False(t, isConvertedResponsesInputStringRejection(400, []byte(structuredInputRejection), errors.New("failed before EOF")))
}

type structuredInputFailReader struct{ err error }

func (r structuredInputFailReader) Read([]byte) (int, error) { return 0, r.err }

type structuredInputCancelReader struct {
	io.Reader
	cancel context.CancelFunc
}

func (r *structuredInputCancelReader) Read(p []byte) (int, error) {
	n, e := r.Reader.Read(p)
	r.cancel()
	return n, e
}

func TestStructuredInputRecovery_ForwardRejectsUnsafeProof(t *testing.T) {
	for _, kind := range []string{"limit_tail_usage", "limit_tail_second", "read_failure", "canceled_forward", "canceled_request", "committed_ping", "committed_json", "truncated_repr", "duplicate_repr", "websocket_probe_missing", "websocket_probe_invalid", "websocketv2_probe_missing", "websocketv2_probe_invalid", "apikeywebsocket_probe_missing", "apikeywebsocket_probe_invalid"} {
		t.Run(kind, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"text","text":"prior"}]}],"stream":false}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			account := structuredInputRetryTestAccount()
			var reader io.Reader = strings.NewReader(structuredInputRejection)
			switch kind {
			case "truncated_repr", "duplicate_repr":
				message := `1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':`
				if kind == "duplicate_repr" {
					message = `1 validation errors: [{'loc':('body','input','str'),'msg':'Input should be a valid string','input':[],'loc':('body','model','str')}]`
				}
				payload, err := json.Marshal(map[string]any{"error": map[string]any{"message": message}})
				require.NoError(t, err)
				reader = bytes.NewReader(payload)

			case "limit_tail_usage", "limit_tail_second":
				suffix := `{"usage":{"input_tokens":10}}`
				if kind == "limit_tail_second" {
					suffix = structuredInputRejection
				}
				reader = strings.NewReader(structuredInputRejection + strings.Repeat(" ", int(openAIUpstreamErrorBodyReadLimit)) + suffix)
			case "read_failure":
				reader = io.MultiReader(reader, structuredInputFailReader{errors.New("read did not reach EOF")})
			case "canceled_forward":
				reader = &structuredInputCancelReader{reader, cancel}
			case "canceled_request":
				requestctx, requestcancel := context.WithCancel(c.Request.Context())
				defer requestcancel()
				c.Request = c.Request.WithContext(requestctx)
				reader = &structuredInputCancelReader{reader, requestcancel}
			case "committed_ping":
				c.Header("Content-Type", "text/event-stream")
				_, err := c.Writer.Write([]byte(": ping\n\n"))
				require.NoError(t, err)
				c.Writer.Flush()
			case "committed_json":
				c.JSON(200, gin.H{"accepted": true})
			default:
				key := "responses_websockets_enabled"
				if strings.HasPrefix(kind, "websocketv2_") {
					key = "responses_websockets_v2_enabled"
				}
				if strings.HasPrefix(kind, "apikeywebsocket_") {
					key = "openai_apikey_responses_websockets_v2_enabled"
				}
				delete(account.Extra, openai_compat.ExtraKeyResponsesSupported)
				account.Extra[key] = true
				if strings.HasSuffix(kind, "invalid") {
					account.Extra[openai_compat.ExtraKeyResponsesSupported] = "true"
				}
			}
			tracked := &passthroughCloseTrackingReadCloser{Reader: reader}
			u := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}}, Body: tracked}}
			svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: u}
			result, err := svc.ForwardAsChatCompletions(ctx, c, account, body, "", "")
			require.Error(t, err)
			require.Nil(t, result)
			require.Len(t, u.requests, 1)
			require.True(t, tracked.closed)
			require.False(t, OpenAIStructuredInputRecoveredViaRawChat(c))
			require.Equal(t, "/v1/responses", u.requests[0].URL.Path)
		})
	}
}

type structuredInputRoutingRecorder struct {
	*httpUpstreamRecorder
	firstBody          *passthroughCloseTrackingReadCloser
	proxies            []string
	accounts           []int64
	slots              []int
	firstClosedAtRetry bool
}

func (u *structuredInputRoutingRecorder) Do(req *http.Request, proxy string, id int64, slots int) (*http.Response, error) {
	if len(u.requests) == 1 {
		u.firstClosedAtRetry = u.firstBody.closed
	}
	u.proxies = append(u.proxies, proxy)
	u.accounts = append(u.accounts, id)
	u.slots = append(u.slots, slots)
	return u.httpUpstreamRecorder.Do(req, proxy, id, slots)
}
func (u *structuredInputRoutingRecorder) DoWithTLS(req *http.Request, proxy string, id int64, slots int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, slots)
}

func TestStructuredInputRecovery_RoutingAndSecondPartial(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial_%t", partial), func(t *testing.T) {
			body := []byte(`{"model":"client-model","stream":true,"service_tier":"flex","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"text","text":"prior"}]}],"vendor_field":"keep"}`)
			original := append([]byte(nil), body...)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
			first := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(structuredInputRejection)}
			stream := `data: {"id":"partial_raw","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}],"usage":{"prompt_tokens":10,"completion_tokens":15,"total_tokens":25}}` + "\n\n"
			var second io.Reader = strings.NewReader(stream + "data: [DONE]\n\n")
			if partial {
				second = io.MultiReader(strings.NewReader(stream), structuredInputFailReader{errors.New("provider read failure")})
			}
			u := &structuredInputRoutingRecorder{httpUpstreamRecorder: &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}}, Body: first}, {StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(second)}}}, firstBody: first}
			svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: u}
			account := structuredInputRetryTestAccount()
			account.Credentials["base_url"] = "http://upstream.example/v9"
			account.Proxy = &Proxy{Protocol: "http", Host: "proxy.test", Port: 8080}
			account.Concurrency = 3
			before, err := json.Marshal(account.Extra)
			require.NoError(t, err)
			result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.4")
			if partial {
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover), "metered second partial cannot replay")
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, result)
			require.Equal(t, 10, result.Usage.InputTokens)
			require.Equal(t, 15, result.Usage.OutputTokens)
			require.Len(t, u.requests, 2)
			require.True(t, u.firstClosedAtRetry)
			require.Equal(t, []string{"http://proxy.test:8080", "http://proxy.test:8080"}, u.proxies)
			require.Equal(t, []int64{account.ID, account.ID}, u.accounts)
			require.Equal(t, []int{3, 3}, u.slots)
			require.Equal(t, "/v9/responses", u.requests[0].URL.Path)
			require.Equal(t, "/v9/chat/completions", u.requests[1].URL.Path)
			require.Equal(t, "flex", gjson.GetBytes(u.bodies[1], "service_tier").String())
			require.Equal(t, "keep", gjson.GetBytes(u.bodies[1], "vendor_field").String())
			require.Equal(t, "gpt-5.4", result.BillingModel)
			require.NotNil(t, result.ServiceTier)
			require.Equal(t, "flex", *result.ServiceTier)
			require.Equal(t, original, body)
			after, marshalErr := json.Marshal(account.Extra)
			require.NoError(t, marshalErr)
			require.Equal(t, before, after)
		})
	}
}

func TestStructuredInputRecovery_AudioLocalRefusalDoesNotDispatch(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"aA==","format":"wav"}}]}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	u := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: u}
	result, err := svc.ForwardAsChatCompletions(context.Background(), c, structuredInputRetryTestAccount(), body, "", "")
	require.ErrorIs(t, err, apicompat.ErrUnsupportedInputAudio)
	require.Nil(t, result)
	require.Empty(t, u.requests)
	require.Equal(t, 400, rec.Code)
}

func TestStructuredInputRecovery_EligibilityBoundaries(t *testing.T) {
	for _, kind := range []string{"allowed", "nil_context", "nil_gin", "nil_request", "nil_writer", "nil_account", "oauth", "other_platform", "native_shape", "string_input", "object_input", "probe_false", "probe_missing", "probe_invalid", "force_responses", "force_chat"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			account := structuredInputRetryTestAccount()
			shape := false
			outbound := []byte(`{"input":[{"role":"user","content":"hi"}]}`)
			want := false
			switch kind {
			case "allowed":
				want = true
			case "nil_context":
				ctx = nil
			case "nil_gin":
				c = nil
			case "nil_request":
				c.Request = nil
			case "nil_writer":
				c.Writer = nil
			case "nil_account":
				account = nil
			case "oauth":
				account.Type = AccountTypeOAuth
			case "other_platform":
				account.Platform = PlatformGrok
			case "native_shape":
				shape = true
			case "string_input":
				outbound = []byte(`{"input":"hi"}`)
			case "object_input":
				outbound = []byte(`{"input":{"role":"user","content":"hi"}}`)
				want = true
			case "probe_false":
				account.Extra[openai_compat.ExtraKeyResponsesSupported] = false
			case "probe_missing":
				delete(account.Extra, openai_compat.ExtraKeyResponsesSupported)
			case "probe_invalid":
				account.Extra[openai_compat.ExtraKeyResponsesSupported] = 1
			case "force_responses":
				account.Extra[openai_compat.ExtraKeyResponsesMode] = "force_responses"
			case "force_chat":
				account.Extra[openai_compat.ExtraKeyResponsesMode] = "force_chat_completions"
			}
			require.Equal(t, want, canRecoverConvertedResponsesInput(ctx, c, account, shape, outbound))
		})
	}
}

func TestStructuredInputRecovery_TransportMarkerResetsPerAttempt(t *testing.T) {
	require.False(t, OpenAIStructuredInputRecoveredViaRawChat(nil))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Set(openAIStructuredInputRawFallbackKey, "invalid")
	require.False(t, OpenAIStructuredInputRecoveredViaRawChat(c))
	c.Set(openAIStructuredInputRawFallbackKey, true)
	require.True(t, OpenAIStructuredInputRecoveredViaRawChat(c))
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`)
	u := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"other refusal"}}`))}}
	svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: u}
	_, err := svc.ForwardAsChatCompletions(context.Background(), c, structuredInputRetryTestAccount(), body, "", "")
	require.Error(t, err)
	require.False(t, OpenAIStructuredInputRecoveredViaRawChat(c))
	require.Len(t, u.requests, 1)
}
