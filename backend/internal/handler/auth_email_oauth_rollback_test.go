package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 注册流程走到最后一步才失败时，账号不能留在库里——连软删的行都不该有。
//
// 这是 2026-08-28 线上故障的直接教训。当时建号在事务外先提交，后面每一步失败都靠
// RollbackOAuthEmailAccountCreation 软删补偿，结果 users 表里堆了一串软删行、自增 ID
// 一路跳号，而补偿本身一旦失败就会留下真正的孤儿账号。现在建号也走同一个事务，
// 失败路径只依赖事务回滚，不再有补偿代码。
//
// 触发晚期失败的手法是「注册码有效、但邀请人邀请码指向一个不存在的返利档案」：
// 校验注册码那一步会放行，一直到 FinalizeOAuthEmailAccount 去绑定邀请人时才报错，
// 那时账号在旧实现里已经落库了。
func TestCompleteEmailOAuthRegistrationLeavesNoAccountWhenFinalizeFails(t *testing.T) {
	affiliateRepo := newOAuthEmailAffiliateRepoStub(map[string]int64{"AFF456": 2002})
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		invitationEnabled: true,
		settingValues: map[string]string{
			service.SettingKeyAffiliateEnabled: "true",
		},
		affiliateFactory: func(_ *dbent.Client, settingSvc *service.SettingService) *service.AffiliateService {
			return service.NewAffiliateService(affiliateRepo, settingSvc, nil, nil)
		},
	})
	ctx := context.Background()

	invitation, err := client.RedeemCode.Create().
		SetCode("INVITE789").
		SetType(service.RedeemTypeInvitation).
		SetStatus(service.StatusUnused).
		SetValue(0).
		Save(ctx)
	require.NoError(t, err)

	const email = "rollback-target@example.com"
	session, err := client.PendingAuthSession.Create().
		SetSessionToken("email-oauth-rollback-session-token").
		SetIntent(oauthIntentLogin).
		SetProviderType("google").
		SetProviderKey("google").
		SetProviderSubject("google-rollback-user").
		SetResolvedEmail(email).
		SetRedirectTo("/dashboard").
		SetBrowserSessionKey("browser-rollback-key").
		SetUpstreamIdentityClaims(map[string]any{
			"email":            email,
			"email_verified":   true,
			"username":         "rollback-target",
			"provider":         "google",
			"provider_key":     "google",
			"provider_subject": "google-rollback-user",
			// stub 只认得 AFF456，这个码会让绑定邀请人那一步失败
			"aff_code": "NOSUCHCODE99",
		}).
		SetLocalFlowState(map[string]any{
			"step":  oauthPendingChoiceStep,
			"error": "invitation_required",
		}).
		SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/google/complete-registration",
		strings.NewReader(`{"password":"secret-123","invitation_code":"INVITE789"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: oauthPendingSessionCookieName, Value: encodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: oauthPendingBrowserCookieName, Value: encodeCookieValue("browser-rollback-key")})
	c.Request = req

	handler.completeEmailOAuthRegistration(c, "google")

	require.NotEqual(t, http.StatusOK, recorder.Code, "绑定邀请人失败了，不能当成注册成功")
	require.NotEmpty(t, affiliateRepo.ensureUserIDs,
		"这个用例要覆盖的是「建号之后才失败」，如果连返利档案都没初始化，说明失败发生得太早，用例没测到目标路径")

	// SkipSoftDelete 是这个断言的关键：普通查询会被软删过滤器挡掉，
	// 「被软删的半成品账号」和「压根没建过」在那种查询下看起来一模一样。
	rawCtx := mixins.SkipSoftDelete(ctx)
	leftover, err := client.User.Query().Where(dbuser.EmailEQ(email)).Count(rawCtx)
	require.NoError(t, err)
	require.Zero(t, leftover, "注册没走完，users 表里不该留下任何一行，包括软删的")

	// 一次性注册码在失败之后必须还能再用，否则用户换个邀请码重试就没码可用了。
	storedInvitation, err := client.RedeemCode.Query().Where(redeemcode.IDEQ(invitation.ID)).Only(ctx)
	require.NoError(t, err)
	require.Nil(t, storedInvitation.UsedBy, "注册失败时一次性注册码不能被占用")
	require.Equal(t, service.StatusUnused, storedInvitation.Status)
}
