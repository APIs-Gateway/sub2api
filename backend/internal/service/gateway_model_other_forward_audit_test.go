//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type otherModelAuditUpstream struct {
	HTTPUpstream
	calls       int
	accountID   int64
	rawBody     []byte
	response    string
	requestURL  string
	contentType string
}

func (u *otherModelAuditUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.calls++
	u.accountID = accountID
	u.requestURL = req.URL.String()
	u.contentType = req.Header.Get("Content-Type")
	var err error
	u.rawBody, err = io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(u.response))}, nil
}

func (u *otherModelAuditUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

func TestGatewayModelOtherForward_RealOutboundGuards(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"anthropic_chat", "gemini_chat", "alpha_raw", "alpha_sanitized", "images_json", "count_tokens"} {
		for _, tc := range []struct {
			name, fields string
			ambiguous    bool
		}{
			{"plain_duplicate", `"model":"public-small","model":"public-large"`, true},
			{"escaped_duplicate", `"model":"public-small","\u006dodel":"public-large"`, true},
			{"case_duplicate", `"model":"public-small","Model":"public-large"`, true},
			{"equal_duplicate", `"model":"public-small","model":"public-small"`, true},
			{"canonical_control", `"model":"public-small"`, false},
			{"nested_control", `"model":"public-small","metadata":{"model":"nested-first","model":"nested-last"}`, false},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				fields := tc.fields
				if route == "images_json" {
					fields = strings.NewReplacer("public-small", "gpt-image-2", "public-large", "gpt-image-1").Replace(fields)
				}
				body := []byte(`{` + fields + `,"stream":false,"prompt":"draw a cat","messages":[{"role":"user","content":"audit"}],"commands":{"search_query":[{"q":"audit"}]},"opaque":{"number":1e+03}}`)
				if route == "alpha_sanitized" {
					body = append(body[:len(body)-1], []byte(`,"prompt_cache_key":"remove-me"}`)...)
				}
				c, rec := commandCodeClientToolsContext(body)
				c.Request.Header.Set("Content-Type", "application/json")
				upstream := &otherModelAuditUpstream{response: `{"encrypted_output":"audit result"}`}
				account := &Account{ID: 7890, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-key", "base_url": "https://api.example.com"}}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
				var err error
				switch route {
				case "count_tokens":
					account.Platform = PlatformAnthropic
					account.Extra = map[string]any{"anthropic_passthrough": true}
					account.Credentials["model_mapping"] = map[string]any{"public-small": "claude-sonnet-4-5", "public-large": "claude-opus-4-5"}
					upstream.response = `{"input_tokens":42}`
					parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "public-small"}
					err = (&GatewayService{cfg: &config.Config{}, httpUpstream: upstream}).ForwardCountTokens(context.Background(), c, account, parsed)
				case "anthropic_chat":
					account.Platform = PlatformAnthropic
					account.Credentials["model_mapping"] = map[string]any{"public-small": "claude-sonnet-4-5", "public-large": "claude-sonnet-4-5"}
					upstream.response = namespaceToolAnthropicStream()
					_, err = (&GatewayService{cfg: &config.Config{}, httpUpstream: upstream}).ForwardAsChatCompletions(context.Background(), c, account, body, nil)
				case "gemini_chat":
					account.Platform, account.Type = PlatformGemini, AccountTypeOAuth
					account.Credentials = map[string]any{"access_token": "fixture-token", "project_id": "fixture-project", "model_mapping": map[string]any{"public-small": "gemini-2.5-flash", "public-large": "gemini-2.5-flash"}}
					upstream.response = "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"audit\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":7,\"candidatesTokenCount\":3}}}\n\ndata: [DONE]\n\n"
					_, err = (&GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream, tokenProvider: &GeminiTokenProvider{}}).ForwardAsChatCompletions(context.Background(), c, account, body)
				case "images_json":
					c.Request.URL.Path = openAIImagesGenerationsEndpoint
					upstream.response = `{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}`
					// Direct forwarding independently checks raw JSON. OLD receives
					// the same valid parser result for the first model, as ingress did.
					parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Endpoint: openAIImagesGenerationsEndpoint, ContentType: "application/json", N: 1, Prompt: "draw a cat"}
					_, err = svc.ForwardImages(context.Background(), c, account, body, parsed, "")
				default:
					account.Credentials["model_mapping"] = map[string]any{"public-small": "provider-small", "public-large": "provider-large"}
					err = svc.ForwardAlphaSearch(context.Background(), c, account, body)
				}
				t.Logf("calls=%d account=%d url=%s error=%v outbound=%s", upstream.calls, upstream.accountID, upstream.requestURL, err, upstream.rawBody)
				if tc.ambiguous {
					require.Zero(t, upstream.calls, "must reject before the real outbound call on both forwarding protocols")
					require.Error(t, err)
					require.Equal(t, http.StatusBadRequest, rec.Code)
					require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
					require.Contains(t, rec.Body.String(), "canonical field name")
					return
				}
				require.NoError(t, err)
				require.Equal(t, 1, upstream.calls, "controls must prove the outbound harness is usable")
				require.Equal(t, account.ID, upstream.accountID)
				require.NotEmpty(t, upstream.rawBody)
				if route == "count_tokens" {
					require.Equal(t, "claude-sonnet-4-5", gjson.GetBytes(upstream.rawBody, "model").String())
					require.Equal(t, int64(42), gjson.GetBytes(rec.Body.Bytes(), "input_tokens").Int())
				}
				if strings.HasPrefix(route, "alpha_") {
					require.Equal(t, "provider-small", gjson.GetBytes(upstream.rawBody, "model").String())
					require.Equal(t, gjson.GetBytes(body, "commands").Raw, gjson.GetBytes(upstream.rawBody, "commands").Raw)
					require.False(t, gjson.GetBytes(upstream.rawBody, "prompt_cache_key").Exists())
				}
			})
		}
	}
}

func TestGatewayModelOtherForward_ImagesParserContracts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, fields := range []string{
		`"model":"gpt-image-2","model":"gpt-image-1"`,
		`"model":"gpt-image-2","\u006dodel":"gpt-image-1"`,
		`"model":"gpt-image-2","Model":"gpt-image-1"`,
		`"model":"gpt-image-2","model":"gpt-image-2"`,
	} {
		t.Run(fields, func(t *testing.T) {
			body := []byte(`{` + fields + `,"prompt":"draw a cat"}`)
			c, _ := commandCodeClientToolsContext(body)
			c.Request.URL.Path = openAIImagesGenerationsEndpoint
			c.Request.Header.Set("Content-Type", "application/json")
			parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
			require.ErrorContains(t, err, "canonical field name")
			require.Nil(t, parsed)
		})
	}
	t.Run("JSON missing model keeps default", func(t *testing.T) {
		body := []byte(`{"prompt":"draw a cat","metadata":{"model":"nested-first","model":"nested-last"}}`)
		c, _ := commandCodeClientToolsContext(body)
		c.Request.URL.Path = openAIImagesGenerationsEndpoint
		parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
		require.NoError(t, err)
		require.Equal(t, "gpt-image-2", parsed.Model)
	})
	t.Run("multipart is a separate unchanged protocol", func(t *testing.T) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("model", "gpt-image-2"))
		require.NoError(t, writer.WriteField("stream", "false"))
		require.NoError(t, writer.WriteField("prompt", `keep {"model":"a","model":"b"} as text`))
		part, err := writer.CreateFormFile("image", "input.png")
		require.NoError(t, err)
		_, err = part.Write([]byte("fixture-image"))
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, openAIImagesEditsEndpoint, bytes.NewReader(body.Bytes()))
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body.Bytes())
		require.NoError(t, err)
		require.True(t, parsed.Multipart)
		require.Equal(t, "gpt-image-2", parsed.Model)
		require.Len(t, parsed.Uploads, 1)
		require.Equal(t, "fixture-image", string(parsed.Uploads[0].Data))
		upstream := &otherModelAuditUpstream{response: `{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}`}
		account := &Account{ID: 7890, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-key", "base_url": "https://api.example.com"}}
		result, err := (&OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}).ForwardImages(context.Background(), c, account, body.Bytes(), parsed, "")
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, 1, upstream.calls)
		_, params, err := mime.ParseMediaType(upstream.contentType)
		require.NoError(t, err)
		form, err := multipart.NewReader(bytes.NewReader(upstream.rawBody), params["boundary"]).ReadForm(1 << 20)
		require.NoError(t, err)
		defer func() { _ = form.RemoveAll() }()
		require.Equal(t, []string{"gpt-image-2"}, form.Value["model"])
		require.Equal(t, []string{`keep {"model":"a","model":"b"} as text`}, form.Value["prompt"])
		require.Len(t, form.File["image"], 1)
		file, err := form.File["image"][0].Open()
		require.NoError(t, err)
		defer func() { _ = file.Close() }()
		data, err := io.ReadAll(file)
		require.NoError(t, err)
		require.Equal(t, "fixture-image", string(data))
	})
}

func TestGatewayModelOtherForward_MalformedDiagnostics(t *testing.T) {
	body := []byte(`{"model":`)
	for _, route := range []string{"anthropic_chat", "gemini_chat", "embeddings", "alpha_search"} {
		t.Run(route, func(t *testing.T) {
			c, rec := commandCodeClientToolsContext(body)
			account := &Account{ID: 7890}
			var err error
			switch route {
			case "anthropic_chat":
				_, err = (&GatewayService{}).ForwardAsChatCompletions(context.Background(), c, account, body, nil)
				require.ErrorContains(t, err, "parse chat completions request:")
				require.Empty(t, rec.Body.String(), "existing direct Anthropic parse error does not write a new response")
			case "gemini_chat":
				_, err = (&GeminiMessagesCompatService{}).ForwardAsChatCompletions(context.Background(), c, account, body)
				require.Error(t, err)
				require.Equal(t, "Failed to parse request body", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
			case "embeddings":
				_, err = (&OpenAIGatewayService{}).ForwardEmbeddings(context.Background(), c, account, body, "")
				require.ErrorContains(t, err, "missing model in request")
				require.Equal(t, "model is required", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
			case "alpha_search":
				err = (&OpenAIGatewayService{}).ForwardAlphaSearch(context.Background(), c, account, body)
				require.ErrorContains(t, err, "model is required")
				require.Empty(t, rec.Body.String())
			}
		})
	}
}
