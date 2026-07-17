package service

import (
	"bytes"
	"context"
	"errors"
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
		require.Equal(t, "alpha-search=v1", upstream.lastReq.Header.Get("OpenAI-Beta"))
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
