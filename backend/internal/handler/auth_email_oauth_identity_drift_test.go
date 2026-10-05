package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/authidentity"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestEmailOAuthBoundIdentityPublicCallback(t *testing.T) {
	for _, provider := range []string{"google", "github"} {
		for _, anotherOwner := range []bool{false, true} {
			name := provider + "/new_email"
			if anotherOwner {
				name += "_owned_by_another_account"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				h, client, requests := emailDriftPublicHandler(t, provider, "new@example.com", "12345", true, emailDriftGrantSettings(provider))
				owner := emailDriftUser(t, h, client, "site@example.com")
				emailDriftBindIdentity(t, client, owner.ID, provider, "12345", "old@example.com")
				var other *dbent.User
				if anotherOwner {
					other = emailDriftUser(t, h, client, "new@example.com")
				}
				// A previous authenticated bind already received its one-time defaults.
				require.NoError(t, h.authService.ApplyProviderDefaultSettingsOnFirstBind(ctx, owner.ID, provider))
				before, err := client.User.Get(ctx, owner.ID)
				require.NoError(t, err)
				userCount, err := client.User.Query().Count(ctx)
				require.NoError(t, err)

				for range 2 {
					location := emailDriftPublicCallback(t, h, provider, "state", provider)
					require.Equal(t, owner.ID, emailDriftTokenOwner(t, h, location))
					stored, err := client.User.Get(ctx, owner.ID)
					require.NoError(t, err)
					require.Equal(t, before.Email, stored.Email)
					require.Equal(t, before.Username, stored.Username)
					require.Equal(t, before.Balance, stored.Balance)
					require.Equal(t, before.Concurrency, stored.Concurrency)
					require.Equal(t, before.TotalRecharged, stored.TotalRecharged)
					require.Equal(t, 1, countProviderGrantRecords(t, client, owner.ID, provider, "first_bind"))
				}
				count, err := client.User.Query().Count(ctx)
				require.NoError(t, err)
				require.Equal(t, userCount, count)
				pending, err := client.PendingAuthSession.Query().Count(ctx)
				require.NoError(t, err)
				require.Zero(t, pending)
				identity := emailDriftIdentity(t, client, provider, "12345")
				require.Equal(t, owner.ID, identity.UserID)
				require.Equal(t, "new@example.com", identity.Metadata["email"])
				require.Equal(t, true, identity.Metadata["email_verified"])
				require.Equal(t, int32(2), requests.Load())
				if other != nil {
					reloaded, err := client.User.Get(ctx, other.ID)
					require.NoError(t, err)
					require.Equal(t, other.Balance, reloaded.Balance)
					require.Equal(t, other.Email, reloaded.Email)
					identities, err := client.AuthIdentity.Query().Where(authidentity.UserIDEQ(other.ID)).Count(ctx)
					require.NoError(t, err)
					require.Zero(t, identities)
					require.Zero(t, countProviderGrantRecords(t, client, other.ID, provider, "first_bind"))
				}
			})
		}
	}
}

func TestEmailOAuthBoundIdentityAfterAuthenticatedPendingBind(t *testing.T) {
	for _, provider := range []string{"google", "github"} {
		t.Run(provider, func(t *testing.T) {
			ctx := context.Background()
			h, client, _ := emailDriftPublicHandler(t, provider, "changed@example.com", "12345", true, emailDriftGrantSettings(provider))
			owner := emailDriftUser(t, h, client, "site@example.com")
			session, err := client.PendingAuthSession.Create().
				SetSessionToken("email-drift-bind-session").
				SetIntent("adopt_existing_user_by_email").
				SetProviderType(provider).
				SetProviderKey(provider).
				SetProviderSubject("12345").
				SetTargetUserID(owner.ID).
				SetResolvedEmail("original-provider@example.com").
				SetBrowserSessionKey("email-drift-browser").
				SetUpstreamIdentityClaims(map[string]any{"email": "original-provider@example.com", "email_verified": true}).
				SetExpiresAt(time.Now().UTC().Add(time.Minute)).
				Save(ctx)
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/bind-login", strings.NewReader(`{"email":"site@example.com","password":"secret-123","adopt_display_name":false,"adopt_avatar":false}`))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.AddCookie(encodedCookie(oauthPendingSessionCookieName, session.SessionToken))
			c.Request.AddCookie(encodedCookie(oauthPendingBrowserCookieName, session.BrowserSessionKey))
			h.BindPendingOAuthLogin(c)
			require.Equal(t, http.StatusOK, recorder.Code)
			payload := decodeJSONResponseData(t, recorder)
			claims, err := h.authService.ValidateToken(payload["access_token"].(string))
			require.NoError(t, err)
			require.Equal(t, owner.ID, claims.UserID)
			bound := emailDriftIdentity(t, client, provider, "12345")
			require.Equal(t, owner.ID, bound.UserID)
			consumed, err := client.PendingAuthSession.Get(ctx, session.ID)
			require.NoError(t, err)
			require.NotNil(t, consumed.ConsumedAt)
			before, err := client.User.Get(ctx, owner.ID)
			require.NoError(t, err)
			require.Equal(t, 17.5, before.Balance)
			require.Equal(t, 6, before.Concurrency)

			location := emailDriftPublicCallback(t, h, provider, "state", provider)
			require.Equal(t, owner.ID, emailDriftTokenOwner(t, h, location))
			reloaded, err := client.User.Get(ctx, owner.ID)
			require.NoError(t, err)
			require.Equal(t, "site@example.com", reloaded.Email)
			require.Equal(t, before.Balance, reloaded.Balance)
			require.Equal(t, before.Concurrency, reloaded.Concurrency)
			require.Equal(t, 1, countProviderGrantRecords(t, client, owner.ID, provider, "first_bind"))
		})
	}
}

func TestEmailOAuthBoundIdentityPublicSafetyControls(t *testing.T) {
	for _, scenario := range []string{"invalid_state", "wrong_provider_cookie", "unverified", "wrong_subject", "different_provider", "disabled", "backend_mode"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			settings := map[string]string{}
			if scenario == "backend_mode" {
				settings[service.SettingKeyBackendModeEnabled] = "true"
			}
			provider, subject, email := "google", "12345", "changed@example.com"
			if scenario == "wrong_subject" {
				subject, email = "wrong-subject", "site@example.com"
			}
			if scenario == "different_provider" {
				provider, email = "github", "site@example.com"
			}
			if scenario == "disabled" || scenario == "backend_mode" {
				email = "site@example.com"
			}
			h, client, requests := emailDriftPublicHandler(t, provider, email, subject, scenario != "unverified", settings)
			owner := emailDriftUser(t, h, client, "site@example.com")
			emailDriftBindIdentity(t, client, owner.ID, "google", "12345", "old@example.com")
			if scenario == "disabled" {
				require.NoError(t, client.User.UpdateOneID(owner.ID).SetStatus("disabled").Exec(ctx))
			}
			state, cookieProvider := "state", provider
			if scenario == "invalid_state" {
				state = "different-state"
			}
			if scenario == "wrong_provider_cookie" {
				cookieProvider = "github"
			}
			location := emailDriftPublicCallback(t, h, provider, state, cookieProvider)
			parsed, err := url.Parse(location)
			require.NoError(t, err)
			fragment, err := url.ParseQuery(parsed.Fragment)
			require.NoError(t, err)
			require.Empty(t, fragment.Get("access_token"))
			expected := map[string]string{
				"invalid_state":         "invalid_state",
				"wrong_provider_cookie": "invalid_state",
				"unverified":            "userinfo_failed",
				"wrong_subject":         "OAUTH_EXISTING_ACCOUNT_BIND_REQUIRED",
				"different_provider":    "OAUTH_EXISTING_ACCOUNT_BIND_REQUIRED",
				"disabled":              "USER_NOT_ACTIVE",
				"backend_mode":          "login_blocked",
			}
			require.Equal(t, expected[scenario], fragment.Get("error"))
			if scenario == "invalid_state" || scenario == "wrong_provider_cookie" {
				require.Zero(t, requests.Load())
			}
			stored, err := client.User.Get(ctx, owner.ID)
			require.NoError(t, err)
			require.Equal(t, owner.Email, stored.Email)
			require.Equal(t, owner.Balance, stored.Balance)
			require.Equal(t, owner.TotalRecharged, stored.TotalRecharged)
			identity := emailDriftIdentity(t, client, "google", "12345")
			require.Equal(t, owner.ID, identity.UserID)
			count, err := client.AuthIdentity.Query().Where(authidentity.ProviderTypeEQ("google")).Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, count)
			otherProvider, err := client.AuthIdentity.Query().Where(authidentity.ProviderTypeEQ("github")).Count(ctx)
			require.NoError(t, err)
			require.Zero(t, otherProvider)
		})
	}
}

func TestEmailOAuthBoundIdentityServicePolicyControls(t *testing.T) {
	for _, scenario := range []string{"invalid_email", "reserved_email", "suffix_policy", "unverified", "different_provider_key", "oidc_mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			settings := map[string]string{}
			if scenario == "suffix_policy" {
				settings[service.SettingKeyRegistrationEmailSuffixWhitelist] = `["allowed.example"]`
			}
			h, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{settingValues: settings})
			owner := emailDriftUser(t, h, client, "site@example.com")
			provider, key := "google", "google"
			if scenario == "oidc_mismatch" {
				provider, key = "oidc", "oidc"
			}
			emailDriftBindIdentity(t, client, owner.ID, provider, "12345", "old@example.com")
			input := service.EmailOAuthIdentityInput{ProviderType: provider, ProviderKey: key, ProviderSubject: "12345", Email: "site@example.com", EmailVerified: true}
			expected := ""
			switch scenario {
			case "invalid_email":
				input.Email, expected = "invalid", "INVALID_EMAIL"
			case "reserved_email":
				input.Email, expected = "reserved"+service.LinuxDoConnectSyntheticEmailDomain, "EMAIL_RESERVED"
			case "suffix_policy":
				input.Email, expected = "changed@denied.example", "EMAIL_SUFFIX_NOT_ALLOWED"
			case "unverified":
				input.EmailVerified, expected = false, "OAUTH_EMAIL_NOT_VERIFIED"
			case "different_provider_key":
				input.ProviderKey, expected = "another-google-key", "OAUTH_EXISTING_ACCOUNT_BIND_REQUIRED"
			case "oidc_mismatch":
				input.Email, expected = "changed@example.com", "AUTH_IDENTITY_EMAIL_MISMATCH"
			}
			tokens, user, err := h.authService.LoginOrRegisterVerifiedEmailOAuth(context.Background(), input)
			require.Error(t, err)
			require.Nil(t, tokens)
			require.Nil(t, user)
			require.Contains(t, err.Error(), expected)
			reloaded, err := client.User.Get(context.Background(), owner.ID)
			require.NoError(t, err)
			require.Equal(t, owner.Email, reloaded.Email)
			require.Equal(t, owner.Balance, reloaded.Balance)
			require.Equal(t, "old@example.com", emailDriftIdentity(t, client, provider, "12345").Metadata["email"])
		})
	}
}

func emailDriftPublicHandler(t *testing.T, provider, email, subject string, verified bool, settings map[string]string) (*AuthHandler, *dbent.Client, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			requests.Add(1)
			if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "fixture-code" {
				http.Error(w, "invalid fixture authorization exchange", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-provider-token", "token_type": "Bearer"})
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer fixture-provider-token" {
				http.Error(w, "invalid fixture provider authorization", http.StatusUnauthorized)
				return
			}
			if provider == "google" {
				_ = json.NewEncoder(w).Encode(map[string]any{"sub": subject, "email": email, "email_verified": verified})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": subject, "login": "provider-name"})
			}
		case "/emails":
			if r.Header.Get("Authorization") != "Bearer fixture-provider-token" {
				http.Error(w, "invalid fixture provider authorization", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"email": email, "primary": true, "verified": verified}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	h, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{settingValues: settings})
	cfg := &config.Config{}
	providerConfig := config.EmailOAuthProviderConfig{
		Enabled:             true,
		ClientID:            "fixture-client",
		ClientSecret:        "fixture-secret",
		AuthorizeURL:        upstream.URL + "/authorize",
		TokenURL:            upstream.URL + "/token",
		UserInfoURL:         upstream.URL + "/userinfo",
		EmailsURL:           upstream.URL + "/emails",
		RedirectURL:         "https://app.example/api/v1/auth/oauth/" + provider + "/callback",
		FrontendRedirectURL: "/auth/oauth/callback",
	}
	if provider == "google" {
		cfg.GoogleOAuth = providerConfig
	} else {
		cfg.GitHubOAuth = providerConfig
	}
	// AuthService keeps its original real grant settings; handler provider URLs
	// point to the trusted exchange/userinfo fixture through the public callback.
	h.settingSvc = service.NewSettingService(&oauthPendingFlowSettingRepoStub{values: settings}, cfg)
	return h, client, requests
}

func emailDriftPublicCallback(t *testing.T, h *AuthHandler, provider, state, cookieProvider string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/"+provider+"/callback?code=fixture-code&state="+url.QueryEscape(state), nil)
	c.Request.AddCookie(encodedCookie(emailOAuthStateCookieName, "state"))
	c.Request.AddCookie(encodedCookie(emailOAuthProviderCookie, cookieProvider))
	c.Request.AddCookie(encodedCookie(emailOAuthRedirectCookie, "/dashboard"))
	if provider == "google" {
		h.GoogleOAuthCallback(c)
	} else {
		h.GitHubOAuthCallback(c)
	}
	require.Equal(t, http.StatusFound, recorder.Code)
	return recorder.Header().Get("Location")
}

func emailDriftTokenOwner(t *testing.T, h *AuthHandler, location string) int64 {
	t.Helper()
	parsed, err := url.Parse(location)
	require.NoError(t, err)
	fragment, err := url.ParseQuery(parsed.Fragment)
	require.NoError(t, err)
	require.Empty(t, fragment.Get("error"))
	require.NotEmpty(t, fragment.Get("access_token"))
	claims, err := h.authService.ValidateToken(fragment.Get("access_token"))
	require.NoError(t, err)
	return claims.UserID
}

func emailDriftUser(t *testing.T, h *AuthHandler, client *dbent.Client, email string) *dbent.User {
	t.Helper()
	hash, err := h.authService.HashPassword("secret-123")
	require.NoError(t, err)
	user, err := client.User.Create().SetEmail(email).SetUsername("site-name").SetPasswordHash(hash).
		SetRole(service.RoleUser).SetStatus(service.StatusActive).SetSignupSource("email").
		SetBalance(5).SetConcurrency(4).SetTotalRecharged(9).Save(context.Background())
	require.NoError(t, err)
	return user
}

func emailDriftBindIdentity(t *testing.T, client *dbent.Client, userID int64, provider, subject, email string) {
	t.Helper()
	_, err := client.AuthIdentity.Create().SetUserID(userID).SetProviderType(provider).SetProviderKey(provider).
		SetProviderSubject(subject).SetMetadata(map[string]any{"email": email, "email_verified": true}).Save(context.Background())
	require.NoError(t, err)
}

func emailDriftIdentity(t *testing.T, client *dbent.Client, provider, subject string) *dbent.AuthIdentity {
	t.Helper()
	identity, err := client.AuthIdentity.Query().Where(authidentity.ProviderTypeEQ(provider), authidentity.ProviderKeyEQ(provider), authidentity.ProviderSubjectEQ(subject)).Only(context.Background())
	require.NoError(t, err)
	return identity
}

func emailDriftGrantSettings(provider string) map[string]string {
	if provider == "google" {
		return map[string]string{
			service.SettingKeyAuthSourceDefaultGoogleBalance:          "12.5",
			service.SettingKeyAuthSourceDefaultGoogleConcurrency:      "2",
			service.SettingKeyAuthSourceDefaultGoogleGrantOnFirstBind: "true",
		}
	}
	return map[string]string{
		service.SettingKeyAuthSourceDefaultGitHubBalance:          "12.5",
		service.SettingKeyAuthSourceDefaultGitHubConcurrency:      "2",
		service.SettingKeyAuthSourceDefaultGitHubGrantOnFirstBind: "true",
	}
}
