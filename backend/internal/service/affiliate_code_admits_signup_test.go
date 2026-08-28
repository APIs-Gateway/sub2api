//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// admitAffRepoStub 在周上限那套 stub 之上放开 GetAffiliateByCode：
// 准入判断必须先把邀请码解析成邀请人，才谈得上查他本周还剩多少名额。
type admitAffRepoStub struct {
	weeklyInviteAffRepoStub
	summary    *AffiliateSummary
	summaryErr error
	codeSeen   string
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
