//go:build unit

package service

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestExtractImagesUpstreamError_IncompleteIsRetryable(t *testing.T) {
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_1\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n"

	got := extractOpenAIImagesUpstreamError([]byte(body))
	if got == nil {
		t.Fatal("response.incomplete should produce an upstream error")
	}
	if got.StatusCode != http.StatusBadGateway {
		t.Fatalf("incomplete(max_output_tokens) should be 502 retryable, got %d", got.StatusCode)
	}
	if !IsOpenAIImagesRetryableUpstreamError(got) {
		t.Fatal("incomplete(max_output_tokens) should be retryable for failover")
	}
	if got.Code != "response_incomplete" {
		t.Fatalf("unexpected code %q", got.Code)
	}
	if !strings.Contains(got.Message, "max_output_tokens") {
		t.Fatalf("message should carry reason, got %q", got.Message)
	}
}

func TestExtractImagesUpstreamError_IncompleteContentFilterNotRetryable(t *testing.T) {
	body := "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"r\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"content_filter\"}}}\n\n"

	got := extractOpenAIImagesUpstreamError([]byte(body))
	if got == nil {
		t.Fatal("content_filter incomplete should produce an upstream error")
	}
	if got.StatusCode != http.StatusBadRequest {
		t.Fatalf("content_filter incomplete should be 400, got %d", got.StatusCode)
	}
	if IsOpenAIImagesRetryableUpstreamError(got) {
		t.Fatal("content_filter incomplete must not be retryable")
	}
}

func TestExtractImagesUpstreamError_ErrorAndFailedUnchanged(t *testing.T) {
	errorBody := "data: {\"type\":\"error\",\"error\":{\"type\":\"image_generation_user_error\",\"code\":\"moderation_blocked\",\"message\":\"rejected\"}}\n\n"
	got := extractOpenAIImagesUpstreamError([]byte(errorBody))
	if got == nil {
		t.Fatal("error event should still produce an upstream error")
	}
	if got.StatusCode != http.StatusBadRequest {
		t.Fatalf("moderation_blocked should be 400, got %d", got.StatusCode)
	}

	failedBody := "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\",\"message\":\"boom\"}}}\n\n"
	got = extractOpenAIImagesUpstreamError([]byte(failedBody))
	if got == nil {
		t.Fatal("response.failed event should still produce an upstream error")
	}
	if got.StatusCode != http.StatusBadGateway {
		t.Fatalf("server response.failed should be 502, got %d", got.StatusCode)
	}
}

func TestImagesOAuthNonStreaming_CompletedNoImageTriggersSameAccountRetry(t *testing.T) {
	upstreamSSE := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_x\",\"status\":\"in_progress\",\"model\":\"gpt-5.4-mini-2026-03-17\",\"output\":[]}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_x\",\"status\":\"completed\",\"model\":\"gpt-5.4-mini-2026-03-17\",\"output\":[],\"tool_usage\":{\"image_gen\":{\"output_tokens\":0}}}}\n\n"

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstreamSSE))}

	svc := &OpenAIGatewayService{}
	_, _, _, err := svc.handleOpenAIImagesOAuthNonStreamingResponse(resp, c, "b64_json", "gpt-image-2")
	if err == nil {
		t.Fatal("completed-but-no-image should return an error")
	}

	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("expected *UpstreamFailoverError to trigger retry, got %T: %v", err, err)
	}
	if failoverErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", failoverErr.StatusCode)
	}
	if !failoverErr.RetryableOnSameAccount {
		t.Fatal("soft no-image failures should prefer same-account retry")
	}
}

func TestImagesOAuthNonStreaming_ContentRefusalReturns400NoRetry(t *testing.T) {
	upstreamSSE := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"status\":\"in_progress\",\"model\":\"gpt-5.4-mini\",\"output\":[]}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"I'm sorry, this request was blocked by the safety policy.\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"model\":\"gpt-5.4-mini\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"I'm sorry, this request was blocked by the safety policy.\"}]}],\"tool_usage\":{\"image_gen\":{\"output_tokens\":0}}}}\n\n"

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstreamSSE))}

	svc := &OpenAIGatewayService{}
	_, _, _, err := svc.handleOpenAIImagesOAuthNonStreamingResponse(resp, c, "b64_json", "gpt-image-2")
	if err == nil {
		t.Fatal("content refusal should return an error")
	}

	var failoverErr *UpstreamFailoverError
	if errors.As(err, &failoverErr) {
		t.Fatalf("content refusal must not be a retryable failover error, got %v", failoverErr)
	}

	var imgErr *OpenAIImagesUpstreamError
	if !errors.As(err, &imgErr) {
		t.Fatalf("expected *OpenAIImagesUpstreamError, got %T: %v", err, err)
	}
	if imgErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("content refusal should be 400, got %d", imgErr.StatusCode)
	}
	if !strings.Contains(imgErr.Message, "safety policy") {
		t.Fatalf("refusal message should carry model reason, got %q", imgErr.Message)
	}
}
