//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExtractGrokMediaModelSupportsJSONAndMultipart(t *testing.T) {
	require.Equal(t, "grok-imagine", ExtractGrokMediaModel("application/json", []byte(`{"model":"grok-imagine"}`)))

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("prompt", "draw a cat"))
	require.NoError(t, writer.WriteField("model", "grok-imagine-edit"))
	require.NoError(t, writer.Close())

	require.Equal(t, "grok-imagine-edit", ExtractGrokMediaModel(writer.FormDataContentType(), body.Bytes()))
	require.Empty(t, ExtractGrokMediaModel("application/json", []byte(`{"prompt":"no model"}`)))
	require.Empty(t, ExtractGrokMediaModel("multipart/form-data", []byte("invalid")))
}

func TestExtractGrokMediaModelRejectsMalformedMultipartBody(t *testing.T) {
	require.Empty(t, ExtractGrokMediaModel("multipart/form-data; boundary=media", []byte("--media\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\n")))
}

func TestGrokMediaEndpointContracts(t *testing.T) {
	require.True(t, GrokMediaEndpointImagesGenerations.RequiresRequestBody())
	require.True(t, GrokMediaEndpointImagesEdits.IsGenerationRequest())
	require.True(t, GrokMediaEndpointVideosGenerations.IsGenerationRequest())
	require.False(t, GrokMediaEndpointVideoStatus.RequiresRequestBody())
	require.False(t, GrokMediaEndpointVideoStatus.IsGenerationRequest())
	require.Equal(t, http.MethodPost, GrokMediaEndpointImagesEdits.httpMethod())
	require.Equal(t, http.MethodGet, GrokMediaEndpointVideoStatus.httpMethod())
	require.Equal(t, "https://xai.test/v1/images/edits", mustGrokMediaURL(t, GrokMediaEndpointImagesEdits, ""))
	_, err := GrokMediaEndpoint("unknown").upstreamURL("https://xai.test/v1", "")
	require.Error(t, err)
}

func mustGrokMediaURL(t *testing.T, endpoint GrokMediaEndpoint, requestID string) string {
	t.Helper()
	url, err := endpoint.upstreamURL("https://xai.test/v1", requestID)
	require.NoError(t, err)
	return url
}

func TestForwardGrokMediaImagesGenerationPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine","prompt":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          61,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "api-key", "base_url": "https://xai.test/v1"},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-image-req"},
		},
		Body: io.NopCloser(strings.NewReader(`{"data":[]}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/images/generations", upstream.lastReq.URL.String())
	require.Equal(t, http.MethodPost, upstream.lastReq.Method)
	require.Equal(t, "Bearer api-key", upstream.lastReq.Header.Get("Authorization"))
	require.JSONEq(t, string(body), string(upstream.lastBody))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"data":[]}`, recorder.Body.String())
	require.Equal(t, "xai-image-req", result.RequestID)
	require.Equal(t, "grok-imagine", result.Model)
}

func TestForwardGrokMediaVideoStatusUsesGETWithoutBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/request-123", nil)

	account := &Account{
		ID:          62,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "api-key", "base_url": "https://xai.test/v1"},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Xai-Request-Id": []string{"xai-video-req"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"request-123","status":"completed"}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideoStatus, "request-123", nil, "")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/videos/request-123", upstream.lastReq.URL.String())
	require.Equal(t, http.MethodGet, upstream.lastReq.Method)
	require.Equal(t, "Bearer api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Empty(t, upstream.lastReq.Header.Get("Content-Type"))
	require.Empty(t, upstream.lastBody)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"id":"request-123","status":"completed"}`, recorder.Body.String())
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	require.Equal(t, "xai-video-req", result.RequestID)
}

func TestForwardGrokMediaRejectsInvalidAccountsAndEndpoints(t *testing.T) {
	service := &OpenAIGatewayService{}
	_, err := service.ForwardGrokMedia(context.Background(), nil, nil, GrokMediaEndpointImagesGenerations, "", nil, "")
	require.EqualError(t, err, "grok account is required")

	account := &Account{Platform: PlatformOpenAI}
	_, err = service.ForwardGrokMedia(context.Background(), nil, account, GrokMediaEndpointImagesGenerations, "", nil, "")
	require.EqualError(t, err, "account platform openai is not supported for grok media")

	account = &Account{
		Type:        AccountTypeAPIKey,
		Platform:    PlatformGrok,
		Credentials: map[string]any{"api_key": "api-key", "base_url": "https://xai.test/v1"},
	}
	_, err = service.ForwardGrokMedia(context.Background(), nil, account, GrokMediaEndpoint("unknown"), "", nil, "")
	require.EqualError(t, err, "unsupported grok media endpoint: unknown")
}

type grokMediaReadError struct{}

func (grokMediaReadError) Read([]byte) (int, error) {
	return 0, errors.New("response body read failed")
}

func TestForwardGrokMediaReturnsUpstreamReadError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"grok-imagine"}`))
	account := &Account{
		ID:          64,
		Platform:    PlatformGrok,
		Credentials: map[string]any{"api_key": "api-key", "base_url": "https://xai.test/v1"},
	}
	svc := &OpenAIGatewayService{httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(grokMediaReadError{}),
	}}}

	_, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", []byte(`{"model":"grok-imagine"}`), "application/json")
	require.Error(t, err)
}

func TestForwardGrokMediaHandlesNonFailoverAndFailoverResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID:          63,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "api-key", "base_url": "https://xai.test/v1"},
	}
	body := []byte(`{"model":"grok-imagine","prompt":"cat"}`)

	newContext := func() (*gin.Context, *httptest.ResponseRecorder) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
		return c, recorder
	}
	response := func(status int, body string) *http.Response {
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
	}

	svc := &OpenAIGatewayService{httpUpstream: &httpUpstreamRecorder{resp: response(http.StatusBadRequest, `{"error":{"message":"invalid prompt"}}`)}}
	c, recorder := newContext()
	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "grok-imagine", result.Model)

	svc = &OpenAIGatewayService{httpUpstream: &httpUpstreamRecorder{resp: response(http.StatusBadGateway, `{"error":{"message":"upstream down"}}`)}}
	c, _ = newContext()
	_, err = svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideosGenerations, "", body, "application/json")
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)

	svc = &OpenAIGatewayService{httpUpstream: &httpUpstreamRecorder{err: errors.New("dial failed")}}
	c, _ = newContext()
	_, err = svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideosGenerations, "", body, "application/json")
	require.ErrorAs(t, err, &failoverErr)
}

func TestForwardGrokMediaVideoStatus404FailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/request-404", nil)
	account := &Account{
		ID:          65,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "api-key", "base_url": "https://xai.test/v1"},
	}
	svc := &OpenAIGatewayService{httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"video not found"}}`)),
	}}}

	_, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideoStatus, "request-404", nil, "")
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusNotFound, failoverErr.StatusCode)
}
