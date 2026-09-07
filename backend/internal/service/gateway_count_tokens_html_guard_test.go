//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestIsHTMLUpstreamCountTokensResponse 覆盖 isHTMLUpstreamCountTokensResponse 自身新增
// 的判断面：Content-Type 信号（含大小写、charset 参数、与响应体判断的 OR 关系），以及
// 委托给既有 isHTMLResponse 的 body-前缀判断。isHTMLResponse 本身的详尽用例已在
// ratelimit_service_403_html_test.go 的 TestIsHTMLResponse 覆盖，这里不重复。
func TestIsHTMLUpstreamCountTokensResponse(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
		body    string
		want    bool
	}{
		{
			name:    "content_type_text_html_with_charset_empty_body",
			headers: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			body:    "",
			want:    true,
		},
		{
			name:    "content_type_text_html_case_insensitive",
			headers: http.Header{"Content-Type": []string{"TEXT/HTML"}},
			body:    "",
			want:    true,
		},
		{
			name:    "content_type_missing_falls_back_to_bare_html_body",
			headers: http.Header{},
			body:    "<html><body>Forbidden</body></html>",
			want:    true,
		},
		{
			name:    "content_type_missing_falls_back_to_doctype_body",
			headers: nil,
			body:    "\n\t <!DOCTYPE HTML><html></html>",
			want:    true,
		},
		{
			name:    "content_type_json_with_json_body_is_not_html",
			headers: http.Header{"Content-Type": []string{"application/json"}},
			body:    `{"error":{"message":"forbidden"}}`,
			want:    false,
		},
		{
			name:    "content_type_missing_json_body_is_not_html",
			headers: http.Header{},
			body:    `{"error":{"message":"forbidden"}}`,
			want:    false,
		},
		{
			name:    "content_type_text_html_wins_even_over_json_looking_body",
			headers: http.Header{"Content-Type": []string{"text/html"}},
			body:    `{"error":{"message":"forbidden"}}`,
			want:    true,
		},
		{
			name:    "content_type_xml_with_xml_body_is_not_html",
			headers: http.Header{"Content-Type": []string{"application/xml"}},
			body:    `<?xml version="1.0"?><error/>`,
			want:    false,
		},
		{
			name:    "nil_headers_and_empty_body",
			headers: nil,
			body:    "",
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isHTMLUpstreamCountTokensResponse(tc.headers, []byte(tc.body)))
		})
	}
}

// newCountTokensHTMLGuardRateLimitService 构建一个真实的 RateLimitService（配合
// rateLimitAccountRepoStub），用于观察 HandleUpstreamError 是否被调用（即账号是否被
// 冷却/处罚），而不是像多数 count_tokens 透传测试那样直接传 nil 或空结构体。
func newCountTokensHTMLGuardRateLimitService(t *testing.T, cfg *config.Config) (*RateLimitService, *rateLimitAccountRepoStub) {
	t.Helper()
	repo := &rateLimitAccountRepoStub{}
	rls := NewRateLimitService(repo, nil, cfg, nil, nil)
	return rls, repo
}

// TestForwardCountTokens_HTMLGuardSkipsAccountCooldown 覆盖主（非透传）count_tokens
// 转发路径：ForwardCountTokens 内 buildCountTokensRequest → claudeAPICountTokensURL 分支。
func TestForwardCountTokens_HTMLGuardSkipsAccountCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	body := []byte(`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hi"}]}`)

	// 非透传的 Anthropic API Key 账号：不含 anthropic_passthrough extra，
	// 走 ForwardCountTokens 主分支（而非 forwardCountTokensAnthropicAPIKeyPassthrough）。
	newAccount := func() *Account {
		return &Account{
			ID:          810,
			Name:        "gwct-html-guard-main",
			Platform:    PlatformAnthropic,
			Type:        AccountTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-ant-test"},
			Status:      StatusActive,
			Schedulable: true,
		}
	}

	newContext := func() (*gin.Context, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
		return c, rec
	}

	t.Run("html_403_skips_cooldown", func(t *testing.T) {
		rls, repo := newCountTokensHTMLGuardRateLimitService(t, cfg)
		upstream := &anthropicHTTPUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
				Body:       io.NopCloser(strings.NewReader("<!DOCTYPE html><html><body>403 Forbidden</body></html>")),
			},
		}
		svc := &GatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: rls}
		c, rec := newContext()
		parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-haiku-4-5"}

		err := svc.ForwardCountTokens(context.Background(), c, newAccount(), parsed)

		require.Error(t, err, "上游错误仍应作为 error 返回给 handler 层记录日志")
		require.Equal(t, http.StatusForbidden, rec.Code, "调用方仍应原样看到 403")
		require.Contains(t, rec.Body.String(), "upstream_error")
		require.Equal(t, 0, repo.setErrorCalls, "HTML 403 不得永久禁用账号")
		require.Equal(t, 0, repo.tempCalls, "HTML 403 不得把账号设为临时不可调度")
	})

	// 边界情况：Content-Type 缺失，仅靠响应体前缀（无 doctype，裸 <html>）识别。
	t.Run("missing_content_type_bare_html_body_skips_cooldown", func(t *testing.T) {
		rls, repo := newCountTokensHTMLGuardRateLimitService(t, cfg)
		upstream := &anthropicHTTPUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("<html><body>Forbidden</body></html>")),
			},
		}
		svc := &GatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: rls}
		c, rec := newContext()
		parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-haiku-4-5"}

		err := svc.ForwardCountTokens(context.Background(), c, newAccount(), parsed)

		require.Error(t, err)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, 0, repo.setErrorCalls)
		require.Equal(t, 0, repo.tempCalls)
	})

	// 回归保护：结构化 JSON 403 仍必须正常冷却账号，不能被这次改动连带放行。
	t.Run("json_403_still_cools_down", func(t *testing.T) {
		rls, repo := newCountTokensHTMLGuardRateLimitService(t, cfg)
		upstream := &anthropicHTTPUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"permission_error","message":"account suspended"}}`)),
			},
		}
		svc := &GatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: rls}
		c, rec := newContext()
		parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-haiku-4-5"}

		err := svc.ForwardCountTokens(context.Background(), c, newAccount(), parsed)

		require.Error(t, err)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, 1, repo.setErrorCalls, "结构化 JSON 403 必须仍然处罚账号")
		require.Contains(t, repo.lastErrorMsg, "account suspended")
	})
}

// TestForwardCountTokensAnthropicAPIKeyPassthrough_HTMLGuardSkipsAccountCooldown 覆盖
// Anthropic API Key 透传路径：forwardCountTokensAnthropicAPIKeyPassthrough。
func TestForwardCountTokensAnthropicAPIKeyPassthrough_HTMLGuardSkipsAccountCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	body := []byte(`{"model":"claude-sonnet-4-5-20250929","messages":[{"role":"user","content":"hi"}]}`)

	newAccount := func() *Account {
		return &Account{
			ID:          811,
			Name:        "gwct-html-guard-passthrough",
			Platform:    PlatformAnthropic,
			Type:        AccountTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "sk-proxy",
				"base_url": "https://proxy.example.com",
			},
			Extra:       map[string]any{"anthropic_passthrough": true},
			Status:      StatusActive,
			Schedulable: true,
		}
	}

	newContext := func() (*gin.Context, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
		return c, rec
	}

	t.Run("html_403_skips_cooldown", func(t *testing.T) {
		rls, repo := newCountTokensHTMLGuardRateLimitService(t, cfg)
		upstream := &anthropicHTTPUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
				Body:       io.NopCloser(strings.NewReader("<!DOCTYPE html><html><body>403 Forbidden</body></html>")),
			},
		}
		svc := &GatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: rls}
		c, rec := newContext()
		parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-5-20250929"}

		err := svc.ForwardCountTokens(context.Background(), c, newAccount(), parsed)

		require.Error(t, err)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, 0, repo.setErrorCalls, "HTML 403 不得永久禁用账号")
		require.Equal(t, 0, repo.tempCalls, "HTML 403 不得把账号设为临时不可调度")
	})

	// 边界情况：Content-Type 是 text/html，但响应体为空（反代直接掐断连接/空白挑战页）。
	t.Run("content_type_text_html_empty_body_skips_cooldown", func(t *testing.T) {
		rls, repo := newCountTokensHTMLGuardRateLimitService(t, cfg)
		upstream := &anthropicHTTPUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     http.Header{"Content-Type": []string{"text/html"}},
				Body:       io.NopCloser(strings.NewReader("")),
			},
		}
		svc := &GatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: rls}
		c, rec := newContext()
		parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-5-20250929"}

		err := svc.ForwardCountTokens(context.Background(), c, newAccount(), parsed)

		require.Error(t, err)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, 0, repo.setErrorCalls)
		require.Equal(t, 0, repo.tempCalls)
	})

	// 回归保护：结构化 JSON 403 仍必须正常冷却账号。
	t.Run("json_403_still_cools_down", func(t *testing.T) {
		rls, repo := newCountTokensHTMLGuardRateLimitService(t, cfg)
		upstream := &anthropicHTTPUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"permission_error","message":"account suspended"}}`)),
			},
		}
		svc := &GatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: rls}
		c, rec := newContext()
		parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-5-20250929"}

		err := svc.ForwardCountTokens(context.Background(), c, newAccount(), parsed)

		require.Error(t, err)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, 1, repo.setErrorCalls, "结构化 JSON 403 必须仍然处罚账号")
		require.Contains(t, repo.lastErrorMsg, "account suspended")
	})
}
