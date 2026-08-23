package service

import (
	"context"
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

func TestHandleNonStreamingResponse_GrokCompactPathConvertsResponseBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)

	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	jsonBody := `{"output":[` +
		`{"type":"reasoning","encrypted_content":"grok-enc-state"},` +
		`{"type":"message","content":[{"text":"the summary"}]}` +
		`],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(jsonBody)),
	}
	account := &Account{ID: 1, Platform: PlatformGrok, Type: AccountTypeOAuth}

	result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "grok-4.5", "grok-4.5")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 3, result.InputTokens)
	require.Equal(t, 2, result.OutputTokens)
	require.Equal(t, "compaction", gjson.Get(rec.Body.String(), "output.0.type").String())
	require.Equal(t, "grok-enc-state", gjson.Get(rec.Body.String(), "output.0.encrypted_content").String())
	require.Equal(t, "the summary", gjson.Get(rec.Body.String(), "output.0.summary.0.text").String())
}

func TestHandleNonStreamingResponse_GrokCompactPathWrapsConversionError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)

	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	// No reasoning.encrypted_content anywhere in output -> convertGrokResponseToOpenAICompact errors.
	jsonBody := `{"output":[{"type":"message","content":[{"text":"no reasoning here"}]}]}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(jsonBody)),
	}
	account := &Account{ID: 1, Platform: PlatformGrok, Type: AccountTypeOAuth}

	result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "grok-4.5", "grok-4.5")
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "convert Grok compact response")
}

func TestHandleNonStreamingResponse_GrokNonCompactPathSkipsConversion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	// Native Grok Responses output shape, not a compaction request -> must pass through untouched.
	jsonBody := `{"output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],` +
		`"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(jsonBody)),
	}
	account := &Account{ID: 1, Platform: PlatformGrok, Type: AccountTypeOAuth}

	result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "grok-4.5", "grok-4.5")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "message", gjson.Get(rec.Body.String(), "output.0.type").String())
	require.False(t, gjson.Get(rec.Body.String(), "output.0.type").Exists() && gjson.Get(rec.Body.String(), "output.0.type").String() == "compaction")
}

func TestHandleNonStreamingResponse_NonGrokCompactPathSkipsConversion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)

	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	// Same "no reasoning.encrypted_content" shape that would error for Grok — for a
	// non-Grok account the conversion must never run, so this must succeed untouched.
	jsonBody := `{"output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],` +
		`"usage":{"input_tokens":4,"output_tokens":5,"total_tokens":9}}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(jsonBody)),
	}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "gpt-5.4", "gpt-5.4")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 4, result.InputTokens)
	require.Equal(t, "message", gjson.Get(rec.Body.String(), "output.0.type").String())
}

// dropOpenAIEncryptedReasoningInputItems is exercised for the []any case via
// SanitizeOpenAICrossModeFailoverReasoning in openai_gateway_cross_mode_reasoning_test.go.
// This adds the []map[string]any and bare map[string]any branches, plus the
// "every item dropped" case, none of which that file reaches.
func TestDropOpenAIEncryptedReasoningInputItems_AdditionalShapes(t *testing.T) {
	t.Run("[]map[string]any input drops the encrypted reasoning item", func(t *testing.T) {
		reqBody := map[string]any{
			"input": []map[string]any{
				{"type": "reasoning", "id": "rs_1", "encrypted_content": "ENC"},
				{"type": "message", "content": "hi"},
			},
		}
		changed := dropOpenAIEncryptedReasoningInputItems(reqBody)
		require.True(t, changed)
		input, ok := reqBody["input"].([]map[string]any)
		require.True(t, ok)
		require.Len(t, input, 1)
		require.Equal(t, "message", input[0]["type"])
	})

	t.Run("[]map[string]any with nothing to drop reports no change", func(t *testing.T) {
		reqBody := map[string]any{
			"input": []map[string]any{{"type": "message", "content": "hi"}},
		}
		changed := dropOpenAIEncryptedReasoningInputItems(reqBody)
		require.False(t, changed)
	})

	t.Run("[]map[string]any dropping every item deletes the input key", func(t *testing.T) {
		reqBody := map[string]any{
			"input": []map[string]any{{"type": "reasoning", "encrypted_content": "ENC"}},
		}
		changed := dropOpenAIEncryptedReasoningInputItems(reqBody)
		require.True(t, changed)
		_, has := reqBody["input"]
		require.False(t, has)
	})

	t.Run("bare map[string]any encrypted reasoning item deletes the input key", func(t *testing.T) {
		reqBody := map[string]any{
			"input": map[string]any{"type": "reasoning", "encrypted_content": "ENC"},
		}
		changed := dropOpenAIEncryptedReasoningInputItems(reqBody)
		require.True(t, changed)
		_, has := reqBody["input"]
		require.False(t, has)
	})

	t.Run("bare map[string]any without encrypted content reports no change", func(t *testing.T) {
		reqBody := map[string]any{
			"input": map[string]any{"type": "message"},
		}
		changed := dropOpenAIEncryptedReasoningInputItems(reqBody)
		require.False(t, changed)
	})

	t.Run("[]any dropping every item deletes the input key", func(t *testing.T) {
		reqBody := map[string]any{
			"input": []any{map[string]any{"type": "reasoning", "encrypted_content": "ENC"}},
		}
		changed := dropOpenAIEncryptedReasoningInputItems(reqBody)
		require.True(t, changed)
		_, has := reqBody["input"]
		require.False(t, has)
	})

	t.Run("empty reqBody is a no-op", func(t *testing.T) {
		require.False(t, dropOpenAIEncryptedReasoningInputItems(nil))
		require.False(t, dropOpenAIEncryptedReasoningInputItems(map[string]any{}))
	})

	t.Run("missing input key is a no-op", func(t *testing.T) {
		require.False(t, dropOpenAIEncryptedReasoningInputItems(map[string]any{"model": "gpt-5.1"}))
	})

	t.Run("unsupported input shape is a no-op", func(t *testing.T) {
		require.False(t, dropOpenAIEncryptedReasoningInputItems(map[string]any{"input": "not-a-list"}))
	})
}

func TestIsOpenAIEncryptedReasoningInputItem(t *testing.T) {
	require.True(t, isOpenAIEncryptedReasoningInputItem(map[string]any{"type": "reasoning", "encrypted_content": "ENC"}))
	require.False(t, isOpenAIEncryptedReasoningInputItem(map[string]any{"type": "reasoning"}))
	require.False(t, isOpenAIEncryptedReasoningInputItem(map[string]any{"type": "message", "encrypted_content": "ENC"}))
	require.False(t, isOpenAIEncryptedReasoningInputItem("not-a-map"))
}
