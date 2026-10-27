package handler

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestTotpLoginClaimMatchesVerifiedSession(t *testing.T) {
	expected := &service.TotpLoginSession{UserID: 7, Email: "fixture@example.com", TokenExpiry: time.Now()}
	copy := *expected
	require.True(t, sameTotpLoginSession(expected, &copy))
	require.False(t, sameTotpLoginSession(expected, nil))
	require.False(t, sameTotpLoginSession(nil, &copy))
	for _, change := range []func(*service.TotpLoginSession){
		func(s *service.TotpLoginSession) { s.UserID++ },
		func(s *service.TotpLoginSession) { s.Email = "other@example.com" },
		func(s *service.TotpLoginSession) { s.TokenExpiry = s.TokenExpiry.Add(time.Second) },
		func(s *service.TotpLoginSession) {
			s.PendingOAuthBind = &service.PendingOAuthBindLoginSession{PendingSessionToken: "other"}
		},
	} {
		copy := *expected
		change(&copy)
		require.False(t, sameTotpLoginSession(expected, &copy))
	}
	expected.PendingOAuthBind = &service.PendingOAuthBindLoginSession{PendingSessionToken: "pending", BrowserSessionKey: "browser"}
	copy = *expected
	pendingCopy := *expected.PendingOAuthBind
	copy.PendingOAuthBind = &pendingCopy
	require.True(t, sameTotpLoginSession(expected, &copy))
	copy.PendingOAuthBind.BrowserSessionKey = "other-browser"
	require.False(t, sameTotpLoginSession(expected, &copy))
}
