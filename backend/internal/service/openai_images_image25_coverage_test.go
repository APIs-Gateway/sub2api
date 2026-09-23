package service

import (
	"context"
	"errors"
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

func TestOpenAIImagesRejectedDriverPassthroughByAccountType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL", "gpt-5.4-mini")
	body := `{"error":{"message":"The 'gpt-5.4-mini' model is not supported when using Codex with a ChatGPT account.","type":"invalid_request_error"}}`
	for _, tc := range []struct {
		name        string
		accountType string
		passthrough bool
	}{
		{name: "setup_token_passes_through", accountType: AccountTypeSetupToken, passthrough: true},
		{name: "apikey_is_not_driver_gated", accountType: AccountTypeAPIKey, passthrough: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &modelNotFoundAccountRepoStub{}
			svc := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, openAIImagesGenerationsEndpoint, nil)
			account := openAICodexPlanGatedOAuthAccount()
			account.Type = tc.accountType
			resp := &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
			_, err := svc.handleOpenAIImagesErrorResponse(WithOpenAIImagesEndpoint(context.Background()), resp, c, account, "gpt-image-2.5-flare")
			require.Error(t, err)
			var upstreamErr *OpenAIImagesUpstreamError
			if tc.passthrough {
				require.ErrorAs(t, err, &upstreamErr)
				require.Equal(t, http.StatusBadRequest, upstreamErr.StatusCode)
				require.Contains(t, rec.Body.String(), "gpt-5.4-mini")
				require.Empty(t, repo.modelRateLimitCalls)
				require.Zero(t, repo.tempCalls)
			} else {
				require.False(t, errors.As(err, &upstreamErr) && rec.Body.Len() > 0 && strings.Contains(rec.Body.String(), "gpt-5.4-mini"),
					"API key accounts do not use the Codex Responses driver, so the driver passthrough must not apply")
			}
		})
	}
}
