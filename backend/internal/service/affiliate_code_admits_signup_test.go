//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/config"
)

// admitAffRepoStub 在周上限那套 stub 之上放开 GetAffiliateByCode：
// 准入判断必须先把邀请码解析成邀请人，才谈得上查他本周还剩多少名额。
type admitAffRepoStub struct {
	weeklyInviteAffRepoStub
	summary     *AffiliateSummary
	summaryErr  error
	codeSeen    string
	bindErr     error
	bindCalls   []admitBindCall
	ensureCalls []int64
}

type admitBindCall struct {
	userID    int64
	inviterID int64
}

func (r *admitAffRepoStub) EnsureUserAffiliate(_ context.Context, userID int64) (*AffiliateSummary, error) {
	r.ensureCalls = append(r.ensureCalls, userID)
	return &AffiliateSummary{UserID: userID, AffCode: "SELFCODE0001"}, nil
}

func (r *admitAffRepoStub) BindInviter(_ context.Context, userID, inviterID int64) (bool, error) {
	r.bindCalls = append(r.bindCalls, admitBindCall{userID: userID, inviterID: inviterID})
	if r.bindErr != nil {
		return false, r.bindErr
	}
	return true, nil
}

func (r *admitAffRepoStub) GetAffiliateByCode(_ context.Context, code string) (*AffiliateSummary, error) {
	r.codeSeen = code
	if r.summaryErr != nil {
		return nil, r.summaryErr
	}
	return r.summary, nil
}

// admitPublicSettingRepoStub 额外实现 GetMultiple，用来盯住公开设置到底取了哪些键。
type admitPublicSettingRepoStub struct {
	signupSourceSettingRepoStub
	multiKeys []string
}

func (r *admitPublicSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.multiKeys = keys
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if v, ok := r.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func admitSettingService(admits, weeklyLimit string) *SettingService {
	return &SettingService{settingRepo: signupSourceSettingRepoStub{values: map[string]string{
		SettingKeyAffiliateCodeAdmitsSignup:  admits,
		SettingKeyAffiliateWeeklyInviteLimit: weeklyLimit,
	}}}
}

func admitAuthService(admits, weeklyLimit string, repo *admitAffRepoStub) *AuthService {
	settings := admitSettingService(admits, weeklyLimit)
	return &AuthService{
		settingService:   settings,
		affiliateService: &AffiliateService{repo: repo, settingService: settings},
	}
}

const admitTestCode = "HC3YPBNP7LRU"

// TestCanInviteMoreThisWeek 覆盖「建号之前先查名额」这道新闸门。
//
// 它存在的理由是时序：周上限原本只在绑定邀请人那一步检查，而那时账号已经落库，
// 超额的人会拿到报错、邮箱却被一个半成品账号占死，换个码重试只会撞 EMAIL_EXISTS。
func TestCanInviteMoreThisWeek(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("空码直接放行", func(t *testing.T) {
		// 没填推荐码是合法状态（走注册码那条路），不该在这里报错
		svc := &AffiliateService{repo: &admitAffRepoStub{}, settingService: admitSettingService("true", "1")}
		require.NoError(t, svc.CanInviteMoreThisWeek(ctx, "   "))
	})

	t.Run("nil 接收者安全", func(t *testing.T) {
		var svc *AffiliateService
		require.NoError(t, svc.CanInviteMoreThisWeek(ctx, admitTestCode))
	})

	t.Run("码查不到按无效码处理", func(t *testing.T) {
		repo := &admitAffRepoStub{summaryErr: ErrAffiliateProfileNotFound}
		svc := &AffiliateService{repo: repo, settingService: admitSettingService("true", "5")}
		require.ErrorIs(t, svc.CanInviteMoreThisWeek(ctx, admitTestCode), ErrAffiliateInviteCodeInvalid)
	})

	t.Run("仓储报错原样上抛", func(t *testing.T) {
		// 查不动库和「码是假的」必须区分开，否则会把一次故障说成用户填错
		boom := errors.New("affiliate db down")
		repo := &admitAffRepoStub{summaryErr: boom}
		svc := &AffiliateService{repo: repo, settingService: admitSettingService("true", "5")}
		require.ErrorIs(t, svc.CanInviteMoreThisWeek(ctx, admitTestCode), boom)
	})

	t.Run("解析出空档案按无效码处理", func(t *testing.T) {
		repo := &admitAffRepoStub{summary: &AffiliateSummary{UserID: 0}}
		svc := &AffiliateService{repo: repo, settingService: admitSettingService("true", "5")}
		require.ErrorIs(t, svc.CanInviteMoreThisWeek(ctx, admitTestCode), ErrAffiliateInviteCodeInvalid)
	})

	t.Run("本周还有名额时放行", func(t *testing.T) {
		repo := &admitAffRepoStub{summary: &AffiliateSummary{UserID: 9}}
		repo.count = 4
		svc := &AffiliateService{repo: repo, settingService: admitSettingService("true", "5")}
		require.NoError(t, svc.CanInviteMoreThisWeek(ctx, "hc3ypbnp7lru"))
		require.Equal(t, admitTestCode, repo.codeSeen, "邀请码要先归一化成大写再查，否则大小写不同会被当成两个码")
	})

	t.Run("本周已满时拒绝", func(t *testing.T) {
		repo := &admitAffRepoStub{summary: &AffiliateSummary{UserID: 9}}
		repo.count = 5
		svc := &AffiliateService{repo: repo, settingService: admitSettingService("true", "5")}
		require.ErrorIs(t, svc.CanInviteMoreThisWeek(ctx, admitTestCode), ErrAffiliateWeeklyInviteLimitReached)
	})

	t.Run("管理员不受限", func(t *testing.T) {
		repo := &admitAffRepoStub{summary: &AffiliateSummary{UserID: 9}}
		repo.isAdmin = true
		repo.count = 999
		svc := &AffiliateService{repo: repo, settingService: admitSettingService("true", "5")}
		require.NoError(t, svc.CanInviteMoreThisWeek(ctx, admitTestCode))
	})
}

// TestAdmitSignupByAffiliateCode 覆盖「邀请人邀请码能不能顶替注册码」这个判断。
//
// 三种返回值语义必须分清：放行、有话要说的拒绝、以及「没有可用候选」——
// 最后一种要让调用方沿用注册码那边原本的错误，而不是把它盖成别的。
func TestAdmitSignupByAffiliateCode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("开关关闭时恒不放行", func(t *testing.T) {
		repo := &admitAffRepoStub{summary: &AffiliateSummary{UserID: 9}}
		got, err := admitAuthService("false", "0", repo).admitSignupByAffiliateCode(ctx, admitTestCode)
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("缺少依赖时不放行", func(t *testing.T) {
		var nilAuth *AuthService
		got, err := nilAuth.admitSignupByAffiliateCode(ctx, admitTestCode)
		require.NoError(t, err)
		require.Empty(t, got)

		got, err = (&AuthService{}).admitSignupByAffiliateCode(ctx, admitTestCode)
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("有效码且有名额时放行并回传生效的码", func(t *testing.T) {
		repo := &admitAffRepoStub{summary: &AffiliateSummary{UserID: 9}}
		repo.count = 1
		got, err := admitAuthService("true", "5", repo).admitSignupByAffiliateCode(ctx, admitTestCode)
		require.NoError(t, err)
		require.Equal(t, admitTestCode, got)
	})

	t.Run("跳过空候选，用后一个", func(t *testing.T) {
		// 真实场景：邀请链接没带 aff 参数，用户把码手填进了「注册码」那一栏
		repo := &admitAffRepoStub{summary: &AffiliateSummary{UserID: 9}}
		got, err := admitAuthService("true", "5", repo).admitSignupByAffiliateCode(ctx, "  ", admitTestCode)
		require.NoError(t, err)
		require.Equal(t, admitTestCode, got)
	})

	t.Run("候选都不是有效推荐码时交还给调用方", func(t *testing.T) {
		repo := &admitAffRepoStub{summaryErr: ErrAffiliateProfileNotFound}
		got, err := admitAuthService("true", "5", repo).admitSignupByAffiliateCode(ctx, admitTestCode)
		require.NoError(t, err, "这里不该自造错误，注册码那边的原始错误更贴近用户实际填了什么")
		require.Empty(t, got)
	})

	t.Run("码是真的但本周已满，直接抛出限额错误", func(t *testing.T) {
		// 关键行为：不能退化成「注册码无效」，否则用户完全看不懂自己撞了什么
		repo := &admitAffRepoStub{summary: &AffiliateSummary{UserID: 9}}
		repo.count = 5
		got, err := admitAuthService("true", "5", repo).admitSignupByAffiliateCode(ctx, admitTestCode)
		require.ErrorIs(t, err, ErrAffiliateWeeklyInviteLimitReached)
		require.Empty(t, got)
	})
}

// TestLoadInvitationRedeemCode 覆盖注册码校验被抽出来的那段共用逻辑。
// 抽出来的意义在于三个注册入口的判定必须一致，否则同一个码在不同入口结果不同。
func TestLoadInvitationRedeemCode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("空码要求补填", func(t *testing.T) {
		svc := &AuthService{redeemRepo: &redeemCodeRepoStub{}}
		_, err := svc.loadInvitationRedeemCode(ctx, "   ")
		require.ErrorIs(t, err, ErrInvitationCodeRequired)
	})

	t.Run("没有仓储时按无效码处理", func(t *testing.T) {
		_, err := (&AuthService{}).loadInvitationRedeemCode(ctx, "ABC")
		require.ErrorIs(t, err, ErrInvitationCodeInvalid)
	})

	t.Run("查不到按无效码处理", func(t *testing.T) {
		svc := &AuthService{redeemRepo: &redeemCodeRepoStub{codesByCode: map[string]*RedeemCode{}}}
		_, err := svc.loadInvitationRedeemCode(ctx, "ABC")
		require.ErrorIs(t, err, ErrInvitationCodeInvalid)
	})

	t.Run("类型不对按无效码处理", func(t *testing.T) {
		// 余额兑换码不能拿来当注册许可用
		svc := &AuthService{redeemRepo: &redeemCodeRepoStub{codesByCode: map[string]*RedeemCode{
			"ABC": {Code: "ABC", Type: RedeemTypeBalance, Status: StatusUnused},
		}}}
		_, err := svc.loadInvitationRedeemCode(ctx, "ABC")
		require.ErrorIs(t, err, ErrInvitationCodeInvalid)
	})

	t.Run("可用的注册码正常返回", func(t *testing.T) {
		// 两侧留空白是有意的：调用方拿到的码来自用户输入，必须先 trim 再查
		want := &RedeemCode{Code: "ABC", Type: RedeemTypeInvitation, Status: StatusUnused}
		svc := &AuthService{redeemRepo: &redeemCodeRepoStub{codesByCode: map[string]*RedeemCode{"ABC": want}}}
		got, err := svc.loadInvitationRedeemCode(ctx, "  ABC  ")
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "ABC", got.Code)
		require.Equal(t, RedeemTypeInvitation, got.Type)
	})
}

// TestIsAffiliateCodeAdmitsSignupEnabled 钉住默认关闭。
// 打开这个开关等于把站点从「管理员发码才能进」放宽成「有邀请链接就能进」，
// 是产品口径的改变，读不到或读坏都必须回落到关闭。
func TestIsAffiliateCodeAdmitsSignupEnabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	svc := func(v map[string]string) *SettingService {
		return &SettingService{settingRepo: signupSourceSettingRepoStub{values: v}}
	}

	require.False(t, svc(map[string]string{}).IsAffiliateCodeAdmitsSignupEnabled(ctx), "键缺失时默认关闭")
	require.False(t, svc(map[string]string{SettingKeyAffiliateCodeAdmitsSignup: ""}).IsAffiliateCodeAdmitsSignupEnabled(ctx))
	require.False(t, svc(map[string]string{SettingKeyAffiliateCodeAdmitsSignup: "TRUE"}).IsAffiliateCodeAdmitsSignupEnabled(ctx), "只认全小写 true，避免把写坏的值当成开启")
	require.True(t, svc(map[string]string{SettingKeyAffiliateCodeAdmitsSignup: " true "}).IsAffiliateCodeAdmitsSignupEnabled(ctx))
}

// TestGetPublicSettingsReadsSignupSourceKeys 守住一个曾经上线的缺陷：
// keys 列表里漏掉 auth_source_*_signup_enabled 时，parseSignupSourceEnabled 会全部读到空值，
// 于是把已经关闭的注册来源一律报成「可用」，注册页照旧渲染表单，
// 用户要填完整页才撞上 SIGNUP_SOURCE_DISABLED——正是这个公开接口本该避免的情况。
func TestGetPublicSettingsReadsSignupSourceKeys(t *testing.T) {
	t.Parallel()

	repo := &admitPublicSettingRepoStub{signupSourceSettingRepoStub: signupSourceSettingRepoStub{
		values: map[string]string{
			SignupSourceEnabledSettingKey(SignupSourceEmail): "false",
			SettingKeyAffiliateCodeAdmitsSignup:              "true",
		},
	}}
	svc := &SettingService{settingRepo: repo}

	got, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)

	for _, source := range SignupSources {
		require.Contains(t, repo.multiKeys, SignupSourceEnabledSettingKey(source),
			"公开设置必须把每个注册来源的开关键一起取出来")
	}
	require.False(t, got.SignupSourceEnabled[SignupSourceEmail], "关掉的来源要如实报告为不可用")
	require.True(t, got.SignupSourceEnabled["github"], "没配过的来源默认视为可用")
	require.True(t, got.AffiliateCodeAdmitsSignup, "准入开关要透给注册页，它据此决定注册码那栏是否必填")
}

func oauthAdmitAuthService(admits, weeklyLimit string, repo *admitAffRepoStub, codes map[string]*RedeemCode) *AuthService {
	settings := &SettingService{settingRepo: signupSourceSettingRepoStub{values: map[string]string{
		SettingKeyInvitationCodeEnabled:      "true",
		SettingKeyAffiliateCodeAdmitsSignup:  admits,
		SettingKeyAffiliateWeeklyInviteLimit: weeklyLimit,
	}}}
	return &AuthService{
		settingService:   settings,
		redeemRepo:       &redeemCodeRepoStub{codesByCode: codes},
		affiliateService: &AffiliateService{repo: repo, settingService: settings},
	}
}

func admitRepoWithQuota(count int) *admitAffRepoStub {
	repo := &admitAffRepoStub{summary: &AffiliateSummary{UserID: 9}}
	repo.count = count
	return repo
}

// TestValidateOAuthRegistrationInvitation 覆盖第三方注册那条路的准入判定。
//
// 它和邮箱注册那条路必须给出一致的结论：同一个码在两个入口结果不同，用户只会觉得
// 系统在随机拒绝自己。返回值有三段语义——放行并带出真正生效的推荐码、拒绝并给出
// 比「注册码无效」更有解释力的错误、以及沿用注册码那边的原始错误。
func TestValidateOAuthRegistrationInvitation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	validCode := map[string]*RedeemCode{
		"INVITE1": {ID: 7, Code: "INVITE1", Type: RedeemTypeInvitation, Status: StatusUnused},
	}

	t.Run("邀请码功能没开时直接放行", func(t *testing.T) {
		svc := &AuthService{settingService: &SettingService{settingRepo: signupSourceSettingRepoStub{
			values: map[string]string{SettingKeyInvitationCodeEnabled: "false"},
		}}}
		code, aff, err := svc.validateOAuthRegistrationInvitation(ctx, "", "  AFF  ")
		require.NoError(t, err)
		require.Nil(t, code)
		require.Equal(t, "AFF", aff, "推荐码要原样带出去，后面绑定邀请人还要用")
	})

	t.Run("没有任何注册码来源时报服务不可用", func(t *testing.T) {
		svc := &AuthService{settingService: &SettingService{settingRepo: signupSourceSettingRepoStub{
			values: map[string]string{SettingKeyInvitationCodeEnabled: "true"},
		}}}
		_, _, err := svc.validateOAuthRegistrationInvitation(ctx, "INVITE1", "")
		require.ErrorIs(t, err, ErrServiceUnavailable)
	})

	t.Run("注册码有效时返回它，推荐码不变", func(t *testing.T) {
		svc := oauthAdmitAuthService("false", "5", admitRepoWithQuota(0), validCode)
		code, aff, err := svc.validateOAuthRegistrationInvitation(ctx, "INVITE1", "AFF456")
		require.NoError(t, err)
		require.NotNil(t, code)
		require.Equal(t, "INVITE1", code.Code)
		require.Equal(t, "AFF456", aff)
	})

	t.Run("没填注册码且准入关闭时要求补填", func(t *testing.T) {
		svc := oauthAdmitAuthService("false", "5", admitRepoWithQuota(0), validCode)
		_, _, err := svc.validateOAuthRegistrationInvitation(ctx, "", "HC3YPBNP7LRU")
		require.ErrorIs(t, err, ErrInvitationCodeRequired)
	})

	t.Run("没填注册码但有有效推荐码且准入开启时放行", func(t *testing.T) {
		// 顺着邀请链接进来的人手上只有邀请人的码，不该再被要求一张注册码
		svc := oauthAdmitAuthService("true", "5", admitRepoWithQuota(1), validCode)
		code, aff, err := svc.validateOAuthRegistrationInvitation(ctx, "", "HC3YPBNP7LRU")
		require.NoError(t, err)
		require.Nil(t, code, "这条路没有消耗任何一次性注册码")
		require.Equal(t, admitTestCode, aff)
	})

	t.Run("把邀请人邀请码填进注册码栏也能放行", func(t *testing.T) {
		// 用户分不清两个输入框是常态，能认出来就别拒绝
		svc := oauthAdmitAuthService("true", "5", admitRepoWithQuota(1), validCode)
		code, aff, err := svc.validateOAuthRegistrationInvitation(ctx, "HC3YPBNP7LRU", "")
		require.NoError(t, err)
		require.Nil(t, code)
		require.Equal(t, admitTestCode, aff)
	})

	t.Run("注册码无效且没有可用推荐码时保留原始错误", func(t *testing.T) {
		svc := oauthAdmitAuthService("true", "5", &admitAffRepoStub{summaryErr: ErrAffiliateProfileNotFound}, validCode)
		_, _, err := svc.validateOAuthRegistrationInvitation(ctx, "NOPE", "ALSONOPE")
		require.ErrorIs(t, err, ErrInvitationCodeInvalid)
	})

	t.Run("注册码类型不对时同样按无效处理", func(t *testing.T) {
		codes := map[string]*RedeemCode{
			"BALANCE1": {ID: 8, Code: "BALANCE1", Type: RedeemTypeBalance, Status: StatusUnused},
		}
		svc := oauthAdmitAuthService("false", "5", admitRepoWithQuota(0), codes)
		_, _, err := svc.validateOAuthRegistrationInvitation(ctx, "BALANCE1", "")
		require.ErrorIs(t, err, ErrInvitationCodeInvalid)
	})

	t.Run("推荐码本周名额已满时抛限额错误而不是无效码", func(t *testing.T) {
		// 退化成「注册码无效」的话，用户完全看不懂自己撞了什么
		svc := oauthAdmitAuthService("true", "5", admitRepoWithQuota(5), validCode)
		_, _, err := svc.validateOAuthRegistrationInvitation(ctx, "", "HC3YPBNP7LRU")
		require.ErrorIs(t, err, ErrAffiliateWeeklyInviteLimitReached)
	})
}

func admitRegisterSettings(admits, weeklyLimit string) map[string]string {
	return map[string]string{
		SettingKeyRegistrationEnabled:                    "true",
		SignupSourceEnabledSettingKey(SignupSourceEmail): "true",
		SettingKeyInvitationCodeEnabled:                  "true",
		SettingKeyAffiliateEnabled:                       "true",
		SettingKeyAffiliateCodeAdmitsSignup:              admits,
		SettingKeyAffiliateWeeklyInviteLimit:             weeklyLimit,
		SettingKeyAuthSourceDefaultEmailGrantOnSignup:    "false",
	}
}

func admitRegisterService(
	repo UserRepository,
	settings map[string]string,
	codes map[string]*RedeemCode,
	affRepo *admitAffRepoStub,
) *AuthService {
	cfg := &config.Config{
		JWT:     config.JWTConfig{Secret: "test-secret", ExpireHour: 1},
		Default: config.DefaultConfig{UserBalance: 3.5, UserConcurrency: 2},
	}
	settingService := NewSettingService(&settingRepoStub{values: settings}, cfg)
	return NewAuthService(
		nil, repo, &redeemCodeRepoStub{codesByCode: codes}, nil, cfg,
		settingService, nil, nil, nil, nil, nil,
		&AffiliateService{repo: affRepo, settingService: settingService},
		nil,
	)
}

// TestRegisterWithVerificationAdmitsByAffiliateCode 覆盖邮箱注册这条路上的准入与回滚。
//
// 这里关心的不只是「能不能注册成功」，还有失败时账号有没有被留下：周限额必须在建号
// 之前查掉，绑定邀请人失败则必须把已经落库的账号删掉。否则用户的邮箱会被一个半成品
// 账号占死，换个码重试只会撞 EMAIL_EXISTS，而他根本不知道发生了什么。
func TestRegisterWithVerificationAdmitsByAffiliateCode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	validCode := map[string]*RedeemCode{
		"INVITE1": {ID: 7, Code: "INVITE1", Type: RedeemTypeInvitation, Status: StatusUnused},
	}

	t.Run("准入开启时只填邀请人邀请码即可注册并绑定邀请人", func(t *testing.T) {
		repo := &userRepoStub{nextID: 11}
		affRepo := admitRepoWithQuota(1)
		svc := admitRegisterService(repo, admitRegisterSettings("true", "5"), validCode, affRepo)

		token, user, err := svc.RegisterWithVerification(ctx, "invitee@test.com", "password", "", "", "", admitTestCode)
		require.NoError(t, err)
		require.NotEmpty(t, token)
		require.NotNil(t, user)
		require.Equal(t, int64(11), user.ID)
		require.Equal(t, []admitBindCall{{userID: 11, inviterID: 9}}, affRepo.bindCalls)
		require.Empty(t, repo.deletedIDs, "注册成功了就不该有任何回滚")
	})

	t.Run("把邀请人邀请码填进注册码那一栏也能放行", func(t *testing.T) {
		// 注册页把两栏并列摆着，用户填错格子是常态，能认出来就别拒绝
		repo := &userRepoStub{nextID: 12}
		affRepo := admitRepoWithQuota(1)
		svc := admitRegisterService(repo, admitRegisterSettings("true", "5"), validCode, affRepo)

		_, user, err := svc.RegisterWithVerification(ctx, "wrongfield@test.com", "password", "", "", admitTestCode, "")
		require.NoError(t, err)
		require.Equal(t, int64(12), user.ID)
		require.Equal(t, []admitBindCall{{userID: 12, inviterID: 9}}, affRepo.bindCalls)
	})

	t.Run("准入关闭时只有邀请人邀请码不够，且不建号", func(t *testing.T) {
		repo := &userRepoStub{nextID: 13}
		affRepo := admitRepoWithQuota(1)
		svc := admitRegisterService(repo, admitRegisterSettings("false", "5"), validCode, affRepo)

		_, _, err := svc.RegisterWithVerification(ctx, "noadmit@test.com", "password", "", "", "", admitTestCode)
		require.ErrorIs(t, err, ErrInvitationCodeRequired)
		require.Empty(t, repo.created, "被拒绝的注册不该在库里留下账号")
	})

	t.Run("本周名额已满时提前拒绝，账号根本不落库", func(t *testing.T) {
		// 这正是把限额检查从「绑定时」挪到「建号前」的理由
		repo := &userRepoStub{nextID: 14}
		affRepo := admitRepoWithQuota(5)
		svc := admitRegisterService(repo, admitRegisterSettings("true", "5"), validCode, affRepo)

		_, _, err := svc.RegisterWithVerification(ctx, "quotafull@test.com", "password", "", "", "", admitTestCode)
		require.ErrorIs(t, err, ErrAffiliateWeeklyInviteLimitReached)
		require.Empty(t, repo.created, "名额满是可以提前判定的，不该先把账号建出来再说")
		require.Empty(t, repo.deletedIDs)
	})

	t.Run("回滚删号本身也失败时不掩盖原始错误", func(t *testing.T) {
		// 补偿式回滚的固有风险：补偿自己也会失败，那就真留下孤儿账号了。
		// 这时候能做的只有把它记下来，但绝不能因为删不掉就假装注册成功。
		repo := &userRepoStub{nextID: 16, deleteErr: errors.New("delete exploded")}
		affRepo := admitRepoWithQuota(1)
		affRepo.bindErr = errors.New("bind exploded")
		svc := admitRegisterService(repo, admitRegisterSettings("true", "5"), validCode, affRepo)

		_, _, err := svc.RegisterWithVerification(ctx, "delfail@test.com", "password", "", "", "", admitTestCode)
		require.Error(t, err)
		require.Equal(t, []int64{16}, repo.deletedIDs, "回滚要尝试过，哪怕失败")
	})

	t.Run("绑定邀请人失败时把已经建出来的账号删掉", func(t *testing.T) {
		// 账号此刻已经落库，不删的话这个邮箱就被半成品账号占死了
		repo := &userRepoStub{nextID: 15}
		affRepo := admitRepoWithQuota(1)
		affRepo.bindErr = errors.New("bind exploded")
		svc := admitRegisterService(repo, admitRegisterSettings("true", "5"), validCode, affRepo)

		_, _, err := svc.RegisterWithVerification(ctx, "bindfail@test.com", "password", "", "", "", admitTestCode)
		require.Error(t, err)
		require.Len(t, repo.created, 1, "失败发生在建号之后")
		require.Equal(t, []int64{15}, repo.deletedIDs, "必须把半成品账号回滚掉")
	})

	t.Run("注册码有效时邀请人名额已满不该拦住注册", func(t *testing.T) {
		// 顺着别人的邀请链接进来、手上又拿着单独发的注册码：放行本次注册的是注册码，
		// 邀请人名额满只意味着这笔归因挂不上去。为此把整次注册回滚掉的话，用户会拿到
		// 一句指向别人名额的报错，而他既看不见那个码也管不着那个人。
		repo := &userRepoStub{nextID: 17}
		affRepo := admitRepoWithQuota(5)
		svc := admitRegisterService(repo, admitRegisterSettings("true", "5"), validCode, affRepo)

		_, user, err := svc.RegisterWithVerification(ctx, "giftcode@test.com", "password", "", "", "INVITE1", admitTestCode)
		require.NoError(t, err, "注册码有效就该放行，别人的周名额不是拒绝理由")
		require.NotNil(t, user)
		require.Empty(t, affRepo.bindCalls, "名额满时不该挂上邀请人")
		require.Empty(t, repo.deletedIDs, "更不该把已经建好的账号回滚掉")
	})

	t.Run("回滚半成品账号走的是硬删而不是软删", func(t *testing.T) {
		// 软删会把一个从来没注册成功过的账号永久留在库里，还占着一个用户 ID
		repo := &purgeAwareUserRepoStub{}
		repo.nextID = 18
		affRepo := admitRepoWithQuota(1)
		affRepo.bindErr = errors.New("bind exploded")
		svc := admitRegisterService(repo, admitRegisterSettings("true", "5"), validCode, affRepo)

		_, _, err := svc.RegisterWithVerification(ctx, "hardpurge@test.com", "password", "", "", "", admitTestCode)
		require.Error(t, err)
		require.Equal(t, []int64{18}, repo.hardDeleted, "回滚必须真删，不能留下软删记录")
	})
}

// purgeAwareUserRepoStub 记录删号那一刻 context 里有没有带上「跳过软删除」的标记，
// 用来把「回滚必须是硬删」这条约束钉在单元测试里，而不是等上线后去库里数孤儿账号。
type purgeAwareUserRepoStub struct {
	userRepoStub
	hardDeleted []int64
}

func (s *purgeAwareUserRepoStub) Delete(ctx context.Context, id int64) error {
	if mixins.IsSoftDeleteSkipped(ctx) {
		s.hardDeleted = append(s.hardDeleted, id)
	}
	return s.userRepoStub.Delete(ctx, id)
}

func oauthBindAuthService(weeklyLimit string, repo *admitAffRepoStub) *AuthService {
	settings := &SettingService{settingRepo: signupSourceSettingRepoStub{values: map[string]string{
		SettingKeyAffiliateEnabled:           "true",
		SettingKeyAffiliateCodeAdmitsSignup:  "true",
		SettingKeyAffiliateWeeklyInviteLimit: weeklyLimit,
	}}}
	return &AuthService{
		settingService:   settings,
		affiliateService: &AffiliateService{repo: repo, settingService: settings},
	}
}

// TestBindOAuthAffiliateWeeklyLimit 覆盖第三方注册收尾那一步的同一条规则。
//
// 这一步是整个注册流程的最后一环，此刻账号、订阅、注册码核销都已经落库；它返回
// error，调用方就会把这些全部回滚掉。所以这里对「什么错该致命」的判断必须精确：
// 名额满只是挂不上邀请人，推荐码是假的才说明这次注册本身不该发生。
func TestBindOAuthAffiliateWeeklyLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("名额已满时跳过归因但不让注册失败", func(t *testing.T) {
		affRepo := admitRepoWithQuota(5)
		svc := oauthBindAuthService("5", affRepo)

		require.NoError(t, svc.bindOAuthAffiliate(ctx, 42, admitTestCode),
			"返回 error 会让调用方把刚建好的账号连同注册码一起回滚掉")
		require.Empty(t, affRepo.bindCalls, "名额满就不该挂上邀请人")
	})

	t.Run("推荐码本身无效时仍然要挡住建号", func(t *testing.T) {
		// 这是防止第三方登录绕开注册规则的既有语义，不在这次放宽的范围内
		affRepo := &admitAffRepoStub{summaryErr: ErrAffiliateProfileNotFound}
		svc := oauthBindAuthService("5", affRepo)

		require.Error(t, svc.bindOAuthAffiliate(ctx, 42, admitTestCode))
	})

	t.Run("名额够用时正常绑定邀请人", func(t *testing.T) {
		affRepo := admitRepoWithQuota(1)
		svc := oauthBindAuthService("5", affRepo)

		require.NoError(t, svc.bindOAuthAffiliate(ctx, 42, admitTestCode))
		require.Equal(t, []admitBindCall{{userID: 42, inviterID: 9}}, affRepo.bindCalls)
	})
}
