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

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type grokMediaContentUpstreamStub struct {
	requests  []*http.Request
	responses []*http.Response
}

func (s *grokMediaContentUpstreamStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.requests = append(s.requests, req)
	if len(s.responses) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	resp := s.responses[0]
	s.responses = s.responses[1:]
	return resp, nil
}

func (s *grokMediaContentUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, accountID, accountConcurrency)
}

func newGrokMediaContentTestAccount() *Account {
	return &Account{
		ID:          9,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "upstream-key", "base_url": "https://relay.example/v1"},
	}
}

func newGrokMediaContentTestContext(target string, headers map[string]string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	for name, value := range headers {
		c.Request.Header.Set(name, value)
	}
	return c, recorder
}

func grokMediaContentStatusResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestGrokMediaContentEndpointContract(t *testing.T) {
	require.False(t, GrokMediaEndpointVideoContent.RequiresRequestBody())
	require.True(t, GrokMediaEndpointVideoContent.IsVideoLookupRequest())
	require.Equal(t, http.MethodGet, GrokMediaEndpointVideoContent.httpMethod())
	url, err := GrokMediaEndpointVideoContent.upstreamURL("https://xai.test/v1", "task-1")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/videos/task-1/content", url)
	_, err = GrokMediaEndpointVideoContent.upstreamURL("https://xai.test/v1", "")
	require.Error(t, err)
}

func TestGrokMediaContentProxyURLUsesInboundPathPrefix(t *testing.T) {
	c, _ := newGrokMediaContentTestContext("https://api.example/videos/task-1/content", nil)
	require.Equal(t, "/videos/task-1/content", grokMediaContentProxyURL(c, "task-1"))
	v1c, _ := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1/content", nil)
	require.Equal(t, "/v1/videos/task%2Fone/content", grokMediaContentProxyURL(v1c, "task/one"))
	require.Empty(t, grokMediaContentProxyURL(nil, "task-1"))
}

func TestIsGrokCLIProxyTarget(t *testing.T) {
	require.True(t, isGrokCLIProxyTarget("https://cli-chat-proxy.grok.com/v1"))
	require.False(t, isGrokCLIProxyTarget("https://example.test/v1"))
}

func TestForwardGrokMediaContentUsesBoundCredentialAndStreamsRange(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{
		grokMediaContentStatusResponse(`{"status":"completed"}`),
		{
			StatusCode: http.StatusPartialContent,
			Header: http.Header{
				"Content-Type":   []string{"video/mp4"},
				"Content-Length": []string{"13"},
				"Content-Range":  []string{"bytes 0-12/100"},
				"Accept-Ranges":  []string{"bytes"},
			},
			Body: io.NopCloser(strings.NewReader("video-payload")),
		},
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, recorder := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1/content", map[string]string{
		"Range": "bytes=0-12",
	})

	result, err := svc.ForwardGrokMedia(context.Background(), c, newGrokMediaContentTestAccount(), GrokMediaEndpointVideoContent, "task-1", nil, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusPartialContent, recorder.Code)
	require.Equal(t, "video-payload", recorder.Body.String())
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "https://relay.example/v1/videos/task-1", upstream.requests[0].URL.String())
	require.Equal(t, "Bearer upstream-key", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "https://relay.example/v1/videos/task-1/content", upstream.requests[1].URL.String())
	require.Equal(t, "Bearer upstream-key", upstream.requests[1].Header.Get("Authorization"))
	require.Equal(t, "bytes=0-12", upstream.requests[1].Header.Get("Range"))
	require.Equal(t, "*/*", upstream.requests[1].Header.Get("Accept"))
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.requests[0].Context()))
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.requests[1].Context()))
	require.Equal(t, "video/mp4", recorder.Header().Get("Content-Type"))
	require.Equal(t, "bytes 0-12/100", recorder.Header().Get("Content-Range"))
	require.True(t, IsResponseCommitted(c))
}

func TestForwardGrokMediaContentFollowsAuthenticatedRelayURL(t *testing.T) {
	for _, statusURL := range []string{
		"/v1/videos/task-1/content",
		"https://relay.example/v1/videos/task-1/content",
	} {
		t.Run(statusURL, func(t *testing.T) {
			upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{
				grokMediaContentStatusResponse(`{"video":{"url":"` + statusURL + `"}}`),
				{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"video/mp4"}},
					Body:       io.NopCloser(strings.NewReader("video-payload")),
				},
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			c, recorder := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1/content", nil)

			_, err := svc.ForwardGrokMedia(context.Background(), c, newGrokMediaContentTestAccount(), GrokMediaEndpointVideoContent, "task-1", nil, "")

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "https://relay.example/v1/videos/task-1/content", upstream.requests[1].URL.String())
			require.Equal(t, "Bearer upstream-key", upstream.requests[1].Header.Get("Authorization"))
		})
	}
}

func TestForwardGrokMediaContentFetchesValidatedSignedURLWithoutCredential(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{
		grokMediaContentStatusResponse(`{"video":{"url":"https://vidgen.x.ai/signed/task-1.mp4"}}`),
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"video/mp4"}},
			Body:       io.NopCloser(strings.NewReader("video-payload")),
		},
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, recorder := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1/content", nil)

	_, err := svc.ForwardGrokMedia(context.Background(), c, newGrokMediaContentTestAccount(), GrokMediaEndpointVideoContent, "task-1", nil, "")

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "video-payload", recorder.Body.String())
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "https://vidgen.x.ai/signed/task-1.mp4", upstream.requests[1].URL.String())
	require.Empty(t, upstream.requests[1].Header.Get("Authorization"))
	require.Empty(t, upstream.requests[1].Header.Get("User-Agent"))
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.requests[1].Context()))
}

func TestForwardGrokMediaContentRejectsUntrustedSignedURL(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{
		grokMediaContentStatusResponse(`{"video":{"url":"http://169.254.169.254/latest/meta-data"}}`),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, _ := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1/content", nil)

	_, err := svc.ForwardGrokMedia(context.Background(), c, newGrokMediaContentTestAccount(), GrokMediaEndpointVideoContent, "task-1", nil, "")

	require.ErrorContains(t, err, "unsupported video content URL")
	require.Len(t, upstream.requests, 1)
}

func TestGrokMediaSignedVideoContentURLRejectsDeceptiveOrigins(t *testing.T) {
	for _, rawURL := range []string{
		"https://vidgen.x.ai.attacker.invalid/video.mp4",
		"https://vidgen.x.ai@attacker.invalid/video.mp4",
		"https://vidgen.x.ai:444/video.mp4",
		"http://vidgen.x.ai/video.mp4",
	} {
		t.Run(rawURL, func(t *testing.T) {
			_, err := grokMediaSignedVideoContentURL([]byte(`{"video":{"url":"`+rawURL+`"}}`), "task-1")
			require.ErrorContains(t, err, "unsupported video content URL")
		})
	}
}

func TestGrokMediaSignedVideoContentURLRejectsDifferentRelayTask(t *testing.T) {
	_, err := grokMediaSignedVideoContentURL([]byte(`{"video":{"url":"/v1/videos/task-2/content"}}`), "task-1")
	require.ErrorContains(t, err, "unsupported video content URL")
}

func TestForwardGrokMediaContentPreservesRangeNotSatisfiable(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{
		grokMediaContentStatusResponse(`{"status":"completed"}`),
		{
			StatusCode: http.StatusRequestedRangeNotSatisfiable,
			Header: http.Header{
				"Content-Type":   []string{"text/plain"},
				"Content-Length": []string{"11"},
				"Content-Range":  []string{"bytes */100"},
				"Accept-Ranges":  []string{"bytes"},
			},
			Body: io.NopCloser(strings.NewReader("bad-range!!")),
		},
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, recorder := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1/content", map[string]string{"Range": "bytes=500-600"})

	_, err := svc.ForwardGrokMedia(context.Background(), c, newGrokMediaContentTestAccount(), GrokMediaEndpointVideoContent, "task-1", nil, "")

	require.NoError(t, err)
	require.Equal(t, http.StatusRequestedRangeNotSatisfiable, recorder.Code)
	require.Equal(t, "bad-range!!", recorder.Body.String())
	require.Equal(t, "bytes */100", recorder.Header().Get("Content-Range"))
}

func TestForwardGrokMediaContentRejectsRedirectResponses(t *testing.T) {
	for _, response := range []struct {
		name string
		resp *http.Response
	}{
		{
			name: "status",
			resp: &http.Response{StatusCode: http.StatusFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))},
		},
		{
			name: "content",
			resp: &http.Response{StatusCode: http.StatusFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))},
		},
	} {
		t.Run(response.name, func(t *testing.T) {
			responses := []*http.Response{grokMediaContentStatusResponse(`{"status":"completed"}`), response.resp}
			if response.name == "status" {
				responses = []*http.Response{response.resp}
			}
			upstream := &grokMediaContentUpstreamStub{responses: responses}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			c, _ := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1/content", nil)

			_, err := svc.ForwardGrokMedia(context.Background(), c, newGrokMediaContentTestAccount(), GrokMediaEndpointVideoContent, "task-1", nil, "")

			require.ErrorContains(t, err, "redirect is not allowed")
		})
	}
}

func TestForwardGrokMediaContentHandlesEmptyStatusResponse(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{nil}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, _ := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1/content", nil)

	_, err := svc.ForwardGrokMedia(context.Background(), c, newGrokMediaContentTestAccount(), GrokMediaEndpointVideoContent, "task-1", nil, "")

	require.ErrorContains(t, err, "status response is incomplete")
}

func TestForwardGrokMediaContentHandlesEmptyContentResponse(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{
		grokMediaContentStatusResponse(`{"status":"completed"}`),
		nil,
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, _ := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1/content", nil)

	_, err := svc.ForwardGrokMedia(context.Background(), c, newGrokMediaContentTestAccount(), GrokMediaEndpointVideoContent, "task-1", nil, "")

	require.ErrorContains(t, err, "content response is incomplete")
}

func TestForwardGrokMediaContentHandlesStatusReadError(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(grokMediaContentReadError{}),
	}}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, _ := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1/content", nil)

	_, err := svc.ForwardGrokMedia(context.Background(), c, newGrokMediaContentTestAccount(), GrokMediaEndpointVideoContent, "task-1", nil, "")

	require.ErrorContains(t, err, "content read failed")
}

func TestForwardGrokMediaContentHandlesUpstreamClientError(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, _ := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1/content", nil)

	_, err := svc.ForwardGrokMedia(context.Background(), c, newGrokMediaContentTestAccount(), GrokMediaEndpointVideoContent, "task-1", nil, "")

	require.Error(t, err)
}

func TestForwardGrokMediaContentHandlesNonFailoverError(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{
		grokMediaContentStatusResponse(`{"status":"completed"}`),
		{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad range"}}`)),
		},
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, recorder := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1/content", nil)

	result, err := svc.ForwardGrokMedia(context.Background(), c, newGrokMediaContentTestAccount(), GrokMediaEndpointVideoContent, "task-1", nil, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "bad range")
}

func TestForwardGrokMediaContentHandlesStatusError(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"status bad"}}`)),
	}}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, recorder := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1/content", nil)

	result, err := svc.ForwardGrokMedia(context.Background(), c, newGrokMediaContentTestAccount(), GrokMediaEndpointVideoContent, "task-1", nil, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "status bad")
}

func TestForwardGrokMediaContentUsesSafeBinaryDefaults(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{
		grokMediaContentStatusResponse(`{"status":"completed"}`),
		{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Set-Cookie": []string{"secret=upstream"}},
			Body:          io.NopCloser(strings.NewReader("full-video")),
			ContentLength: -1,
		},
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, recorder := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1/content", nil)

	_, err := svc.ForwardGrokMedia(context.Background(), c, newGrokMediaContentTestAccount(), GrokMediaEndpointVideoContent, "task-1", nil, "")

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "full-video", recorder.Body.String())
	require.Equal(t, "application/octet-stream", recorder.Header().Get("Content-Type"))
	require.Empty(t, recorder.Header().Get("Content-Length"))
	require.Empty(t, recorder.Header().Get("Set-Cookie"))
}

type grokMediaContentReadError struct{}

func (grokMediaContentReadError) Read([]byte) (int, error) {
	return 0, errors.New("content read failed")
}

func TestForwardGrokVideoStatusRewritesSignedContentURLToSameOrigin(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"video":{"url":"https://vidgen.x.ai/signed/task-1.mp4"},"counter":9007199254740993}`)),
		},
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, recorder := newGrokMediaContentTestContext("https://api.example/v1/videos/task-1", nil)

	_, err := svc.ForwardGrokMedia(context.Background(), c, newGrokMediaContentTestAccount(), GrokMediaEndpointVideoStatus, "task-1", nil, "")

	require.NoError(t, err)
	require.Equal(t, "/v1/videos/task-1/content", gjsonGetString(recorder.Body.String(), "video.url"))
	require.Equal(t, "9007199254740993", gjsonGetString(recorder.Body.String(), "counter"))
}

func TestRewriteGrokMediaVideoContentURLsPreservesOtherURLsAndEscapedIDs(t *testing.T) {
	body := []byte(`{"nested":[{"url":"https://relay.example/v1/videos/task%2Fone/content"},{"url":"https://relay.example/v1/videos/task-two/content"}]}`)
	rewritten := rewriteGrokMediaVideoContentURLs(body, "task/one", "/v1/videos/task%2Fone/content")

	require.Equal(t, "/v1/videos/task%2Fone/content", gjson.GetBytes(rewritten, "nested.0.url").String())
	require.Equal(t, "https://relay.example/v1/videos/task-two/content", gjson.GetBytes(rewritten, "nested.1.url").String())
	require.Equal(t, string(body), string(rewriteGrokMediaVideoContentURLs(body, "task-1", "/v1/videos/task-1/content")))
	otherBody := []byte(`{"status":"done"}`)
	require.Equal(t, string(otherBody), string(rewriteGrokMediaVideoContentURLs(otherBody, "task-1", "/v1/videos/task-1/content")))
}

func TestWriteGrokMediaContentResponseRejectsIncompleteResponse(t *testing.T) {
	require.Error(t, writeGrokMediaContentResponse(nil, nil))
}

func TestWriteGrokMediaContentResponseReturnsCopyError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"video/mp4"}},
		Body:       io.NopCloser(grokMediaContentReadError{}),
	}

	require.ErrorContains(t, writeGrokMediaContentResponse(c, resp), "content read failed")
}

func TestGrokMediaContentURLHelpersRejectMalformedValues(t *testing.T) {
	require.False(t, isGrokMediaVideoContentURL("%zz", "task-1"))
	require.False(t, isGrokMediaVideoContentURL("/videos", "task-1"))
	require.False(t, isGrokMediaVideoContentURL("/videos/%zz/content", "task-1"))
	require.False(t, rewriteGrokMediaKnownVideoURL(nil, "/content"))
	require.False(t, rewriteGrokMediaKnownVideoURL(anySliceValue(), "/content"))
	require.False(t, rewriteGrokMediaKnownVideoURL(mapValueWithoutVideo(), "/content"))
}

func anySliceValue() *any {
	value := any([]any{"not-video"})
	return &value
}

func mapValueWithoutVideo() *any {
	value := any(map[string]any{"status": "done"})
	return &value
}

func gjsonGetString(body, path string) string {
	return gjson.Get(body, path).String()
}
