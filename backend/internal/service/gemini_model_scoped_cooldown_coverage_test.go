//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGeminiModelScopedCooldown_FinalPolicyPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		call func(*GeminiMessagesCompatService, *gin.Context, *Account) error
	}{
		{
			name: "anthropic_compat_temp_unscheduled",
			call: func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account) error {
				body := []byte(`{"model":"claude-sonnet-4","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
				_, err := svc.Forward(context.Background(), c, account, body)
				return err
			},
		},
		{
			name: "native_temp_unscheduled",
			call: func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account) error {
				body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
				_, err := svc.ForwardNative(context.Background(), c, account, "gemini-test", "generateContent", false, body)
				return err
			},
		},
		{
			name: "chat_completions_custom_error_code",
			call: func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account) error {
				body := []byte(`{"model":"gemini-test","messages":[{"role":"user","content":"hello"}]}`)
				_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body)
				return err
			},
		},
		{
			name: "native_custom_error_code",
			call: func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account) error {
				body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
				_, err := svc.ForwardNative(context.Background(), c, account, "gemini-test", "generateContent", false, body)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &modelNotFoundAccountRepoStub{}
			credentials := map[string]any{
				"api_key": "test-key",
				"model_mapping": map[string]any{
					"claude-sonnet-4": "gemini-upstream",
					"gemini-test":     "gemini-upstream",
				},
			}
			if tt.name == "chat_completions_custom_error_code" || tt.name == "native_custom_error_code" {
				credentials["custom_error_codes_enabled"] = true
				credentials["custom_error_codes"] = []any{float64(http.StatusServiceUnavailable)}
			} else {
				credentials["temp_unschedulable_enabled"] = true
				credentials["temp_unschedulable_rules"] = []any{
					map[string]any{
						"error_code":       float64(http.StatusServiceUnavailable),
						"keywords":         []any{"overloaded"},
						"duration_minutes": float64(10),
					},
				}
			}

			svc := &GeminiMessagesCompatService{
				httpUpstream: &geminiCompatHTTPUpstreamStub{
					response: &http.Response{
						StatusCode: http.StatusServiceUnavailable,
						Header:     http.Header{"x-request-id": []string{"gemini-test-request"}},
						Body:       ioNopCloserString(`{"error":{"message":"overloaded"}}`),
					},
				},
				rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil),
				cfg:              &config.Config{},
			}
			account := &Account{
				ID:          901,
				Platform:    PlatformGemini,
				Type:        AccountTypeAPIKey,
				Credentials: credentials,
			}

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

			err := tt.call(svc, c, account)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			if strings.Contains(tt.name, "temp_unscheduled") {
				require.NotEmpty(t, repo.modelRateLimitCalls)
				require.Equal(t, "gemini-upstream", repo.modelRateLimitCalls[0].scope)
			}
		})
	}
}

func TestModelScopedCooldownContextHelpers(t *testing.T) {
	require.Empty(t, firstRequestedModel(nil))
	require.Empty(t, firstRequestedModel([]string{"  "}))
	require.Equal(t, "gemini-test", firstRequestedModel([]string{" gemini-test ", "ignored"}))

	base := context.Background()
	require.Equal(t, base, withTempUnschedulableModel(base, nil))
	require.Empty(t, tempUnschedulableModel(nil, nil))

	ctx := withTempUnschedulableModel(nil, []string{"gemini-test"})
	require.Equal(t, "gemini-test", tempUnschedulableModel(ctx, nil))
	require.Equal(t, "override", tempUnschedulableModel(ctx, []string{" override "}))
}

func TestMatchTempUnschedulableRules_HandlesFilteringAndBodyLimit(t *testing.T) {
	account := &Account{
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{"error_code": float64(http.StatusServiceUnavailable), "keywords": []any{"overloaded"}, "duration_minutes": float64(1)},
				map[string]any{"error_code": float64(http.StatusBadRequest), "keywords": []any{"bad request"}, "duration_minutes": float64(1)},
				map[string]any{"error_code": float64(http.StatusServiceUnavailable), "keywords": []any{}, "duration_minutes": float64(1)},
			},
		},
	}

	require.Nil(t, matchTempUnschedulableRules(nil, http.StatusServiceUnavailable, []byte("overloaded")))
	require.Nil(t, matchTempUnschedulableRules(account, 0, []byte("overloaded")))
	require.Nil(t, matchTempUnschedulableRules(account, http.StatusServiceUnavailable, nil))
	require.Empty(t, matchTempUnschedulableRules(account, http.StatusServiceUnavailable, make([]byte, tempUnschedBodyMaxBytes+1)))

	matches := matchTempUnschedulableRules(account, http.StatusServiceUnavailable, []byte("service is overloaded"))
	require.Len(t, matches, 1)
	require.Equal(t, 0, matches[0].ruleIndex)
	require.Equal(t, "overloaded", matches[0].matchedKeyword)
}

func ioNopCloserString(value string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(value))
}
