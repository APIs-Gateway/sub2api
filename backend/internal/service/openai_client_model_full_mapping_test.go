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

// client_model_full_mapping（free 站）：开关打开后，用户请求 A、账号映射到 C、上游回显 C 或别的写法 R 时，
// 客户端字节里的模型名只能是 A；审计（UpstreamModelMismatchMark）保留上游真实值；开关关闭时与改动前逐字节一致。

const (
	fullMapA       = "deepseek-v4-pro"
	fullMapC       = "deepseek-v4.1-flash"
	fullMapR       = "combo/deepseek-v4-1-flash"
	fullMapOutText = "I am deepseek-v4.1-flash, ready"
)

const (
	fullMapStreamRequestBody    = `{"model":"deepseek-v4-pro","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	fullMapNonStreamRequestBody = `{"model":"deepseek-v4-pro","stream":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
)

// completed 事件里的 model：流式主用例是 C（与发送一致），SSE→JSON 用例用 R 触发审计标记。
// error 帧放在输出开始之后、completed 之前：completed 之后的帧不会被转发给客户端。
func fullMapResponsesSSE(completedModel string) string {
	return "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"" + fullMapR + "\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + fullMapOutText + "\"}\n\n" +
		"data: {\"type\":\"error\",\"error\":{\"message\":\"" + fullMapC + " overloaded\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"" + completedModel + "\",\"usage\":{\"input_tokens\":596,\"output_tokens\":5,\"total_tokens\":601,\"quota\":{\"model\":\"" + fullMapC + "\"}}}}\n\n"
}

func fullMapResponsesJSON() string {
	return `{"id":"r1","object":"response","model":"` + fullMapR + `","status":"completed",` +
		`"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"` + fullMapOutText + `"}]}],` +
		`"metadata":{"note":"` + fullMapC + ` overloaded"},` +
		`"usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15,"quota":{"model":"` + fullMapC + `"}}}`
}

func newFullMapJSONUpstream(body string) *httpUpstreamRecorder {
	return &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}}
}

func newFullMapResponsesTest(t *testing.T, upstream *httpUpstreamRecorder, enabled bool) (*OpenAIGatewayService, *gin.Context, *httptest.ResponseRecorder, *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := newOpenAIImageGenerationControlTestService(upstream)
	svc.cfg.Gateway.DisableUpstreamModelMismatchBlock = true
	svc.cfg.Gateway.ClientModelFullMapping = enabled
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()
	account.Credentials["model_mapping"] = map[string]any{fullMapA: fullMapC}
	return svc, c, recorder, account
}

func TestClientModelFullMapping_Responses_Stream(t *testing.T) {
	upstream := newUpstreamModelMismatchSSEUpstream(fullMapResponsesSSE(fullMapC))
	svc, c, recorder, account := newFullMapResponsesTest(t, upstream, true)

	result, err := svc.Forward(context.Background(), c, account, []byte(fullMapStreamRequestBody))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, fullMapC, gjson.GetBytes(upstream.lastBody, "model").String(), "mapped model must be what we sent upstream")

	got := recorder.Body.String()
	require.NotContains(t, strings.ReplaceAll(got, fullMapOutText, ""), fullMapC, "mapped name leaked outside the output text")
	require.NotContains(t, got, "combo/")
	require.Contains(t, got, `"delta":"`+fullMapOutText+`"`, "output text must be preserved verbatim")
	require.Contains(t, got, `"quota":{"model":"`+fullMapA+`"}`)
	require.Contains(t, got, `"message":"`+fullMapA+` overloaded"`)
	require.Contains(t, got, `"model":"`+fullMapA+`"`)

	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark, "audit mark must still be recorded")
	require.Equal(t, fullMapR, mark.ResponseModel, "audit must keep the real upstream model")
	require.False(t, mark.Blocked)
	v, ok := clientModelViewFrom(c)
	require.True(t, ok)
	require.Equal(t, fullMapA, v.client)
}

func TestClientModelFullMapping_Responses_NonStream(t *testing.T) {
	upstream := newFullMapJSONUpstream(fullMapResponsesJSON())
	svc, c, recorder, account := newFullMapResponsesTest(t, upstream, true)

	result, err := svc.Forward(context.Background(), c, account, []byte(fullMapNonStreamRequestBody))
	require.NoError(t, err)
	require.NotNil(t, result)

	got := recorder.Body.String()
	require.True(t, gjson.Valid(got), got)
	require.NotContains(t, strings.ReplaceAll(got, fullMapOutText, ""), fullMapC, "mapped name leaked outside the output text")
	require.NotContains(t, got, "combo/")
	require.Equal(t, fullMapA, gjson.Get(got, "model").String())
	require.Equal(t, fullMapA, gjson.Get(got, "usage.quota.model").String())
	require.Equal(t, fullMapA+" overloaded", gjson.Get(got, "metadata.note").String())
	require.Equal(t, fullMapOutText, gjson.Get(got, "output.0.content.0.text").String(), "output text must be preserved verbatim")

	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.Equal(t, fullMapR, mark.ResponseModel)
}

func TestClientModelFullMapping_Responses_SSEToJSON(t *testing.T) {
	upstream := newUpstreamModelMismatchSSEUpstream(fullMapResponsesSSE(fullMapR))
	svc, c, recorder, account := newFullMapResponsesTest(t, upstream, true)

	result, err := svc.Forward(context.Background(), c, account, []byte(fullMapNonStreamRequestBody))
	require.NoError(t, err)
	require.NotNil(t, result)

	got := recorder.Body.String()
	require.True(t, gjson.Valid(got), got)
	require.NotContains(t, got, "combo/")
	require.Equal(t, fullMapA, gjson.Get(got, "model").String())
	require.Equal(t, fullMapA, gjson.Get(got, "usage.quota.model").String())

	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.Equal(t, fullMapR, mark.ResponseModel)
}

// 开关关闭：行为与改动前一致（只改顶层 model / response.model，其余原样），不建 view。
func TestClientModelFullMapping_GateOff_Unchanged(t *testing.T) {
	upstream := newUpstreamModelMismatchSSEUpstream(fullMapResponsesSSE(fullMapC))
	svc, c, recorder, account := newFullMapResponsesTest(t, upstream, false)

	result, err := svc.Forward(context.Background(), c, account, []byte(fullMapStreamRequestBody))
	require.NoError(t, err)
	require.NotNil(t, result)

	got := recorder.Body.String()
	require.Contains(t, got, `"quota":{"model":"`+fullMapC+`"}`, "gate off: nested model fields stay as upstream sent them")
	require.Contains(t, got, `"message":"`+fullMapC+` overloaded"`)
	require.Contains(t, got, `"model":"`+fullMapA+`"`, "legacy top-level alignment still applies")
	require.NotContains(t, got, "combo/")
	_, ok := clientModelViewFrom(c)
	require.False(t, ok)
}

func newFullMapChatTest(t *testing.T, enabled bool) (*OpenAIGatewayService, *gin.Context, *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"` + fullMapA + `","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop before response parsing"}}`)),
	}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	svc.cfg.Gateway.ClientModelFullMapping = enabled
	account := newOpenAIImageGenerationControlTestAccount()
	account.Credentials["model_mapping"] = map[string]any{fullMapA: fullMapC}
	return svc, c, account
}

func TestClientModelFullMapping_ChatCompletions_SetsView(t *testing.T) {
	svc, c, account := newFullMapChatTest(t, true)
	body := []byte(`{"model":"` + fullMapA + `","messages":[{"role":"user","content":"hello"}],"stream":false}`)

	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.Error(t, err)
	require.Equal(t, fullMapC, gjson.GetBytes(svc.httpUpstream.(*httpUpstreamRecorder).lastBody, "model").String())

	v, ok := clientModelViewFrom(c)
	require.True(t, ok)
	require.Equal(t, fullMapA, v.client)
	require.Equal(t, fullMapA+" down", scrubClientVisibleModelText(c, fullMapC+" down"), "mapped upstream name must be a foreign name")
}

func TestClientModelFullMapping_ChatCompletions_GateOffNoView(t *testing.T) {
	svc, c, account := newFullMapChatTest(t, false)
	body := []byte(`{"model":"` + fullMapA + `","messages":[{"role":"user","content":"hello"}],"stream":false}`)

	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.Error(t, err)
	_, ok := clientModelViewFrom(c)
	require.False(t, ok)
}

func TestClientModelFullMapping_Messages_SetsView(t *testing.T) {
	svc, c, account := newFullMapChatTest(t, true)
	body := []byte(`{"model":"` + fullMapA + `","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)

	_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.Error(t, err)

	v, ok := clientModelViewFrom(c)
	require.True(t, ok)
	require.Equal(t, fullMapA, v.client)
	require.Equal(t, fullMapA+" down", scrubClientVisibleModelText(c, fullMapC+" down"))
}

func TestClientModelFullMapping_Messages_GateOffNoView(t *testing.T) {
	svc, c, account := newFullMapChatTest(t, false)
	body := []byte(`{"model":"` + fullMapA + `","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)

	_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.Error(t, err)
	_, ok := clientModelViewFrom(c)
	require.False(t, ok)
}

// 比对必须先于对齐：checkUpstreamModelMismatch 读到的是上游原始值，并把它追加成外来名。
func TestClientModelFullMapping_MismatchCheckNotesForeign(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	v, _ := newClientModelView(fullMapA, fullMapC)
	setClientModelView(c, v)
	svc := &OpenAIGatewayService{}
	_ = svc.checkUpstreamModelMismatch(c, nil, "", nil, fullMapC, fullMapR, false, false, OpenAIUsage{}, fullMapA)
	require.Equal(t, fullMapA, scrubClientVisibleModelText(c, fullMapR))
	require.Equal(t, fullMapR, GetOpsUpstreamModelMismatch(c).ResponseModel)

	// 一致时不追加外来名
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	v2, _ := newClientModelView(fullMapA)
	setClientModelView(c2, v2)
	require.Nil(t, svc.checkUpstreamModelMismatch(c2, nil, "", nil, fullMapC, fullMapC, false, false, OpenAIUsage{}, fullMapA))
	require.Equal(t, fullMapC, scrubClientVisibleModelText(c2, fullMapC))
}

// F2：被「容忍」的上游回显（日期快照等，upstreamModelMatches 判一致）也要记进外来名；
// 比对、打标与拦截行为不变（一致 → 不打标、不拦截）。
func TestClientModelFullMapping_MismatchCheckNotesToleratedEcho(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	v, _ := newClientModelView(fullMapA, fullMapC)
	setClientModelView(c, v)
	svc := &OpenAIGatewayService{}
	const snapshot = fullMapC + "-20251101"

	require.Nil(t, svc.checkUpstreamModelMismatch(c, nil, "", nil, fullMapC, snapshot, false, true, OpenAIUsage{}, fullMapA))

	require.Nil(t, GetOpsUpstreamModelMismatch(c), "容忍的回显不打标")
	require.Equal(t, "model "+fullMapA+" overloaded", scrubClientVisibleModelText(c, "model "+snapshot+" overloaded"))

	// 回显与发送完全一致：不追加外来名（view 不变）
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	v2, _ := newClientModelView(fullMapA)
	setClientModelView(c2, v2)
	require.Nil(t, svc.checkUpstreamModelMismatch(c2, nil, "", nil, fullMapC, fullMapC, false, true, OpenAIUsage{}, fullMapA))
	require.Equal(t, snapshot, scrubClientVisibleModelText(c2, snapshot))
}
