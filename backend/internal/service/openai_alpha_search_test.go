package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type alphaSearchAccountStateRepo struct {
	AccountRepository
	setErrorCalls int
	lastError     string
}

func (r *alphaSearchAccountStateRepo) SetError(_ context.Context, _ int64, errorMsg string) error {
	r.setErrorCalls++
	r.lastError = errorMsg
	return nil
}

func alphaSearchResponsesSSE(output string) string {
	return "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":` + strconv.Quote(output) + `}` + "\n\n" +
		"event: response.output_text.annotation.added\n" +
		`data: {"type":"response.output_text.annotation.added","annotation":{"type":"url_citation","url":"https://example.com/news","title":"Example News"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":` + strconv.Quote(output) + `}]}]}}` + "\n\n"
}

func TestForwardAlphaSearchOAuthPreservesWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{
		"id":"search-session",
		"model":"gpt-5.6-sol",
		"reasoning":{"effort":"max","context":"all_turns"},
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"latest news"}]}],
		"commands":{"search_query":[{"q":"OpenAI news","recency":1}]},
		"settings":{"allowed_callers":["direct"],"external_web_access":true},
		"max_output_tokens":2000,
		"future_field":{"keep":true}
	}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search?feature=standalone", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", codexCLIUserAgent)
	c.Request.Header.Set("Originator", "codex_cli_rs")
	c.Request.Header.Set("Version", "0.144.1")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"encrypted_output":"ciphertext","output":"search result"}`)),
	}}
	service := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:          42,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-account",
		},
	}

	err := service.ForwardAlphaSearch(context.Background(), c, account, body)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"encrypted_output":"ciphertext","output":"search result"}`, recorder.Body.String())
	require.Equal(t, chatgptCodexAlphaSearchURL+"?feature=standalone", upstream.lastReq.URL.String())
	require.Equal(t, "chatgpt.com", upstream.lastReq.Host)
	require.Equal(t, "Bearer oauth-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "chatgpt-account", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "0.144.1", upstream.lastReq.Header.Get("Version"))
	require.Empty(t, upstream.lastReq.Header.Get("OpenAI-Beta"))
	require.JSONEq(t, string(body), string(upstream.lastBody))
}

func TestForwardAlphaSearchPATUsesResponsesWebSearchFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{
		"id":"search-session",
		"model":"gpt-5.6-sol",
		"commands":{"search_query":[{"q":"OpenAI news"}]},
		"prompt_cache_key":"responses-cache-key",
		"prompt_cache_retention":"24h"
	}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", codexCLIUserAgent)
	c.Request.Header.Set("Originator", "codex_cli_rs")
	c.Request.Header.Set("Version", "0.144.1")
	c.Request.Header.Set("OpenAI-Beta", "responses=experimental")
	c.Request.Header.Set("Accept-Language", "zh-CN")
	c.Request.Header.Set("Authorization", "Bearer client-token")
	c.Request.Header.Set("Session_ID", "session-client")
	c.Request.Header.Set("Conversation_ID", "conversation-client")
	c.Request.Header.Set("X-Codex-Beta-Features", "feature-a")
	c.Request.Header.Set("X-Codex-Turn-State", "turn-state")
	c.Request.Header.Set(responsesLiteHeaderKey, "true")
	c.Request.Header.Set("X-Codex-Turn-Metadata", `{"turn_id":"turn-1"}`)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"req-search"}},
		Body:       io.NopCloser(strings.NewReader(alphaSearchResponsesSSE("search result"))),
	}}
	service := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:          43,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":               "at-test-token",
			"auth_mode":                  OpenAIAuthModePersonalAccessToken,
			"chatgpt_account_id":         "chatgpt-account",
			"chatgpt_account_is_fedramp": true,
		},
	}

	err := service.ForwardAlphaSearch(context.Background(), c, account, body)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"output":"search result","results":[{"type":"text_result","ref_id":"turn0search0","url":"https://example.com/news","title":"Example News"}]}`, recorder.Body.String())
	require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
	require.Equal(t, "Bearer at-test-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "chatgpt-account", upstream.lastReq.Header.Get("ChatGPT-Account-ID"))
	require.Equal(t, "true", upstream.lastReq.Header.Get("X-OpenAI-Fedramp"))
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "responses=experimental", upstream.lastReq.Header.Get("OpenAI-Beta"))
	require.Equal(t, "0.144.1", upstream.lastReq.Header.Get("Version"))
	require.Equal(t, `{"turn_id":"turn-1"}`, upstream.lastReq.Header.Get("X-Codex-Turn-Metadata"))
	require.Equal(t, "codex_cli_rs", upstream.lastReq.Header.Get("Originator"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Codex-Beta-Features"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Codex-Turn-State"))
	require.Empty(t, upstream.lastReq.Header.Get(responsesLiteHeaderKey))
	require.Empty(t, upstream.lastReq.Header.Get("Accept-Language"))
	require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_retention").Exists())
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.Equal(t, "web_search", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Contains(t, gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String(), `"search_query"`)
}

func TestForwardAlphaSearchPATBackfillsMissingChatGPTAccountMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"OpenAI news"}]}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	var whoamiCalls int32
	whoamiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&whoamiCalls, 1)
		require.Equal(t, "Bearer at-test-token", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"email":"pat@example.com",
			"chatgpt_user_id":"user-123",
			"chatgpt_account_id":"acct-123",
			"chatgpt_plan_type":"plus",
			"chatgpt_account_is_fedramp":true
		}`))
	}))
	defer whoamiServer.Close()
	oldWhoamiURL := openAICodexPATWhoamiURL
	openAICodexPATWhoamiURL = whoamiServer.URL
	defer func() { openAICodexPATWhoamiURL = oldWhoamiURL }()

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(alphaSearchResponsesSSE("search result"))),
	}}
	oauthService := NewOpenAIOAuthService(nil, nil)
	service := &OpenAIGatewayService{
		cfg:                 &config.Config{},
		httpUpstream:        upstream,
		openAITokenProvider: NewOpenAITokenProvider(nil, nil, oauthService),
	}
	account := &Account{
		ID:          45,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "at-test-token",
			"auth_mode":    OpenAIAuthModePersonalAccessToken,
		},
	}

	err := service.ForwardAlphaSearch(context.Background(), c, account, body)

	require.NoError(t, err)
	require.Equal(t, int32(1), atomic.LoadInt32(&whoamiCalls))
	require.Equal(t, "acct-123", upstream.lastReq.Header.Get("ChatGPT-Account-ID"))
	require.Equal(t, "true", upstream.lastReq.Header.Get("X-OpenAI-Fedramp"))
	require.Equal(t, "acct-123", account.Credentials["chatgpt_account_id"])
	require.Equal(t, "user-123", account.Credentials["chatgpt_user_id"])
	require.Equal(t, OpenAIAuthModePersonalAccessToken, account.Credentials["auth_mode"])
}

func TestForwardAlphaSearchAPIKeyMapsModelAndPassesThroughError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"news"}]}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/alpha/search", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := `{"error":{"type":"invalid_request_error","message":"bad search"}}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	service := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:       7,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://compat.example/v4",
			"model_mapping": map[string]any{
				"gpt-5.6-sol": "upstream-5.6",
			},
		},
	}

	err := service.ForwardAlphaSearch(context.Background(), c, account, body)

	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.JSONEq(t, upstreamBody, recorder.Body.String())
	require.Equal(t, "https://compat.example/v4/alpha/search", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer sk-test", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "upstream-5.6", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "commands.search_query").IsArray())
}

func TestForwardAlphaSearchReturnsFailoverBeforeWriting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}}
	service := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:       8,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
	}

	err := service.ForwardAlphaSearch(context.Background(), c, account, body)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Equal(t, openAIPlatformAlphaSearchURL, upstream.lastReq.URL.String())
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())
}

func TestForwardAlphaSearchAPIKeyUnsupportedEndpointFailsOverWithoutAccountError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"news"}]}}`)

	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"endpoint unavailable"}}`)),
			}}
			repo := &alphaSearchAccountStateRepo{}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: repo}
			account := &Account{
				ID:       9,
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_key":  "sk-test",
					"base_url": "https://relay.example",
				},
			}

			err := svc.ForwardAlphaSearch(context.Background(), c, account, body)

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, status, failoverErr.StatusCode)
			require.Zero(t, repo.setErrorCalls)
			require.Empty(t, repo.lastError)
			require.False(t, c.Writer.Written())
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestForwardAlphaSearchOAuthNotFoundPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"news"}]}}`)
	upstreamBody := `{"detail":"Not Found"}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID:       10,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-account",
		},
	}

	err := svc.ForwardAlphaSearch(context.Background(), c, account, body)

	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.JSONEq(t, upstreamBody, recorder.Body.String())
}

func TestForwardAlphaSearchUnauthorizedDoesNotMarkAccountError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"news"}]}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"detail":"Unauthorized"}`)),
	}}
	repo := &alphaSearchAccountStateRepo{}
	cfg := &config.Config{}
	service := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     upstream,
		accountRepo:      repo,
		rateLimitService: NewRateLimitService(repo, nil, cfg, nil, nil),
	}
	account := &Account{
		ID:          44,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			// 刻意不设置 auth_mode：覆盖历史上把 at- token 当普通 OAuth 导入的账号。
			"access_token":       "at-test-token",
			"chatgpt_account_id": "chatgpt-account",
		},
	}

	err := service.ForwardAlphaSearch(context.Background(), c, account, body)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusUnauthorized, failoverErr.StatusCode)
	require.Zero(t, repo.setErrorCalls)
	require.Empty(t, repo.lastError)
	require.False(t, c.Writer.Written())
}

func TestForwardAlphaSearchPATResponsesFallbackUnauthorizedDoesNotMarkAccountError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"news"}]}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"detail":"Unauthorized"}`)),
	}}
	repo := &alphaSearchAccountStateRepo{}
	cfg := &config.Config{}
	service := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     upstream,
		accountRepo:      repo,
		rateLimitService: NewRateLimitService(repo, nil, cfg, nil, nil),
	}
	account := &Account{
		ID:          46,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "at-test-token",
			"auth_mode":          OpenAIAuthModePersonalAccessToken,
			"chatgpt_account_id": "chatgpt-account",
		},
	}

	err := service.ForwardAlphaSearch(context.Background(), c, account, body)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusUnauthorized, failoverErr.StatusCode)
	require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "responses=experimental", upstream.lastReq.Header.Get("OpenAI-Beta"))
	require.Zero(t, repo.setErrorCalls)
	require.Empty(t, repo.lastError)
	require.False(t, c.Writer.Written())
}

func TestIsOpenAIAlphaSearchEndpointUnsupported(t *testing.T) {
	apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	require.True(t, isOpenAIAlphaSearchEndpointUnsupported(apiKey, http.StatusNotFound))
	require.True(t, isOpenAIAlphaSearchEndpointUnsupported(apiKey, http.StatusMethodNotAllowed))
	require.False(t, isOpenAIAlphaSearchEndpointUnsupported(apiKey, http.StatusBadRequest))
	require.False(t, isOpenAIAlphaSearchEndpointUnsupported(oauth, http.StatusNotFound))
	require.False(t, isOpenAIAlphaSearchEndpointUnsupported(nil, http.StatusNotFound))
}

func TestForwardAlphaSearchEdgeCases(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("rejects_nil_inputs_and_invalid_model", func(t *testing.T) {
		var svc *OpenAIGatewayService
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", nil)

		require.Error(t, svc.ForwardAlphaSearch(context.Background(), c, &Account{}, []byte(`{"model":"gpt-5.6-sol"}`)))
		require.Error(t, (&OpenAIGatewayService{}).ForwardAlphaSearch(context.Background(), c, nil, []byte(`{"model":"gpt-5.6-sol"}`)))
		require.Error(t, (&OpenAIGatewayService{}).ForwardAlphaSearch(context.Background(), c, &Account{}, []byte(`{"model":123}`)))
	})

	t.Run("returns_token_and_transport_errors", func(t *testing.T) {
		newContext := func() *gin.Context {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", nil)
			return c
		}
		account := &Account{
			ID:       61,
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"access_token": "oauth-token",
			},
		}

		t.Run("transport_error_is_failover", func(t *testing.T) {
			svc := &OpenAIGatewayService{
				cfg:          &config.Config{},
				httpUpstream: &httpUpstreamRecorder{err: errors.New("connection refused")},
			}
			err := svc.ForwardAlphaSearch(context.Background(), newContext(), account, []byte(`{"model":"gpt-5.6-sol"}`))
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
		})

		t.Run("missing_token_is_plain_error", func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			missingToken := *account
			missingToken.Credentials = nil
			require.Error(t, svc.ForwardAlphaSearch(context.Background(), newContext(), &missingToken, []byte(`{"model":"gpt-5.6-sol"}`)))
		})

		t.Run("invalid_base_url_is_returned", func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			invalidBase := *account
			invalidBase.Type = AccountTypeAPIKey
			invalidBase.Credentials = map[string]any{
				"api_key":  "sk-test",
				"base_url": "http://[::1",
			}
			require.Error(t, svc.ForwardAlphaSearch(context.Background(), newContext(), &invalidBase, []byte(`{"model":"gpt-5.6-sol"}`)))
		})
	})

	t.Run("proxy_default_headers_and_content_type", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search?feature=standalone", nil)
		c.Request.Header.Set("OpenAI-Beta", "alpha-search=v1")
		upstream := &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}}
		svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
		proxyID := int64(91)
		account := &Account{
			ID:       62,
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			ProxyID:  &proxyID,
			Proxy:    &Proxy{Protocol: "http", Host: "proxy.example", Port: 8080},
			Credentials: map[string]any{
				"access_token": "oauth-token",
			},
		}

		err := svc.ForwardAlphaSearch(context.Background(), c, account, []byte(`{"model":"gpt-5.6-sol"}`))

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		require.Empty(t, upstream.lastReq.Header.Get("OpenAI-Beta"))
		require.Equal(t, codexCLIVersion, upstream.lastReq.Header.Get("Version"))
	})

	t.Run("upstream_body_read_error_is_returned", func(t *testing.T) {
		svc := &OpenAIGatewayService{
			cfg: &config.Config{},
			httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       errReadCloser{err: io.ErrUnexpectedEOF},
			}},
		}
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-test"}}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", nil)

		require.Error(t, svc.ForwardAlphaSearch(context.Background(), c, account, []byte(`{"model":"gpt-5.6-sol"}`)))
	})
}

func TestOpenAIAlphaSearchURLValidation(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{Security: config.SecurityConfig{
		URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true},
	}}}

	_, err := svc.openAIAlphaSearchURL(nil)
	require.EqualError(t, err, "account is required")

	_, err = svc.openAIAlphaSearchURL(&Account{Type: "unsupported"})
	require.EqualError(t, err, "unsupported OpenAI account type: unsupported")

	_, err = svc.openAIAlphaSearchURL(&Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "http://[::1"},
	})
	require.Error(t, err)

	value, err := svc.openAIAlphaSearchURL(&Account{Type: AccountTypeAPIKey})
	require.NoError(t, err)
	require.Equal(t, openAIPlatformAlphaSearchURL, value)
}

func TestBuildOpenAIAlphaSearchRequestRejectsUnsupportedAccountType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}

	_, err := svc.buildOpenAIAlphaSearchRequest(
		context.Background(),
		c,
		&Account{Platform: PlatformOpenAI, Type: "unsupported"},
		[]byte(`{"model":"gpt-5.6-sol"}`),
		"token",
	)

	require.EqualError(t, err, "unsupported OpenAI account type: unsupported")
}

func TestBuildOpenAIAlphaSearchResponsesWebSearchBodyCarriesSettings(t *testing.T) {
	body, err := buildOpenAIAlphaSearchResponsesWebSearchBody([]byte(`{
		"commands":{"search_query":[{"q":"OpenAI news"}]},
		"settings":{"search_context_size":"high","user_location":{"type":"approximate","country":"US"}},
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"latest"}]}]
	}`), "gpt-5.6-sol")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(body, "model").String())
	require.True(t, gjson.GetBytes(body, "stream").Bool())
	require.False(t, gjson.GetBytes(body, "store").Bool())
	require.Equal(t, "web_search", gjson.GetBytes(body, "tools.0.type").String())
	require.Equal(t, "high", gjson.GetBytes(body, "tools.0.search_context_size").String())
	require.Equal(t, "US", gjson.GetBytes(body, "tools.0.user_location.country").String())
	require.Contains(t, gjson.GetBytes(body, "input.0.content.0.text").String(), "latest")
}

func TestBuildOpenAIAlphaSearchResponsesWebSearchBodyRequiresModel(t *testing.T) {
	_, err := buildOpenAIAlphaSearchResponsesWebSearchBody([]byte(`{"commands":{}}`), " ")
	require.EqualError(t, err, "model is required")
}

func TestOpenAIAlphaSearchResponsesSSEUsesCompletedOutputWhenNoDelta(t *testing.T) {
	body := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"completed result"}]}]}}` + "\n\n"
	converted, err := openAIAlphaSearchResponseFromResponsesSSE([]byte(body))
	require.NoError(t, err)
	require.JSONEq(t, `{"output":"completed result"}`, string(converted))
}

func TestOpenAIAlphaSearchPromptIncludesRequestPartsAndTruncates(t *testing.T) {
	body := []byte(`{"commands":{"search_query":[{"q":"news"}]},"settings":{"search_context_size":"high"},"input":[{"role":"user"}]}`)
	prompt := openAIAlphaSearchResponsesWebSearchPrompt(body)
	require.Contains(t, prompt, "Commands JSON:")
	require.Contains(t, prompt, "Search settings JSON:")
	require.Contains(t, prompt, "Recent conversation/input JSON:")
	require.Equal(t, "short", truncateOpenAIAlphaSearchPromptJSON("short", 20))
	require.Equal(t, "0123\n...<truncated>", truncateOpenAIAlphaSearchPromptJSON("0123456789", 4))
}

func TestShouldApplyOpenAIAlphaSearchAccountErrorSideEffects(t *testing.T) {
	require.False(t, shouldApplyOpenAIAlphaSearchAccountErrorSideEffects(http.StatusUnauthorized))
	require.True(t, shouldApplyOpenAIAlphaSearchAccountErrorSideEffects(http.StatusForbidden))
	require.True(t, shouldApplyOpenAIAlphaSearchAccountErrorSideEffects(http.StatusTooManyRequests))
}
