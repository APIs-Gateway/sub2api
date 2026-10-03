//go:build unit

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
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const grokProductionRefusal = `{"code":"permission-denied","error":"I'm sorry, I can't help with that request."}`

func TestGrokRequestRefusalPreservesExistingSchedulerHealth(t *testing.T) {
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	svc := &OpenAIGatewayService{rateLimitService: newOpenAIAdvancedSchedulerRateLimitService("true")}
	ttft := 123
	svc.ReportOpenAIAccountScheduleResult(151601, false, nil)
	svc.ReportOpenAIAccountScheduleResult(151601, true, &ttft)
	rateBefore, ttftBefore, hasBefore := svc.openaiAccountStats.snapshot(151601)
	metricsBefore := svc.SnapshotOpenAIAccountSchedulerMetrics()
	refusal := &GrokContentPolicyRejectionError{message: "request rejected"}
	svc.ReportOpenAIAccountScheduleError(151601, fmt.Errorf("wrapped: %w", refusal))
	rateAfter, ttftAfter, hasAfter := svc.openaiAccountStats.snapshot(151601)
	require.Equal(t, rateBefore, rateAfter)
	require.Equal(t, ttftBefore, ttftAfter)
	require.Equal(t, hasBefore, hasAfter)
	require.Equal(t, metricsBefore, svc.SnapshotOpenAIAccountSchedulerMetrics())
	svc.ReportOpenAIAccountScheduleError(151601, errors.New("real upstream fault"))
	rateFault, _, _ := svc.openaiAccountStats.snapshot(151601)
	require.Greater(t, rateFault, rateAfter)
}

func TestGrokRequestRefusalStrictClassification(t *testing.T) {
	positives := []string{
		grokProductionRefusal,
		`{"error":{"code":"permission_denied","message":"I'm sorry, I can't help with that request."}}`,
		`{"code":"permission_denied","error":{"error":"I'm sorry, I cannot help with that request."}}`,
		`{"code":"permission_denied","message":"  I'M SORRY, I CAN'T HELP WITH THAT REQUEST.  "}`,
		`{"code":"permission_denied","detail":"I’m sorry, I can’t help with that request."}`,
		`{"error_code":"permission_denied","message":"I‘m sorry, I cannot help with that request."}`,
	}
	for i, body := range positives {
		t.Run(fmt.Sprintf("recognized_%d", i), func(t *testing.T) {
			require.True(t, isGrokContentPolicyRejection(http.StatusForbidden, []byte(body)))
			for _, status := range []int{400, 401, 402, 429, 500} {
				require.False(t, isGrokContentPolicyRejection(status, []byte(body)))
			}
		})
	}
	negatives := []string{
		`{"code":"permission-denied"}`,
		`{"error":"I'm sorry, I can't help with that request."}`,
		`{"code":"permission-denied","metadata":{"message":"I'm sorry, I can't help with that request."}}`,
		`{"code":"permission-denied","message":{"debug":"I'm sorry, I can't help with that request."}}`,
		`{"code":"permission-denied","detail":["I'm sorry, I can't help with that request."]}`,
		`{"code":"permission-denied","error":{"message":{"text":"I'm sorry, I can't help with that request."}}}`,
		`{"code":"permission-denied","error":"\"I'm sorry, I can't help with that request.\""}`,
		`{"code":"permission-denied","error":"Reported: I'm sorry, I can't help with that request."}`,
		`{"code":"permission-denied","error":"I'm sorry, I can't help with that request. Try another account."}`,
		`{"code":"permission-denied","error":"https://test/I'm sorry, I can't help with that request."}`,
		`{"code":"permission-denied","error":"{\"message\":\"I'm sorry, I can't help with that request.\"}"}`,
		`{"code":"account_suspended","code":"permission-denied","error":"I'm sorry, I can't help with that request."}`,
		`{"code":"permission-denied","error":{"message":"account_suspended","message":"I'm sorry, I can't help with that request."}}`,
		`{"code":"permission-denied","error":"I'm sorry, I can't help with that request.","extra":{"code":"subscription_required"}}`,
		`{"code":"permission-denied","error":"I'm sorry, I can't help with that request.","detail":"account has been disabled"}`,
		`{"code":"permission-denied","error":"I'm sorry, I can't help with that request.","detail":"account\u0020has\u0020been\u0020disabled"}`,
		`{"code":"permission-denied","error":"I'm sorry, I can't help with that request.","extra":{"message":"\u0061ccount suspended"}}`,
		`"I'm sorry, I can't help with that request."`, "I'm sorry, I can't help with that request.", "", "{", "null", "[]",
	}
	for _, code := range []string{"account_suspended", "account_disabled", "user_suspended", "user_disabled", "subscription_required", "entitlement_required", "not_entitled", "plan_required"} {
		negatives = append(negatives, `{"code":"permission-denied","error":{"code":"`+code+`","message":"I'm sorry, I can't help with that request."}}`)
	}
	for i, body := range negatives {
		t.Run(fmt.Sprintf("account_or_ambiguous_%d", i), func(t *testing.T) {
			require.False(t, isGrokContentPolicyRejection(http.StatusForbidden, []byte(body)), body)
		})
	}
}

func grokRefusalTestAccount(route string) *Account {
	account := &Account{ID: 151601, Platform: PlatformGrok, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-key", "base_url": "https://grok.test/v1",
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules":   []any{map[string]any{"error_code": 403, "keywords": []any{"permission-denied", "entitlement_required"}, "duration_minutes": 5}}}}
	if route == "native" {
		account.Type = AccountTypeOAuth
		account.Credentials["access_token"] = "test-token"
	} else if route == "bridge" {
		account.Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: true}
	}
	return account
}

func TestGrokRequestRefusalActualForwardPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"raw", "bridge", "native"} {
		for _, stream := range []bool{false, true} {
			for _, committed := range []bool{false, true} {
				if committed && !stream {
					continue
				}
				t.Run(fmt.Sprintf("%s_stream_%t_committed_%t", route, stream, committed), func(t *testing.T) {
					account := grokRefusalTestAccount(route)
					repo := &tokenRefreshAccountRepo{}
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 403, Header: http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"refusal-request"}}, Body: io.NopCloser(strings.NewReader(grokProductionRefusal))}}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), accountRepo: repo, httpUpstream: upstream}
					path := "/v1/chat/completions"
					body := []byte(fmt.Sprintf(`{"model":"grok-4.3","messages":[{"role":"user","content":"hi"}],"stream":%t}`, stream))
					if route == "native" {
						path = "/v1/responses"
						body = []byte(fmt.Sprintf(`{"model":"grok-4.3","input":"hi","stream":%t}`, stream))
					}
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
					if committed {
						c.Header("Content-Type", "text/event-stream")
						_, _ = c.Writer.Write([]byte(":\n\n"))
						c.Writer.Flush()
					}
					var result *OpenAIForwardResult
					var err error
					if route == "native" {
						result, err = svc.forwardGrokResponses(context.Background(), c, account, body, "grok-4.3", stream, time.Now())
					} else {
						result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
					}
					require.Nil(t, result)
					require.Error(t, err)
					require.True(t, IsGrokContentPolicyRejectionError(err))
					var failover *UpstreamFailoverError
					require.False(t, errors.As(err, &failover))
					require.Len(t, upstream.requests, 1)
					require.Zero(t, repo.setErrorCalls)
					require.Zero(t, repo.setTempUnschedCalls)
					require.Zero(t, repo.updateCalls)
					require.Zero(t, repo.updateExtraCalls)
					require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
					require.True(t, IsResponseCommitted(c))
					rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
					require.True(t, ok)
					events := rawEvents.([]*OpsUpstreamErrorEvent)
					require.Len(t, events, 1)
					require.Equal(t, account.ID, events[0].AccountID)
					require.Equal(t, 403, events[0].UpstreamStatusCode)
					require.Equal(t, "refusal-request", events[0].UpstreamRequestID)
					require.Equal(t, "http_error", events[0].Kind)
					require.Empty(t, events[0].UpstreamResponseBody)
					require.Empty(t, events[0].Detail)
					if committed {
						require.Equal(t, 200, recorder.Code)
						require.False(t, json.Valid(recorder.Body.Bytes()))
						if route == "native" {
							require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: response.failed"))
						} else {
							require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: error"))
						}
					} else {
						require.Equal(t, 403, recorder.Code)
						require.True(t, json.Valid(recorder.Body.Bytes()))
					}
					require.Contains(t, recorder.Body.String(), "upstream content safety system")
					require.NotContains(t, recorder.Body.String(), "I'm sorry")
					require.NotContains(t, recorder.Body.String(), "permission-denied")
				})
			}
		}
	}
}

func TestGrokRawChatActualOutboundCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"grok-4.3","presence_penalty":0,"presence_penalty":0.4,"presencePenalty":0.2,"presencePenalty":0.5,"large_id":9007199254740993,"custom":{"count":9007199254740993},"messages":[{"role":"user","role":"system","name":"rules","name":"rules2","content":"brief"},{"role":"user","name":"alice","content":"hi"},{"role":"assistant","name":"agent","tool_calls":[{"function":{"name":"lookup","arguments":"{}"}}]}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"name":{"type":"string"}}}}}],"stream":false}`)
	original := bytes.Clone(body)
	for _, platform := range []string{PlatformGrok, PlatformOpenAI} {
		t.Run(platform, func(t *testing.T) {
			account := grokRefusalTestAccount("raw")
			account.Platform = platform
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"chatcmpl_test","object":"chat.completion","model":"grok-4.3","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
			require.NoError(t, err)
			require.Equal(t, original, body)
			if platform == PlatformGrok {
				require.False(t, gjson.GetBytes(upstream.lastBody, "presence_penalty").Exists())
				require.False(t, gjson.GetBytes(upstream.lastBody, "presencePenalty").Exists())
				require.False(t, gjson.GetBytes(upstream.lastBody, "messages.0.name").Exists())
				require.False(t, gjson.GetBytes(upstream.lastBody, "messages.2.name").Exists())
				require.Equal(t, "system", gjson.GetBytes(upstream.lastBody, "messages.0.role").String())
			} else {
				require.Equal(t, body, upstream.lastBody)
			}
			for _, key := range []string{"large_id", "custom.count"} {
				require.Equal(t, "9007199254740993", gjson.GetBytes(upstream.lastBody, key).Raw)
			}
			require.Equal(t, "alice", gjson.GetBytes(upstream.lastBody, "messages.1.name").String())
			require.Equal(t, "lookup", gjson.GetBytes(upstream.lastBody, "messages.2.tool_calls.0.function.name").String())
			require.Equal(t, "lookup", gjson.GetBytes(upstream.lastBody, "tools.0.function.name").String())
			require.Equal(t, "string", gjson.GetBytes(upstream.lastBody, "tools.0.function.parameters.properties.name.type").String())
		})
	}
}
