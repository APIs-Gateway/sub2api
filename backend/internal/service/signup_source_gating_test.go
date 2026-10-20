//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// 编译期断言：stub 必须完整实现设置仓储接口。
var _ SettingRepository = signupSourceSettingRepoStub{}

// signupSourceSettingRepoStub 只服务于分渠道注册开关的读取路径：
// GetValue 命中 values 就返回，否则返回 missErr（默认模拟"键不存在"）。
type signupSourceSettingRepoStub struct {
	values  map[string]string
	missErr error
}

func (r signupSourceSettingRepoStub) Get(context.Context, string) (*Setting, error) {
	panic("unexpected Get call")
}

func (r signupSourceSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	if r.missErr != nil {
		return "", r.missErr
	}
	return "", errors.New("setting not found")
}

func (r signupSourceSettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

func (r signupSourceSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

func (r signupSourceSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (r signupSourceSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (r signupSourceSettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

// TestNormalizeOAuthSignupSource 覆盖来源归一化：空值和未知来源都落到 email，
// 白名单内的来源原样返回，大小写与空白被清洗。
// 这层归一化决定了最终写进 users.signup_source 的值，也决定了开关读哪个键，
// 两者必须一致，否则会出现"关了某渠道却没挡住"。
func TestNormalizeOAuthSignupSource(t *testing.T) {
	t.Parallel()

	require.Equal(t, SignupSourceEmail, normalizeOAuthSignupSource(""))
	require.Equal(t, SignupSourceEmail, normalizeOAuthSignupSource("email"))
	require.Equal(t, SignupSourceEmail, normalizeOAuthSignupSource("  "))
	// 未知来源不应凭空造出一个新渠道，统一落到 email
	require.Equal(t, SignupSourceEmail, normalizeOAuthSignupSource("myspace"))

	for _, source := range []string{"github", "google", "linuxdo", "wechat", "oidc", "dingtalk"} {
		require.Equal(t, source, normalizeOAuthSignupSource(source))
		require.Equal(t, source, normalizeOAuthSignupSource("  "+source+"  "))
	}
	// 大小写不敏感
	require.Equal(t, "github", normalizeOAuthSignupSource("GitHub"))
}

// TestSignupSourceEnabledSettingKey 固定设置键的拼法。
// 这个键会写进数据库，改动等于破坏既有站点的配置，所以用测试钉死。
func TestSignupSourceEnabledSettingKey(t *testing.T) {
	t.Parallel()

	require.Equal(t, "auth_source_email_signup_enabled", SignupSourceEnabledSettingKey("email"))
	require.Equal(t, "auth_source_github_signup_enabled", SignupSourceEnabledSettingKey("github"))
	// 未知来源先归一化再拼键，不会产生 auth_source_myspace_signup_enabled 这种野键
	require.Equal(t, "auth_source_email_signup_enabled", SignupSourceEnabledSettingKey("myspace"))
}

// TestParseSignupSourceEnabled 验证批量读取的缺省语义：
// 只有显式的 "false" 才算关闭，键不存在或为空都视为允许。
func TestParseSignupSourceEnabled(t *testing.T) {
	t.Parallel()

	// 空设置：全部来源都应放行，且七个来源都要有值（前端要据此渲染开关）
	all := parseSignupSourceEnabled(map[string]string{})
	require.Len(t, all, len(SignupSources))
	for _, source := range SignupSources {
		require.True(t, all[source], "source %q should default to enabled", source)
	}

	mixed := parseSignupSourceEnabled(map[string]string{
		SignupSourceEnabledSettingKey("email"):  "false",
		SignupSourceEnabledSettingKey("github"): "true",
		SignupSourceEnabledSettingKey("google"): "",
	})
	require.False(t, mixed["email"], "explicit false must disable")
	require.True(t, mixed["github"])
	require.True(t, mixed["google"], "empty value must fall back to enabled")
	require.True(t, mixed["linuxdo"], "absent key must fall back to enabled")
}

// TestIsSignupSourceEnabled 覆盖单渠道判定，重点是"缺省放行"这条与其它开关
// 相反的约定：设置不存在说明站点还没配过分渠道开关，此时必须保持升级前的行为，
// 由 registration_enabled 总闸继续把关，否则升级会把所有站点的注册全部关死。
func TestIsSignupSourceEnabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// 键不存在（repo 报错）→ 放行
	missing := &SettingService{settingRepo: signupSourceSettingRepoStub{}}
	require.True(t, missing.IsSignupSourceEnabled(ctx, "github"))
	require.True(t, missing.IsSignupSourceEnabled(ctx, SignupSourceEmail))

	// 显式关闭 email，但 github 未配置 → 只有 email 被挡
	onlyThirdParty := &SettingService{settingRepo: signupSourceSettingRepoStub{
		values: map[string]string{
			SignupSourceEnabledSettingKey(SignupSourceEmail): "false",
		},
	}}
	require.False(t, onlyThirdParty.IsSignupSourceEnabled(ctx, SignupSourceEmail))
	require.True(t, onlyThirdParty.IsSignupSourceEnabled(ctx, "github"))

	// 空字符串与非 false 的任意值都视为允许
	loose := &SettingService{settingRepo: signupSourceSettingRepoStub{
		values: map[string]string{
			SignupSourceEnabledSettingKey("github"): "",
			SignupSourceEnabledSettingKey("google"): "true",
		},
	}}
	require.True(t, loose.IsSignupSourceEnabled(ctx, "github"))
	require.True(t, loose.IsSignupSourceEnabled(ctx, "google"))

	// 带空白的 false 也应生效（管理端写入不保证已 trim）
	trimmed := &SettingService{settingRepo: signupSourceSettingRepoStub{
		values: map[string]string{
			SignupSourceEnabledSettingKey("wechat"): "  false  ",
		},
	}}
	require.False(t, trimmed.IsSignupSourceEnabled(ctx, "wechat"))
}
