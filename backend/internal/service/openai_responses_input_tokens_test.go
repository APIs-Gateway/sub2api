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

type inputTokensUpstream struct {
	HTTPUpstream
	status      int
	body        string
	err         error
	calls       int
	req         *http.Request
	wire        []byte
	closed      bool
	missingBody bool
	readErr     bool
	afterCall   func()
}

type inputTokensBrokenReader struct{}

func (inputTokensBrokenReader) Read([]byte) (int, error) {
	return 0, errors.New("private read failure")
}

type inputTokensResponseBody struct {
	io.Reader
	closed *bool
}

func (b inputTokensResponseBody) Close() error { *b.closed = true; return nil }
func (u *inputTokensUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	u.req = req
	u.wire, _ = io.ReadAll(req.Body)
	if u.afterCall != nil {
		u.afterCall()
	}
	if u.err != nil {
		return nil, u.err
	}
	if u.missingBody {
		return &http.Response{StatusCode: u.status}, nil
	}
	if u.readErr {
		return &http.Response{StatusCode: u.status, Body: inputTokensResponseBody{Reader: inputTokensBrokenReader{}, closed: &u.closed}}, nil
	}
	return &http.Response{StatusCode: u.status, Header: http.Header{"X-Request-Id": {"count-1"}}, Body: inputTokensResponseBody{Reader: strings.NewReader(u.body), closed: &u.closed}}, nil
}

func inputTokensFixture(t *testing.T, account *Account, upstream *inputTokensUpstream, body string, cfg *config.Config) (*httptest.ResponseRecorder, error) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(body))
	c.Request.Header.Set("Authorization", "Bearer downstream-key")
	c.Request.Header.Set("User-Agent", "test-client")
	s := &OpenAIGatewayService{httpUpstream: upstream, cfg: cfg}
	return rec, s.ForwardResponsesInputTokens(c.Request.Context(), c, account, []byte(body))
}

func TestResponsesInputTokensNativeWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(accountType, func(t *testing.T) {
			upstream := &inputTokensUpstream{status: 200, body: `{"object":"response.input_tokens","input_tokens":42}`}
			account := &Account{ID: 42, Platform: PlatformOpenAI, Type: accountType, Credentials: map[string]any{"api_key": "upstream-key", "access_token": "upstream-key", "model_mapping": map[string]any{"alias": "gpt-4o"}}}
			body := `{"model":"alias","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.invalid/img"}]}],"conversation":"conv-1","tools":[{"type":"function","name":"f","parameters":{"x":"y"}}],"future":{"keep":true}}`
			rec, err := inputTokensFixture(t, account, upstream, body, &config.Config{})
			require.NoError(t, err)
			require.Equal(t, 200, rec.Code)
			require.JSONEq(t, upstream.body, rec.Body.String())
			require.Equal(t, "https://api.openai.com/v1/responses/input_tokens", upstream.req.URL.String())
			require.Equal(t, http.MethodPost, upstream.req.Method)
			require.Equal(t, "Bearer upstream-key", upstream.req.Header.Get("Authorization"))
			require.Equal(t, "test-client", upstream.req.Header.Get("User-Agent"))
			require.Equal(t, "gpt-4o", gjson.GetBytes(upstream.wire, "model").String())
			require.JSONEq(t, string(ReplaceModelInBody([]byte(body), "gpt-4o")), string(upstream.wire))
			require.Equal(t, "count-1", rec.Header().Get("X-Request-Id"))
			require.Empty(t, rec.Header().Get("X-Sub2api-Token-Count"))
			require.True(t, upstream.closed)
		})
	}
}

func TestResponsesInputTokensFallbackAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name, platform, accountType, base string
		upstreamStatus                    int
		response                          string
		wantStatus, wantCalls             int
		estimated                         bool
	}{
		{"relay", PlatformOpenAI, AccountTypeAPIKey, "https://relay.invalid/v1", 0, "", 200, 0, true},
		{"grok", PlatformGrok, AccountTypeAPIKey, "", 0, "", 200, 0, true},
		{"official-custom-path", PlatformOpenAI, AccountTypeAPIKey, "https://api.openai.com/foo", 0, "", 200, 0, true},
		{"404", PlatformOpenAI, AccountTypeAPIKey, "", 404, `{"error":{"message":"not found"}}`, 200, 1, true},
		{"oauth-scope", PlatformOpenAI, AccountTypeOAuth, "", 403, `{"error":{"code":"missing_scope"}}`, 200, 1, true},
		{"ordinary-403", PlatformOpenAI, AccountTypeOAuth, "", 403, `{"error":{"message":"permission denied"}}`, 403, 1, false},
		{"html-403", PlatformOpenAI, AccountTypeOAuth, "", 403, `<html>blocked</html>`, 403, 1, false},
		{"invalid-key", PlatformOpenAI, AccountTypeAPIKey, "", 401, `{"error":{"code":"invalid_api_key"}}`, 401, 1, false},
		{"limited", PlatformOpenAI, AccountTypeAPIKey, "", 429, `{"error":{"message":"rate limit"}}`, 429, 1, false},
		{"server", PlatformOpenAI, AccountTypeAPIKey, "", 503, `{"error":{"message":"internal"}}`, 503, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &inputTokensUpstream{status: tc.upstreamStatus, body: tc.response}
			account := &Account{Platform: tc.platform, Type: tc.accountType, Credentials: map[string]any{"base_url": tc.base, "api_key": "key", "access_token": "key"}}
			rec, err := inputTokensFixture(t, account, upstream, `{"model":"gpt-4o","instructions":"系统规则","input":"Hello 世界","tools":[{"type":"function","name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`, &config.Config{})
			if tc.wantStatus == 200 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, tc.wantStatus, rec.Code)
			require.Equal(t, tc.wantCalls, upstream.calls)
			if tc.estimated {
				require.Equal(t, "estimated", rec.Header().Get("X-Sub2api-Token-Count"))
				require.Positive(t, gjson.GetBytes(rec.Body.Bytes(), "input_tokens").Int())
			}
			if upstream.calls > 0 {
				require.True(t, upstream.closed)
			}
		})
	}
}

func TestResponsesInputTokensRejectsInvalidSuccess(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `[]`, `{"Object":"response.input_tokens","input_tokens":3}`, `{"object":"response.input_tokens","Input_Tokens":3}`, `{"object":"response.input_tokens","input_tokens":2,"input_tokens":3}`, `{"object":"response.input_tokens","input_tokens":null}`, `{"object":"response.input_tokens","input_tokens":"3"}`, `{"object":"response.input_tokens","input_tokens":3.5}`, `{"object":"response.input_tokens","input_tokens":-1}`, `{"object":"response","input_tokens":3}`, `{"object":"response.input_tokens","input_tokens":3}garbage`} {
		t.Run(body, func(t *testing.T) {
			upstream := &inputTokensUpstream{status: 200, body: body}
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key"}}
			rec, err := inputTokensFixture(t, account, upstream, `{"model":"gpt-4o","input":"hi"}`, &config.Config{})
			require.Error(t, err)
			require.Equal(t, 502, rec.Code)
			require.True(t, upstream.closed)
		})
	}
	upstream := &inputTokensUpstream{status: 200, body: `{"object":"response.input_tokens","input_tokens":0}`}
	rec, err := inputTokensFixture(t, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key"}}, upstream, `{"model":"gpt-4o"}`, &config.Config{})
	require.NoError(t, err)
	require.Equal(t, int64(0), gjson.GetBytes(rec.Body.Bytes(), "input_tokens").Int())
}

func TestResponsesInputTokensBoundsAndUnresolvedState(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key"}}
	upstream := &inputTokensUpstream{status: 200, body: strings.Repeat("x", 64)}
	cfg := &config.Config{}
	cfg.Gateway.UpstreamResponseReadMaxBytes = 32
	rec, err := inputTokensFixture(t, account, upstream, `{"model":"gpt-4o"}`, cfg)
	require.ErrorIs(t, err, ErrUpstreamResponseBodyTooLarge)
	require.Equal(t, 502, rec.Code)
	require.True(t, upstream.closed)
	account.Credentials["base_url"] = "https://relay.invalid"
	for _, suffix := range []string{`,"previous_response_id":"resp-1"`, `,"conversation":"conv-1"`, `,"input":[{"role":"user","content":[{"type":"input_image","image_url":"http://127.0.0.1/secret"}]}]`, `,"input":[{"type":"item_reference","id":"stored"}]`, `,"input":123`} {
		upstream := &inputTokensUpstream{}
		rec, err := inputTokensFixture(t, account, upstream, `{"model":"gpt-4o"`+suffix+`}`, &config.Config{})
		require.Error(t, err)
		require.Equal(t, 400, rec.Code)
		require.Zero(t, upstream.calls)
		require.Empty(t, rec.Header().Get("X-Sub2api-Token-Count"))
	}
}

func TestResponsesInputTokensNoCallAfterCancellation(t *testing.T) {
	upstream := &inputTokensUpstream{}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", bytes.NewReader(nil)).WithContext(ctx)
	s := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	err := s.ForwardResponsesInputTokens(ctx, c, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, []byte(`{"model":"gpt-4o","input":"hi"}`))
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, upstream.calls)
	require.Empty(t, rec.Body.String())
}

func TestResponsesInputTokensSelfContainedFormats(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.invalid"}}
	for _, body := range []string{
		`{"model":"gpt-4o","input":"Hello world"}`,
		`{"model":"gpt-4-turbo","instructions":"规则","input":[{"role":"user","content":"你好 world"}]}`,
		`{"model":"gpt-3.5-turbo","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"},{"type":"text","text":" world"}]}]}`,
		`{"model":"gpt-4o","input":[{"type":"function_call","name":"f","arguments":"{\"city\":\"London\"}","call_id":"call_1"},{"type":"function_call_output","call_id":"call_1","output":"rainy"}]}`,
		`{"model":"gpt-4o","input":[{"type":"message","role":"assistant","id":"msg_1","content":[{"type":"output_text","text":"done"}]}],"tool_choice":"auto","text":{"format":{"type":"json_schema","name":"result","schema":{"type":"object"}}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			upstream := &inputTokensUpstream{}
			rec, err := inputTokensFixture(t, account, upstream, body, &config.Config{})
			require.NoError(t, err)
			require.Equal(t, 200, rec.Code)
			require.Zero(t, upstream.calls)
			require.Equal(t, "estimated", rec.Header().Get("X-Sub2api-Token-Count"))
			count := gjson.GetBytes(rec.Body.Bytes(), "input_tokens").Int()
			require.Positive(t, count)
			if body == `{"model":"gpt-4o","input":"Hello world"}` {
				require.Equal(t, int64(2), count)
			}
		})
	}
	plain, _ := inputTokensFixture(t, account, &inputTokensUpstream{}, `{"model":"gpt-4o","input":"hello"}`, &config.Config{})
	withTool, _ := inputTokensFixture(t, account, &inputTokensUpstream{}, `{"model":"gpt-4o","input":"hello","tools":[{"type":"function","name":"f","future_schema":{"description":"field that typed tool conversion must not lose"}}]}`, &config.Config{})
	require.Greater(t, gjson.GetBytes(withTool.Body.Bytes(), "input_tokens").Int(), gjson.GetBytes(plain.Body.Bytes(), "input_tokens").Int())
}

func TestResponsesInputTokensInvalidRequestsNeverReachUpstream(t *testing.T) {
	for _, body := range []string{`[]`, `null`, `{}`, `{"model":3}`, `{"model":" "}`, `{"model":"gpt-4o","model":"different"}`, `{"model":"gpt-4o","Model":"restricted-model"}`, `{"model":"gpt-4o","MODEL":"restricted-model"}`, `{"model":"gpt-4o","Input":"unaudited input"}`, `{"model":"gpt-4o","input":"a","input":"b"}`, `{"model":"gpt-4o"} trailing`, `{"model":"gpt-4o","instructions":5}`} {
		t.Run(body, func(t *testing.T) {
			upstream := &inputTokensUpstream{}
			rec, err := inputTokensFixture(t, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key"}}, upstream, body, &config.Config{})
			require.Error(t, err)
			require.Equal(t, 400, rec.Code)
			require.Zero(t, upstream.calls)
		})
	}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.invalid"}}
	for _, input := range []string{`[null]`, `[{}]`, `[{"type":"message","role":3}]`, `[{"role":"tool","content":"bad role"}]`, `[{"role":"user","content":null}]`, `[{"type":"function_call","call_id":"call_1","arguments":"{}"}]`, `[{"type":"function_call_output","call_id":"call_1","output":null}]`, `[{"role":"user","content":[{"type":"input_text"}]}]`, `[{"role":"user","content":5}]`, `[{"role":"user","content":[{"type":"input_file","file_data":"data:application/pdf;base64,AAAA"}]}]`, `[{"type":"reasoning","encrypted_content":"opaque"}]`} {
		upstream := &inputTokensUpstream{}
		rec, err := inputTokensFixture(t, account, upstream, `{"model":"gpt-4o","input":`+input+`}`, &config.Config{})
		require.Error(t, err)
		require.Equal(t, 400, rec.Code)
		require.Zero(t, upstream.calls)
	}
}

func TestResponsesInputTokensTransportAndCredentialFailures(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	upstream := &inputTokensUpstream{}
	rec, err := inputTokensFixture(t, account, upstream, `{"model":"gpt-4o"}`, &config.Config{})
	require.Error(t, err)
	require.Equal(t, 502, rec.Code)
	require.Zero(t, upstream.calls)
	account.Credentials = map[string]any{"api_key": "key"}
	upstream.err = errors.New("private transport details must not reach client")
	rec, err = inputTokensFixture(t, account, upstream, `{"model":"gpt-4o"}`, &config.Config{})
	require.Error(t, err)
	require.Equal(t, 502, rec.Code)
	require.Equal(t, 1, upstream.calls)
	require.NotContains(t, rec.Body.String(), "private transport")
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = true
	cfg.Security.URLAllowlist.UpstreamHosts = []string{"relay.invalid"}
	upstream = &inputTokensUpstream{}
	rec, err = inputTokensFixture(t, account, upstream, `{"model":"gpt-4o"}`, cfg)
	require.Error(t, err)
	require.Equal(t, 502, rec.Code)
	require.Zero(t, upstream.calls)
}

func TestResponsesInputTokensAccountClientRestriction(t *testing.T) {
	for _, status := range []int{200, 404} {
		upstream := &inputTokensUpstream{status: status, body: `{"object":"response.input_tokens","input_tokens":42}`}
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "key"}, Extra: map[string]any{"codex_cli_only": true}}
		rec, err := inputTokensFixture(t, account, upstream, `{"model":"gpt-4o","input":"hello"}`, &config.Config{})
		require.ErrorIs(t, err, ErrCodexClientRestricted)
		require.Equal(t, 403, rec.Code)
		require.Zero(t, upstream.calls)
		require.Empty(t, rec.Header().Get("X-Sub2api-Token-Count"))
		require.False(t, gjson.GetBytes(rec.Body.Bytes(), "input_tokens").Exists())
	}
	upstream := &inputTokensUpstream{status: 200, body: `{"object":"response.input_tokens","input_tokens":42}`}
	cfg := &config.Config{}
	cfg.Gateway.ForceCodexCLI = true
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "key"}, Extra: map[string]any{"codex_cli_only": true}}
	rec, err := inputTokensFixture(t, account, upstream, `{"model":"gpt-4o","input":"hello"}`, cfg)
	require.NoError(t, err)
	require.Equal(t, 200, rec.Code)
	require.Equal(t, 1, upstream.calls)
}

func TestResponsesInputTokensHostedToolsRequireNative(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.invalid"}}
	for _, kind := range []string{"image_generation", "web_search", "file_search", "mcp", "custom"} {
		upstream := &inputTokensUpstream{}
		rec, err := inputTokensFixture(t, account, upstream, `{"model":"gpt-4o","input":"hello","tools":[{"type":"`+kind+`"}]}`, &config.Config{})
		require.Error(t, err)
		require.Equal(t, 400, rec.Code)
		require.Zero(t, upstream.calls)
	}
	account.Credentials = map[string]any{"api_key": "key"}
	upstream := &inputTokensUpstream{status: 200, body: `{"object":"response.input_tokens","input_tokens":42}`}
	rec, err := inputTokensFixture(t, account, upstream, `{"model":"gpt-4o","input":"hello","tools":[{"type":"web_search"}]}`, &config.Config{})
	require.NoError(t, err)
	require.Equal(t, 200, rec.Code)
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, "web_search", gjson.GetBytes(upstream.wire, "tools.0.type").String())
}

func TestResponsesInputTokensInvalidLocalToolDescriptors(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.invalid"}}
	for _, tools := range []string{`[null]`, `[3]`, `[{"type":"function"}]`, `[{"type":"function","name":" "}]`, `[{"type":"file_search","vector_store_ids":["vs_private"]}]`} {
		t.Run(tools, func(t *testing.T) {
			upstream := &inputTokensUpstream{}
			rec, err := inputTokensFixture(t, account, upstream, `{"model":"gpt-4o","input":"hello","tools":`+tools+`}`, &config.Config{})
			require.Error(t, err)
			require.Equal(t, 400, rec.Code)
			require.Zero(t, upstream.calls)
			require.Empty(t, rec.Header().Get("X-Sub2api-Token-Count"))
		})
	}
}

func TestResponsesInputTokensIncompleteTransportResponse(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key"}}
	for _, upstream := range []*inputTokensUpstream{{status: 200, missingBody: true}, {status: 200, readErr: true}} {
		rec, err := inputTokensFixture(t, account, upstream, `{"model":"gpt-4o"}`, &config.Config{})
		require.Error(t, err)
		require.Equal(t, 502, rec.Code)
		require.NotContains(t, rec.Body.String(), "private read failure")
		if upstream.readErr {
			require.True(t, upstream.closed)
		}
	}
	rec, err := inputTokensFixture(t, nil, &inputTokensUpstream{}, `{"model":"gpt-4o"}`, &config.Config{})
	require.Error(t, err)
	require.Equal(t, 503, rec.Code)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := &inputTokensUpstream{status: 200, body: `{"object":"response.input_tokens","input_tokens":42}`, afterCall: cancel}
	rec = httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil).WithContext(ctx)
	s := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	err = s.ForwardResponsesInputTokens(ctx, c, account, []byte(`{"model":"gpt-4o"}`))
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, upstream.closed)
	require.Empty(t, rec.Body.String())
}
