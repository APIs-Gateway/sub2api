package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
)

// testCNProviderAnthropicConnection 探测国产（CN）供应商账号在 api_protocol=anthropic
// 下的原生 Anthropic 兼容端点（/v1/messages）。
//
// 修复背景（issue #804）：在引入 IsCNProvider/GetAPIProtocol 之前，这类账号会落到
// 通用的 Claude 测试器（testClaudeAccountConnection），该测试器：
//  1. 无条件拼接 ?beta=true 查询参数——这是官方 Anthropic API 的特殊行为，第三方
//     Anthropic 兼容端点通常不需要，个别甚至会因未知参数报错；
//  2. base_url 未配置时默认回退到 https://api.anthropic.com——会把供应商的 API Key
//     发送给 Anthropic 官方服务而不是供应商自己的端点，必定 401 且存在跨供应商
//     凭证泄露风险。
//
// 本探测器要求管理员显式配置 base_url（不回退到官方端点），不拼接 beta 参数，并在
// base_url 形态明显是 OpenAI 兼容端点时给出可操作的报错提示，避免朴素拼接
// {base}/v1/messages 产生难以定位的 404。
func (s *AccountTestService) testCNProviderAnthropicConnection(c *gin.Context, account *Account, modelID string) error {
	ctx := c.Request.Context()

	testModelID := strings.TrimSpace(modelID)
	if testModelID == "" {
		testModelID = claude.DefaultTestModel
	}
	testModelID = account.GetMappedModel(testModelID)

	authToken := strings.TrimSpace(account.GetOpenAIApiKey())
	if authToken == "" {
		return s.sendErrorAndEnd(c, "No API key available")
	}

	rawBaseURL := account.GetAnthropicProtocolBaseURL()
	if rawBaseURL == "" {
		return s.sendErrorAndEnd(c, "api_protocol is anthropic but base_url is not configured")
	}
	baseURL, err := s.validateUpstreamBaseURL(rawBaseURL)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Invalid Anthropic base URL: %s", err.Error()))
	}
	if hint := cnAnthropicBaseURLMisconfigHint(baseURL); hint != "" {
		return s.sendErrorAndEnd(c, hint)
	}
	apiURL := strings.TrimRight(baseURL, "/") + "/v1/messages"

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	payload, err := createTestPayload(testModelID)
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create Anthropic test payload")
	}
	payloadBytes, _ := json.Marshal(payload)

	s.sendEvent(c, TestEvent{Type: "test_start", Model: testModelID})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create Anthropic test request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("anthropic-version", "2023-06-01")
	for key, value := range claude.DefaultHeaders {
		req.Header.Set(key, value)
	}
	setAnthropicAPIKeyAuthHeader(req.Header, account, authToken)
	account.ApplyHeaderOverrides(req.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Anthropic endpoint request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("Anthropic endpoint returned %d: %s", resp.StatusCode, string(body))
		if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && s.accountRepo != nil {
			_ = s.accountRepo.SetError(ctx, account.ID, errMsg)
		}
		return s.sendErrorAndEnd(c, errMsg)
	}

	return s.processClaudeStream(c, resp.Body)
}

// cnAnthropicBaseURLMisconfigHint 在 api_protocol=anthropic 的 base_url 形态明显是
// OpenAI 兼容端点时（paas 路径 / API 版本号后缀 / chat completions 或 responses
// 后缀），给出可操作的报错提示：朴素拼接 {base}/v1/messages 只会 404
// （例如 .../api/paas/v4/v1/messages），却看不出真正的配置错误在哪里。
func cnAnthropicBaseURLMisconfigHint(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	path := strings.ToLower(strings.TrimRight(parsed.Path, "/"))
	if path == "" {
		return ""
	}
	openAICompatShaped := strings.Contains(path, "/paas/") ||
		strings.HasSuffix(path, "/chat/completions") ||
		strings.HasSuffix(path, "/responses") ||
		openAIBaseURLHasVersionSuffix(path)
	if !openAICompatShaped {
		return ""
	}
	return fmt.Sprintf(
		"api_protocol is anthropic but base_url (%s) looks like an OpenAI-compatible endpoint; "+
			"requests would hit {base}/v1/messages and 404. Point base_url at the provider's native "+
			"Anthropic-compatible endpoint, or switch api_protocol to chat_completions.",
		baseURL,
	)
}
