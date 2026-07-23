//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIsGrokContentPolicyRejection(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "new sensitive code", status: http.StatusForbidden, body: `{"error":{"code":"new_sensitive","message":"image is sensitive"}}`, want: true},
		{name: "nested content policy code", status: http.StatusForbidden, body: `{"response":{"error":{"code":"content_policy_violation"}}}`, want: true},
		{name: "cyber policy code", status: http.StatusForbidden, body: `{"error":{"code":"cyber_policy","message":"request rejected"}}`, want: true},
		{name: "moderation unavailable", status: http.StatusForbidden, body: `{"error":{"message":"The moderation feature is not available for this request"}}`, want: true},
		{name: "request moderation rejection", status: http.StatusForbidden, body: `{"error":{"message":"request rejected by content moderation"}}`, want: true},
		{name: "entitlement forbidden", status: http.StatusForbidden, body: `{"error":{"message":"subscription required"}}`, want: false},
		{name: "account suspension", status: http.StatusForbidden, body: `{"error":{"message":"account suspended due to policy violation"}}`, want: false},
		{name: "account marker wins", status: http.StatusForbidden, body: `{"error":{"code":"account_suspended","reason":"policy_violation","message":"request blocked by policy"}}`, want: false},
		{name: "ambiguous policy code", status: http.StatusForbidden, body: `{"error":{"code":"policy_violation","message":"policy violation"}}`, want: false},
		{name: "request scoped policy message", status: http.StatusForbidden, body: `{"error":{"code":"policy_violation","message":"request blocked by policy"}}`, want: true},
		{name: "wrong status", status: http.StatusBadRequest, body: `{"error":{"code":"new_sensitive"}}`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isGrokContentPolicyRejection(tt.status, []byte(tt.body)))
		})
	}
}

func TestGrokContentPolicy403DoesNotMutateOrFailover(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 4715, Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"code":"new_sensitive","message":"text is sensitive"}}`)

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusForbidden, nil, body)

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.False(t, svc.shouldFailoverGrokUpstreamError(http.StatusForbidden, body))
	require.True(t, svc.shouldFailoverGrokUpstreamError(http.StatusForbidden, []byte(`{"error":{"message":"subscription required"}}`)))
}

func TestGrokAccountUpstreamError402TempUnschedulesAndRecovers(t *testing.T) {
	repo := &openaiTransportAccountRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 610, Platform: PlatformGrok, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}
	before := time.Now()

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusPaymentRequired, nil, nil)

	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Len(t, repo.tempUnschedCalls, 1)
	require.Equal(t, int64(610), repo.tempUnschedCalls[0].accountID)
	require.Equal(t, "grok payment required", repo.tempUnschedCalls[0].reason)
	require.True(t, repo.tempUnschedCalls[0].until.After(before.Add(30*time.Minute-time.Second)))
	require.True(t, repo.tempUnschedCalls[0].until.Before(before.Add(30*time.Minute+time.Second)))

	expired := time.Now().Add(-time.Second)
	account.TempUnschedulableUntil = &expired
	svc.openaiAccountRuntimeBlockUntil.Store(account.ID, expired)

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.True(t, account.IsSchedulable())
}

func TestForwardGrokResponsesContentPolicy403ReturnsInvalidRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	body := []byte(`{"error":{"code":"new_sensitive","message":"image is sensitive"}}`)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:       4716,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "grok-access-token",
			"base_url":     "https://api.x.ai/v1",
		},
	}

	result, err := svc.forwardGrokResponses(
		context.Background(), c, account,
		[]byte(`{"model":"grok-4.3","input":"hi","stream":false}`),
		"grok-4.3", false, time.Now(),
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
	require.Equal(t, "image is sensitive", gjson.Get(recorder.Body.String(), "error.message").String())
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}
