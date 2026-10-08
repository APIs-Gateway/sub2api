//go:build integration

package handler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/authidentity"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// This fixture uses the complete PostgreSQL migration schema and the real
// repositories, refresh cache, affiliate admission and signup grant services.
// The SQLite request tests are supplemental; they are not PostgreSQL evidence.
func TestDingTalkNoEmailRegistrationPG(t *testing.T) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23",
		tcpostgres.WithDatabase("dingtalk_registration"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Terminate(context.Background())) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	require.NoError(t, db.PingContext(ctx))
	require.NoError(t, repository.ApplyMigrations(ctx, db))
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	refresh := repository.NewRefreshTokenCache(atomicTotpRedis(t))
	group, err := client.Group.Create().SetName("DingTalk signup card").
		SetSubscriptionType(service.SubscriptionTypeSubscription).SetDailyLimitUsd(2).
		SetWeeklyLimitUsd(8).SetMonthlyLimitUsd(20).Save(ctx)
	require.NoError(t, err)
	settings := repository.NewSettingRepository(client)
	serial := 0

	newHandler := func(t *testing.T, overrides map[string]string) (*AuthHandler, *service.AffiliateService, *service.PointsService) {
		t.Helper()
		cfg := &config.Config{JWT: config.JWTConfig{Secret: "dingtalk-pg-fixture-secret", ExpireHour: 1,
			AccessTokenExpireMinutes: 60, RefreshTokenExpireDays: 7},
			Default: config.DefaultConfig{UserBalance: 0, UserConcurrency: 1}}
		values := map[string]string{
			service.SettingKeyRegistrationEnabled: "true", service.SettingKeyInvitationCodeEnabled: "false",
			service.SettingKeyEmailVerifyEnabled: "false", service.SettingKeyForceEmailOnThirdPartySignup: "false",
			service.SettingKeyRegistrationEmailSuffixWhitelist: "[]", service.SettingKeyBackendModeEnabled: "false",
			service.SignupSourceEnabledSettingKey("dingtalk"): "true", service.SettingKeyDingTalkConnectEnabled: "false",
			service.SettingKeyAuthSourceDefaultDingTalkBalance: "9.5", service.SettingKeyAuthSourceDefaultDingTalkConcurrency: "6",
			service.SettingKeyAuthSourceDefaultDingTalkSubscriptions: fmt.Sprintf(`[{"group_id":%d,"validity_days":7}]`, group.ID),
			service.SettingKeyAuthSourceDefaultDingTalkGrantOnSignup: "true", service.SettingKeyAuthSourceDefaultDingTalkGrantOnFirstBind: "false",
			service.SettingKeyDefaultUserRPMLimit: "123", service.SettingKeyAffiliateEnabled: "true",
			service.SettingKeyAffiliateCodeAdmitsSignup: "false", service.SettingKeyAffiliateWeeklyInviteLimit: "0",
			service.SettingKeyAffiliateSignupRewardEnabled: "true", service.SettingKeyAffiliateSignupRewardAmount: "40",
			service.SettingKeyPointsEnabled: "true",
		}
		for key, value := range overrides {
			values[key] = value
		}
		require.NoError(t, settings.SetMultiple(ctx, values))
		settingSvc := service.NewSettingService(settings, cfg)
		users := repository.NewUserRepository(client, db)
		affiliates := service.NewAffiliateService(repository.NewAffiliateRepository(client, db), settingSvc, nil, nil)
		groups := repository.NewGroupRepository(client, db)
		subscriptions := service.NewSubscriptionService(groups, repository.NewUserSubscriptionRepository(client), users, nil, nil, client, settingSvc, cfg)
		t.Cleanup(subscriptions.Stop)
		points := service.NewPointsService(repository.NewPointsRepository(client, db), settingSvc, client, subscriptions, groups, affiliates, nil, nil)
		auth := service.NewAuthService(client, users, repository.NewRedeemCodeRepository(client), refresh,
			cfg, settingSvc, nil, nil, nil, nil, subscriptions, affiliates, nil)
		auth.SetSignupRewardAccruer(points)
		return NewAuthHandler(cfg, auth, service.NewUserService(users, settings, nil, nil), settingSvc, nil, nil, nil, nil), affiliates, points
	}
	newSession := func(t *testing.T, change func(*dbent.PendingAuthSessionCreate)) *dbent.PendingAuthSession {
		t.Helper()
		serial++
		key := fmt.Sprintf("dingtalk-pg-%d", serial)
		email := key + "@dingtalk-connect.invalid"
		builder := client.PendingAuthSession.Create().SetSessionToken(key).SetBrowserSessionKey(key + "-browser").
			SetIntent("login").SetProviderType("dingtalk").SetProviderKey("dingtalk").SetProviderSubject(key).
			SetResolvedEmail(email).SetRedirectTo("/dashboard").SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
			SetUpstreamIdentityClaims(map[string]any{"username": "PG DingTalk", "suggested_display_name": "PG DingTalk"}).
			SetLocalFlowState(map[string]any{oauthCompletionResponseKey: map[string]any{"redirect": "/dashboard", "synthetic_email": email}})
		if change != nil {
			change(builder)
		}
		session, err := builder.Save(ctx)
		require.NoError(t, err)
		return session
	}
	request := func(h *AuthHandler, session *dbent.PendingAuthSession, browser, body string, exchange bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		path := "/api/v1/auth/oauth/dingtalk/complete-registration"
		if exchange {
			path = "/api/v1/auth/oauth/pending/exchange"
		}
		c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		if session != nil {
			c.Request.AddCookie(&http.Cookie{Name: oauthPendingSessionCookieName, Value: encodeCookieValue(session.SessionToken)})
			c.Request.AddCookie(&http.Cookie{Name: oauthPendingBrowserCookieName, Value: encodeCookieValue(browser)})
		}
		if exchange {
			h.ExchangePendingOAuthCompletion(c)
		} else {
			h.CompleteDingTalkOAuthRegistration(c)
		}
		return rec
	}
	assertUntouched := func(t *testing.T, session *dbent.PendingAuthSession, before int) {
		t.Helper()
		count, err := client.User.Query().Count(ctx)
		require.NoError(t, err)
		require.Equal(t, before, count)
		stored, err := client.PendingAuthSession.Get(ctx, session.ID)
		require.NoError(t, err)
		require.Nil(t, stored.ConsumedAt)
		identities, err := client.AuthIdentity.Query().Where(authidentity.ProviderSubjectEQ(session.ProviderSubject)).Count(ctx)
		require.NoError(t, err)
		require.Zero(t, identities)
		var grants int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_provider_default_grants g JOIN users u ON u.id=g.user_id WHERE u.email=$1`, session.ResolvedEmail).Scan(&grants))
		require.Zero(t, grants)
	}
	assertLogin := func(t *testing.T, h *AuthHandler, session *dbent.PendingAuthSession, rec *httptest.ResponseRecorder) *dbent.User {
		t.Helper()
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			TokenType    string `json:"token_type"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tokens))
		require.NotEmpty(t, tokens.AccessToken)
		require.NotEmpty(t, tokens.RefreshToken)
		require.Equal(t, "Bearer", tokens.TokenType)
		claims, err := h.authService.ValidateToken(tokens.AccessToken)
		require.NoError(t, err)
		identity, err := client.AuthIdentity.Query().Where(authidentity.ProviderTypeEQ("dingtalk"), authidentity.ProviderKeyEQ("dingtalk"), authidentity.ProviderSubjectEQ(session.ProviderSubject)).Only(ctx)
		require.NoError(t, err)
		require.Equal(t, identity.UserID, claims.UserID)
		account, err := client.User.Get(ctx, identity.UserID)
		require.NoError(t, err)
		require.Equal(t, session.ResolvedEmail, account.Email, "client-supplied identity is never authoritative")
		require.Equal(t, "dingtalk", account.SignupSource)
		require.Equal(t, "PG DingTalk", account.Username)
		require.InDelta(t, 9.5, account.Balance, 1e-9)
		require.Equal(t, 6, account.Concurrency)
		require.Equal(t, 123, account.RpmLimit)
		card, err := client.UserSubscription.Query().Where(usersubscription.UserIDEQ(account.ID), usersubscription.GroupIDEQ(group.ID)).Only(ctx)
		require.NoError(t, err)
		require.InDelta(t, 2, card.DailyAmountUsd, 1e-9)
		require.InDelta(t, 2, card.TodayRemaining, 1e-9)
		require.InDelta(t, 14, card.GrantedTotalUsd, 1e-9)
		require.Equal(t, 6, card.ExpireDay-card.StartDay)
		// Signup direct grants retain the fork's per-day derived caps. Group
		// display caps (8/20) do not replace the seven-day card's 14/14 caps.
		require.NotNil(t, card.WeeklyLimitUsd)
		require.InDelta(t, 14, *card.WeeklyLimitUsd, 1e-9)
		require.NotNil(t, card.MonthlyLimitUsd)
		require.InDelta(t, 14, *card.MonthlyLimitUsd, 1e-9)
		hash := sha256.Sum256([]byte(tokens.RefreshToken))
		refreshData, err := refresh.GetRefreshToken(ctx, hex.EncodeToString(hash[:]))
		require.NoError(t, err)
		require.Equal(t, account.ID, refreshData.UserID)
		stored, err := client.PendingAuthSession.Get(ctx, session.ID)
		require.NoError(t, err)
		require.NotNil(t, stored.ConsumedAt)
		return account
	}
	newInviter := func(t *testing.T, affiliates *service.AffiliateService) (int64, string) {
		t.Helper()
		serial++
		inviter, err := client.User.Create().SetEmail(fmt.Sprintf("inviter-%d@example.com", serial)).
			SetPasswordHash("fixture-not-used-for-login").SetRole(service.RoleUser).SetStatus(service.StatusActive).Save(ctx)
		require.NoError(t, err)
		summary, err := affiliates.EnsureUserAffiliate(ctx, inviter.ID)
		require.NoError(t, err)
		return inviter.ID, summary.AffCode
	}
	adoption := `{"adopt_display_name":true,"adopt_avatar":false}`

	t.Run("missing_browser_cookies", func(t *testing.T) {
		h, _, _ := newHandler(t, nil)
		before, err := client.User.Query().Count(ctx)
		require.NoError(t, err)
		rec := request(h, nil, "", `{}`, false)
		require.Equal(t, http.StatusNotFound, rec.Code)
		require.Contains(t, rec.Body.String(), "PENDING_AUTH_SESSION_NOT_FOUND")
		count, err := client.User.Query().Count(ctx)
		require.NoError(t, err)
		require.Equal(t, before, count)
	})

	t.Run("cookie_exchange_then_no_invite_registration_grants_once", func(t *testing.T) {
		h, _, _ := newHandler(t, nil)
		session := newSession(t, nil)
		before, err := client.User.Query().Count(ctx)
		require.NoError(t, err)
		exchange := request(h, session, session.BrowserSessionKey, `{}`, true)
		require.Equal(t, http.StatusOK, exchange.Code)
		payload := decodeJSONResponseData(t, exchange)
		require.Equal(t, session.ResolvedEmail, payload["synthetic_email"])
		require.NotContains(t, payload, "access_token")
		assertUntouched(t, session, before)
		rec := request(h, session, session.BrowserSessionKey, `{"email":"attacker@example.com","username":"forged","provider":"oidc","adopt_display_name":true,"adopt_avatar":false}`, false)
		account := assertLogin(t, h, session, rec)
		replay := request(h, session, session.BrowserSessionKey, adoption, false)
		require.NotEqual(t, http.StatusOK, replay.Code)
		count, err := client.User.Query().Where(user.EmailEQ(session.ResolvedEmail)).Count(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, count)
		cards, err := client.UserSubscription.Query().Where(usersubscription.UserIDEQ(account.ID)).Count(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, cards)
		account, err = client.User.Get(ctx, account.ID)
		require.NoError(t, err)
		require.InDelta(t, 9.5, account.Balance, 1e-9)
	})

	t.Run("existing_target_and_identity_cannot_be_registered_as_new", func(t *testing.T) {
		h, _, _ := newHandler(t, nil)
		existing, err := client.User.Query().First(ctx)
		require.NoError(t, err)
		before, err := client.User.Query().Count(ctx)
		require.NoError(t, err)
		target := newSession(t, func(b *dbent.PendingAuthSessionCreate) { b.SetTargetUserID(existing.ID) })
		rec := request(h, target, target.BrowserSessionKey, adoption, false)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		assertUntouched(t, target, before)
		bound := newSession(t, nil)
		_, err = client.AuthIdentity.Create().SetUserID(existing.ID).SetProviderType("dingtalk").SetProviderKey("dingtalk").
			SetProviderSubject(bound.ProviderSubject).SetVerifiedAt(time.Now()).Save(ctx)
		require.NoError(t, err)
		rec = request(h, bound, bound.BrowserSessionKey, adoption, false)
		require.NotEqual(t, http.StatusOK, rec.Code)
		count, err := client.User.Query().Count(ctx)
		require.NoError(t, err)
		require.Equal(t, before, count)
		identity, err := client.AuthIdentity.Query().Where(authidentity.ProviderSubjectEQ(bound.ProviderSubject)).Only(ctx)
		require.NoError(t, err)
		require.Equal(t, existing.ID, identity.UserID)
		stored, err := client.PendingAuthSession.Get(ctx, bound.ID)
		require.NoError(t, err)
		require.Nil(t, stored.ConsumedAt)
	})

	t.Run("generic_exchange_keeps_unproved_target_unbound", func(t *testing.T) {
		h, _, _ := newHandler(t, nil)
		existing, err := client.User.Query().First(ctx)
		require.NoError(t, err)
		session := newSession(t, func(b *dbent.PendingAuthSessionCreate) {
			b.SetTargetUserID(existing.ID).SetLocalFlowState(map[string]any{oauthCompletionResponseKey: map[string]any{
				"step": "choose_account_action_required", "adoption_required": true,
				"create_account_allowed": true, "redirect": "/dashboard",
			}})
		})
		before, err := client.User.Query().Count(ctx)
		require.NoError(t, err)
		refreshBefore, err := refresh.GetUserTokenHashes(ctx, existing.ID)
		require.NoError(t, err)
		rec := request(h, session, session.BrowserSessionKey, adoption, true)
		require.Equal(t, http.StatusOK, rec.Code)
		payload := decodeJSONResponseData(t, rec)
		require.Equal(t, "choose_account_action_required", payload["step"])
		require.NotContains(t, payload, "access_token")
		assertUntouched(t, session, before)
		refreshAfter, err := refresh.GetUserTokenHashes(ctx, existing.ID)
		require.NoError(t, err)
		require.ElementsMatch(t, refreshBefore, refreshAfter)
	})

	for _, tc := range []struct {
		name    string
		change  func(*dbent.PendingAuthSessionCreate)
		browser string
	}{
		{"wrong_provider_type", func(b *dbent.PendingAuthSessionCreate) { b.SetProviderType("oidc") }, ""},
		{"wrong_provider_key", func(b *dbent.PendingAuthSessionCreate) { b.SetProviderKey("other-tenant") }, ""},
		{"bind_intent", func(b *dbent.PendingAuthSessionCreate) { b.SetIntent("bind_current_user") }, ""},
		{"expired_session", func(b *dbent.PendingAuthSessionCreate) { b.SetExpiresAt(time.Now().Add(-time.Minute)) }, ""},
		{"consumed_session", func(b *dbent.PendingAuthSessionCreate) { b.SetConsumedAt(time.Now()) }, ""},
		{"wrong_browser", nil, "other-browser"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newHandler(t, nil)
			session := newSession(t, tc.change)
			before, err := client.User.Query().Count(ctx)
			require.NoError(t, err)
			browser := session.BrowserSessionKey
			if tc.browser != "" {
				browser = tc.browser
			}
			// A nonempty invitation value passes the old request binder, so the
			// provider cases exercise the actual security boundary independently.
			rec := request(h, session, browser, `{"invitation_code":"binder-filler","adopt_display_name":true,"adopt_avatar":false}`, false)
			require.NotEqual(t, http.StatusOK, rec.Code)
			require.NotContains(t, rec.Body.String(), "access_token")
			if tc.name == "wrong_provider_type" || tc.name == "wrong_provider_key" {
				require.Equal(t, http.StatusBadRequest, rec.Code)
				require.Contains(t, rec.Body.String(), "PENDING_AUTH_SESSION_INVALID")
			}
			if tc.name == "consumed_session" {
				count, err := client.User.Query().Count(ctx)
				require.NoError(t, err)
				require.Equal(t, before, count)
			} else {
				assertUntouched(t, session, before)
			}
		})
	}

	for _, tc := range []struct {
		name, key, reason string
	}{
		{"registration_disabled", service.SettingKeyRegistrationEnabled, "REGISTRATION_DISABLED"},
		{"source_disabled", service.SignupSourceEnabledSettingKey("dingtalk"), "SIGNUP_SOURCE_DISABLED"},
		{"backend_mode", service.SettingKeyBackendModeEnabled, "BACKEND_MODE_ADMIN_ONLY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := "false"
			if tc.name == "backend_mode" {
				value = "true"
			}
			h, _, _ := newHandler(t, map[string]string{tc.key: value})
			session := newSession(t, nil)
			before, err := client.User.Query().Count(ctx)
			require.NoError(t, err)
			rec := request(h, session, session.BrowserSessionKey, adoption, false)
			require.Equal(t, http.StatusForbidden, rec.Code)
			require.Contains(t, rec.Body.String(), tc.reason)
			assertUntouched(t, session, before)
		})
	}

	for _, key := range []string{service.SettingKeyForceEmailOnThirdPartySignup, service.SettingKeyEmailVerifyEnabled} {
		t.Run(key, func(t *testing.T) {
			h, _, _ := newHandler(t, map[string]string{key: "true"})
			session := newSession(t, nil)
			before, err := client.User.Query().Count(ctx)
			require.NoError(t, err)
			rec := request(h, session, session.BrowserSessionKey, adoption, false)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Body.String(), "pending_session")
			require.Contains(t, rec.Body.String(), "choose_account_action_required")
			require.NotContains(t, rec.Body.String(), "access_token")
			assertUntouched(t, session, before)
		})
	}

	t.Run("invite_required_invalid_then_single_redemption", func(t *testing.T) {
		h, _, _ := newHandler(t, map[string]string{service.SettingKeyInvitationCodeEnabled: "true"})
		session := newSession(t, nil)
		before, err := client.User.Query().Count(ctx)
		require.NoError(t, err)
		rec := request(h, session, session.BrowserSessionKey, adoption, false)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Contains(t, rec.Body.String(), "OAUTH_INVITATION_REQUIRED")
		assertUntouched(t, session, before)
		rec = request(h, session, session.BrowserSessionKey, `{"invitation_code":"invalid","adopt_display_name":true,"adopt_avatar":false}`, false)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		assertUntouched(t, session, before)
		code, err := client.RedeemCode.Create().SetCode("dingtalk-real-invite").SetType(service.RedeemTypeInvitation).SetStatus(service.StatusUnused).SetValue(0).Save(ctx)
		require.NoError(t, err)
		rec = request(h, session, session.BrowserSessionKey, `{"invitation_code":"dingtalk-real-invite","adopt_display_name":true,"adopt_avatar":false}`, false)
		account := assertLogin(t, h, session, rec)
		used, err := client.RedeemCode.Get(ctx, code.ID)
		require.NoError(t, err)
		require.Equal(t, service.StatusUsed, used.Status)
		require.NotNil(t, used.UsedBy)
		require.Equal(t, account.ID, *used.UsedBy)
		retry := newSession(t, nil)
		before, err = client.User.Query().Count(ctx)
		require.NoError(t, err)
		rec = request(h, retry, retry.BrowserSessionKey, `{"invitation_code":"dingtalk-real-invite","adopt_display_name":true,"adopt_avatar":false}`, false)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		assertUntouched(t, retry, before)
	})

	for _, admissionEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("affiliate_admission_%t_cannot_bypass_invalid_invite", admissionEnabled), func(t *testing.T) {
			h, affiliates, _ := newHandler(t, map[string]string{service.SettingKeyInvitationCodeEnabled: "true",
				service.SettingKeyAffiliateCodeAdmitsSignup: fmt.Sprint(admissionEnabled)})
			_, affCode := newInviter(t, affiliates)
			if admissionEnabled {
				affCode = "UNKNOWNCODE12"
			}
			session := newSession(t, nil)
			before, err := client.User.Query().Count(ctx)
			require.NoError(t, err)
			body := fmt.Sprintf(`{"aff_code":%q,"adopt_display_name":true,"adopt_avatar":false}`, affCode)
			rec := request(h, session, session.BrowserSessionKey, body, false)
			require.Equal(t, http.StatusForbidden, rec.Code)
			require.Contains(t, rec.Body.String(), "OAUTH_INVITATION_REQUIRED")
			assertUntouched(t, session, before)
		})
	}

	t.Run("affiliate_admission_reward_and_weekly_boundary", func(t *testing.T) {
		h, affiliates, points := newHandler(t, map[string]string{service.SettingKeyInvitationCodeEnabled: "true",
			service.SettingKeyAffiliateCodeAdmitsSignup: "true", service.SettingKeyAffiliateWeeklyInviteLimit: "1"})
		inviterID, affCode := newInviter(t, affiliates)
		session := newSession(t, nil)
		body := fmt.Sprintf(`{"aff_code":%q,"adopt_display_name":true,"adopt_avatar":false}`, affCode)
		account := assertLogin(t, h, session, request(h, session, session.BrowserSessionKey, body, false))
		summary, err := affiliates.EnsureUserAffiliate(ctx, account.ID)
		require.NoError(t, err)
		require.NotNil(t, summary.InviterID)
		require.Equal(t, inviterID, *summary.InviterID)
		var available, frozen, earned, rows int64
		require.NoError(t, db.QueryRowContext(ctx, `SELECT available,frozen,lifetime_earned FROM user_points_accounts WHERE user_id=$1`, inviterID).Scan(&available, &frozen, &earned))
		require.Equal(t, int64(40), available)
		require.Zero(t, frozen)
		require.Equal(t, int64(40), earned)
		granted, err := points.AccrueSignupReward(ctx, account.ID)
		require.NoError(t, err)
		require.Zero(t, granted)
		require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_points_ledger WHERE source_user_id=$1 AND kind='signup_reward'`, account.ID).Scan(&rows))
		require.Equal(t, int64(1), rows)
		replay := request(h, session, session.BrowserSessionKey, body, false)
		require.NotEqual(t, http.StatusOK, replay.Code)
		blocked := newSession(t, nil)
		before, err := client.User.Query().Count(ctx)
		require.NoError(t, err)
		rec := request(h, blocked, blocked.BrowserSessionKey, body, false)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Contains(t, rec.Body.String(), "AFFILIATE_WEEKLY_INVITE_LIMIT_REACHED")
		assertUntouched(t, blocked, before)

		// A separate valid redeem invitation admits signup when the referral
		// attribution quota is full; existing fork behavior skips attribution.
		_, err = client.RedeemCode.Create().SetCode("dingtalk-independent-invite").SetType(service.RedeemTypeInvitation).SetStatus(service.StatusUnused).SetValue(0).Save(ctx)
		require.NoError(t, err)
		independentBody := fmt.Sprintf(`{"invitation_code":"dingtalk-independent-invite","aff_code":%q,"adopt_display_name":true,"adopt_avatar":false}`, affCode)
		independent := assertLogin(t, h, blocked, request(h, blocked, blocked.BrowserSessionKey, independentBody, false))
		independentAffiliate, err := affiliates.EnsureUserAffiliate(ctx, independent.ID)
		require.NoError(t, err)
		require.Nil(t, independentAffiliate.InviterID)
		require.NoError(t, db.QueryRowContext(ctx, `SELECT available,frozen,lifetime_earned FROM user_points_accounts WHERE user_id=$1`, inviterID).Scan(&available, &frozen, &earned))
		require.Equal(t, int64(40), available)
		require.Zero(t, frozen)
		require.Equal(t, int64(40), earned)
	})
}
