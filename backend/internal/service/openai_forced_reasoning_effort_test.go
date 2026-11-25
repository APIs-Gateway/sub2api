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

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 账号级强制思考档位 openai_force_reasoning_effort：
// 请求没带档位（缺失 / 空 / none / minimal）时改成配置值，已有有效档位不动，
// 未配置时发给上游的字节与改动前完全一致。

const forcedEffortLunaModel = "gpt-6-luna"

func forcedEffortAccount(value any) *Account {
	acc := newOpenAIImageGenerationControlTestAccount()
	if value != nil {
		acc.Extra["openai_force_reasoning_effort"] = value
	}
	return acc
}

func TestAccountGetOpenAIForcedReasoningEffort(t *testing.T) {
	cases := []struct {
		name  string
		extra any
		want  string
	}{
		{"unset", nil, ""},
		{"high", "high", "high"},
		{"upper_with_space", "  HIGH ", "high"},
		{"low", "low", "low"},
		{"medium", "Medium", "medium"},
		{"xhigh", "xhigh", "xhigh"},
		{"max", "max", "max"},
		{"none_is_invalid", "none", ""},
		{"minimal_is_invalid", "minimal", ""},
		{"garbage", "ultra", ""},
		{"empty", "  ", ""},
		{"non_string", 5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, forcedEffortAccount(tc.extra).GetOpenAIForcedReasoningEffort())
		})
	}
	require.Equal(t, "", (&Account{Platform: PlatformOpenAI}).GetOpenAIForcedReasoningEffort(), "nil Extra")
	var nilAcc *Account
	require.Equal(t, "", nilAcc.GetOpenAIForcedReasoningEffort(), "nil account")
}

func TestApplyOpenAIForcedReasoningEffort_Helper(t *testing.T) {
	const base = `{"model":"gpt-6-luna","input":"hi"`
	cases := []struct {
		name        string
		account     *Account
		body        string
		wantChanged bool
		wantEffort  string // 空表示期望字段不存在
		wantSame    bool   // 期望字节完全不变
	}{
		{"missing_to_high", forcedEffortAccount("high"), base + `}`, true, "high", false},
		{"empty_object_reasoning", forcedEffortAccount("high"), base + `,"reasoning":{}}`, true, "high", false},
		{"empty_effort", forcedEffortAccount("high"), base + `,"reasoning":{"effort":""}}`, true, "high", false},
		{"none_to_high", forcedEffortAccount("high"), base + `,"reasoning":{"effort":"none"}}`, true, "high", false},
		{"NONE_to_high", forcedEffortAccount("high"), base + `,"reasoning":{"effort":"None"}}`, true, "high", false},
		{"minimal_to_high", forcedEffortAccount("high"), base + `,"reasoning":{"effort":"minimal"}}`, true, "high", false},
		{"configured_xhigh_applies", forcedEffortAccount("XHigh"), base + `,"reasoning":{"effort":"none"}}`, true, "xhigh", false},
		{"reasoning_null", forcedEffortAccount("high"), base + `,"reasoning":null}`, true, "high", false},
		{"low_kept", forcedEffortAccount("high"), base + `,"reasoning":{"effort":"low"}}`, false, "low", true},
		{"medium_kept", forcedEffortAccount("high"), base + `,"reasoning":{"effort":"medium"}}`, false, "medium", true},
		{"high_kept", forcedEffortAccount("xhigh"), base + `,"reasoning":{"effort":"high"}}`, false, "high", true},
		{"xhigh_kept", forcedEffortAccount("high"), base + `,"reasoning":{"effort":"xhigh"}}`, false, "xhigh", true},
		{"max_kept", forcedEffortAccount("high"), base + `,"reasoning":{"effort":"max"}}`, false, "max", true},
		{"unconfigured_missing", forcedEffortAccount(nil), base + `}`, false, "", true},
		{"unconfigured_none", forcedEffortAccount(nil), base + `,"reasoning":{"effort":"none"}}`, false, "none", true},
		{"invalid_config_unchanged", forcedEffortAccount("turbo"), base + `,"reasoning":{"effort":"none"}}`, false, "none", true},
		{"non_openai_platform", func() *Account {
			a := forcedEffortAccount("high")
			a.Platform = PlatformAnthropic
			return a
		}(), base + `}`, false, "", true},
		{"nil_account", nil, base + `}`, false, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := []byte(tc.body)
			out, changed, err := applyOpenAIForcedReasoningEffort(tc.account, in)
			require.NoError(t, err)
			require.Equal(t, tc.wantChanged, changed)
			if tc.wantSame {
				require.Equal(t, tc.body, string(out), "bytes must be untouched")
			}
			got := gjson.GetBytes(out, "reasoning.effort")
			if tc.wantEffort == "" {
				require.False(t, got.Exists())
			} else {
				require.Equal(t, tc.wantEffort, got.String())
			}
			require.Equal(t, "gpt-6-luna", gjson.GetBytes(out, "model").String())
			require.Equal(t, "hi", gjson.GetBytes(out, "input").String())
		})
	}
}

// 只动 reasoning.effort：summary、include、store 等其他字段保持原样。
func TestApplyOpenAIForcedReasoningEffort_OnlyTouchesEffort(t *testing.T) {
	in := `{"model":"gpt-6-luna","store":false,"include":["x"],"reasoning":{"effort":"none","summary":"auto"}}`
	out, changed, err := applyOpenAIForcedReasoningEffort(forcedEffortAccount("high"), []byte(in))
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, `{"model":"gpt-6-luna","store":false,"include":["x"],"reasoning":{"effort":"high","summary":"auto"}}`, string(out))
}

// 强制由 none 改成 high 后，GPT-6 不接受的采样参数必须一起清掉。
func TestApplyOpenAIForcedReasoningEffortForUpstream_StripsGPT6Sampling(t *testing.T) {
	in := `{"model":"gpt-6-luna","temperature":0.7,"top_p":0.9,"reasoning":{"effort":"none"}}`
	out, changed, err := applyOpenAIForcedReasoningEffortForUpstream(forcedEffortAccount("high"), []byte(in), forcedEffortLunaModel)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "high", gjson.GetBytes(out, "reasoning.effort").String())
	require.False(t, gjson.GetBytes(out, "temperature").Exists())
	require.False(t, gjson.GetBytes(out, "top_p").Exists())

	// 非推理模型：不强制，字节不变
	in2 := `{"model":"gpt-4.1","temperature":0.7,"reasoning":{"effort":"none"}}`
	out2, changed2, err := applyOpenAIForcedReasoningEffortForUpstream(forcedEffortAccount("high"), []byte(in2), "gpt-4.1")
	require.NoError(t, err)
	require.False(t, changed2, "non-reasoning model must not be forced")
	require.Equal(t, in2, string(out2), "bytes must be untouched")

	// 没发生强制改写：不碰采样参数（字节不变）
	in3 := `{"model":"gpt-6-luna","temperature":0.7,"reasoning":{"effort":"none"}}`
	out3, changed3, err := applyOpenAIForcedReasoningEffortForUpstream(forcedEffortAccount(nil), []byte(in3), forcedEffortLunaModel)
	require.NoError(t, err)
	require.False(t, changed3)
	require.Equal(t, in3, string(out3))
}

// ---- 三条入口：上游实际收到的 body ----

const forcedEffortResponsesOKJSON = `{"id":"r1","object":"response","model":"gpt-6-luna","status":"completed",` +
	`"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],` +
	`"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`

// 上游回显的模型名必须与发送的一致，否则会被「上游模型不一致」拦成 502。
func newForcedEffortSSEUpstream(model string) *httpUpstreamRecorder {
	sse := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"" + model + "\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"" + model + "\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2,\"total_tokens\":7}}}\n\n"
	return &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}}
}

func newForcedEffortCompatContext(path string, body []byte) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

func runForcedEffortForward(t *testing.T, account *Account, body string) (*httpUpstreamRecorder, *OpenAIForwardResult) {
	t.Helper()
	upstream := newForcedEffortSSEUpstream(gjson.Get(body, "model").String())
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	result, err := svc.Forward(context.Background(), c, account, []byte(body))
	require.NoError(t, err)
	require.NotNil(t, result)
	return upstream, result
}

func runForcedEffortChat(t *testing.T, account *Account, body string) (*httpUpstreamRecorder, *OpenAIForwardResult) {
	t.Helper()
	upstream := newForcedEffortSSEUpstream(gjson.Get(body, "model").String())
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c := newForcedEffortCompatContext("/v1/chat/completions", []byte(body))
	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(body), "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	return upstream, result
}

func runForcedEffortMessages(t *testing.T, account *Account, body string) (*httpUpstreamRecorder, *OpenAIForwardResult) {
	t.Helper()
	upstream := newForcedEffortSSEUpstream(gjson.Get(body, "model").String())
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c := newForcedEffortCompatContext("/v1/messages", []byte(body))
	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, []byte(body), "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	return upstream, result
}

const (
	forcedEffortResponsesBodyNoEffort = `{"model":"gpt-6-luna","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	forcedEffortResponsesBodyNone     = `{"model":"gpt-6-luna","stream":true,"reasoning":{"effort":"none"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	forcedEffortResponsesBodyMinimal  = `{"model":"gpt-6-luna","stream":true,"reasoning":{"effort":"minimal"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	forcedEffortResponsesBodyLow      = `{"model":"gpt-6-luna","stream":true,"reasoning":{"effort":"low"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
)

func TestForcedReasoningEffort_Forward(t *testing.T) {
	t.Run("missing_effort_gets_high", func(t *testing.T) {
		upstream, result := runForcedEffortForward(t, forcedEffortAccount("high"), forcedEffortResponsesBodyNoEffort)
		require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
		require.NotNil(t, result.ReasoningEffort)
		require.Equal(t, "high", *result.ReasoningEffort, "usage log must record the effort actually sent upstream")
	})
	t.Run("none_gets_high", func(t *testing.T) {
		upstream, result := runForcedEffortForward(t, forcedEffortAccount("high"), forcedEffortResponsesBodyNone)
		require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
		require.NotNil(t, result.ReasoningEffort)
		require.Equal(t, "high", *result.ReasoningEffort)
	})
	t.Run("minimal_gets_high", func(t *testing.T) {
		upstream, _ := runForcedEffortForward(t, forcedEffortAccount("high"), forcedEffortResponsesBodyMinimal)
		require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
	})
	t.Run("low_is_kept", func(t *testing.T) {
		upstream, _ := runForcedEffortForward(t, forcedEffortAccount("high"), forcedEffortResponsesBodyLow)
		require.Equal(t, "low", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
	})
	t.Run("none_with_temperature_drops_sampling", func(t *testing.T) {
		body := `{"model":"gpt-6-luna","stream":true,"temperature":0.7,"top_p":0.9,"reasoning":{"effort":"none"},"input":"hi"}`
		upstream, _ := runForcedEffortForward(t, forcedEffortAccount("high"), body)
		require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
		require.False(t, gjson.GetBytes(upstream.lastBody, "temperature").Exists())
		require.False(t, gjson.GetBytes(upstream.lastBody, "top_p").Exists())
	})
	t.Run("unconfigured_body_untouched", func(t *testing.T) {
		for _, body := range []string{forcedEffortResponsesBodyNoEffort, forcedEffortResponsesBodyNone} {
			withNil, _ := runForcedEffortForward(t, forcedEffortAccount(nil), body)
			withInvalid, _ := runForcedEffortForward(t, forcedEffortAccount("turbo"), body)
			require.Equal(t, string(withNil.lastBody), string(withInvalid.lastBody), "invalid config must equal unconfigured")
			require.Equal(t, gjson.Get(body, "reasoning.effort").Exists(), gjson.GetBytes(withNil.lastBody, "reasoning.effort").Exists())
			if gjson.Get(body, "reasoning.effort").Exists() {
				require.Equal(t, "none", gjson.GetBytes(withNil.lastBody, "reasoning.effort").String())
			}
		}
	})
}

func TestForcedReasoningEffort_ChatCompletions(t *testing.T) {
	const noEffort = `{"model":"gpt-6-luna","stream":false,"messages":[{"role":"user","content":"hello"}]}`
	const noneWithTemp = `{"model":"gpt-6-luna","stream":false,"temperature":0.7,"top_p":0.9,"reasoning_effort":"none","messages":[{"role":"user","content":"hello"}]}`

	t.Run("missing_effort_gets_high", func(t *testing.T) {
		upstream, result := runForcedEffortChat(t, forcedEffortAccount("high"), noEffort)
		require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
		require.NotNil(t, result.ReasoningEffort)
		require.Equal(t, "high", *result.ReasoningEffort)
	})
	t.Run("none_with_temperature_forced_drops_sampling", func(t *testing.T) {
		upstream, result := runForcedEffortChat(t, forcedEffortAccount("high"), noneWithTemp)
		require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
		require.False(t, gjson.GetBytes(upstream.lastBody, "temperature").Exists(), "temperature must not reach a thinking GPT-6 upstream")
		require.False(t, gjson.GetBytes(upstream.lastBody, "top_p").Exists())
		require.NotNil(t, result.ReasoningEffort)
		require.Equal(t, "high", *result.ReasoningEffort)
	})
	t.Run("unconfigured_none_keeps_none_and_sampling", func(t *testing.T) {
		upstream, _ := runForcedEffortChat(t, forcedEffortAccount(nil), noneWithTemp)
		require.Equal(t, "none", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
		require.True(t, gjson.GetBytes(upstream.lastBody, "temperature").Exists())
	})
	t.Run("unconfigured_missing_has_no_effort", func(t *testing.T) {
		upstream, _ := runForcedEffortChat(t, forcedEffortAccount(nil), noEffort)
		require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning.effort").Exists())
	})
}

func TestForcedReasoningEffort_Messages(t *testing.T) {
	const noThinking = `{"model":"gpt-6-luna","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hello"}]}`
	const thinkingDisabled = `{"model":"gpt-6-luna","max_tokens":64,"stream":true,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hello"}]}`

	t.Run("no_thinking_keeps_converter_default", func(t *testing.T) {
		// Anthropic 请求没开 thinking 时转换器本身就给 medium，属于有效档位，按约定不动。
		upstream, result := runForcedEffortMessages(t, forcedEffortAccount("high"), noThinking)
		require.Equal(t, "medium", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
		require.NotNil(t, result.ReasoningEffort)
		require.Equal(t, "medium", *result.ReasoningEffort)
	})
	t.Run("thinking_disabled_gets_high", func(t *testing.T) {
		upstream, result := runForcedEffortMessages(t, forcedEffortAccount("high"), thinkingDisabled)
		require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
		require.NotNil(t, result.ReasoningEffort)
		require.Equal(t, "high", *result.ReasoningEffort)
	})
	t.Run("unconfigured_matches_baseline", func(t *testing.T) {
		withNil, _ := runForcedEffortMessages(t, forcedEffortAccount(nil), thinkingDisabled)
		withInvalid, _ := runForcedEffortMessages(t, forcedEffortAccount("turbo"), thinkingDisabled)
		require.Equal(t, string(withNil.lastBody), string(withInvalid.lastBody))
		require.NotEqual(t, "high", gjson.GetBytes(withNil.lastBody, "reasoning.effort").String())
	})
}

// ---- 模型门槛：非推理模型即使开了开关也字节不变 ----

func TestForcedReasoningEffort_NonReasoningModelUntouched(t *testing.T) {
	const model = "gpt-4.1"
	swap := func(s string) string { return strings.ReplaceAll(s, forcedEffortLunaModel, model) }

	t.Run("chat_no_reasoning_in_upstream_body", func(t *testing.T) {
		body := swap(`{"model":"gpt-6-luna","stream":false,"temperature":0.7,"messages":[{"role":"user","content":"hello"}]}`)
		forced, _ := runForcedEffortChat(t, forcedEffortAccount("high"), body)
		require.False(t, gjson.GetBytes(forced.lastBody, "reasoning").Exists(), "upstream body must not carry reasoning")
		plain, _ := runForcedEffortChat(t, forcedEffortAccount(nil), body)
		require.Equal(t, string(plain.lastBody), string(forced.lastBody))
	})
	t.Run("chat_explicit_none_untouched", func(t *testing.T) {
		body := swap(`{"model":"gpt-6-luna","stream":false,"reasoning_effort":"none","messages":[{"role":"user","content":"hello"}]}`)
		forced, _ := runForcedEffortChat(t, forcedEffortAccount("high"), body)
		plain, _ := runForcedEffortChat(t, forcedEffortAccount(nil), body)
		require.Equal(t, string(plain.lastBody), string(forced.lastBody))
		require.Equal(t, "none", gjson.GetBytes(forced.lastBody, "reasoning.effort").String())
	})
	t.Run("forward_untouched", func(t *testing.T) {
		for _, body := range []string{swap(forcedEffortResponsesBodyNoEffort), swap(forcedEffortResponsesBodyNone)} {
			forced, _ := runForcedEffortForward(t, forcedEffortAccount("high"), body)
			plain, _ := runForcedEffortForward(t, forcedEffortAccount(nil), body)
			require.Equal(t, string(plain.lastBody), string(forced.lastBody))
		}
	})
	t.Run("messages_untouched", func(t *testing.T) {
		body := swap(`{"model":"gpt-6-luna","max_tokens":64,"stream":true,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hello"}]}`)
		forced, _ := runForcedEffortMessages(t, forcedEffortAccount("high"), body)
		plain, _ := runForcedEffortMessages(t, forcedEffortAccount(nil), body)
		require.Equal(t, string(plain.lastBody), string(forced.lastBody))
		require.NotEqual(t, "high", gjson.GetBytes(forced.lastBody, "reasoning.effort").String())
	})
}

// ---- Forward 的排除条件 ----

// 带 image_generation 工具的 gpt-6-luna 文本请求：只按图片模型排除，工具存在不跳过强制。
func TestForcedReasoningEffort_Forward_ImageGenerationToolStillForced(t *testing.T) {
	body := `{"model":"gpt-6-luna","stream":true,"reasoning":{"effort":"none"},"tools":[{"type":"image_generation"}],"input":"draw a cat"}`
	upstream, _ := runForcedEffortForward(t, forcedEffortAccount("high"), body)
	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
	require.Equal(t, "image_generation", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
}

// compact 请求：开关开启时上游 body 仍是 none。
func TestForcedReasoningEffort_Forward_CompactSkipped(t *testing.T) {
	body := `{"model":"gpt-6-luna","reasoning":{"effort":"none"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	run := func(account *Account) *httpUpstreamRecorder {
		upstream := newForcedEffortSSEUpstream(gjson.Get(body, "model").String())
		svc := newOpenAIImageGenerationControlTestService(upstream)
		c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
		require.True(t, isOpenAIResponsesCompactPath(c), "test must really hit the compact path")
		// 上游返回体按 compact 解析可能报错，这里只关心发出去的 body。
		_, _ = svc.Forward(context.Background(), c, account, []byte(body))
		require.NotEmpty(t, upstream.lastBody, "request must reach upstream")
		return upstream
	}
	forced := run(forcedEffortAccount("high"))
	plain := run(forcedEffortAccount(nil))
	require.Equal(t, "none", gjson.GetBytes(forced.lastBody, "reasoning.effort").String())
	require.Equal(t, string(plain.lastBody), string(forced.lastBody))
}

// ---- 错误分支与 Cursor 形态（Responses 形态的 chat 请求） ----

// reasoning 是数组时 sjson 无法写入 reasoning.effort：返回错误，body 原样不动。
func TestApplyOpenAIForcedReasoningEffort_SetError(t *testing.T) {
	in := `{"model":"gpt-6-luna","reasoning":[1]}`
	out, changed, err := applyOpenAIForcedReasoningEffort(forcedEffortAccount("high"), []byte(in))
	require.Error(t, err)
	require.False(t, changed)
	require.Equal(t, in, string(out))

	out, changed, err = applyOpenAIForcedReasoningEffortForUpstream(forcedEffortAccount("high"), []byte(in), forcedEffortLunaModel)
	require.Error(t, err)
	require.False(t, changed)
	require.Equal(t, in, string(out))
}

func TestForcedReasoningEffort_Forward_SetErrorStopsBeforeUpstream(t *testing.T) {
	upstream := newForcedEffortSSEUpstream(forcedEffortLunaModel)
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	body := `{"model":"gpt-6-luna","stream":true,"reasoning":[1],"input":"hi"}`
	result, err := svc.Forward(context.Background(), c, forcedEffortAccount("high"), []byte(body))
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "forced reasoning effort")
	require.Empty(t, upstream.lastBody, "request must not reach upstream")
}

// Cursor 形态：chat 入口收到 Responses 形态的 body（有 input、没有 messages）会原样转发，
// 强制档位同样要生效，用量日志记录强制后的档位。
func TestForcedReasoningEffort_ChatCompletions_ResponsesShape(t *testing.T) {
	const body = `{"model":"gpt-6-luna","stream":false,"temperature":0.7,"reasoning":{"effort":"none"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	upstream, result := runForcedEffortChat(t, forcedEffortAccount("high"), body)
	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "temperature").Exists())
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "high", *result.ReasoningEffort)

	plain, _ := runForcedEffortChat(t, forcedEffortAccount(nil), body)
	require.Equal(t, "none", gjson.GetBytes(plain.lastBody, "reasoning.effort").String())
}

func TestForcedReasoningEffort_ChatCompletions_ResponsesShape_SetError(t *testing.T) {
	upstream := newForcedEffortSSEUpstream(forcedEffortLunaModel)
	svc := newOpenAIImageGenerationControlTestService(upstream)
	body := `{"model":"gpt-6-luna","stream":false,"reasoning":[1],"input":"hi"}`
	c := newForcedEffortCompatContext("/v1/chat/completions", []byte(body))
	result, err := svc.ForwardAsChatCompletions(context.Background(), c, forcedEffortAccount("high"), []byte(body), "", "")
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "forced reasoning effort")
	require.Empty(t, upstream.lastBody)
}
