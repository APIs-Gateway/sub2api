package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOIDCTokenCaseParsing(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       oidcTokenResponse
		valid      bool
	}{
		{"camel", `{"accessToken":" access ","refreshToken":"refresh","tokenType":"Bearer","expiresIn":7200,"scope":"email"}`, oidcTokenResponse{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", ExpiresIn: 7200, Scope: "email"}, true},
		{"standard preferred", `{"access_token":"standard","accessToken":"alternate","refresh_token":"standard-refresh","refreshToken":"alternate-refresh","token_type":"Bearer","tokenType":"Other","expires_in":60,"expiresIn":7200}`, oidcTokenResponse{AccessToken: "standard", RefreshToken: "standard-refresh", TokenType: "Bearer", ExpiresIn: 60}, true},
		{"blank standard fallback", `{"access_token":" ","accessToken":"access","refresh_token":"","refreshToken":"refresh","token_type":" ","tokenType":"Bearer","expiresIn":60}`, oidcTokenResponse{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", ExpiresIn: 60}, true},
		{"explicit standard zero", `{"accessToken":"access","expires_in":0,"expiresIn":7200}`, oidcTokenResponse{AccessToken: "access"}, true},
		{"invalid standard does not select alternate", `{"accessToken":"access","expires_in":"invalid","expiresIn":7200}`, oidcTokenResponse{AccessToken: "access"}, true},
		{"numeric string expiry", `{"accessToken":"access","expiresIn":"7200"}`, oidcTokenResponse{AccessToken: "access", ExpiresIn: 7200}, true},
		{"invalid alias expiry", `{"accessToken":"access","expiresIn":"invalid"}`, oidcTokenResponse{AccessToken: "access"}, true},
		{"id token only", `{"id_token":"signed-id","refreshToken":"refresh","expiresIn":60}`, oidcTokenResponse{IDToken: "signed-id", RefreshToken: "refresh", ExpiresIn: 60}, true},
		{"standard form unchanged", `access_token=access&refresh_token=refresh&token_type=Bearer&expires_in=60&scope=email`, oidcTokenResponse{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", ExpiresIn: 60, Scope: "email"}, true},
		{"invalid form expiry", `access_token=access&expires_in=invalid&expiresIn=7200`, oidcTokenResponse{AccessToken: "access"}, true},
		{"camel form remains unsupported", `accessToken=access&expiresIn=7200`, oidcTokenResponse{}, false},
		{"missing tokens", `{"tokenType":"Bearer","expiresIn":60}`, oidcTokenResponse{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, valid := oidcParseTokenResponse(tc.body)
			require.Equal(t, tc.valid, valid)
			if tc.valid {
				require.Equal(t, tc.want, *got)
			} else {
				require.Nil(t, got)
			}
		})
	}
}

func TestOIDCTokenCaseExchangeAndUserInfo(t *testing.T) {
	for _, tc := range []struct {
		name, body, authorization string
		userinfoError             bool
	}{
		{"camel explicit bearer", `{"accessToken":"fixture-access","refreshToken":"fixture-refresh","tokenType":"Bearer","expiresIn":60}`, "Bearer fixture-access", false},
		{"default bearer", `{"accessToken":"fixture-access"}`, "Bearer fixture-access", false},
		{"standard auth wins", `{"access_token":"standard-access","accessToken":"alternate-access","token_type":"Bearer","tokenType":"Other"}`, "Bearer standard-access", false},
		{"reject unsafe token", `{"accessToken":"unsafe\r\nHeader: injection"}`, "", true},
		{"reject unsafe scheme", `{"accessToken":"fixture-access","tokenType":"Bearer\r\nHeader: injection"}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokenCalls, userInfoCalls := 0, 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					tokenCalls++
					require.Equal(t, http.MethodPost, r.Method)
					require.NoError(t, r.ParseForm())
					require.Equal(t, "authorization_code", r.PostForm.Get("grant_type"))
					require.Equal(t, "fixture-code", r.PostForm.Get("code"))
					require.Equal(t, "fixture-verifier", r.PostForm.Get("code_verifier"))
					require.Equal(t, "fixture-secret", r.PostForm.Get("client_secret"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tc.body))
				case "/userinfo":
					userInfoCalls++
					require.Equal(t, tc.authorization, r.Header.Get("Authorization"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"sub":"fixture-sub","email":"fixture@example.com","email_verified":true}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			cfg := config.OIDCConnectConfig{ClientID: "fixture-client", ClientSecret: "fixture-secret", TokenURL: upstream.URL + "/token", UserInfoURL: upstream.URL + "/userinfo", TokenAuthMethod: "client_secret_post"}
			token, err := oidcExchangeCode(context.Background(), cfg, "fixture-code", "https://app.example.com/callback", "fixture-verifier")
			require.NoError(t, err)
			require.Equal(t, 1, tokenCalls)
			claims, err := oidcFetchUserInfo(context.Background(), cfg, token)
			if tc.userinfoError {
				require.Error(t, err)
				require.Nil(t, claims)
				require.Zero(t, userInfoCalls)
			} else {
				require.NoError(t, err)
				require.Equal(t, 1, userInfoCalls)
				require.Equal(t, "fixture-sub", claims.Subject)
			}
		})
	}
}

func TestOIDCTokenCaseActualCallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"accessToken":"fixture-access","tokenType":"Bearer","expiresIn":60}`))
		} else {
			require.Equal(t, "/userinfo", r.URL.Path)
			require.Equal(t, "Bearer fixture-access", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"sub":"fixture-sub","email":"fixture@example.com"}`))
		}
	}))
	defer upstream.Close()
	h, client := newOIDCOAuthHandlerAndClient(t, false, config.OIDCConnectConfig{
		Enabled: true, ClientID: "fixture-client", ClientSecret: "fixture-secret", IssuerURL: upstream.URL,
		AuthorizeURL: upstream.URL + "/authorize", TokenURL: upstream.URL + "/token", UserInfoURL: upstream.URL + "/userinfo",
		Scopes: "openid email", RedirectURL: "https://api.example.com/api/v1/auth/oauth/oidc/callback",
		FrontendRedirectURL: "/auth/oidc/callback", TokenAuthMethod: "client_secret_post",
	})
	t.Cleanup(func() { _ = client.Close() })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/oidc/callback?code=fixture-code&state=fixture-state", nil)
	c.Request.AddCookie(encodedCookie(oidcOAuthStateCookieName, "fixture-state"))
	c.Request.AddCookie(encodedCookie(oidcOAuthRedirectCookie, "/dashboard"))
	c.Request.AddCookie(encodedCookie(oidcOAuthIntentCookieName, oauthIntentLogin))
	c.Request.AddCookie(encodedCookie(oauthPendingBrowserCookieName, "fixture-browser"))
	h.OIDCOAuthCallback(c)
	require.Equal(t, http.StatusFound, recorder.Code)
	require.Equal(t, "/auth/oidc/callback", recorder.Header().Get("Location"))
	require.NotNil(t, findCookie(recorder.Result().Cookies(), oauthPendingSessionCookieName))
}
