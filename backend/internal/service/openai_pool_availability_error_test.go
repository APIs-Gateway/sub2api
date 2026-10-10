//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// a6（pool_mode 上游）的真实错误帧，取自生产 ops_error_logs.upstream_error_detail。

// 流内 response.failed：前面只有 response.created / response.in_progress，没有任何输出。
const a6StreamCommitFailureFrame = `{"request_id":"202610100710247684528578268d9d6H9xEBOKn","response":{"created_at":0,"error":{"code":"smart_route_stream_commit_failure","message":"智能路由流式响应中途失败。 原因：个人池商家在流式响应开始后中断，当前响应无法再切换商家。 解决方案：请重新发起请求。 request_id: 202610100710247684528578268d9d6H9xEBOKn"},"id":"","instructions":null,"max_output_tokens":0,"metadata":null,"model":"gpt-5.6-terra","object":"response","output":[],"parallel_tool_calls":false,"previous_response_id":null,"reasoning":null,"status":"failed","store":false,"temperature":0,"tool_choice":null,"tools":null,"top_p":0,"truncation":null,"usage":null,"user":null},"type":"response.failed"}`

// HTTP 错误体：a6 用 502/504，也会用 400/404/403 携带。
const a6AllCandidatesFailedBody = `{"error":{"code":"smart_route_all_candidates_failed","message":"智能路由候选均请求失败。 原因：个人池候选均已尝试，没有成功。 解决方案：请稍后重试、切换商家或开启平台兜底。 request_id: 202610100744083925882058268d9d6cx0qryFr","param":"","reason":"个人池候选均已尝试，没有成功。","request_id":"202610100744083925882058268d9d6cx0qryFr","solution":"请稍后重试、切换商家或开启平台兜底。","type":"smart_route_all_candidates_failed"}}`

const a6UpstreamUnavailableBody = `{"error":{"code":"upstream_unavailable","message":"服务暂时不可用。 原因：上游服务、网络链路或代理返回异常响应。 解决方案：请稍后重试。 request_id: 202610100744083925882058268d9d6cx0qryFr","param":"","reason":"上游服务、网络链路或代理返回异常响应。","request_id":"202610100744083925882058268d9d6cx0qryFr","solution":"请稍后重试。","type":"upstream_unavailable"}}`

const a6UpstreamTimeoutBody = `{"error":{"code":"upstream_timeout","message":"等待响应超时。 原因：上游服务在规定时间内没有返回有效响应，或流式连接长时间没有新数据。 解决方案：请稍后重试。 request_id: 202610100744083925882058268d9d6cx0qryFr","param":"","reason":"上游服务在规定时间内没有返回有效响应。","request_id":"202610100744083925882058268d9d6cx0qryFr","solution":"请稍后重试。","type":"upstream_timeout"}}`

const a6InvalidParamBody = `{"error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid value for 'temperature'."}}`

func a6FailedFrame(code, message string) string {
	return `{"request_id":"rid-a6","response":{"created_at":0,"error":{"code":"` + code + `","message":"` + message + `"},"id":"","object":"response","output":[],"status":"failed"},"type":"response.failed"}`
}

func a6SSE(frames ...[2]string) string {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString("event: " + f[0] + "\ndata: " + f[1] + "\n\n")
	}
	return b.String()
}

func a6PreOutputFrames(last [2]string) string {
	return a6SSE(
		[2]string{"response.created", `{"type":"response.created","response":{"id":"resp_a6","status":"in_progress"}}`},
		[2]string{"response.in_progress", `{"type":"response.in_progress","response":{"id":"resp_a6","status":"in_progress"}}`},
		last,
	)
}

func newA6PoolAccount() *Account {
	return &Account{
		ID:          101,
		Name:        "a6",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"pool_mode": true},
	}
}

func newA6PlainAccount() *Account {
	return &Account{
		ID:          102,
		Name:        "plain-apikey",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{},
	}
}

type a6StreamRunner struct {
	name string
	run  func(svc *OpenAIGatewayService, c *gin.Context, resp *http.Response, account *Account) error
}

var a6StreamRunners = []a6StreamRunner{
	{
		name: "native",
		run: func(svc *OpenAIGatewayService, c *gin.Context, resp *http.Response, account *Account) error {
			_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			return err
		},
	},
	{
		name: "passthrough",
		run: func(svc *OpenAIGatewayService, c *gin.Context, resp *http.Response, account *Account) error {
			_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			return err
		},
	},
}

func runA6Stream(t *testing.T, runner a6StreamRunner, account *Account, stream string) (*httptest.ResponseRecorder, *gin.Context, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(stream)),
		Header:     http.Header{"X-Request-Id": []string{"rid-a6-test"}},
	}
	err := runner.run(svc, c, resp, account)
	return rec, c, err
}

// 池账号：首个输出前收到可用性错误 failed 帧 -> 可 failover、可池内同号重试、状态 502，
// 客户端 0 字节（测试未开心跳）。
func TestOpenAIStreamPoolAvailabilityFailedBeforeOutputFailsOverAndRetriesOnSameAccount(t *testing.T) {
	frames := map[string]string{
		"smart_route_stream_commit_failure(真实帧)": a6StreamCommitFailureFrame,
		"upstream_unavailable": a6FailedFrame("upstream_unavailable",
			"服务暂时不可用。 原因：上游服务、网络链路或代理返回异常响应。 解决方案：请稍后重试。 request_id: rid-a6"),
		"upstream_timeout": a6FailedFrame("upstream_timeout",
			"等待响应超时。 原因：上游服务在规定时间内没有返回有效响应，或流式连接长时间没有新数据。 request_id: rid-a6"),
	}
	for _, runner := range a6StreamRunners {
		for name, frame := range frames {
			t.Run(runner.name+"/"+name, func(t *testing.T) {
				stream := a6PreOutputFrames([2]string{"response.failed", frame})
				rec, c, err := runA6Stream(t, runner, newA6PoolAccount(), stream)

				require.Error(t, err)
				var failoverErr *UpstreamFailoverError
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
				require.True(t, failoverErr.RetryableOnSameAccount)
				require.False(t, c.Writer.Written())
				require.Empty(t, rec.Body.String())
			})
		}
	}
}

// 裸 error 事件（flat / 嵌套）携带可用性错误码时同样按池内重试处理，且不被当成首个输出。
func TestOpenAIStreamPoolAvailabilityErrorFrameBeforeOutputFailsOver(t *testing.T) {
	errorFrame := `{"type":"error","error":{"code":"upstream_unavailable","message":"服务暂时不可用。 请稍后重试。"}}`
	for _, runner := range a6StreamRunners {
		t.Run(runner.name, func(t *testing.T) {
			stream := a6PreOutputFrames([2]string{"error", errorFrame})
			rec, c, err := runA6Stream(t, runner, newA6PoolAccount(), stream)

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			require.True(t, failoverErr.RetryableOnSameAccount)
			require.False(t, c.Writer.Written())
			require.Empty(t, rec.Body.String())
		})
	}
}

// 非池账号：同样输入，行为与改动前一致——仍按通用 response.failed 规则 failover（502），
// 但不做同号重试（RetryableOnSameAccount=false）。
func TestOpenAIStreamNonPoolAccountAvailabilityFailedFrameBehaviorUnchanged(t *testing.T) {
	for _, runner := range a6StreamRunners {
		t.Run(runner.name, func(t *testing.T) {
			stream := a6PreOutputFrames([2]string{"response.failed", a6StreamCommitFailureFrame})
			rec, c, err := runA6Stream(t, runner, newA6PlainAccount(), stream)

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			require.False(t, failoverErr.RetryableOnSameAccount)
			require.False(t, c.Writer.Written())
			require.Empty(t, rec.Body.String())
		})
	}
}

// 已有真实输出之后再来该 failed 帧：不 failover，按原样收尾（失败帧照常转发给客户端）。
func TestOpenAIStreamPoolAvailabilityFailedAfterOutputIsNotFailedOver(t *testing.T) {
	stream := a6SSE(
		[2]string{"response.created", `{"type":"response.created","response":{"id":"resp_a6","status":"in_progress"}}`},
		[2]string{"response.output_text.delta", `{"type":"response.output_text.delta","delta":"hello-a6","output_index":0}`},
		[2]string{"response.failed", a6StreamCommitFailureFrame},
	)
	for _, runner := range a6StreamRunners {
		t.Run(runner.name, func(t *testing.T) {
			rec, _, err := runA6Stream(t, runner, newA6PoolAccount(), stream)

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr), "已输出内容后不能再 failover: %v", err)
			require.Contains(t, rec.Body.String(), "hello-a6")
			require.Contains(t, rec.Body.String(), "response.failed")
		})
	}
}

// HTTP 层（非 passthrough 判定）：池账号 400/403/404/502/504 + 命中错误体 -> failover；
// 普通参数错误体、非池账号不变。
func TestOpenAIPoolAvailabilityHTTPFailoverDecision(t *testing.T) {
	svc := &OpenAIGatewayService{}
	pool := newA6PoolAccount()
	plain := newA6PlainAccount()

	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusBadGateway, http.StatusGatewayTimeout} {
		for _, body := range []string{a6AllCandidatesFailedBody, a6UpstreamUnavailableBody, a6UpstreamTimeoutBody} {
			require.True(t, svc.shouldFailoverOpenAIUpstreamResponse(pool, status, "", []byte(body)), "status=%d", status)
			require.True(t, shouldFailoverOpenAIPassthroughResponse(pool, status, []byte(body)), "passthrough status=%d", status)
		}
	}

	// 普通 400 参数错误：行为不变（不 failover）。
	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(pool, http.StatusBadRequest, "", []byte(a6InvalidParamBody)))
	require.False(t, shouldFailoverOpenAIPassthroughResponse(pool, http.StatusBadRequest, []byte(a6InvalidParamBody)))

	// 非池账号：400/404 携带同样的错误体仍不 failover。
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound} {
		require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(plain, status, "", []byte(a6UpstreamUnavailableBody)), "status=%d", status)
		require.False(t, shouldFailoverOpenAIPassthroughResponse(plain, status, []byte(a6UpstreamUnavailableBody)), "passthrough status=%d", status)
	}

	// 状态码不在 400/403/404/502/504 内不触发（例如 200 / 429）。
	require.False(t, openAIPoolAvailabilityHTTPErrorForAccount(pool, http.StatusOK, []byte(a6UpstreamUnavailableBody)))
	require.False(t, openAIPoolAvailabilityHTTPErrorForAccount(pool, http.StatusTooManyRequests, []byte(a6UpstreamUnavailableBody)))
}

// HTTP 层构造：池账号命中 -> StatusCode 规范化为 502 且 RetryableOnSameAccount=true；
// 普通参数错误 / 非池账号 / nil 原样返回。
func TestApplyOpenAIPoolAvailabilityFailoverNormalizesStatus(t *testing.T) {
	pool := newA6PoolAccount()
	plain := newA6PlainAccount()

	got := applyOpenAIPoolAvailabilityFailover(pool, &UpstreamFailoverError{
		StatusCode:   http.StatusBadRequest,
		ResponseBody: []byte(a6UpstreamUnavailableBody),
	}, []byte(a6UpstreamUnavailableBody))
	require.Equal(t, http.StatusBadGateway, got.StatusCode)
	require.True(t, got.RetryableOnSameAccount)

	got = applyOpenAIPoolAvailabilityFailover(pool, &UpstreamFailoverError{
		StatusCode:   http.StatusBadRequest,
		ResponseBody: []byte(a6InvalidParamBody),
	}, []byte(a6InvalidParamBody))
	require.Equal(t, http.StatusBadRequest, got.StatusCode)
	require.False(t, got.RetryableOnSameAccount)

	got = applyOpenAIPoolAvailabilityFailover(plain, &UpstreamFailoverError{
		StatusCode:   http.StatusBadRequest,
		ResponseBody: []byte(a6UpstreamUnavailableBody),
	}, []byte(a6UpstreamUnavailableBody))
	require.Equal(t, http.StatusBadRequest, got.StatusCode)
	require.False(t, got.RetryableOnSameAccount)

	require.Nil(t, applyOpenAIPoolAvailabilityFailover(pool, nil, []byte(a6UpstreamUnavailableBody)))
}

// passthrough 的 HTTP failover 构造：原始上游状态（400）已写入 ops，
// 返回的 failover 错误规范化为 502 + 同号重试；池账号不因 403 被摘除（不走账号状态处理）。
func TestOpenAIPassthroughPoolAvailabilityHTTPFailoverKeepsOpsStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound} {
		svc := &OpenAIGatewayService{}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		resp := &http.Response{StatusCode: status, Header: http.Header{}}

		err := svc.handleFailoverErrorResponsePassthrough(
			context.Background(), resp, c, newA6PoolAccount(),
			[]byte(`{"model":"gpt-5.6-terra","stream":true}`), []byte(a6UpstreamUnavailableBody),
		)
		var failoverErr *UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr, "status=%d", status)
		require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode, "status=%d", status)
		require.True(t, failoverErr.RetryableOnSameAccount, "status=%d", status)

		recorded, ok := c.Get(OpsUpstreamStatusCodeKey)
		require.True(t, ok)
		require.Equal(t, status, recorded, "ops 里记录的仍是原始上游状态")
		require.Empty(t, rec.Body.String())
	}
}

// 池账号的可用性错误不触发账号级状态处理（尤其 403 不得把池账号摘掉）。
func TestHandleOpenAIAccountUpstreamErrorSkipsPoolAvailabilityError(t *testing.T) {
	repo := &capacityShedAccountRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: rateLimitService}

	require.False(t, svc.handleOpenAIAccountUpstreamError(
		context.Background(), newA6PoolAccount(), http.StatusForbidden, nil, []byte(a6UpstreamUnavailableBody), "gpt-5.6-terra",
	))
	require.Zero(t, repo.tempUnschedCalls)
}

// 链层：failover 耗尽、LastStatus=502 -> 可回退（FallbackWorthy）；
// 未规范化的原始 400 则是 Terminal，说明规范化是换组的前提。
func TestPoolAvailabilityFailoverExhaustedIsFallbackWorthy(t *testing.T) {
	body := []byte(a6AllCandidatesFailedBody)
	failoverErr := applyOpenAIPoolAvailabilityFailover(newA6PoolAccount(), &UpstreamFailoverError{
		StatusCode:   http.StatusBadRequest,
		ResponseBody: body,
	}, body)

	res := ClassifyHopFailure(HopFailure{Kind: HopFailureFailoverExhausted, LastStatus: failoverErr.StatusCode})
	require.Equal(t, HopOutcomeFallbackWorthy, res.Outcome)
	require.Equal(t, FallbackReasonFailoverExhausted, res.Reason)

	raw := ClassifyHopFailure(HopFailure{Kind: HopFailureFailoverExhausted, LastStatus: http.StatusBadRequest})
	require.Equal(t, HopOutcomeTerminal, raw.Outcome)
}

// 匹配函数本身：code / type / 中文兜底 / 无关错误 / 空对象。
func TestIsOpenAIPoolAvailabilityErrorPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		extra   []string
		want    bool
	}{
		{"failed 帧 response.error.code(真实帧)", a6StreamCommitFailureFrame, nil, true},
		{"HTTP error.code smart_route_all_candidates_failed(真实体)", a6AllCandidatesFailedBody, nil, true},
		{"HTTP error.code upstream_unavailable", a6UpstreamUnavailableBody, nil, true},
		{"HTTP error.code upstream_timeout", a6UpstreamTimeoutBody, nil, true},
		{"只有 error.type 命中", `{"error":{"type":"upstream_timeout"}}`, nil, true},
		{"只有 response.error.type 命中", `{"type":"response.failed","response":{"error":{"type":"smart_route_stream_commit_failure"}}}`, nil, true},
		{"code 大小写不敏感", `{"error":{"code":"UPSTREAM_UNAVAILABLE"}}`, nil, true},
		{"扁平 error 事件", `{"type":"error","code":"upstream_unavailable","message":"x"}`, nil, true},
		{"中文兜底 流式响应中途失败", `{"error":{"code":"x","message":"智能路由流式响应中途失败。"}}`, nil, true},
		{"中文兜底 智能路由候选均请求失败", `{"error":{"message":"智能路由候选均请求失败。 原因：..."}}`, nil, true},
		{"中文兜底 服务暂时不可用", `{"response":{"error":{"message":"服务暂时不可用。 请稍后重试"}}}`, nil, true},
		{"中文兜底 等待响应超时", `{"error":{"message":"等待响应超时。"}}`, nil, true},
		{"账号追加 code 命中", `{"error":{"code":"my_custom_code"}}`, []string{"my_custom_code"}, true},
		{"账号追加 code 未配置不命中", `{"error":{"code":"my_custom_code"}}`, nil, false},
		{"参数错误不命中", a6InvalidParamBody, nil, false},
		{"限流不命中", `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached"}}`, nil, false},
		{"关键词在 error 对象之外不命中", `{"error":{"code":"x","message":"y"},"note":"服务暂时不可用"}`, nil, false},
		{"关键词出现在输出内容里不命中", `{"type":"response.output_text.delta","delta":"服务暂时不可用"}`, nil, false},
		{"code 只是子串不命中", `{"error":{"code":"upstream_unavailable_for_legal_reasons"}}`, nil, false},
		{"error 不是对象不命中", `{"error":"upstream_unavailable"}`, nil, false},
		{"invalid_request_error 回显关键词不兜底", `{"error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid input: 服务暂时不可用"}}`, nil, false},
		{"invalid_request 前缀的 code 回显关键词不兜底", `{"error":{"code":"invalid_request_body","message":"等待响应超时"}}`, nil, false},
		{"invalid_request 前缀仍认 code 命中", `{"error":{"type":"invalid_request_error","code":"upstream_unavailable"}}`, nil, true},
		{"空对象", `{}`, nil, false},
		{"空 error 对象", `{"error":{}}`, nil, false},
		{"空 response.error 对象", `{"response":{"error":{}}}`, nil, false},
		{"非 JSON", `upstream_unavailable`, nil, false},
		{"空串", ``, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isOpenAIPoolAvailabilityErrorPayload([]byte(tc.payload), tc.extra))
		})
	}
	require.False(t, isOpenAIPoolAvailabilityErrorObject(gjson.Result{}, nil))
	require.False(t, isOpenAIPoolAvailabilityErrorPayload(nil, nil))
}

// 账号维度：只对池账号生效；账号级追加 code 读取。
func TestOpenAIPoolAvailabilityErrorForAccount(t *testing.T) {
	body := []byte(a6UpstreamUnavailableBody)
	require.True(t, openAIPoolAvailabilityErrorForAccount(newA6PoolAccount(), body))
	require.False(t, openAIPoolAvailabilityErrorForAccount(newA6PlainAccount(), body))
	require.False(t, openAIPoolAvailabilityErrorForAccount(nil, body))

	custom := newA6PoolAccount()
	custom.Credentials[poolModeAvailabilityErrorCodesCredentialKey] = []any{" Merchant_Down ", "merchant_down", "", 12}
	require.Equal(t, []string{"merchant_down"}, custom.GetPoolModeAvailabilityErrorCodes())
	require.True(t, openAIPoolAvailabilityErrorForAccount(custom, []byte(`{"error":{"code":"MERCHANT_DOWN"}}`)))
	require.True(t, openAIPoolAvailabilityErrorForAccount(custom, body), "追加列表不覆盖内置默认列表")
	require.False(t, openAIPoolAvailabilityErrorForAccount(newA6PoolAccount(), []byte(`{"error":{"code":"merchant_down"}}`)))

	// 非池账号即使配置了追加 code 也不生效。
	plain := newA6PlainAccount()
	plain.Credentials[poolModeAvailabilityErrorCodesCredentialKey] = []any{"merchant_down"}
	require.False(t, openAIPoolAvailabilityErrorForAccount(plain, []byte(`{"error":{"code":"merchant_down"}}`)))

	var nilAccount *Account
	require.Nil(t, nilAccount.GetPoolModeAvailabilityErrorCodes())
	require.Nil(t, (&Account{}).GetPoolModeAvailabilityErrorCodes())
	bad := &Account{Credentials: map[string]any{poolModeAvailabilityErrorCodesCredentialKey: "upstream_unavailable"}}
	require.Nil(t, bad.GetPoolModeAvailabilityErrorCodes())
}

func a6ChatPreOutputBody(errorFrame string) string {
	return strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_a6","object":"response","status":"in_progress","output":[]}}`, "",
		`data: {"type":"response.in_progress","response":{"id":"resp_a6","status":"in_progress"}}`, "",
		`data: ` + errorFrame, "",
	}, "\n")
}

func a6PostOutputBody(errorFrame string) string {
	return strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_a6","object":"response","status":"in_progress","output":[]}}`, "",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_a6","role":"assistant","status":"in_progress","content":[]}}`, "",
		`data: {"type":"response.content_part.added","output_index":0,"content_index":0,"item_id":"msg_a6","part":{"type":"output_text","text":""}}`, "",
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_a6","delta":"hello-a6"}`, "",
		`data: ` + errorFrame, "",
	}, "\n")
}

const a6BareErrorFrame = `{"error":{"code":"upstream_unavailable","message":"服务暂时不可用。 原因：上游服务、网络链路或代理返回异常响应。 解决方案：请稍后重试。如当前使用智能路由，请先重试；若仍失败，建议切换固定商家。"}}`

// chat 转换流：池账号在首输出前收到可用性错误帧（无 type 的 {"error":...} 帧）-> failover，客户端 0 字节。
func TestHandleChatStreamingResponsePoolAvailabilityErrorBeforeOutputFailsOver(t *testing.T) {
	c, rec := a6NewPathContext(t, "/v1/chat/completions", nil)
	resp := a6SSEResponse("text/event-stream", "rid_a6_chat", a6ChatPreOutputBody(a6BareErrorFrame))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	result, err := svc.handleChatStreamingResponse(resp, c, newA6PoolAccount(), "model", "model", "model", time.Now(), 0)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

// 非池账号同输入不 failover（原错误帧照旧写给客户端），确认新增分支只对池账号生效。
func TestHandleChatStreamingResponseNonPoolAvailabilityErrorUnchanged(t *testing.T) {
	c, _ := a6NewPathContext(t, "/v1/chat/completions", nil)
	resp := a6SSEResponse("text/event-stream", "rid_a6_chat_plain", a6ChatPreOutputBody(a6BareErrorFrame))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	_, err := svc.handleChatStreamingResponse(resp, c, newA6PlainAccount(), "model", "model", "model", time.Now(), 0)

	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
}

// raw chat 流：同上。
func TestForwardAsRawChatCompletionsPoolAvailabilityErrorBeforeOutputFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_a6_raw"}},
		Body:       io.NopCloser(strings.NewReader("data: " + a6BareErrorFrame + "\n\n")),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	account := rawChatCompletionsTestAccount()
	account.Credentials["pool_mode"] = true

	result, err := svc.forwardAsRawChatCompletions(context.Background(), c, account, body, "")

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

// 已提交输出之后命中可用性错误：写给客户端的流内 error 文案换成通用文案，不带上游原文 / 「商家」。
func TestHandleChatStreamingResponsePoolAvailabilityErrorAfterOutputUsesGenericMessage(t *testing.T) {
	c, rec := a6NewPathContext(t, "/v1/chat/completions", nil)
	resp := a6SSEResponse("text/event-stream", "rid_a6_chat_post", a6PostOutputBody(a6BareErrorFrame))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	_, err := svc.handleChatStreamingResponse(resp, c, newA6PoolAccount(), "model", "model", "model", time.Now(), 0)

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	out := rec.Body.String()
	require.Contains(t, out, "hello-a6")
	require.Contains(t, out, "Upstream service temporarily unavailable")
	require.NotContains(t, out, "商家")
	require.NotContains(t, out, "智能路由")
	require.NotContains(t, err.Error(), "商家")
}

func TestHandleAnthropicStreamingResponsePoolAvailabilityErrorAfterOutputUsesGenericMessage(t *testing.T) {
	c, rec := a6NewPathContext(t, "/v1/messages", nil)
	resp := a6SSEResponse("text/event-stream", "rid_a6_msg_post", a6PostOutputBody(`{"type":"error",`+strings.TrimPrefix(a6BareErrorFrame, "{")))
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}

	_, err := svc.handleAnthropicStreamingResponse(resp, c, newA6PoolAccount(), "model", "model", "model", time.Now())

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	out := rec.Body.String()
	require.Contains(t, out, "hello-a6")
	require.Contains(t, out, "Upstream service temporarily unavailable")
	require.NotContains(t, out, "商家")
	require.NotContains(t, out, "智能路由")
	require.NotContains(t, err.Error(), "商家")
}

// 超时类可用性错误同号重试最多 1 次：HTTP 504 / upstream_timeout 体 / 流内 upstream_timeout 帧；
// 非超时类不设额外上限。
func TestPoolAvailabilityTimeoutLimitsSameAccountRetry(t *testing.T) {
	pool := newA6PoolAccount()
	apply := func(status int, body string) *UpstreamFailoverError {
		return applyOpenAIPoolAvailabilityFailover(pool, &UpstreamFailoverError{StatusCode: status, ResponseBody: []byte(body)}, []byte(body))
	}
	require.Equal(t, 1, apply(http.StatusGatewayTimeout, a6UpstreamTimeoutBody).SameAccountRetryLimit)
	require.Equal(t, 1, apply(http.StatusGatewayTimeout, a6UpstreamUnavailableBody).SameAccountRetryLimit)
	require.Equal(t, 1, apply(http.StatusBadRequest, a6UpstreamTimeoutBody).SameAccountRetryLimit)
	require.Zero(t, apply(http.StatusBadRequest, a6UpstreamUnavailableBody).SameAccountRetryLimit)
	require.Zero(t, apply(http.StatusBadGateway, a6AllCandidatesFailedBody).SameAccountRetryLimit)

	timeoutFrame := []byte(a6FailedFrame("upstream_timeout", "等待响应超时。 请稍后重试。"))
	require.Equal(t, 1, openAIPoolAvailabilityStreamRetryLimit(pool, timeoutFrame))
	require.Zero(t, openAIPoolAvailabilityStreamRetryLimit(pool, []byte(a6StreamCommitFailureFrame)))
	require.Zero(t, openAIPoolAvailabilityStreamRetryLimit(newA6PlainAccount(), timeoutFrame))

	svc := &OpenAIGatewayService{}
	ferr := svc.newOpenAIStreamFailoverError(nil, pool, true, "rid", timeoutFrame, "等待响应超时", nil)
	require.True(t, ferr.RetryableOnSameAccount)
	require.Equal(t, 1, ferr.SameAccountRetryLimit)
	ferr = svc.newOpenAIStreamFailoverError(nil, pool, true, "rid", []byte(a6StreamCommitFailureFrame), "x", nil)
	require.Zero(t, ferr.SameAccountRetryLimit)
}

func a6NewPathContext(t *testing.T, path string, body io.Reader) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, body)
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

func a6SSEResponse(contentType, requestID, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}, "x-request-id": []string{requestID}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
