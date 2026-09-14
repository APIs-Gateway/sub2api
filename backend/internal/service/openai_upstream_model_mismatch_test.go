//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestUpstreamModelMatches(t *testing.T) {
	cases := []struct {
		name, sent, got string
		want            bool
	}{
		{"exact", "gpt-5.6-sol", "gpt-5.6-sol", true},
		{"case-insensitive", "GPT-5.6-Sol", "gpt-5.6-sol ", true},
		{"empty got passes", "gpt-5.6-sol", "", true},
		{"mapped account 3233: sent luna got luna", "gpt-5.6-luna", "gpt-5.6-luna", true},
		{"date snapshot dashed", "gpt-5.6-sol", "gpt-5.6-sol-2026-09-01", true},
		{"date snapshot compact", "gpt-5.6-sol", "gpt-5.6-sol-20260901", true},
		{"latest alias", "gpt-5.6-sol-latest", "gpt-5.6-sol-2026-09-01", true},
		{"swapped family", "gpt-5.6-sol", "gpt-6-sol", false},
		{"swapped tier", "gpt-5.6-sol", "gpt-5.6-luna", false},
		{"non-date suffix", "gpt-5.6-sol", "gpt-5.6-sol-mini", false},
		{"empty sent never blocks", "", "gpt-6-sol", true},
		{"provider prefix on sent", "openai/gpt-5.6-sol", "gpt-5.6-sol", true},
		{"provider prefix on got", "gpt-5.6-sol", "openai/gpt-5.6-sol", true},
		{"provider prefix + date snapshot", "openai/gpt-5.6-sol", "gpt-5.6-sol-2026-09-01", true},
		{"codex alias reasoning suffix", "gpt-5.4-high", "gpt-5.4", true},
		{"codex alias bare version", "gpt-5.3", "gpt-5.3-codex", true},
		{"codex alias reverse: upstream echoes suffix", "gpt-5.4", "gpt-5.4-high", true},
		{"codex alias upgrade via table", "gpt-5.1", "gpt-5.4", true},
		{"provider prefix does not hide swapped family", "openai/gpt-5.6-sol", "gpt-6-sol", false},
		{"provider prefix does not hide non-date suffix", "gpt-5.6-sol", "anthropic/gpt-5.6-sol-mini", false},
		{"downgrade to mini is not an alias", "gpt-5.4", "gpt-5-mini", false},
		{"downgrade to bare family is not an alias", "gpt-5.4", "gpt-5", false},
		{"downgrade codex to older mini is not an alias", "gpt-5.3-codex", "gpt-5.1-codex-mini", false},
		{"unknown newer model downgraded", "gpt-5.8", "gpt-5.4", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, upstreamModelMatches(tc.sent, tc.got))
		})
	}
}

func TestExtractUpstreamResponseModel(t *testing.T) {
	require.Equal(t, "gpt-6-sol", extractUpstreamResponseModel([]byte(`{"type":"response.created","response":{"id":"r","model":"gpt-6-sol"}}`)))
	require.Equal(t, "gpt-6-sol", extractUpstreamResponseModel([]byte(`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-6-sol"}`)))
	require.Equal(t, "", extractUpstreamResponseModel([]byte(`{"type":"response.output_text.delta","delta":"x"}`)))
	require.Equal(t, "", extractUpstreamResponseModel(nil))
}

func TestMarkOpsUpstreamModelMismatch_FirstWins_ClearResets(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
	MarkOpsUpstreamModelMismatch(c, UpstreamModelMismatchMark{SentModel: "a", ResponseModel: "b", AccountID: 1})
	MarkOpsUpstreamModelMismatch(c, UpstreamModelMismatchMark{SentModel: "x", ResponseModel: "y", AccountID: 2})
	require.Equal(t, int64(1), GetOpsUpstreamModelMismatch(c).AccountID)
	ClearOpsUpstreamModelMismatch(c)
	require.Nil(t, GetOpsUpstreamModelMismatch(c))
}

func TestCheckUpstreamModelMismatch_DisabledStillMarks(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.cfg.Gateway.DisableUpstreamModelMismatchBlock = true
	err := svc.checkUpstreamModelMismatch(c, &Account{ID: 7, Platform: PlatformOpenAI}, "req", nil, "gpt-5.6-sol", "gpt-6-sol", true, true, OpenAIUsage{})
	require.Nil(t, err)
	require.NotNil(t, GetOpsUpstreamModelMismatch(c))
}

func TestCheckUpstreamModelMismatch_CannotBlockOnlyMarks(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	err := svc.checkUpstreamModelMismatch(c, &Account{ID: 7, Platform: PlatformOpenAI}, "req", nil, "gpt-5.6-sol", "gpt-6-sol", true, false, OpenAIUsage{})
	require.Nil(t, err)
	require.Equal(t, "gpt-6-sol", GetOpsUpstreamModelMismatch(c).ResponseModel)
}

func TestCheckUpstreamModelMismatch_EnabledReturnsFailover(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	err := svc.checkUpstreamModelMismatch(c, &Account{ID: 7, Platform: PlatformOpenAI}, "req", nil, "gpt-5.6-sol", "gpt-6-sol", true, true, OpenAIUsage{})
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadGateway, err.StatusCode)
	require.Contains(t, string(err.ResponseBody), "upstream_model_mismatch")
	mark := GetOpsUpstreamModelMismatch(c)
	require.Equal(t, "gpt-5.6-sol", mark.SentModel)
	require.Equal(t, "gpt-6-sol", mark.ResponseModel)
}

// 对外笼统、对内详尽：ResponseBody 的 message 是固定笼统文案，不带 "upstream" / sent / got / 模型名；
// ops 事件与 ops upstream error 里 sent=… got=…、账号、request id 一个不少。
func TestCheckUpstreamModelMismatch_ClientMessageGenericInternalDetailed(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	h := http.Header{}
	h.Set("Server", "nginx")
	h.Set("X-Oneapi-Request-Id", "one-9")
	err := svc.checkUpstreamModelMismatch(c, &Account{ID: 7, Name: "a6-key-1", Platform: PlatformOpenAI}, "one-9", h, "gpt-6-astra", "gpt-5.6-terra", true, true, OpenAIUsage{})
	require.NotNil(t, err)

	body := string(err.ResponseBody)
	require.Equal(t, UpstreamModelMismatchClientMessage, gjson.Get(body, "error.message").String())
	require.Equal(t, "upstream_error", gjson.Get(body, "error.type").String(), "type/code 保留供内部识别")
	require.Equal(t, "upstream_model_mismatch", gjson.Get(body, "error.code").String())
	msg := gjson.Get(body, "error.message").String()
	require.NotContains(t, strings.ToLower(msg), "upstream")
	require.NotContains(t, strings.ToLower(msg), "model")
	require.NotContains(t, body, "gpt-6-astra")
	require.NotContains(t, body, "gpt-5.6-terra")
	require.NotContains(t, body, "sent=")
	require.NotContains(t, body, "got=")
	require.NotContains(t, body, "a6-key-1")

	events, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	evList, ok := events.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	ev := evList[0]
	require.Contains(t, ev.Message, "sent=gpt-6-astra")
	require.Contains(t, ev.Message, "got=gpt-5.6-terra")
	require.Equal(t, int64(7), ev.AccountID)
	require.Equal(t, "a6-key-1", ev.AccountName)
	require.Equal(t, "one-9", ev.UpstreamRequestID)
	require.Equal(t, "nginx", ev.UpstreamHeaders["server"])
	require.Equal(t, "one-9", ev.UpstreamHeaders["x-oneapi-request-id"])
	require.Equal(t, "failover", ev.Kind)
	require.Equal(t, http.StatusBadGateway, ev.UpstreamStatusCode)

	opsMsg, _ := c.Get(OpsUpstreamErrorMessageKey)
	require.Equal(t, "upstream returned a different model than requested: sent=gpt-6-astra got=gpt-5.6-terra", opsMsg)
	opsStatus, _ := c.Get(OpsUpstreamStatusCodeKey)
	require.Equal(t, http.StatusBadGateway, opsStatus)
}

// Blocked 只在 checkUpstreamModelMismatch 真正返回 failover（本次尝试被拦截）时为 true；
// 开关关闭 / canBlock=false 仍打标但 Blocked=false，handler 据此决定是否落审计行。
func TestCheckUpstreamModelMismatch_SetsBlockedOnlyWhenReturningFailover(t *testing.T) {
	cases := []struct {
		name        string
		disable     bool
		canBlock    bool
		wantErr     bool
		wantBlocked bool
	}{
		{"blocked", false, true, true, true},
		{"switch disabled", true, true, false, false},
		{"cannot block", false, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			svc.cfg.Gateway.DisableUpstreamModelMismatchBlock = tc.disable
			err := svc.checkUpstreamModelMismatch(c, &Account{ID: 7, Platform: PlatformOpenAI}, "req", nil, "gpt-5.6-sol", "gpt-6-sol", true, tc.canBlock, OpenAIUsage{})
			require.Equal(t, tc.wantErr, err != nil)
			mark := GetOpsUpstreamModelMismatch(c)
			require.NotNil(t, mark)
			require.Equal(t, tc.wantBlocked, mark.Blocked)
		})
	}
}

// grok 系列只记录不拦截：xAI 用带日期的模型名，豁免规则覆盖不了，真实回显未验证，先观察。
func TestCheckUpstreamModelMismatch_GrokObserveOnly(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	err := svc.checkUpstreamModelMismatch(c, &Account{ID: 7, Platform: PlatformGrok}, "req", nil, "grok-4.3", "grok-4.3-0709", true, true, OpenAIUsage{})
	require.Nil(t, err, "grok 不一致只打标不 failover")
	mark := GetOpsUpstreamModelMismatch(c)
	require.NotNil(t, mark)
	require.False(t, mark.Blocked)
	require.Equal(t, "grok-4.3", mark.SentModel)
	require.Equal(t, "grok-4.3-0709", mark.ResponseModel)
}

// 池模式账号（中转自身是一池多 key）：单个内部节点偷换模型不应立刻把整个凭证换掉并降权，
// 先走 handler 现有的同账号重试（pool_mode_retry_count 次）；非池模式 / 无账号保持直接切号。
func TestCheckUpstreamModelMismatch_PoolModeRetryableOnSameAccount(t *testing.T) {
	cases := []struct {
		name    string
		account *Account
		want    bool
	}{
		{"pool mode", &Account{ID: 7, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"pool_mode": true}}, true},
		{"pool mode off", &Account{ID: 7, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"pool_mode": false}}, false},
		{"api key without pool flag", &Account{ID: 7, Type: AccountTypeAPIKey, Platform: PlatformOpenAI}, false},
		{"oauth", &Account{ID: 7, Type: AccountTypeOAuth, Platform: PlatformOpenAI}, false},
		{"nil account", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			err := svc.checkUpstreamModelMismatch(c, tc.account, "req", nil, "gpt-5.6-sol", "gpt-6-sol", true, true, OpenAIUsage{})
			require.NotNil(t, err, "不一致仍必须拦截并返回 failover")
			require.Equal(t, http.StatusBadGateway, err.StatusCode)
			require.Equal(t, tc.want, err.RetryableOnSameAccount)
			require.False(t, err.RequestScopedTransient)
			mark := GetOpsUpstreamModelMismatch(c)
			require.NotNil(t, mark)
			require.True(t, mark.Blocked)
		})
	}
}

// 上游模型不一致事件记录上游响应头指纹（白名单），不带 set-cookie / authorization 等敏感头。
func TestCheckUpstreamModelMismatch_RecordsUpstreamHeaderFingerprint(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	h := http.Header{}
	h.Set("Server", "nginx/1.25")
	h.Set("X-New-Api-Version", "v0.9.1")
	h.Set("X-Oneapi-Request-Id", "  oneapi-abc  ")
	h.Set("Set-Cookie", "session=secret")
	h.Set("Authorization", "Bearer secret")
	err := svc.checkUpstreamModelMismatch(c, &Account{ID: 7, Platform: PlatformOpenAI}, "oneapi-abc", h, "gpt-6-astra", "gpt-5.6-terra", true, true, OpenAIUsage{})
	require.NotNil(t, err)

	events, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	list, ok := events.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, list, 1)
	ev := list[0]
	require.Equal(t, int64(7), ev.AccountID)
	require.Equal(t, "oneapi-abc", ev.UpstreamRequestID)
	require.Equal(t, map[string]string{
		"server":              "nginx/1.25",
		"x-new-api-version":   "v0.9.1",
		"x-oneapi-request-id": "oneapi-abc",
	}, ev.UpstreamHeaders)
	require.NotContains(t, ev.UpstreamHeaders, "set-cookie")
	require.NotContains(t, ev.UpstreamHeaders, "authorization")

	// WS 路径传 nil：事件不带 upstream_headers。
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.NotNil(t, svc.checkUpstreamModelMismatch(c2, &Account{ID: 7, Platform: PlatformOpenAI}, "", nil, "gpt-6-astra", "gpt-5.6-terra", true, true, OpenAIUsage{}))
	events2, _ := c2.Get(OpsUpstreamErrorsKey)
	list2, ok := events2.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Nil(t, list2[0].UpstreamHeaders)
}

// failover 耗尽后 ResolveUpstreamErrorResponse 不得用对外笼统文案覆盖 ops 顶层内部消息（含 sent/got）。
func TestResolveUpstreamErrorResponse_KeepsOpsMessageForUpstreamModelMismatchBody(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	ferr := svc.checkUpstreamModelMismatch(c, &Account{ID: 7, Platform: PlatformOpenAI}, "req", nil, "gpt-6-astra", "gpt-5.6-terra", true, true, OpenAIUsage{})
	require.NotNil(t, ferr)
	require.True(t, IsUpstreamModelMismatchErrorBody(ferr.ResponseBody))
	require.False(t, IsUpstreamModelMismatchErrorBody([]byte(`{"error":{"code":"other"}}`)))
	require.False(t, IsUpstreamModelMismatchErrorBody(nil))
	require.False(t, IsUpstreamModelMismatchErrorBody([]byte(`not json`)))

	status, errType, msg := ResolveUpstreamErrorResponse(c, PlatformOpenAI, ferr.StatusCode, ferr.ResponseBody)
	require.Equal(t, http.StatusBadGateway, status)
	require.Equal(t, "upstream_error", errType)
	require.Equal(t, "Upstream service temporarily unavailable", msg)
	opsMsg, _ := c.Get(OpsUpstreamErrorMessageKey)
	require.Equal(t, "upstream returned a different model than requested: sent=gpt-6-astra got=gpt-5.6-terra", opsMsg, "内部消息不能被笼统文案覆盖")
	opsStatus, _ := c.Get(OpsUpstreamStatusCodeKey)
	require.Equal(t, http.StatusBadGateway, opsStatus)
}
