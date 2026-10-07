//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type otherIngressModelAuditUpstream struct {
	service.HTTPUpstream
	cancel     context.CancelFunc
	calls      int
	accountID  int64
	rawBody    []byte
	requestURL string
}

func (u *otherIngressModelAuditUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.calls++
	u.accountID = accountID
	u.requestURL = req.URL.String()
	var err error
	u.rawBody, err = io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if u.cancel != nil {
		// Chat controls prove real selection and the mapped provider request,
		// then end at the transport boundary without unrelated usage workers.
		u.cancel()
		return nil, context.Canceled
	}
	// CountTokens is auxiliary and has no usage worker or funding reserve.
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":42}`))}, nil
}

func (u *otherIngressModelAuditUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

func TestGatewayModelOtherIngress_RejectsBeforeRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"anthropic_chat", "gemini_chat", "alpha_search", "images_generation", "images_edits", "count_tokens"} {
		for _, tc := range []struct {
			name, fields string
			ambiguous    bool
		}{
			{"plain_duplicate", `"model":"gpt-image-2","model":"gpt-image-1"`, true},
			{"escaped_duplicate", `"model":"gpt-image-2","\u006dodel":"gpt-image-1"`, true},
			{"case_duplicate", `"model":"gpt-image-2","Model":"gpt-image-1"`, true},
			{"equal_duplicate", `"model":"gpt-image-2","model":"gpt-image-2"`, true},
			{"canonical_control", `"model":"gpt-image-2"`, false},
			{"nested_control", `"model":"gpt-image-2","metadata":{"model":"nested-first","model":"nested-last"}`, false},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				upstream := &otherIngressModelAuditUpstream{cancel: cancel}
				if route == "count_tokens" {
					upstream.cancel = nil
				}
				repo := &embeddingsModelAuditAccountRepo{}
				openAI := newOpenAIResponsesFailoverTestHandlerWithRepo(t, upstream, repo)
				cfg := &config.Config{RunMode: config.RunModeSimple}
				platform := service.PlatformAnthropic
				if route == "gemini_chat" {
					platform = service.PlatformGemini
				}
				group := &service.Group{ID: 3131, Platform: platform}
				groupRepo := &countTokensModelAvailabilityGroupRepo{group: group}
				if route == "anthropic_chat" || route == "gemini_chat" || route == "count_tokens" {
					mappedModel := "claude-opus-4-5"
					if platform == service.PlatformGemini {
						mappedModel = "gemini-2.5-flash"
					}
					// Unlike the old empty fixture, both versions can select an
					// active account, pass model admission and really forward.
					repo.accounts = []service.Account{{
						ID: 7890, Platform: platform, Type: service.AccountTypeAPIKey,
						Status: service.StatusActive, Schedulable: true, Concurrency: 3,
						AccountGroups: []service.AccountGroup{{GroupID: group.ID}},
						Credentials: map[string]any{"api_key": "fixture-key", "base_url": "https://api.example.com", "model_mapping": map[string]any{"claude-sonnet-4-5": mappedModel, "claude-opus-4-5": mappedModel}},
						Extra:       map[string]any{"anthropic_passthrough": true},
					}}
				}
				gatewayService := service.NewGatewayService(
					repo, groupRepo, nil, nil, nil, nil, nil, nil,
					cfg, nil, nil, nil, nil, openAI.billingCacheService, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
				)
				gemini := service.NewGeminiMessagesCompatService(repo, groupRepo, nil, nil, nil, nil, upstream, nil, cfg)
				native := &GatewayHandler{gatewayService: gatewayService, geminiCompatService: gemini, billingCacheService: openAI.billingCacheService, concurrencyHelper: openAI.concurrencyHelper, cfg: cfg}
				path := "/v1/chat/completions"
				invoke := native.ChatCompletions
				switch route {
				case "count_tokens":
					path, invoke = "/v1/messages/count_tokens", native.CountTokens
				case "alpha_search":
					path, invoke = "/v1/alpha/search", openAI.AlphaSearch
				case "images_generation":
					path, invoke = "/v1/images/generations", openAI.Images
				case "images_edits":
					path, invoke = "/v1/images/edits", openAI.Images
				}
				// Chat must use a supported text model, not the Images-only guard.
				fields := tc.fields
				if route == "anthropic_chat" || route == "gemini_chat" || route == "count_tokens" {
					fields = stringsForModelAuditChat(fields)
				}
				c, rec := newOpenAIFailoverTestContext(t, ctx, path, `{`+fields+`,"stream":false,"prompt":"draw a cat","input":"audit","messages":[{"role":"user","content":"audit"}],"commands":{},"images":[{"image_url":"https://example.com/input.png"}]}`, true)
				key, ok := middleware.GetAPIKeyFromContext(c)
				require.True(t, ok)
				if route == "anthropic_chat" || route == "gemini_chat" || route == "count_tokens" {
					key.Group.Platform = platform
				}
				require.NotPanics(t, func() { invoke(c) })
				t.Logf("routing_calls=%d upstream_calls=%d account=%d url=%s outbound=%s status=%d body=%s", repo.routingCalls.Load(), upstream.calls, upstream.accountID, upstream.requestURL, upstream.rawBody, rec.Code, rec.Body.String())
				if tc.ambiguous {
					require.Zero(t, repo.routingCalls.Load())
					require.Zero(t, upstream.calls)
					require.Equal(t, http.StatusBadRequest, rec.Code)
					require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
					require.Contains(t, rec.Body.String(), "canonical field name")
					return
				}
				require.Positive(t, repo.routingCalls.Load(), "canonical controls must prove the repository is reached normally")
				if route == "anthropic_chat" || route == "gemini_chat" || route == "count_tokens" {
					require.Equal(t, 1, upstream.calls, "a genuinely eligible account must reach actual outbound forwarding")
					require.EqualValues(t, 7890, upstream.accountID)
					if route == "gemini_chat" {
						require.Equal(t, "https://api.example.com/v1beta/models/gemini-2.5-flash:generateContent", upstream.requestURL)
						require.Equal(t, "audit", gjson.GetBytes(upstream.rawBody, "contents.0.parts.0.text").String())
					} else {
						require.Equal(t, "claude-opus-4-5", gjson.GetBytes(upstream.rawBody, "model").String())
					}
					if route == "count_tokens" {
						require.Equal(t, http.StatusOK, rec.Code)
						require.Equal(t, int64(42), gjson.GetBytes(rec.Body.Bytes(), "input_tokens").Int())
					} else {
						require.ErrorIs(t, ctx.Err(), context.Canceled)
					}
				} else {
					require.Zero(t, upstream.calls, "empty OpenAI capacity fixture must not reach upstream")
					require.Equal(t, http.StatusServiceUnavailable, rec.Code)
				}
			})
		}
	}
}

func stringsForModelAuditChat(fields string) string {
	return strings.NewReplacer("gpt-image-2", "claude-sonnet-4-5", "gpt-image-1", "claude-opus-4-5").Replace(fields)
}
