package service

import (
	"bytes"
	"context"
	"fmt"
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

func newAnthropicHistoryGateway(responses ...*http.Response) (*OpenAIGatewayService, *httpUpstreamRecorder, *Account) {
	upstream := &httpUpstreamRecorder{responses: responses}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
	account := &Account{
		ID:          1,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Extra:       openAICompatResponsesExtra(),
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.openai.com/v1"},
	}
	return svc, upstream, account
}

func forwardAnthropicHistory(t *testing.T, svc *OpenAIGatewayService, account *Account, body []byte, sessionKey string) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, sessionKey, "gpt-5.3-codex")
	require.NoError(t, err)
	require.NotNil(t, result)
}

func longAnthropicToolHistory(count int) []byte {
	messages := []string{`{"role":"user","content":"ROOT_TASK: inspect and repair the repository"}`}
	for i := 0; i < count; i++ {
		callID := fmt.Sprintf("call_%d", i)
		messages = append(messages,
			fmt.Sprintf(`{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":"Read","input":{"file_path":"file.go"}}]}`, callID),
			fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"read result"},{"type":"text","text":"LATEST_TASK: fix the failing test"}]}`, callID),
		)
	}
	return []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[` + strings.Join(messages, ",") + `],"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}],"stream":false}`)
}

func TestAnthropicHistoryFullReplayWithoutPreviousResponse(t *testing.T) {
	for _, messageCount := range []int{11, 12, 13, 14} {
		t.Run(fmt.Sprintf("messages_%d", messageCount), func(t *testing.T) {
			messages := []string{`{"role":"user","content":"ROOT_BOUNDARY_TASK"}`}
			for i := 1; i < messageCount; i++ {
				role := "user"
				if i%2 == 1 {
					role = "assistant"
				}
				messages = append(messages, fmt.Sprintf(`{"role":%q,"content":"checkpoint-%d"}`, role, i))
			}
			body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[` + strings.Join(messages, ",") + `],"stream":false}`)
			svc, upstream, account := newAnthropicHistoryGateway(openAICompatSSECompletedResponse("resp_boundary", "gpt-5.3-codex"))
			forwardAnthropicHistory(t, svc, account, body, "")
			require.False(t, gjson.GetBytes(upstream.lastBody, "previous_response_id").Exists())
			require.Equal(t, int64(messageCount+1), gjson.GetBytes(upstream.lastBody, "input.#").Int())
			require.Contains(t, string(upstream.lastBody), "ROOT_BOUNDARY_TASK")
			require.Contains(t, string(upstream.lastBody), fmt.Sprintf("checkpoint-%d", messageCount-1))
		})
	}

	for _, sessionKey := range []string{"", "explicit-session"} {
		t.Run("tool_history_"+sessionKey, func(t *testing.T) {
			body := longAnthropicToolHistory(7)
			svc, upstream, account := newAnthropicHistoryGateway(openAICompatSSECompletedResponse("resp_tool", "gpt-5.3-codex"))
			forwardAnthropicHistory(t, svc, account, body, sessionKey)
			require.False(t, gjson.GetBytes(upstream.lastBody, "previous_response_id").Exists())
			require.Contains(t, string(upstream.lastBody), "ROOT_TASK")
			require.Contains(t, string(upstream.lastBody), "LATEST_TASK")
			require.Contains(t, string(upstream.lastBody), `"call_id":"call_0"`)
		})
	}
}

func TestAnthropicHistoryContentCacheKeysDoNotSelectPreviousResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  string
		branch string
	}{
		{
			name:   "digest",
			first:  `{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"shared root"}],"stream":false}`,
			branch: `{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"shared root"},{"role":"assistant","content":"different branch"},{"role":"user","content":"branch task"}],"stream":false}`,
		},
		{
			name:   "cache_control",
			first:  `{"model":"claude-sonnet-4-5","max_tokens":16,"system":[{"type":"text","text":"shared anchor","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"shared root"}],"stream":false}`,
			branch: `{"model":"claude-sonnet-4-5","max_tokens":16,"system":[{"type":"text","text":"shared anchor","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"shared root"},{"role":"assistant","content":"different branch"},{"role":"user","content":"branch task"}],"stream":false}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, upstream, account := newAnthropicHistoryGateway(
				openAICompatSSECompletedResponse("resp_sibling", "gpt-5.3-codex"),
				openAICompatSSECompletedResponse("resp_branch", "gpt-5.3-codex"),
			)
			forwardAnthropicHistory(t, svc, account, []byte(tc.first), "")
			forwardAnthropicHistory(t, svc, account, []byte(tc.branch), "")
			require.Len(t, upstream.bodies, 2)
			require.NotEmpty(t, gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").String())
			require.Equal(t, gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").String(), gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").String())
			require.False(t, gjson.GetBytes(upstream.bodies[1], "previous_response_id").Exists())
			require.Contains(t, string(upstream.bodies[1]), "different branch")
		})
	}
}

func TestAnthropicHistoryExplicitAndMetadataSessionsStillContinue(t *testing.T) {
	metadata := `{"user_id":"{\"device_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"account_uuid\":\"\",\"session_id\":\"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\"}"}`
	for _, tc := range []struct {
		name       string
		sessionKey string
		metadata   string
	}{
		{name: "explicit", sessionKey: "stable-session"},
		{name: "metadata", metadata: `,"metadata":` + metadata},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, upstream, account := newAnthropicHistoryGateway(
				openAICompatSSECompletedResponse("resp_first", "gpt-5.3-codex"),
				openAICompatSSECompletedResponse("resp_second", "gpt-5.3-codex"),
			)
			first := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16` + tc.metadata + `,"messages":[{"role":"user","content":"first task"}],"stream":false}`)
			second := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16` + tc.metadata + `,"messages":[{"role":"user","content":"first task"},{"role":"assistant","content":"first answer"},{"role":"user","content":"second task"}],"stream":false}`)
			forwardAnthropicHistory(t, svc, account, first, tc.sessionKey)
			forwardAnthropicHistory(t, svc, account, second, tc.sessionKey)
			require.Equal(t, "resp_first", gjson.GetBytes(upstream.bodies[1], "previous_response_id").String())
			require.NotEmpty(t, gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").String())
			require.Contains(t, string(upstream.bodies[1]), "second task")
		})
	}
}

func TestAnthropicHistoryMissingPreviousResponseRetriesWithFullHistory(t *testing.T) {
	unavailable := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"previous_response_id is not available for this user","type":"invalid_request_error"}}`)),
	}
	svc, upstream, account := newAnthropicHistoryGateway(unavailable, openAICompatSSECompletedResponse("resp_recovered", "gpt-5.3-codex"))
	svc.bindOpenAICompatSessionResponseID(context.Background(), nil, account, "stable-session", "resp_missing")
	forwardAnthropicHistory(t, svc, account, longAnthropicToolHistory(7), "stable-session")
	require.Len(t, upstream.bodies, 2)
	require.Equal(t, "resp_missing", gjson.GetBytes(upstream.bodies[0], "previous_response_id").String())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "previous_response_id").Exists())
	require.Contains(t, string(upstream.bodies[1]), "ROOT_TASK")
	require.Contains(t, string(upstream.bodies[1]), `"call_id":"call_0"`)
}
