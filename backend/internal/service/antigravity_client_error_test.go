//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const antigravityPrivateError = `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"projects/private-project-123 caller pool-sa@internal.example.com","details":[{"metadata":{"consumer":"projects/987654321","secret":"do-not-echo"}}]}}`

func assertAntigravityClientSafe(t *testing.T, client string) {
	t.Helper()
	for _, private := range []string{"private-project-123", "pool-sa@", "987654321", "do-not-echo", "details"} {
		require.NotContains(t, client, private)
	}
}

func antigravityClientErrorFixture(t *testing.T, status int, upstream string) (*AntigravityGatewayService, *Account, *gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	svc := &AntigravityGatewayService{
		settingService: NewSettingService(&antigravitySettingRepoStub{}, &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize, LogUpstreamErrorBody: true}}),
		tokenProvider:  &AntigravityTokenProvider{},
		httpUpstream: &httpUpstreamStub{resp: &http.Response{StatusCode: status,
			Header: http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"private-upstream-request"}},
			Body:   io.NopCloser(strings.NewReader(upstream))}},
	}
	account := &Account{ID: 1584, Name: "pool-account", Platform: PlatformAntigravity, Type: AccountTypeOAuth,
		Status: StatusActive, Concurrency: 1, Credentials: map[string]any{"access_token": "token", "project_id": "pool-project"}}
	return svc, account, c, rec
}

func TestAntigravityClaudeClientErrorForwardFailoverKeepsPolicyNotIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		upstream string
		retry    bool
	}{
		{"ordinary", 403, antigravityPrivateError, false},
		{"project_config", 400, `{"error":{"message":"invalid project resource name projects/private-project-123 for pool-sa@internal.example.com"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, account, c, rec := antigravityClientErrorFixture(t, tc.status, tc.upstream)
			result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}],"max_tokens":1}`), false)
			require.Nil(t, result)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.Equal(t, tc.status, failover.StatusCode)
			require.Equal(t, []byte(tc.upstream), failover.ResponseBody)
			require.Equal(t, tc.retry, failover.RetryableOnSameAccount)
			require.True(t, failover.RedactClientMessage)
			require.False(t, failover.BillingNoCharge, "redaction is not proof that provider work is free")
			require.Empty(t, rec.Body.String())
			require.False(t, IsResponseCommitted(c))
			_, _, msg := ResolveUpstreamFailoverErrorResponse(c, PlatformAntigravity, failover)
			assertAntigravityClientSafe(t, msg)
			raw, exists := c.Get(OpsUpstreamErrorMessageKey)
			require.True(t, exists)
			require.Contains(t, raw, "private-project-123")
		})
	}
}

func TestAntigravityClaudeMappedClientErrorPreservesRulesAndRawOps(t *testing.T) {
	custom := "Contact your administrator"
	code := http.StatusTeapot
	for _, tc := range []struct {
		name     string
		rule     *model.ErrorPassthroughRule
		status   int
		body     string
		want     string
		wantCode int
	}{
		{"default", nil, 400, antigravityPrivateError, "Invalid request", 400},
		{"prompt_too_long", nil, 400, `{"error":{"message":"Prompt is too long for projects/private-project-123 caller pool-sa@internal.example.com"}}`, "Prompt is too long", 400},
		{"passthrough", &model.ErrorPassthroughRule{PassthroughBody: true}, 403, antigravityPrivateError, "Upstream access forbidden, please contact administrator", 418},
		{"custom", &model.ErrorPassthroughRule{CustomMessage: &custom}, 403, antigravityPrivateError, custom, 418},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, account, c, rec := antigravityClientErrorFixture(t, tc.status, tc.body)
			if tc.rule != nil {
				tc.rule.ID, tc.rule.Enabled, tc.rule.Name = 1584, true, "raw project matcher"
				tc.rule.ErrorCodes, tc.rule.Keywords = []int{tc.status}, []string{"projects/private-project-123"}
				tc.rule.MatchMode, tc.rule.Platforms = model.MatchModeAll, []string{PlatformAntigravity}
				tc.rule.ResponseCode, tc.rule.SkipMonitoring = &code, true
				BindErrorPassthroughService(c, NewErrorPassthroughService(&mockErrorPassthroughRepo{rules: []*model.ErrorPassthroughRule{tc.rule}}, nil))
			}
			require.Error(t, svc.WriteMappedClaudeError(c, account, tc.status, "upstream-request", []byte(tc.body)))
			require.Equal(t, tc.wantCode, rec.Code)
			var client struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &client))
			require.Equal(t, tc.want, client.Error.Message)
			assertAntigravityClientSafe(t, rec.Body.String())
			require.True(t, IsResponseCommitted(c))
			raw, _ := c.Get(OpsUpstreamErrorMessageKey)
			require.Contains(t, raw, "private-project-123")
			if tc.rule != nil {
				skip, _ := c.Get(OpsSkipPassthroughKey)
				require.Equal(t, true, skip)
			}
		})
	}
}

func TestAntigravityGeminiClientErrorStreamAndBuffered(t *testing.T) {
	for _, stream := range []bool{true, false} {
		for _, wrapped := range []bool{true, false} {
			name := "buffered"
			if stream {
				name = "stream"
			}
			if wrapped {
				name += "_wrapped"
			}
			t.Run(name, func(t *testing.T) {
				payload := antigravityPrivateError
				if wrapped {
					payload = `{"response":` + payload + `}`
				}
				svc, _, c, rec := antigravityClientErrorFixture(t, 200, "")
				resp := antigravityEmptyStreamTestResponse(payload)
				var result *antigravityStreamResult
				var err error
				if stream {
					result, err = svc.handleGeminiStreamingResponse(c, resp, time.Now())
				} else {
					result, err = svc.handleGeminiStreamToNonStreaming(c, resp, time.Now())
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, 200, rec.Code, "presentation fix retains the existing HTTP-200 in-band error contract")
				assertAntigravityClientSafe(t, rec.Body.String())
				out := rec.Body.Bytes()
				if stream {
					require.Equal(t, 1, strings.Count(rec.Body.String(), "data:"))
					out = bytes.TrimSpace(bytes.TrimPrefix(out, []byte("data: ")))
				}
				require.JSONEq(t, `{"error":{"code":403,"message":"Upstream access forbidden, please contact administrator","status":"PERMISSION_DENIED"}}`, string(out))
				raw, _ := c.Get(OpsUpstreamErrorMessageKey)
				require.Contains(t, raw, "private-project-123")
				detail, _ := c.Get(OpsUpstreamErrorDetailKey)
				require.Contains(t, detail, "do-not-echo")
			})
		}
	}
}

func TestAntigravityGeminiClientErrorDoesNotScrubSuccessfulPartsOrUsage(t *testing.T) {
	payload := `{"candidates":[{"content":{"parts":[{"text":"projects/private-project-123 pool-sa@internal.example.com"},{"functionCall":{"name":"lookup","args":{"error":{"message":"details do-not-echo"}}}}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2}}`
	for _, stream := range []bool{true, false} {
		t.Run(map[bool]string{true: "stream", false: "buffered"}[stream], func(t *testing.T) {
			svc, _, c, rec := antigravityClientErrorFixture(t, 200, "")
			resp := antigravityEmptyStreamTestResponse(payload)
			var result *antigravityStreamResult
			var err error
			if stream {
				result, err = svc.handleGeminiStreamingResponse(c, resp, time.Now())
			} else {
				result, err = svc.handleGeminiStreamToNonStreaming(c, resp, time.Now())
			}
			require.NoError(t, err)
			require.Equal(t, 10, result.usage.InputTokens)
			require.Equal(t, 2, result.usage.OutputTokens)
			require.Contains(t, rec.Body.String(), "private-project-123")
			require.Contains(t, rec.Body.String(), "pool-sa@")
			require.Contains(t, rec.Body.String(), "details do-not-echo")
		})
	}
}

func TestAntigravitySafeGeminiErrorEnvelopeVariants(t *testing.T) {
	for _, body := range []string{"not json", `{"error":null}`, `{"candidates":[]}`} {
		_, _, ok := antigravitySafeGeminiError([]byte(body))
		require.False(t, ok)
	}
	for _, body := range []string{`{"error":"projects/private-project-123"}`, `{"error":{"code":200,"message":"private"}}`} {
		safe, status, ok := antigravitySafeGeminiError([]byte(body))
		require.True(t, ok)
		require.Equal(t, 502, status)
		assertAntigravityClientSafe(t, string(safe))
	}
	safe, status, ok := antigravitySafeGeminiError([]byte(`{"error":{"code":429,"message":"private"},"usageMetadata":{"promptTokenCount":10},"candidates":[{"content":{"parts":[{"text":"keep model output"}]}}],"metadata":{"secret":"do-not-echo"}}`))
	require.True(t, ok)
	require.Equal(t, 429, status)
	require.Contains(t, string(safe), "keep model output")
	require.Contains(t, string(safe), `"promptTokenCount":10`)
	require.NotContains(t, string(safe), "do-not-echo")
}

// Claude conversion never relays native Google error envelopes. Preserve its
// existing empty-stream error policy; metered-empty accounting is tracked in #1585.
func TestAntigravityClaudeClientErrorConvertedStreamDoesNotEchoGoogleError(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(map[bool]string{true: "stream", false: "buffered"}[stream], func(t *testing.T) {
			svc, _, c, rec := antigravityClientErrorFixture(t, 200, "")
			resp := antigravityEmptyStreamTestResponse(`{"response":` + antigravityPrivateError + `}`)
			var result *antigravityStreamResult
			var err error
			if stream {
				result, err = svc.handleClaudeStreamingResponse(c, resp, time.Now(), "claude-opus-4-6")
			} else {
				result, err = svc.handleClaudeStreamToNonStreaming(c, resp, time.Now(), "claude-opus-4-6")
			}
			require.Nil(t, result)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.Empty(t, rec.Body.String())
			assertAntigravityClientSafe(t, string(failover.ResponseBody))
			require.False(t, failover.BillingNoCharge)
		})
	}
}
