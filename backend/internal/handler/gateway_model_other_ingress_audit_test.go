//go:build unit

package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

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
				// The empty, counted account repository is usable on OLD and
				// returns no capacity normally. Reaching it is a business failure
				// of the guard, not a panic caused by missing dependencies.
				repo := &embeddingsModelAuditAccountRepo{}
				openAI := newOpenAIResponsesFailoverTestHandlerWithRepo(t, &openAIResponsesFailoverCancelUpstream{}, repo)
				cfg := &config.Config{RunMode: config.RunModeSimple}
				platform := service.PlatformAnthropic
				if route == "gemini_chat" {
					platform = service.PlatformGemini
				}
				group := &service.Group{ID: 3131, Platform: platform}
				gatewayService := service.NewGatewayService(
					repo, &countTokensModelAvailabilityGroupRepo{group: group}, nil, nil, nil, nil, nil, nil,
					cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
				)
				native := &GatewayHandler{gatewayService: gatewayService, billingCacheService: openAI.billingCacheService, concurrencyHelper: openAI.concurrencyHelper, cfg: cfg}
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
				c, rec := newOpenAIFailoverTestContext(t, context.Background(), path, `{`+fields+`,"stream":false,"prompt":"draw a cat","input":"audit","messages":[{"role":"user","content":"audit"}],"commands":{},"images":[{"image_url":"https://example.com/input.png"}]}`, true)
				key, ok := middleware.GetAPIKeyFromContext(c)
				require.True(t, ok)
				if route == "anthropic_chat" || route == "gemini_chat" || route == "count_tokens" {
					key.Group.Platform = platform
				}
				require.NotPanics(t, func() { invoke(c) })
				t.Logf("routing_calls=%d status=%d body=%s", repo.routingCalls.Load(), rec.Code, rec.Body.String())
				if tc.ambiguous {
					require.Zero(t, repo.routingCalls.Load())
					require.Equal(t, http.StatusBadRequest, rec.Code)
					require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
					require.Contains(t, rec.Body.String(), "canonical field name")
					return
				}
				require.Positive(t, repo.routingCalls.Load(), "canonical controls must prove the repository is reached normally")
			})
		}
	}
}

func stringsForModelAuditChat(fields string) string {
	return strings.NewReplacer("gpt-image-2", "claude-sonnet-4-5", "gpt-image-1", "claude-opus-4-5").Replace(fields)
}
