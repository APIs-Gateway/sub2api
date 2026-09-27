//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// responsesBodyToAnthropicForCacheTest 复刻 Responses→Anthropic 的转换步骤。
func responsesBodyToAnthropicForCacheTest(t *testing.T, raw string) []byte {
	t.Helper()

	adapted, _, err := adaptResponsesClientToolsForAnthropic([]byte(raw))
	require.NoError(t, err)

	var req apicompat.ResponsesRequest
	require.NoError(t, json.Unmarshal(adapted, &req))

	anthropicReq, err := apicompat.ResponsesToAnthropicRequest(&req)
	require.NoError(t, err)

	body, err := json.Marshal(anthropicReq)
	require.NoError(t, err)

	body = StripEmptyTextBlocks(body)
	body = applyResponsesAnthropicCacheBreakpoints(body, req.Model)
	return enforceCacheControlLimit(body)
}

func countAnthropicCacheBreakpoints(body []byte) int {
	count := 0
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("cache_control").Exists() {
				count++
			}
			return true
		})
		return true
	})
	gjson.GetBytes(body, "system").ForEach(func(_, block gjson.Result) bool {
		if block.Get("cache_control").Exists() {
			count++
		}
		return true
	})
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("cache_control").Exists() {
			count++
		}
		return true
	})
	return count
}

// Codex 每轮都会重新发整段 input。断点必须落在“本轮最后一条 message”上，
// 缓存前缀才会随对话增长；否则上游只会写一次 tools 固定前缀。
func TestApplyResponsesAnthropicCacheBreakpoints_FollowsLatestTurn(t *testing.T) {
	t.Parallel()

	firstTurn := responsesBodyToAnthropicForCacheTest(t, `{
		"model":"claude-opus-5-5",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"turn one"}]}
		],
		"tools":[{"type":"function","name":"exec","description":"run","parameters":{"type":"object","properties":{}}}]
	}`)

	require.Equal(t, "ephemeral", gjson.GetBytes(firstTurn, "messages.0.content.0.cache_control.type").String())
	require.Equal(t, "5m", gjson.GetBytes(firstTurn, "messages.0.content.0.cache_control.ttl").String())

	secondTurn := responsesBodyToAnthropicForCacheTest(t, `{
		"model":"claude-opus-5-5",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"turn one"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer one"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"turn two"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer two"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"turn three"}]}
		],
		"tools":[{"type":"function","name":"exec","description":"run","parameters":{"type":"object","properties":{}}}]
	}`)

	messages := gjson.GetBytes(secondTurn, "messages").Array()
	require.Len(t, messages, 5)
	lastIdx := len(messages) - 1
	require.Equal(t, "ephemeral", gjson.GetBytes(secondTurn, "messages."+strconv.Itoa(lastIdx)+".content.0.cache_control.type").String())
	// 另一个断点落在倒数第二个 user turn，与 Parrot 语义一致。
	require.Equal(t, "ephemeral", gjson.GetBytes(secondTurn, "messages.2.content.0.cache_control.type").String())
	require.False(t, gjson.GetBytes(secondTurn, "messages.0.content.0.cache_control").Exists())
	require.Equal(t, 2, countAnthropicCacheBreakpoints(secondTurn))
	require.LessOrEqual(t, countAnthropicCacheBreakpoints(secondTurn), 4)
}

func TestApplyResponsesAnthropicCacheBreakpoints_Idempotent(t *testing.T) {
	t.Parallel()

	body := []byte(`{"messages":[
		{"role":"user","content":[{"type":"text","text":"q1"}]},
		{"role":"assistant","content":[{"type":"text","text":"a1"}]},
		{"role":"user","content":[{"type":"text","text":"q2"}]}
	]}`)

	once := applyResponsesAnthropicCacheBreakpoints(body, "claude-opus-5-5")
	require.JSONEq(t, string(once), string(applyResponsesAnthropicCacheBreakpoints(once, "claude-opus-5-5")))
}

// thinking / redacted_thinking 不允许带 cache_control，断点必须回退到前一个 block。
func TestApplyResponsesAnthropicCacheBreakpoints_SkipsThinkingBlocks(t *testing.T) {
	t.Parallel()

	body := []byte(`{"messages":[{"role":"assistant","content":[
		{"type":"text","text":"visible"},
		{"type":"thinking","thinking":"hidden","signature":"sig"},
		{"type":"redacted_thinking","data":"redacted"}
	]}]}`)

	out := applyResponsesAnthropicCacheBreakpoints(body, "claude-opus-5-5")
	require.Equal(t, "ephemeral", gjson.GetBytes(out, "messages.0.content.0.cache_control.type").String())
	require.False(t, gjson.GetBytes(out, "messages.0.content.1.cache_control").Exists())
	require.False(t, gjson.GetBytes(out, "messages.0.content.2.cache_control").Exists())

	onlyThinking := []byte(`{"messages":[{"role":"assistant","content":[
		{"type":"thinking","thinking":"hidden","signature":"sig"},
		{"type":"redacted_thinking","data":"redacted"}
	]}]}`)
	require.JSONEq(t, string(onlyThinking), string(applyResponsesAnthropicCacheBreakpoints(onlyThinking, "claude-opus-5-5")))
}

func TestApplyResponsesAnthropicCacheBreakpoints_SkipsUnknownBlocks(t *testing.T) {
	t.Parallel()
	body := []byte(`{"messages":[{"role":"user","content":[
		{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"},
		{"type":"future_extension","data":"opaque"}
	]}]}`)
	out := applyResponsesAnthropicCacheBreakpoints(body, "claude-opus-5-5")
	require.Equal(t, "ephemeral", gjson.GetBytes(out, "messages.0.content.0.cache_control.type").String())
	require.False(t, gjson.GetBytes(out, "messages.0.content.1.cache_control").Exists())

	onlyUnknown := []byte(`{"messages":[{"role":"user","content":[{"type":"future_extension","data":"opaque"}]}]}`)
	require.JSONEq(t, string(onlyUnknown), string(applyResponsesAnthropicCacheBreakpoints(onlyUnknown, "claude-opus-5-5")))
}

// DeepSeek / Kimi 等 Anthropic 兼容端点不保证接受 cache_control.ttl，保持原样透传。
func TestApplyResponsesAnthropicCacheBreakpoints_NonClaudeModelsUntouched(t *testing.T) {
	t.Parallel()

	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	for _, model := range []string{"deepseek-v4-flash", "kimi-k2.5", "glm-5.3", ""} {
		require.JSONEq(t, string(body), string(applyResponsesAnthropicCacheBreakpoints(body, model)))
	}
}

func TestApplyResponsesAnthropicCacheBreakpoints_PreservesJSONAndFourBlockLimit(t *testing.T) {
	t.Parallel()
	body := []byte(`{"system":[{"type":"text","text":"rules","cache_control":{"type":"ephemeral","ttl":"1h"}}],"tools":[{"name":"exec","cache_control":{"type":"ephemeral","ttl":"5m"}},{"name":"read","cache_control":{"type":"ephemeral","ttl":"5m"}}],"messages":[
		{"role":"user","content":[{"type":"text","text":"old","cache_control":{"type":"ephemeral","ttl":"1h"}}]},
		{"role":"assistant","content":[{"type":"text","text":"answer"}]},
		{"role":"user","content":[{"type":"text","text":"quoted \"value\" and slash \\"}]},
		{"role":"assistant","content":[{"type":"text","text":"continued"}]},
		{"role":"user","content":[{"type":"text","text":"latest"},{"type":"thinking","thinking":"hidden","signature":"sig"}]}
	]}`)
	require.True(t, json.Valid(body))

	out := enforceCacheControlLimit(applyResponsesAnthropicCacheBreakpoints(body, "claude-opus-5-5"))
	require.True(t, json.Valid(out))
	require.Equal(t, "quoted \"value\" and slash \\", gjson.GetBytes(out, "messages.2.content.0.text").String())
	require.Equal(t, "latest", gjson.GetBytes(out, "messages.4.content.0.text").String())
	require.Equal(t, "ephemeral", gjson.GetBytes(out, "messages.2.content.0.cache_control.type").String())
	require.Equal(t, "ephemeral", gjson.GetBytes(out, "messages.4.content.0.cache_control.type").String())
	require.False(t, gjson.GetBytes(out, "messages.4.content.1.cache_control").Exists())
	require.False(t, gjson.GetBytes(out, "messages.0.content.0.cache_control").Exists())
	require.Equal(t, "1h", gjson.GetBytes(out, "system.0.cache_control.ttl").String())
	require.Equal(t, maxCacheControlBlocks, countAnthropicCacheBreakpoints(out))
	// 超额时现有策略先删 tools 断点，保留 system 与当前轮 message。
	require.False(t, gjson.GetBytes(out, "tools.1.cache_control").Exists())
}

func TestForwardAsResponsesAppliesTurnFollowingCacheBreakpoints(t *testing.T) {
	for _, tc := range []struct {
		name, mappedModel string
		wantInject        bool
	}{
		{"claude model mapping", "claude-opus-5-5", true},
		{"non claude model mapping", "deepseek-v4-flash", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			body := `{"model":"public-model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer two"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"third"}]}]}`
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(namespaceToolAnthropicStream()))}}
			account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-key", "model_mapping": map[string]any{"public-model": tc.mappedModel}}}
			svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}

			result, err := svc.ForwardAsResponses(context.Background(), c, account, []byte(body), nil)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, tc.mappedModel, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Len(t, gjson.GetBytes(upstream.lastBody, "messages").Array(), 5)
			if tc.wantInject {
				require.Equal(t, "ephemeral", gjson.GetBytes(upstream.lastBody, "messages.4.content.0.cache_control.type").String())
				require.Equal(t, "ephemeral", gjson.GetBytes(upstream.lastBody, "messages.2.content.0.cache_control.type").String())
				require.LessOrEqual(t, countAnthropicCacheBreakpoints(upstream.lastBody), maxCacheControlBlocks)
			} else {
				require.Zero(t, countAnthropicCacheBreakpoints(upstream.lastBody))
			}
		})
	}
}
