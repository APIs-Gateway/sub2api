//go:build unit

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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestOpenAIImagesTextFallbackError covers the body-parsing wrapper around
// openAIImagesTextFallbackErrorForText. openAIImagesTextFallbackErrorForText
// itself already has exhaustive coverage in
// openai_upstream_bugfix_helpers_test.go (TestOpenAIImagesTextFallbackErrorForText),
// as does extractOpenAIImagesModelText/isOpenAIImagesContentPolicyRefusal
// (TestOpenAIImagesDiagnosticsHelpers) — this test only needs to prove the
// body -> text -> classification wiring.
func TestOpenAIImagesTextFallbackError(t *testing.T) {
	t.Parallel()

	t.Run("no text output at all (true vacuum) yields nil", func(t *testing.T) {
		body := `{"type":"response.completed","response":{"output":[]}}`
		require.Nil(t, openAIImagesTextFallbackError([]byte(body)))
	})

	t.Run("text-only output yields the capability failure classification", func(t *testing.T) {
		body := `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"Here's a plan: draw a red circle."}]}]}}`
		got := openAIImagesTextFallbackError([]byte(body))
		require.NotNil(t, got)
		require.Equal(t, "image_generation_unavailable", got.Code)
	})
}

// openAIImagesCooldownAccountRepoStub records SetModelRateLimit calls. It
// embeds the (nil) AccountRepository interface so any other method call
// would panic — coolOpenAIImagesOAuthTool must only touch SetModelRateLimit.
type openAIImagesCooldownAccountRepoStub struct {
	AccountRepository
	calls []openAIImagesCooldownCall
	err   error
}

type openAIImagesCooldownCall struct {
	accountID int64
	scope     string
	resetAt   time.Time
	reason    string
}

func (r *openAIImagesCooldownAccountRepoStub) SetModelRateLimit(_ context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	call := openAIImagesCooldownCall{accountID: id, scope: scope, resetAt: resetAt}
	if len(reason) > 0 {
		call.reason = reason[0]
	}
	r.calls = append(r.calls, call)
	return r.err
}

func TestCoolOpenAIImagesOAuthTool(t *testing.T) {
	t.Run("nil service is a no-op", func(t *testing.T) {
		var s *OpenAIGatewayService
		require.NotPanics(t, func() {
			s.coolOpenAIImagesOAuthTool(context.Background(), &Account{ID: 1, Platform: PlatformOpenAI})
		})
	})

	t.Run("nil account is a no-op", func(t *testing.T) {
		repo := &openAIImagesCooldownAccountRepoStub{}
		s := &OpenAIGatewayService{accountRepo: repo}
		s.coolOpenAIImagesOAuthTool(context.Background(), nil)
		require.Empty(t, repo.calls)
	})

	t.Run("non-OpenAI platform is a no-op", func(t *testing.T) {
		repo := &openAIImagesCooldownAccountRepoStub{}
		s := &OpenAIGatewayService{accountRepo: repo}
		s.coolOpenAIImagesOAuthTool(context.Background(), &Account{ID: 7, Platform: PlatformAnthropic})
		require.Empty(t, repo.calls)
	})

	t.Run("writes a 30-minute cooldown on the shared image generation rate limit scope", func(t *testing.T) {
		repo := &openAIImagesCooldownAccountRepoStub{}
		s := &OpenAIGatewayService{accountRepo: repo}
		before := time.Now()

		s.coolOpenAIImagesOAuthTool(context.Background(), &Account{ID: 9, Platform: PlatformOpenAI})

		require.Len(t, repo.calls, 1)
		call := repo.calls[0]
		require.Equal(t, int64(9), call.accountID)
		require.Equal(t, openAIImageGenerationRateLimitKey, call.scope)
		require.Equal(t, openAIImagesOAuthUnavailableReason, call.reason)
		require.WithinDuration(t, before.Add(openAIImagesOAuthUnavailableCooldown), call.resetAt, 5*time.Second)
	})

	t.Run("repo error is swallowed (best-effort write)", func(t *testing.T) {
		repo := &openAIImagesCooldownAccountRepoStub{err: errors.New("db down")}
		s := &OpenAIGatewayService{accountRepo: repo}
		require.NotPanics(t, func() {
			s.coolOpenAIImagesOAuthTool(context.Background(), &Account{ID: 11, Platform: PlatformOpenAI})
		})
		require.Len(t, repo.calls, 1)
	})
}

func TestHandleOpenAIImagesOAuthResponseError_ImageGenerationUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("nothing written yet: forces a same-request account switch and cools the tool", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
		writerSizeBefore := OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c)

		repo := &openAIImagesCooldownAccountRepoStub{}
		svc := &OpenAIGatewayService{accountRepo: repo}
		account := &Account{ID: 5, Name: "acct", Platform: PlatformOpenAI}
		upstreamErr := &OpenAIImagesUpstreamError{
			StatusCode: http.StatusBadGateway,
			ErrorType:  "upstream_error",
			Code:       "image_generation_unavailable",
			Message:    "Upstream did not execute image generation",
		}

		err := svc.handleOpenAIImagesOAuthResponseError(
			context.Background(), c, account, "gpt-image-2",
			"https://chatgpt.com/backend-api/codex/responses",
			&http.Response{Header: http.Header{}}, writerSizeBefore, upstreamErr,
		)

		require.Len(t, repo.calls, 1, "should cool the account's image tool")
		require.Equal(t, int64(5), repo.calls[0].accountID)

		var failoverErr *UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
		require.False(t, failoverErr.RetryableOnSameAccount, "must force switching accounts, not retry the cooled-down one")
	})

	t.Run("response already written: returns the original error untouched but still cools the tool", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
		writerSizeBefore := OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c)
		_, writeErr := c.Writer.Write([]byte("partial"))
		require.NoError(t, writeErr)

		repo := &openAIImagesCooldownAccountRepoStub{}
		svc := &OpenAIGatewayService{accountRepo: repo}
		account := &Account{ID: 6, Name: "acct2", Platform: PlatformOpenAI}
		upstreamErr := &OpenAIImagesUpstreamError{
			StatusCode: http.StatusBadGateway,
			ErrorType:  "upstream_error",
			Code:       "image_generation_unavailable",
			Message:    "Upstream did not execute image generation",
		}

		err := svc.handleOpenAIImagesOAuthResponseError(
			context.Background(), c, account, "gpt-image-2",
			"https://chatgpt.com/backend-api/codex/responses",
			&http.Response{Header: http.Header{}}, writerSizeBefore, upstreamErr,
		)

		require.Len(t, repo.calls, 1, "should still cool the account's image tool")
		var gotErr *OpenAIImagesUpstreamError
		require.ErrorAs(t, err, &gotErr)
		require.Same(t, upstreamErr, gotErr)

		var failoverErr *UpstreamFailoverError
		require.False(t, errors.As(err, &failoverErr), "must not wrap into a failover error once bytes were already written to the client")
	})
}

// TestImagesOAuthNonStreaming_PlainTextPlanTriggersAccountCooldownFailover
// covers the non-streaming counterpart of the streaming
// image_generation_unavailable path: the model only replied with a text plan
// (no safety/policy keywords), so the response must not be written to the
// client (it's retryable) and must be classified as image_generation_unavailable.
func TestImagesOAuthNonStreaming_PlainTextPlanTriggersAccountCooldownFailover(t *testing.T) {
	upstreamSSE := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"status\":\"in_progress\",\"model\":\"gpt-5.4-mini\",\"output\":[]}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"model\":\"gpt-5.4-mini\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Here's a plan: draw a red circle on a white canvas.\"}]}],\"tool_usage\":{\"image_gen\":{\"output_tokens\":0}}}}\n\n"

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstreamSSE))}

	svc := &OpenAIGatewayService{}
	_, _, _, err := svc.handleOpenAIImagesOAuthNonStreamingResponse(resp, c, "b64_json", "gpt-image-2")
	require.Error(t, err)

	var imgErr *OpenAIImagesUpstreamError
	require.ErrorAs(t, err, &imgErr)
	require.Equal(t, http.StatusBadGateway, imgErr.StatusCode)
	require.Equal(t, "image_generation_unavailable", imgErr.Code)
	require.True(t, IsOpenAIImagesRetryableUpstreamError(imgErr))

	// A retryable (>=500) soft failure must not write a client response — the
	// caller retries on a different account instead of surfacing this to the
	// client directly.
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

// TestOpenAIGatewayServiceForwardImages_OAuthStreamingTextOnlyPlanCoolsAndFailsOver
// exercises the streaming path end-to-end: the model streams
// response.output_text.delta chunks (a plan, not a refusal) and then
// completes with no image output. This must not be surfaced to the client as
// an SSE error frame (it's retryable on another account); instead the tool is
// cooled down on this account and a same-request failover is triggered.
func TestOpenAIGatewayServiceForwardImages_OAuthStreamingTextOnlyPlanCoolsAndFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":true}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	repo := &openAIImagesCooldownAccountRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.True(t, parsed.Stream)

	upstreamSSE := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"model\":\"gpt-5.4-mini\",\"output\":[]}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Here's a plan: draw a red circle on a white canvas.\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"model\":\"gpt-5.4-mini\",\"output\":[],\"tool_usage\":{\"image_gen\":{\"output_tokens\":0}}}}\n\n"

	svc.httpUpstream = &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_text_plan"},
			},
			Body: io.NopCloser(strings.NewReader(upstreamSSE)),
		},
	}

	account := &Account{
		ID:       31,
		Name:     "openai-oauth-text-plan",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.False(t, failoverErr.RetryableOnSameAccount, "must switch accounts, not retry the cooled-down one")

	require.NotContains(t, rec.Body.String(), "event: error", "a retryable capability failure must not be surfaced to the client")

	require.Len(t, repo.calls, 1, "should cool the account's image tool for a same-request failover")
	require.Equal(t, account.ID, repo.calls[0].accountID)
	require.Equal(t, openAIImageGenerationRateLimitKey, repo.calls[0].scope)
}

// TestOpenAIGatewayServiceForwardImages_OAuthStreamingRefusalWritesClientError
// covers the complementary streaming branch: the accumulated delta text hits
// a safety/content-policy keyword, so it must be surfaced to the client as a
// terminal (non-retryable) SSE error frame instead of triggering a same-account
// cooldown/failover.
func TestOpenAIGatewayServiceForwardImages_OAuthStreamingRefusalWritesClientError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw something blocked","stream":true}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.True(t, parsed.Stream)

	upstreamSSE := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"r2\",\"status\":\"in_progress\",\"model\":\"gpt-5.4-mini\",\"output\":[]}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"I'm sorry, this violates our content policy and I cannot generate it.\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r2\",\"status\":\"completed\",\"model\":\"gpt-5.4-mini\",\"output\":[],\"tool_usage\":{\"image_gen\":{\"output_tokens\":0}}}}\n\n"

	svc.httpUpstream = &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_refusal"},
			},
			Body: io.NopCloser(strings.NewReader(upstreamSSE)),
		},
	}

	account := &Account{
		ID:       32,
		Name:     "openai-oauth-refusal",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")

	require.Nil(t, result)
	var upstreamErr *OpenAIImagesUpstreamError
	require.ErrorAs(t, err, &upstreamErr)
	require.Equal(t, http.StatusBadRequest, upstreamErr.StatusCode)
	require.Equal(t, "content_policy_violation", upstreamErr.Code)

	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "a content policy refusal must not trigger a same-request failover")

	require.Contains(t, rec.Body.String(), "event: error")
	require.Contains(t, rec.Body.String(), "content policy")
}

// TestOpenAIGatewayServiceForwardImages_OAuthStreamingCompletedBodyOnlyTextFallsBackToDataBytes
// covers the case where the model never streams output_text.delta chunks
// (so the fallbackText accumulator stays empty) but the final
// response.completed event itself carries a message/output_text part. The
// streaming handler must fall back to classifying openAIImagesTextFallbackError
// straight off that completed event's raw bytes instead of treating it as a
// true vacuum response.
func TestOpenAIGatewayServiceForwardImages_OAuthStreamingCompletedBodyOnlyTextFallsBackToDataBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":true}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	repo := &openAIImagesCooldownAccountRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.True(t, parsed.Stream)

	// No response.output_text.delta events at all — the text only shows up in
	// the completed event's own output array.
	upstreamSSE := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"r3\",\"status\":\"in_progress\",\"model\":\"gpt-5.4-mini\",\"output\":[]}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r3\",\"status\":\"completed\",\"model\":\"gpt-5.4-mini\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Here's a plan: draw a red circle on a white canvas.\"}]}],\"tool_usage\":{\"image_gen\":{\"output_tokens\":0}}}}\n\n"

	svc.httpUpstream = &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Request-Id": []string{"req_img_completed_body_text"},
			},
			Body: io.NopCloser(strings.NewReader(upstreamSSE)),
		},
	}

	account := &Account{
		ID:       33,
		Name:     "openai-oauth-completed-body-text",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.False(t, failoverErr.RetryableOnSameAccount)

	require.Len(t, repo.calls, 1, "should cool the account's image tool for a same-request failover")
	require.Equal(t, account.ID, repo.calls[0].accountID)
}
