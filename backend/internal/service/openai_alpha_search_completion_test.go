package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAlphaSearchPATFallbackRejectsUnsuccessfulResponsesStreams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	delta := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"unfinished answer\"}\n\n"
	completed := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"
	cases := map[string]string{
		"empty":                     "",
		"truncated":                 delta,
		"done_only":                 "data: [DONE]\n\n",
		"done_without_completion":   delta + "data: [DONE]\n\n",
		"failed":                    delta + "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n",
		"incomplete":                delta + "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n",
		"error":                     delta + "data: {\"type\":\"error\",\"error\":{\"message\":\"unavailable\"}}\n\n",
		"non_success_completion":    delta + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"failed\",\"output\":[]}}\n\n",
		"missing_response":          delta + "data: {\"type\":\"response.completed\"}\n\n",
		"missing_status":            delta + "data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n",
		"missing_output":            delta + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n",
		"missing_event_type":        delta + "data: {\"delta\":\"ignored\"}\n\n" + completed,
		"completion_with_error":     delta + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"error\":{\"message\":\"bad\"}}}\n\n",
		"malformed_before_terminal": delta + "data: {bad json}\n\n" + completed,
		"malformed_after_terminal":  delta + completed + "data: {bad json}\n\n",
		"event_after_terminal":      delta + completed + delta,
		"duplicate_terminal":        delta + completed + completed,
		"done_before_terminal":      delta + "data: [DONE]\n\n" + completed,
	}
	for name, wire := range cases {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.5","commands":{"search_query":[{"q":"example"}]}}`)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(wire)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
				Credentials: map[string]any{"access_token": "at-test-token", "auth_mode": OpenAIAuthModePersonalAccessToken, "chatgpt_account_id": "fixture-account"}}
			err := svc.ForwardAlphaSearch(context.Background(), c, account, body)
			require.Error(t, err)
			require.False(t, c.Writer.Written(), "do not write partial content as a successful search")
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestAlphaSearchPATFallbackCompletionPreservesOutputAndCitations(t *testing.T) {
	terminal := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"completed answer\",\"annotations\":[{\"type\":\"url_citation\",\"url\":\"https://example.com/news\",\"title\":\"Example News\"}]}]}]}}\n\n"
	for name, wire := range map[string]string{
		"streamed_text_first": "data: {\"type\":\"response.output_text.delta\",\"delta\":\"streamed answer\"}\n\n" + terminal + "data: [DONE]\n\n",
		"completed_text_only": terminal,
	} {
		t.Run(name, func(t *testing.T) {
			body, err := openAIAlphaSearchResponseFromResponsesSSE([]byte(wire))
			require.NoError(t, err)
			wantOutput := "completed answer"
			if name == "streamed_text_first" {
				wantOutput = "streamed answer"
			}
			require.JSONEq(t, `{"output":"`+wantOutput+`","results":[{"type":"text_result","ref_id":"turn0search0","url":"https://example.com/news","title":"Example News"}]}`, string(body))
		})
	}
}
