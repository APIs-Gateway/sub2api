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

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// gateway.chat_completions_reasoning_details：chat 响应透出加密推理 / 摘要，请求带回时还原成 reasoning 项，
// 解密失败时剥掉 reasoning_details 退回 <thinking> 重试一次。

const chatRDModel = "gpt-6-luna"

func chatRDSSE(events ...string) string {
	var sb strings.Builder
	for _, e := range events {
		sb.WriteString("data: " + e + "\n\n")
	}
	return sb.String()
}

const (
	chatRDCreated  = `{"type":"response.created","response":{"id":"resp_1","model":"gpt-6-luna","status":"in_progress"}}`
	chatRDSummary1 = `{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"s1"}`
	chatRDSummary2 = `{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":1,"delta":"s2"}`
	chatRDItemDone = `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"ENC-1","summary":[{"type":"summary_text","text":"s1"},{"type":"summary_text","text":"s2"}]}}`
	chatRDText     = `{"type":"response.output_text.delta","output_index":1,"delta":"hello"}`
	chatRDDone     = `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-6-luna","status":"completed","output":[` +
		`{"type":"reasoning","id":"rs_1","encrypted_content":"ENC-1","summary":[{"type":"summary_text","text":"s1"},{"type":"summary_text","text":"s2"}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],` +
		`"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"output_tokens_details":{"reasoning_tokens":7}}}}`
)

func chatRDResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newChatRDService(upstream *httpUpstreamRecorder, enabled bool) (*OpenAIGatewayService, *Account) {
	cfg := &config.Config{}
	cfg.Gateway.ChatCompletionsReasoningDetails = enabled
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
	account := &Account{
		ID: 7, Name: "a6", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "fixture-key", "base_url": "https://api.openai.com"},
		Extra:       map[string]any{"openai_responses_supported": true},
	}
	return svc, account
}

func runChatRD(t *testing.T, svc *OpenAIGatewayService, account *Account, body string) (*httptest.ResponseRecorder, *gin.Context, *OpenAIForwardResult, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(body), "", "")
	return rec, c, result, err
}

func chatRDUpstream(sse string) *httpUpstreamRecorder {
	return &httpUpstreamRecorder{resp: chatRDResponse(http.StatusOK, sse)}
}

const chatRDNonStreamReq = `{"model":"gpt-6-luna","stream":false,"messages":[{"role":"user","content":"hi"}]}`
const chatRDStreamReq = `{"model":"gpt-6-luna","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func chatRDFullSSE() string {
	return chatRDSSE(chatRDCreated, chatRDSummary1, chatRDSummary2, chatRDItemDone, chatRDText, chatRDDone)
}

// ---- 响应方向 ----

func TestChatReasoningDetails_NonStream_OffOmitsEverything(t *testing.T) {
	svc, account := newChatRDService(chatRDUpstream(chatRDFullSSE()), false)
	rec, _, _, err := runChatRD(t, svc, account, chatRDNonStreamReq)
	require.NoError(t, err)
	got := rec.Body.String()
	require.NotContains(t, got, "reasoning_details")
	require.NotContains(t, got, `"reasoning":`)
	require.NotContains(t, got, "ENC-1")
	require.Equal(t, "s1s2", gjson.Get(got, "choices.0.message.reasoning_content").String(), "legacy concatenation unchanged")
	require.Equal(t, int64(7), gjson.Get(got, "usage.completion_tokens_details.reasoning_tokens").Int())
}

func TestChatReasoningDetails_NonStream_On(t *testing.T) {
	svc, account := newChatRDService(chatRDUpstream(chatRDFullSSE()), true)
	rec, _, _, err := runChatRD(t, svc, account, chatRDNonStreamReq)
	require.NoError(t, err)
	got := rec.Body.String()
	msg := gjson.Get(got, "choices.0.message")
	require.Equal(t, "s1\n\ns2", msg.Get("reasoning_content").String())
	require.Equal(t, "s1\n\ns2", msg.Get("reasoning").String())
	require.Equal(t, "hello", msg.Get("content").String())
	details := msg.Get("reasoning_details").Array()
	require.Len(t, details, 2)
	require.JSONEq(t, `{"type":"reasoning.summary","summary":"s1\n\ns2","id":"rs_1","format":"openai-responses-v1","index":0}`, details[0].Raw)
	require.JSONEq(t, `{"type":"reasoning.encrypted","data":"ENC-1","id":"rs_1","format":"openai-responses-v1","index":1}`, details[1].Raw)
	require.Equal(t, int64(7), gjson.Get(got, "usage.completion_tokens_details.reasoning_tokens").Int())
}

// 终止事件 output 为空时，摘要和密文从 output_item.done 兜底取。
func TestChatReasoningDetails_NonStream_On_EmptyTerminalOutputFallsBackToDoneItems(t *testing.T) {
	emptyDone := `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-6-luna","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`
	svc, account := newChatRDService(chatRDUpstream(chatRDSSE(chatRDCreated, chatRDSummary1, chatRDSummary2, chatRDItemDone, chatRDText, emptyDone)), true)
	rec, _, _, err := runChatRD(t, svc, account, chatRDNonStreamReq)
	require.NoError(t, err)
	msg := gjson.Get(rec.Body.String(), "choices.0.message")
	require.Equal(t, "s1\n\ns2", msg.Get("reasoning_content").String(), "separator restored for delta-rebuilt output")
	require.Equal(t, "ENC-1", msg.Get("reasoning_details.1.data").String())
	require.Equal(t, "hello", msg.Get("content").String())
}

func chatRDStreamEvents(t *testing.T, body string) []gjson.Result {
	t.Helper()
	var out []gjson.Result
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") && line != "data: [DONE]" {
			out = append(out, gjson.Parse(strings.TrimPrefix(line, "data: ")))
		}
	}
	return out
}

func TestChatReasoningDetails_Stream_OnEmitsOnceInOrder(t *testing.T) {
	svc, account := newChatRDService(chatRDUpstream(chatRDFullSSE()), true)
	rec, _, _, err := runChatRD(t, svc, account, chatRDStreamReq)
	require.NoError(t, err)
	body := rec.Body.String()
	require.Equal(t, 1, strings.Count(body, "ENC-1"), "encrypted reasoning must be sent exactly once")

	var kinds []string
	var reasoning strings.Builder
	for _, ev := range chatRDStreamEvents(t, body) {
		d := ev.Get("choices.0.delta")
		switch {
		case d.Get("reasoning_details").Exists():
			kinds = append(kinds, "details")
			require.Equal(t, "reasoning.summary", d.Get("reasoning_details.0.type").String())
			require.Equal(t, "s1\n\ns2", d.Get("reasoning_details.0.summary").String())
			require.Equal(t, "reasoning.encrypted", d.Get("reasoning_details.1.type").String())
			require.Equal(t, int64(1), d.Get("reasoning_details.1.index").Int())
		case d.Get("reasoning_content").Exists():
			kinds = append(kinds, "reasoning")
			require.Equal(t, d.Get("reasoning_content").String(), d.Get("reasoning").String(), "alias equals reasoning_content")
			reasoning.WriteString(d.Get("reasoning_content").String())
		case d.Get("content").String() == "hello":
			kinds = append(kinds, "content")
		}
		if fr := ev.Get("choices.0.finish_reason"); fr.Type == gjson.String {
			kinds = append(kinds, "finish")
		}
	}
	require.Equal(t, []string{"reasoning", "reasoning", "details", "content", "finish"}, kinds)
	require.Equal(t, "s1\n\ns2", reasoning.String())
	require.Equal(t, int64(7), gjson.Get(strings.Split(strings.TrimSpace(body), "\n\n")[len(strings.Split(strings.TrimSpace(body), "\n\n"))-2], "usage.completion_tokens_details.reasoning_tokens").Int(), "include_usage chunk carries reasoning_tokens")
}

func TestChatReasoningDetails_Stream_OffHasNoDetailsOrAlias(t *testing.T) {
	svc, account := newChatRDService(chatRDUpstream(chatRDFullSSE()), false)
	rec, _, _, err := runChatRD(t, svc, account, chatRDStreamReq)
	require.NoError(t, err)
	body := rec.Body.String()
	require.NotContains(t, body, "reasoning_details")
	require.NotContains(t, body, `"reasoning":`)
	require.NotContains(t, body, "ENC-1")
	require.Contains(t, body, `"reasoning_content":"s1"`)
	require.Contains(t, body, `"reasoning_content":"s2"`, "no separator when switch is off")
	require.Contains(t, body, `"reasoning_tokens":7`)
}

// ---- 请求方向 ----

const chatRDReplayReq = `{"model":"gpt-6-luna","stream":false,"messages":[
	{"role":"user","content":"hi"},
	{"role":"assistant","content":"earlier","reasoning_content":"my thought","reasoning_details":[
		{"type":"reasoning.encrypted","data":"ENC-OLD","id":"rs_old","format":"openai-responses-v1","index":0}]},
	{"role":"user","content":"again"}]}`

func TestChatReasoningDetails_Request_RestoredRegardlessOfSwitch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		upstream := chatRDUpstream(chatRDFullSSE())
		svc, account := newChatRDService(upstream, enabled)
		_, _, _, err := runChatRD(t, svc, account, chatRDReplayReq)
		require.NoError(t, err)
		input := gjson.GetBytes(upstream.lastBody, "input").Array()
		require.Len(t, input, 4)
		require.Equal(t, "reasoning", input[1].Get("type").String())
		require.Equal(t, "ENC-OLD", input[1].Get("encrypted_content").String())
		require.Equal(t, "rs_old", input[1].Get("id").String())
		require.JSONEq(t, `[]`, input[1].Get("summary").Raw)
		require.Equal(t, "assistant", input[2].Get("role").String())
		require.NotContains(t, input[2].Raw, "thinking")
	}
}

func TestChatReasoningDetails_Request_NoFieldKeepsUpstreamBodyUnchanged(t *testing.T) {
	body := `{"model":"gpt-6-luna","stream":false,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"a","reasoning_content":"r"},{"role":"user","content":"b"}]}`
	off := chatRDUpstream(chatRDFullSSE())
	svcOff, accOff := newChatRDService(off, false)
	_, _, _, err := runChatRD(t, svcOff, accOff, body)
	require.NoError(t, err)
	require.Contains(t, string(off.lastBody), `thinking`)
	require.NotContains(t, string(off.lastBody), `"type":"reasoning"`)
	require.NotContains(t, string(off.lastBody), `"summary"`)
}

// ---- 请求侧：reasoning.summary / reasoning 对象 ----

func TestChatReasoningDetails_Request_SummaryAutoAddedOnlyWhenSwitchOn(t *testing.T) {
	// 第三方上游（不是官方 OpenAI / Codex）：现状会把 summary 清空。
	third := func(enabled bool) (*httpUpstreamRecorder, *OpenAIGatewayService, *Account) {
		up := chatRDUpstream(chatRDFullSSE())
		svc, acc := newChatRDService(up, enabled)
		acc.Credentials["base_url"] = "https://relay.example.com"
		return up, svc, acc
	}
	body := `{"model":"gpt-6-luna","stream":false,"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`

	up, svc, acc := third(false)
	_, _, _, err := runChatRD(t, svc, acc, body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(up.lastBody, "reasoning.summary").Exists(), "switch off: bytes as before")
	require.Equal(t, "high", gjson.GetBytes(up.lastBody, "reasoning.effort").String())

	up, svc, acc = third(true)
	_, _, _, err = runChatRD(t, svc, acc, body)
	require.NoError(t, err)
	require.Equal(t, "high", gjson.GetBytes(up.lastBody, "reasoning.effort").String())
	require.Equal(t, "auto", gjson.GetBytes(up.lastBody, "reasoning.summary").String())

	// 没有任何档位：推理模型也补 summary，且不产生空 effort。
	up, svc, acc = third(true)
	_, _, _, err = runChatRD(t, svc, acc, `{"model":"gpt-6-luna","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, err)
	require.Equal(t, "auto", gjson.GetBytes(up.lastBody, "reasoning.summary").String())
	require.False(t, gjson.GetBytes(up.lastBody, "reasoning.effort").Exists())

	// 非推理模型且没有 reasoning 对象：不补。
	up, svc, acc = third(true)
	_, _, _, err = runChatRD(t, svc, acc, `{"model":"gpt-4.1","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	_ = err
	require.False(t, gjson.GetBytes(up.lastBody, "reasoning").Exists())

	// effort=none：不补。
	up, svc, acc = third(true)
	_, _, _, err = runChatRD(t, svc, acc, `{"model":"gpt-6-luna","stream":false,"reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, err)
	require.Equal(t, "none", gjson.GetBytes(up.lastBody, "reasoning.effort").String())
	require.False(t, gjson.GetBytes(up.lastBody, "reasoning.summary").Exists())
}

func TestChatReasoningDetails_Request_ForcedEffortAndSummaryTogether(t *testing.T) {
	up := chatRDUpstream(chatRDFullSSE())
	svc, acc := newChatRDService(up, true)
	acc.Credentials["base_url"] = "https://relay.example.com"
	acc.Extra["openai_force_reasoning_effort"] = "high"
	_, _, _, err := runChatRD(t, svc, acc, `{"model":"gpt-6-luna","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, err)
	require.Equal(t, "high", gjson.GetBytes(up.lastBody, "reasoning.effort").String())
	require.Equal(t, "auto", gjson.GetBytes(up.lastBody, "reasoning.summary").String())

	// 客户端显式 none 被强制提升后，summary 也要补上。
	up = chatRDUpstream(chatRDFullSSE())
	svc, acc = newChatRDService(up, true)
	acc.Credentials["base_url"] = "https://relay.example.com"
	acc.Extra["openai_force_reasoning_effort"] = "high"
	_, _, _, err = runChatRD(t, svc, acc, `{"model":"gpt-6-luna","stream":false,"reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, err)
	require.Equal(t, "high", gjson.GetBytes(up.lastBody, "reasoning.effort").String())
	require.Equal(t, "auto", gjson.GetBytes(up.lastBody, "reasoning.summary").String())
}

func TestChatReasoningDetails_Request_OpenRouterReasoningObject(t *testing.T) {
	run := func(enabled bool, body string) []byte {
		up := chatRDUpstream(chatRDFullSSE())
		svc, acc := newChatRDService(up, enabled)
		acc.Credentials["base_url"] = "https://relay.example.com"
		_, _, _, err := runChatRD(t, svc, acc, body)
		require.NoError(t, err)
		return up.lastBody
	}
	msgs := `"messages":[{"role":"user","content":"hi"}]`

	got := run(true, `{"model":"gpt-6-luna","stream":false,"reasoning":{"effort":"high","summary":"detailed","max_tokens":2000,"exclude":true},`+msgs+`}`)
	require.Equal(t, "high", gjson.GetBytes(got, "reasoning.effort").String())
	require.Equal(t, "detailed", gjson.GetBytes(got, "reasoning.summary").String())
	require.False(t, gjson.GetBytes(got, "reasoning.max_tokens").Exists())
	require.False(t, gjson.GetBytes(got, "reasoning.exclude").Exists())

	// reasoning_effort 优先于 reasoning.effort；非法 summary 退回 auto。
	got = run(true, `{"model":"gpt-6-luna","stream":false,"reasoning_effort":"low","reasoning":{"effort":"high","summary":"weird"},`+msgs+`}`)
	require.Equal(t, "low", gjson.GetBytes(got, "reasoning.effort").String())
	require.Equal(t, "auto", gjson.GetBytes(got, "reasoning.summary").String())

	// enabled=false：不传 effort，也不补 summary。
	got = run(true, `{"model":"gpt-6-luna","stream":false,"reasoning":{"enabled":false,"effort":"high"},`+msgs+`}`)
	require.False(t, gjson.GetBytes(got, "reasoning").Exists())

	// effort=none：不补 summary。
	got = run(true, `{"model":"gpt-6-luna","stream":false,"reasoning":{"effort":"none","summary":"detailed"},`+msgs+`}`)
	require.Equal(t, "none", gjson.GetBytes(got, "reasoning.effort").String())
	require.False(t, gjson.GetBytes(got, "reasoning.summary").Exists())

	// 开关关闭：reasoning 对象被忽略，与改动前一致。
	got = run(false, `{"model":"gpt-6-luna","stream":false,"reasoning":{"effort":"high","summary":"detailed"},`+msgs+`}`)
	require.False(t, gjson.GetBytes(got, "reasoning").Exists())
}

func TestChatReasoningDetails_Request_AliasAndPlainDetailsFoldIntoThinking(t *testing.T) {
	body := `{"model":"gpt-6-luna","stream":false,"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"a1","reasoning":"alias thought"},
		{"role":"assistant","content":"a2","reasoning_details":[{"type":"reasoning.summary","summary":"S"},{"type":"reasoning.text","text":"T"}]}]}`
	up := chatRDUpstream(chatRDFullSSE())
	svc, acc := newChatRDService(up, true)
	_, _, _, err := runChatRD(t, svc, acc, body)
	require.NoError(t, err)
	input := gjson.GetBytes(up.lastBody, "input").Array()
	require.Len(t, input, 3)
	require.Equal(t, "<thinking>alias thought</thinking>\na1", input[1].Get("content.0.text").String())
	require.Equal(t, "<thinking>S\n\nT</thinking>\na2", input[2].Get("content.0.text").String())

	// 开关关闭：别名与 summary/text 元素一律忽略（与改动前一致）。
	up = chatRDUpstream(chatRDFullSSE())
	svc, acc = newChatRDService(up, false)
	_, _, _, err = runChatRD(t, svc, acc, body)
	require.NoError(t, err)
	require.NotContains(t, string(up.lastBody), "thinking")
}

// ---- invalid_encrypted_content 兜底 ----

func TestChatReasoningDetails_InvalidEncryptedContentRetriesOnceWithThinking(t *testing.T) {
	bad := `{"error":{"type":"invalid_request_error","code":"invalid_encrypted_content","message":"The encrypted content gAAA could not be verified."}}`
	up := &httpUpstreamRecorder{responses: []*http.Response{
		chatRDResponse(http.StatusBadRequest, bad),
		chatRDResponse(http.StatusOK, chatRDFullSSE()),
	}}
	svc, acc := newChatRDService(up, true)
	acc.Credentials["base_url"] = "https://relay.example.com"
	before := *acc

	rec, c, result, err := runChatRD(t, svc, acc, chatRDReplayReq)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, up.bodies, 2, "exactly one retry")

	first := gjson.GetBytes(up.bodies[0], "input").Array()
	require.Equal(t, "reasoning", first[1].Get("type").String())
	require.Equal(t, "ENC-OLD", first[1].Get("encrypted_content").String())

	second := gjson.GetBytes(up.bodies[1], "input").Array()
	require.Len(t, second, 3)
	require.NotContains(t, string(up.bodies[1]), "ENC-OLD")
	require.NotContains(t, string(up.bodies[1]), `"type":"reasoning"`)
	require.Equal(t, "<thinking>my thought</thinking>\nearlier", second[1].Get("content.0.text").String())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "hello", gjson.Get(rec.Body.String(), "choices.0.message.content").String())
	require.Equal(t, before.Status, acc.Status)
	require.Equal(t, before.Schedulable, acc.Schedulable)
	require.Nil(t, acc.RateLimitResetAt)
	require.Nil(t, acc.TempUnschedulableUntil)
	_, failed := c.Get(OpsUpstreamErrorsKey)
	require.False(t, failed, "must not be recorded as an upstream/account failure")
}

func TestChatReasoningDetails_InvalidEncryptedContentSecondFailureNotRetriedAgain(t *testing.T) {
	bad := `{"error":{"type":"invalid_request_error","code":"invalid_encrypted_content","message":"nope"}}`
	up := &httpUpstreamRecorder{responses: []*http.Response{
		chatRDResponse(http.StatusBadRequest, bad),
		chatRDResponse(http.StatusBadRequest, bad),
	}}
	svc, acc := newChatRDService(up, true)
	_, _, _, err := runChatRD(t, svc, acc, chatRDReplayReq)
	require.Error(t, err)
	require.Len(t, up.bodies, 2, "never more than one retry")
}

func TestChatReasoningDetails_InvalidEncryptedContentWithoutReasoningDetailsNotRetried(t *testing.T) {
	bad := `{"error":{"type":"invalid_request_error","code":"invalid_encrypted_content","message":"nope"}}`
	up := &httpUpstreamRecorder{responses: []*http.Response{chatRDResponse(http.StatusBadRequest, bad)}}
	svc, acc := newChatRDService(up, true)
	_, _, _, err := runChatRD(t, svc, acc, chatRDNonStreamReq)
	require.Error(t, err)
	require.Len(t, up.bodies, 1)
}

func TestIsOpenAIInvalidEncryptedContentResponse(t *testing.T) {
	require.True(t, isOpenAIInvalidEncryptedContentResponse([]byte(`{"error":{"code":"invalid_encrypted_content","message":"x"}}`)))
	require.True(t, isOpenAIInvalidEncryptedContentResponse([]byte(`{"error":{"code":"thinking_signature_invalid","message":"The encrypted content abc could not be verified; could not be decrypted or parsed"}}`)))
	require.False(t, isOpenAIInvalidEncryptedContentResponse([]byte(`{"error":{"code":"thinking_signature_invalid","message":"other"}}`)))
	require.False(t, isOpenAIInvalidEncryptedContentResponse([]byte(`{"error":{"code":"rate_limit","message":"x"}}`)))
}

func TestStripChatReasoningDetails(t *testing.T) {
	out, changed := stripChatReasoningDetails([]byte(`{"messages":[{"role":"user","content":"u"},{"role":"assistant","reasoning_details":[1],"content":"a"},{"role":"assistant","reasoning_details":[],"content":"b"}]}`))
	require.True(t, changed)
	require.JSONEq(t, `{"messages":[{"role":"user","content":"u"},{"role":"assistant","content":"a"},{"role":"assistant","content":"b"}]}`, string(out))

	same := []byte(`{"messages":[{"role":"user","content":"u"}]}`)
	out, changed = stripChatReasoningDetails(same)
	require.False(t, changed)
	require.Equal(t, string(same), string(out))

	out, changed = stripChatReasoningDetails([]byte(`{"input":"x"}`))
	require.False(t, changed)
	require.Equal(t, `{"input":"x"}`, string(out))
}

func TestEnsureChatResponsesReasoningSummary_Branches(t *testing.T) {
	// 已有 summary 且客户端没指定：不动。
	in := []byte(`{"reasoning":{"effort":"high","summary":"concise"}}`)
	out, err := ensureChatResponsesReasoningSummary(in, "gpt-4.1", chatClientReasoningOptions{})
	require.NoError(t, err)
	require.Equal(t, string(in), string(out))
	// 客户端指定的覆盖已有。
	out, err = ensureChatResponsesReasoningSummary(in, "gpt-4.1", chatClientReasoningOptions{Summary: "detailed"})
	require.NoError(t, err)
	require.Equal(t, "detailed", gjson.GetBytes(out, "reasoning.summary").String())
	// Disabled。
	out, err = ensureChatResponsesReasoningSummary([]byte(`{}`), "gpt-6-luna", chatClientReasoningOptions{Disabled: true})
	require.NoError(t, err)
	require.Equal(t, `{}`, string(out))
	// 非推理模型无 reasoning 对象。
	out, err = ensureChatResponsesReasoningSummary([]byte(`{}`), "gpt-4.1", chatClientReasoningOptions{})
	require.NoError(t, err)
	require.Equal(t, `{}`, string(out))

	require.Equal(t, chatClientReasoningOptions{}, parseChatClientReasoningOptions([]byte(`{"reasoning":"high"}`)))
	require.Equal(t, chatClientReasoningOptions{Effort: "high", Summary: "auto", Disabled: true},
		parseChatClientReasoningOptions([]byte(`{"reasoning":{"effort":" High ","summary":"AUTO","enabled":false}}`)))
}

// ---- 完全映射：密文不被改写 ----

func TestClientModelView_DoesNotRewriteEncryptedReasoning(t *testing.T) {
	// 外来名 a6api-pro 与 base64 里碰巧出现的字母数字串同形。
	view, ok := newClientModelView("luna-public", "a6api-pro")
	require.True(t, ok)
	require.Equal(t, "luna-public", view.scrubText("a6api-pro"), "sanity: the name itself would be rewritten in plain text")

	cipher := "gAAAAA6api-pro6apiA6API-PRO=="
	payloads := []string{
		`{"id":"c1","model":"a6api-pro","reasoning_details":[{"type":"reasoning.encrypted","data":"` + cipher + `","id":"a6api-pro","format":"openai-responses-v1","index":0}]}`,
		`{"model":"a6api-pro","message":{"signature":"` + cipher + `"}}`,
		`{"model":"a6api-pro","thoughtSignature":"` + cipher + `"}`,
		`{"model":"a6api-pro","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.encrypted","data":"` + cipher + `"}]}}]}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"` + cipher + `"}}`,
		`{"type":"response.completed","response":{"model":"a6api-pro","output":[{"type":"reasoning","encrypted_content":"` + cipher + `"}]}}`,
	}
	for _, p := range payloads {
		out := string(view.alignPayload([]byte(p)))
		require.Contains(t, out, cipher, p)
	}
	// model 字段本身仍被改写。
	require.Contains(t, string(view.alignPayload([]byte(payloads[1]))), `"model":"luna-public"`)
}
