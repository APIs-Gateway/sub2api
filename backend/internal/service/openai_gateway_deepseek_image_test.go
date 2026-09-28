//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAliasDeepSeekResponsesInputImages(t *testing.T) {
	t.Parallel()
	const dataURI = "data:image/png;base64,AQID"
	cases := []struct {
		name          string
		body          string
		imagePath     string
		wantURL       string
		wantUnchanged bool
	}{
		{
			name:      "codex image_url string",
			body:      `{"model":"deepseek-flash","store":true,"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"` + dataURI + `"}]}]}`,
			imagePath: "input.0.content.0", wantURL: dataURI,
		},
		{
			name:      "nested image_url object",
			body:      `{"input":[{"type":"message","content":[{"type":"input_image","image_url":{"url":"` + dataURI + `"}}]}]}`,
			imagePath: "input.0.content.0", wantURL: dataURI,
		},
		{
			name:      "chat image_url part",
			body:      `{"input":[{"type":"message","content":[{"type":"image_url","image_url":{"url":"` + dataURI + `"}}]}]}`,
			imagePath: "input.0.content.0", wantURL: dataURI,
		},
		{
			name:      "anthropic inline image",
			body:      `{"input":[{"type":"message","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AQID"}}]}]}`,
			imagePath: "input.0.content.0", wantURL: dataURI,
		},
		{
			name:      "file id only",
			body:      `{"input":[{"type":"message","content":[{"type":"input_image","file_id":"file-123"}]}]}`,
			imagePath: "input.0.content.0", wantUnchanged: true,
		},
		{
			name:          "unlifted tool output is outside this adapter",
			body:          `{"input":[{"type":"function_call_output","call_id":"call_image","output":[{"type":"input_image","image_url":"` + dataURI + `"}]}]}`,
			wantUnchanged: true,
		},
		{
			name:          "already aliased",
			body:          `{"input":[{"type":"message","content":[{"type":"input_image","image_url":"` + dataURI + `","url":"` + dataURI + `"}]}]}`,
			wantUnchanged: true,
		},
		{
			name:          "invalid nested URL is not fabricated",
			body:          `{"input":[{"type":"message","content":[{"type":"input_image","image_url":{"url":123}}]}]}`,
			wantUnchanged: true,
		},
		{
			name:          "invalid source data is not fabricated",
			body:          `{"input":[{"type":"message","content":[{"type":"image","source":{"media_type":"image/png","data":123}}]}]}`,
			wantUnchanged: true,
		},
		{
			name:      "existing url is authoritative",
			body:      `{"input":[{"type":"message","content":[{"type":"input_image","image_url":"https://old.example/image.png","url":"` + dataURI + `"}]}]}`,
			imagePath: "input.0.content.0", wantURL: dataURI,
		},
		{
			name:          "no image input",
			body:          `{"input":[{"type":"message","content":[{"type":"input_text","text":"hello"}]}]}`,
			wantUnchanged: true,
		},
		{
			name: "malformed json",
			body: `{"input":[`, wantUnchanged: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := aliasDeepSeekResponsesInputImages([]byte(tc.body))
			if tc.wantUnchanged {
				require.Equal(t, tc.body, string(got))
				return
			}
			part := gjson.GetBytes(got, tc.imagePath)
			require.Equal(t, "input_image", part.Get("type").String())
			require.Equal(t, tc.wantURL, part.Get("image_url").String())
			require.Equal(t, tc.wantURL, part.Get("url").String())
			if tc.name == "codex image_url string" {
				require.True(t, gjson.GetBytes(got, "store").Bool(), "do not change the fork's request state")
			}
		})
	}
}

func TestForwardMappedDeepSeekHTTPResponsesAliasesImageURL(t *testing.T) {
	const body = `{"model":"gpt-5","stream":false,"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AQID"}]}]}`
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	upstream := &httpUpstreamRecorder{err: errors.New("stop after outbound capture")}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID: 1353, Name: "mapped-deepseek", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Concurrency: 1, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{
			"api_key": "sk-test", "base_url": "https://api.deepseek.com",
			"model_mapping": map[string]any{"gpt-5": "deepseek-flash"},
		},
		Extra: map[string]any{
			openai_compat.ExtraKeyResponsesMode:      string(openai_compat.ResponsesSupportModeAuto),
			openai_compat.ExtraKeyResponsesSupported: true,
		},
	}
	result, err := svc.Forward(context.Background(), c, account, []byte(body))
	require.Error(t, err)
	require.Nil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://api.deepseek.com/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "deepseek-flash", gjson.GetBytes(upstream.lastBody, "model").String())
	image := gjson.GetBytes(upstream.lastBody, "input.0.content.0")
	require.Equal(t, "data:image/png;base64,AQID", image.Get("image_url").String())
	require.Equal(t, "data:image/png;base64,AQID", image.Get("url").String())
}

func TestBuildUpstreamRequestDeepSeekResponsesImageScope(t *testing.T) {
	const body = `{"model":"gpt-5","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AQID"}]}]}`
	cases := []struct {
		name        string
		baseURL     string
		path        string
		transport   OpenAIClientTransport
		compat      bool
		wantAliased bool
	}{
		{"official DeepSeek", "https://api.deepseek.com", "/v1/responses", OpenAIClientTransportHTTP, false, true},
		{"official DeepSeek versioned", "https://api.deepseek.com/v1", "/v1/responses", OpenAIClientTransportHTTP, false, true},
		{"official OpenAI", "https://api.openai.com", "/v1/responses", OpenAIClientTransportHTTP, false, false},
		{"lookalike host", "https://api.deepseek.com.example.test", "/v1/responses", OpenAIClientTransportHTTP, false, false},
		{"subdomain", "https://proxy.api.deepseek.com", "/v1/responses", OpenAIClientTransportHTTP, false, false},
		{"compact", "https://api.deepseek.com", "/v1/responses/compact", OpenAIClientTransportHTTP, false, false},
		{"chat route", "https://api.deepseek.com", "/v1/chat/completions", OpenAIClientTransportHTTP, false, false},
		{"Messages bridge", "https://api.deepseek.com", "/v1/responses", OpenAIClientTransportHTTP, true, false},
		{"WebSocket", "https://api.deepseek.com", "/v1/responses", OpenAIClientTransportWS, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			SetOpenAIClientTransport(c, tc.transport)
			setOpenAICompatMessagesBridgeContext(c, tc.compat)
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "sk-test", "base_url": tc.baseURL,
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			req, err := svc.buildUpstreamRequest(context.Background(), c, account, []byte(body), "sk-test", false, "", false)
			require.NoError(t, err)
			wireBody, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			image := gjson.GetBytes(wireBody, "input.0.content.0")
			require.Equal(t, "data:image/png;base64,AQID", image.Get("image_url").String())
			require.Equal(t, tc.wantAliased, image.Get("url").Exists())
		})
	}
}

func TestBuildUpstreamRequestDeepSeekPassthroughKeepsImageBody(t *testing.T) {
	const body = `{"model":"gpt-5","input":[{"type":"message","content":[{"type":"input_image","image_url":"data:image/png;base64,AQID"}]}]}`
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "sk-test", "base_url": "https://api.deepseek.com",
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	req, err := svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, []byte(body), "sk-test")
	require.NoError(t, err)
	wireBody, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(wireBody))
}
