//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func postPolicyTierOAuthAccount() *Account {
	return &Account{
		ID:          1465,
		Name:        "codex-oauth",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-account",
		},
	}
}

func postPolicyTierResponse(chat, stream bool) *http.Response {
	if chat {
		if stream {
			payload := strings.Join([]string{
				`data: {"id":"chatcmpl_1465","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
				"",
				`data: {"id":"chatcmpl_1465","object":"chat.completion.chunk","model":"gpt-5.4","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
				"",
				"data: [DONE]",
				"",
			}, "\n")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid-1465"}},
				Body:       io.NopCloser(strings.NewReader(payload)),
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"rid-1465"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"chatcmpl_1465","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
			)),
		}
	}
	payload := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1465","model":"gpt-5.4","status":"in_progress","output":[]}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_1465","object":"response","model":"gpt-5.4","status":"completed","output":[{"id":"msg_1465","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid-1465"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
	}
}

func postPolicyTierSettings(action, tier string) *OpenAIFastPolicySettings {
	if action == BetaPolicyActionPass {
		return DefaultOpenAIFastPolicySettings()
	}
	return &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{{
		ServiceTier: tier,
		Action:      action,
		Scope:       BetaPolicyScopeAll,
	}}}
}

func postPolicyTierStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// These requests pass through the production forwarders and then the
// production RecordUsage path. A filtered or forced tier must match the actual
// outbound body in both the usage log and the charged amount.
func TestOpenAICompatibilityBillingUsesFinalOutboundTier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name   string
		route  string
		tier   string
		action string
		rule   string
		stream bool
		want   string
	}{
		{"cc oauth filter stream", "cc-oauth", "priority", BetaPolicyActionFilter, "priority", true, ""},
		{"cc oauth force buffered", "cc-oauth", "flex", OpenAIFastPolicyActionForcePriority, "all", false, "priority"},
		{"cc oauth pass buffered", "cc-oauth", "priority", BetaPolicyActionPass, "", false, "priority"},
		{"cc apikey responses force", "cc-apikey-responses", "default", OpenAIFastPolicyActionForcePriority, "all", false, "priority"},
		{"cc responses shape filter", "cc-responses-shape", "priority", BetaPolicyActionFilter, "priority", false, ""},
		{"cc apikey raw filter buffered", "cc-raw", "priority", BetaPolicyActionFilter, "priority", false, ""},
		{"cc apikey raw force stream", "cc-raw", "flex", OpenAIFastPolicyActionForcePriority, "all", true, "priority"},
		{"cc apikey raw pass", "cc-raw", "priority", BetaPolicyActionPass, "", false, "priority"},
		{"messages oauth filter stream", "messages-oauth", "priority", BetaPolicyActionFilter, "priority", true, ""},
		{"messages oauth pass buffered", "messages-oauth", "priority", BetaPolicyActionPass, "", false, "priority"},
		{"messages raw filter", "messages-raw", "priority", BetaPolicyActionFilter, "priority", false, ""},
		{"responses raw filter", "responses-raw", "priority", BetaPolicyActionFilter, "priority", false, ""},
		{"responses raw force stream", "responses-raw", "flex", OpenAIFastPolicyActionForcePriority, "all", true, "priority"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			var account *Account
			var path string
			chatUpstream := false
			switch tc.route {
			case "cc-oauth", "cc-apikey-responses", "cc-responses-shape", "cc-raw":
				path = "/v1/chat/completions"
				body = []byte(fmt.Sprintf(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"service_tier":%q,"stream":%t}`, tc.tier, tc.stream))
				switch tc.route {
				case "cc-oauth", "cc-responses-shape":
					account = postPolicyTierOAuthAccount()
				case "cc-apikey-responses":
					account = responsesSupportedMessagesTestAccount()
				case "cc-raw":
					account = rawChatCompletionsTestAccount()
					chatUpstream = true
				}
				if tc.route == "cc-responses-shape" {
					body = []byte(fmt.Sprintf(`{"model":"gpt-5.4","input":"hello","service_tier":%q,"stream":%t}`, tc.tier, tc.stream))
				}
			case "messages-oauth", "messages-raw":
				path = "/v1/messages"
				body = []byte(fmt.Sprintf(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, tc.stream))
				if tc.route == "messages-oauth" {
					account = postPolicyTierOAuthAccount()
				} else {
					account = forceChatMessagesFallbackAccount()
					chatUpstream = true
				}
			case "responses-raw":
				path = "/v1/responses"
				body = []byte(fmt.Sprintf(`{"model":"gpt-5.4","input":"hello","service_tier":%q,"stream":%t}`, tc.tier, tc.stream))
				account = forceChatResponsesFallbackAccount()
				chatUpstream = true
			}

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			if strings.HasPrefix(tc.route, "messages-") {
				c.Request.Header.Set("anthropic-beta", claude.BetaFastMode)
			}
			upstream := &httpUpstreamRecorder{resp: postPolicyTierResponse(chatUpstream, tc.stream)}
			svc := newOpenAIGatewayServiceWithSettings(t, postPolicyTierSettings(tc.action, tc.rule))
			svc.cfg = rawChatCompletionsTestConfig()
			svc.httpUpstream = upstream

			var result *OpenAIForwardResult
			var err error
			switch tc.route {
			case "cc-oauth", "cc-apikey-responses", "cc-responses-shape", "cc-raw":
				result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
			case "messages-oauth", "messages-raw":
				result, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
			case "responses-raw":
				result, err = svc.Forward(context.Background(), c, account, body)
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tc.want, gjson.GetBytes(upstream.lastBody, "service_tier").String())
			require.Equal(t, tc.want, postPolicyTierStringValue(result.ServiceTier))
			require.Equal(t, 5, result.Usage.InputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)

			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			billing := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
			err = billing.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result:  result,
				APIKey:  &APIKey{ID: 1465, User: &User{ID: 1465}},
				User:    &User{ID: 1465},
				Account: account,
			})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			require.Equal(t, tc.want, postPolicyTierStringValue(usageRepo.lastLog.ServiceTier))
			expected, err := billing.billingService.CalculateCostWithServiceTier("gpt-5.4", UsageTokens{
				InputTokens: 5, OutputTokens: 2,
			}, 1.1, tc.want)
			require.NoError(t, err)
			require.InDelta(t, expected.ActualCost, usageRepo.lastLog.ActualCost, 1e-12)
			require.InDelta(t, usageRepo.lastLog.ActualCost, userRepo.lastAmount, 1e-12)
		})
	}
}

// Failed SSE streams and read errors can still carry billable usage. Their
// results must retain the tier from the attempt that produced that usage.
func TestOpenAICompatibilityPartialUsageKeepsFinalOutboundTier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name       string
		route      string
		tier       string
		action     string
		rule       string
		want       string
		readError bool
	}{
		{"cc oauth force failed event", "cc", "flex", OpenAIFastPolicyActionForcePriority, "all", "priority", false},
		{"messages oauth filter failed event", "messages", "priority", BetaPolicyActionFilter, "priority", "", false},
		{"cc raw filter read error", "raw", "priority", BetaPolicyActionFilter, "priority", "", true},
		{"responses raw force read error", "responses-raw", "flex", OpenAIFastPolicyActionForcePriority, "all", "priority", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			var account *Account
			path := "/v1/chat/completions"
			if tc.route == "messages" {
				path = "/v1/messages"
				body = []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
				account = postPolicyTierOAuthAccount()
			} else if tc.route == "responses-raw" {
				path = "/v1/responses"
				body = []byte(fmt.Sprintf(`{"model":"gpt-5.4","input":"hello","service_tier":%q,"stream":true}`, tc.tier))
				account = forceChatResponsesFallbackAccount()
			} else {
				body = []byte(fmt.Sprintf(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"service_tier":%q,"stream":true}`, tc.tier))
				if tc.route == "raw" {
					account = rawChatCompletionsTestAccount()
				} else {
					account = postPolicyTierOAuthAccount()
				}
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			if tc.route == "messages" {
				c.Request.Header.Set("anthropic-beta", claude.BetaFastMode)
			}
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid-partial-1465"}},
				Body:       io.NopCloser(strings.NewReader(buildResponsesFailedSSEStream("invalid_request_error", "partial result"))),
			}
			if tc.readError {
				payload := strings.Join([]string{
					`data: {"id":"chatcmpl_partial","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
					"",
					`data: {"id":"chatcmpl_partial","object":"chat.completion.chunk","model":"gpt-5.4","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
					"",
				}, "\n")
				resp.Body = io.NopCloser(&chatFallbackReadError{reader: strings.NewReader(payload), err: io.ErrUnexpectedEOF})
			}
			upstream := &httpUpstreamRecorder{resp: resp}
			svc := newOpenAIGatewayServiceWithSettings(t, postPolicyTierSettings(tc.action, tc.rule))
			svc.cfg = rawChatCompletionsTestConfig()
			svc.httpUpstream = upstream

			var result *OpenAIForwardResult
			var err error
			if tc.route == "messages" {
				result, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
			} else if tc.route == "responses-raw" {
				result, err = svc.Forward(context.Background(), c, account, body)
			} else {
				result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
			}
			require.Error(t, err)
			require.NotNil(t, result)
			require.Greater(t, result.Usage.InputTokens, 0)
			require.Equal(t, tc.want, gjson.GetBytes(upstream.lastBody, "service_tier").String())
			require.Equal(t, tc.want, postPolicyTierStringValue(result.ServiceTier))

			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			billing := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
			require.NoError(t, billing.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: result, APIKey: &APIKey{ID: 1465, User: &User{ID: 1465}},
				User: &User{ID: 1465}, Account: account,
			}))
			require.NotNil(t, usageRepo.lastLog)
			require.Equal(t, tc.want, postPolicyTierStringValue(usageRepo.lastLog.ServiceTier))
			require.Greater(t, usageRepo.lastLog.ActualCost, 0.0)
			require.InDelta(t, usageRepo.lastLog.ActualCost, userRepo.lastAmount, 1e-12)
		})
	}
}
