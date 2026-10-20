package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func runOpenAIImageOAuthAccountTestWithSSE(t *testing.T, sse string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)
	svc := &AccountTestService{httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(sse)),
	}}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test"}}
	err := svc.testOpenAIImageOAuth(c, context.Background(), account, "gpt-image-2.5-flare", "draw a cup")
	return rec, err
}

func TestAccountTestService_OpenAIImageOAuthSurfacesTextFallback(t *testing.T) {
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL", "gpt-5.6-sol")
	rec, err := runOpenAIImageOAuthAccountTestWithSSE(t,
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Here is a plan for the cup illustration instead.\"}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"+
			"data: [DONE]\n\n")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "No images returned")
	require.Contains(t, rec.Body.String(), "Responses driver: gpt-5.6-sol; image model: gpt-image-2.5-flare")
	require.NotContains(t, rec.Body.String(), "No images returned")
}

func TestAccountTestService_OpenAIImageOAuthReportsEmptyOutput(t *testing.T) {
	_, err := runOpenAIImageOAuthAccountTestWithSSE(t,
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\ndata: [DONE]\n\n")
	require.ErrorContains(t, err, "No images returned from responses API")
}

func TestOpenAIImagesToolUsageClampsImageInputTokens(t *testing.T) {
	svc := &OpenAIGatewayService{}
	var usage OpenAIUsage
	svc.parseOpenAIImagesSSEUsageBytes([]byte(`{"type":"response.completed","response":{"tool_usage":{"image_gen":{"input_tokens":10,"input_tokens_details":{"image_tokens":50},"output_tokens":5,"output_tokens_details":{"image_tokens":5}}}}}`), &usage)
	require.Equal(t, 10, usage.InputTokens)
	require.Equal(t, 10, usage.ImageInputTokens, "image input tokens must not exceed input tokens")
}
