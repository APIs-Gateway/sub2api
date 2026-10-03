//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Both established compatibility entry points use the same normalizer after
// mimicry. A configured 1h system marker follows the injected 5m tool marker.
func TestCacheTTLOrder_ActualCompatibilityWireAndUsage(t *testing.T) {
	for _, endpoint := range []string{"chat", "responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", endpoint, stream), func(t *testing.T) {
				gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
				t.Cleanup(func() { gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{}) })
				gin.SetMode(gin.TestMode)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, nil)
				upstream := cacheTTLWireUpstream(true)
				svc := newForwardPartialUsageServiceForTest(upstream)
				svc.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
					SettingKeyEnableClaudeOAuthSystemPromptInjection: "true",
					SettingKeyEnableAnthropicCacheTTL1hInjection:     "false",
					SettingKeyRewriteMessageCacheControl:             "false",
					SettingKeyClaudeOAuthSystemPromptBlocks:          `[{"type":"text","text":"{billing_header}"},{"type":"text","text":"{claude_code_system_prompt}","cache_control":{"type":"ephemeral","ttl":"1h"}}]`,
				}}, &config.Config{})
				account := &Account{ID: 7844, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "ttl-test-token"}}
				body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-6","stream":%t,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"probe","parameters":{"type":"object"}}}]}`, stream))
				var result *ForwardResult
				var err error
				if endpoint == "chat" {
					result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
				} else {
					body = []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-6","stream":%t,"input":"hi","tools":[{"type":"function","name":"probe","parameters":{"type":"object"}}]}`, stream))
					result, err = svc.ForwardAsResponses(context.Background(), c, account, body, nil)
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.NotNil(t, upstream.lastReq)
				require.True(t, gjson.GetBytes(upstream.lastBody, "tools.0.cache_control").Exists())
				require.Equal(t, "1h", gjson.GetBytes(upstream.lastBody, "system.1.cache_control.ttl").String())
				require.Equal(t, "1h", gjson.GetBytes(upstream.lastBody, "tools.0.cache_control.ttl").String())
				requireAnthropicLongBeforeShort(t, upstream.lastBody)
				require.Equal(t, 40, result.Usage.CacheCreationInputTokens)
				// Baseline cdbc23de0 compatibility adapters retain aggregate usage
				// but do not expose TTL buckets. Preserve that billing contract.
				require.Equal(t, 0, result.Usage.CacheCreation5mTokens)
				require.Equal(t, 0, result.Usage.CacheCreation1hTokens)
				require.Equal(t, 11, result.Usage.InputTokens)
				require.Equal(t, 7, result.Usage.CacheReadInputTokens)
				require.Equal(t, 3, result.Usage.OutputTokens)
			})
		}
	}
}
