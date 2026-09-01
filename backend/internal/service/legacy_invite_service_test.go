//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"

	"github.com/stretchr/testify/require"
)

// 编译期断言：stub 必须完整实现各自的接口，接口新增方法时这里第一时间报错。
var (
	_ LegacyPaidLookup            = (*legacyPaidLookupStub)(nil)
	_ LegacyInviteClaimRepository = (*legacyClaimRepoStub)(nil)
	_ RedeemCodeRepository        = (*legacyRedeemRepoStub)(nil)
	_ EmailCache                  = (*legacyEmailCacheStub)(nil)
)

// ---------------------------------------------------------------- stubs

type legacyPaidLookupStub struct {
	user      *LegacyPaidUser
	err       error
	callCount int
	lastEmail string
}

func (s *legacyPaidLookupStub) FindPaidUser(_ context.Context, email string) (*LegacyPaidUser, error) {
	s.callCount++
	s.lastEmail = email
	if s.err != nil {
		return nil, s.err
	}
	return s.user, nil
}

func (s *legacyPaidLookupStub) Ping(context.Context) error { return nil }

type legacyClaimRepoStub struct {
	existing *LegacyInviteClaim
	getErr   error
	created  []*LegacyInviteClaim
	getCalls int
}

func (s *legacyClaimRepoStub) GetByEmail(context.Context, string) (*LegacyInviteClaim, error) {
	s.getCalls++
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.existing, nil
}

func (s *legacyClaimRepoStub) Create(_ context.Context, claim *LegacyInviteClaim) error {
	s.created = append(s.created, claim)
	return nil
}

// legacyRedeemRepoStub 只实现 Create，其余方法一律 panic：
// 领码流程不该碰到别的仓储方法，真碰到了应该立刻炸出来而不是悄悄放行。
type legacyRedeemRepoStub struct {
	created []*RedeemCode
}

func (s *legacyRedeemRepoStub) Create(_ context.Context, code *RedeemCode) error {
	s.created = append(s.created, code)
	return nil
}

func (s *legacyRedeemRepoStub) CreateBatch(context.Context, []RedeemCode) error {
	panic("unexpected CreateBatch call")
}
func (s *legacyRedeemRepoStub) GetByID(context.Context, int64) (*RedeemCode, error) {
	panic("unexpected GetByID call")
}
func (s *legacyRedeemRepoStub) GetByCode(context.Context, string) (*RedeemCode, error) {
	panic("unexpected GetByCode call")
}
func (s *legacyRedeemRepoStub) Update(context.Context, *RedeemCode) error {
	panic("unexpected Update call")
}
func (s *legacyRedeemRepoStub) BatchUpdate(context.Context, []int64, RedeemCodeBatchUpdateFields) (int64, error) {
	panic("unexpected BatchUpdate call")
}
func (s *legacyRedeemRepoStub) Delete(context.Context, int64) error {
	panic("unexpected Delete call")
}
func (s *legacyRedeemRepoStub) Use(context.Context, int64, int64) error {
	panic("unexpected Use call")
}
func (s *legacyRedeemRepoStub) List(context.Context, pagination.PaginationParams) ([]RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}
func (s *legacyRedeemRepoStub) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string) ([]RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}
func (s *legacyRedeemRepoStub) ListByUser(context.Context, int64, int) ([]RedeemCode, error) {
	panic("unexpected ListByUser call")
}
func (s *legacyRedeemRepoStub) ListByUserPaginated(context.Context, int64, pagination.PaginationParams, string) ([]RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected ListByUserPaginated call")
}
func (s *legacyRedeemRepoStub) SumPositiveBalanceByUser(context.Context, int64) (float64, error) {
	panic("unexpected SumPositiveBalanceByUser call")
}

// legacyEmailCacheStub 只服务验证码的存取，其余邮件缓存能力一律 panic。
type legacyEmailCacheStub struct {
	codes   map[string]*VerificationCodeData
	deleted []string
}

func newLegacyEmailCacheStub() *legacyEmailCacheStub {
	return &legacyEmailCacheStub{codes: map[string]*VerificationCodeData{}}
}

func (s *legacyEmailCacheStub) GetVerificationCode(_ context.Context, key string) (*VerificationCodeData, error) {
	data, ok := s.codes[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return data, nil
}

func (s *legacyEmailCacheStub) SetVerificationCode(_ context.Context, key string, data *VerificationCodeData, _ time.Duration) error {
	s.codes[key] = data
	return nil
}

func (s *legacyEmailCacheStub) DeleteVerificationCode(_ context.Context, key string) error {
	delete(s.codes, key)
	s.deleted = append(s.deleted, key)
	return nil
}

func (s *legacyEmailCacheStub) GetNotifyVerifyCode(context.Context, string) (*VerificationCodeData, error) {
	panic("unexpected GetNotifyVerifyCode call")
}
func (s *legacyEmailCacheStub) SetNotifyVerifyCode(context.Context, string, *VerificationCodeData, time.Duration) error {
	panic("unexpected SetNotifyVerifyCode call")
}
func (s *legacyEmailCacheStub) DeleteNotifyVerifyCode(context.Context, string) error {
	panic("unexpected DeleteNotifyVerifyCode call")
}
func (s *legacyEmailCacheStub) GetPasswordResetToken(context.Context, string) (*PasswordResetTokenData, error) {
	panic("unexpected GetPasswordResetToken call")
}
func (s *legacyEmailCacheStub) SetPasswordResetToken(context.Context, string, *PasswordResetTokenData, time.Duration) error {
	panic("unexpected SetPasswordResetToken call")
}
func (s *legacyEmailCacheStub) DeletePasswordResetToken(context.Context, string) error {
	panic("unexpected DeletePasswordResetToken call")
}
func (s *legacyEmailCacheStub) IsPasswordResetEmailInCooldown(context.Context, string) bool {
	panic("unexpected IsPasswordResetEmailInCooldown call")
}
func (s *legacyEmailCacheStub) SetPasswordResetEmailCooldown(context.Context, string, time.Duration) error {
	panic("unexpected SetPasswordResetEmailCooldown call")
}
func (s *legacyEmailCacheStub) IncrNotifyCodeUserRate(context.Context, int64, time.Duration) (int64, error) {
	panic("unexpected IncrNotifyCodeUserRate call")
}
func (s *legacyEmailCacheStub) GetNotifyCodeUserRate(context.Context, int64) (int64, error) {
	panic("unexpected GetNotifyCodeUserRate call")
}

// ---------------------------------------------------------------- helpers

type legacyInviteFixture struct {
	svc     *LegacyInviteService
	lookup  *legacyPaidLookupStub
	claims  *legacyClaimRepoStub
	redeems *legacyRedeemRepoStub
	cache   *legacyEmailCacheStub
}

func newLegacyInviteFixture(opts LegacyInviteOptions) *legacyInviteFixture {
	cache := newLegacyEmailCacheStub()
	f := &legacyInviteFixture{
		lookup:  &legacyPaidLookupStub{},
		claims:  &legacyClaimRepoStub{},
		redeems: &legacyRedeemRepoStub{},
		cache:   cache,
	}
	// settingService 传 nil：领码流程只在发信时用它取站点名，而发信路径不在单测覆盖内。
	f.svc = NewLegacyInviteService(f.lookup, f.claims, f.redeems, NewEmailService(nil, cache), nil, opts)
	return f
}

// seedVerifyCode 预置一个有效的领码验证码，模拟用户已经收到邮件的状态。
func (f *legacyInviteFixture) seedVerifyCode(email, code string) {
	f.cache.codes[scopedVerifyCodeKey(legacyInviteVerifyScope, email)] = &VerificationCodeData{
		Code:      code,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
}

func enabledOptions() LegacyInviteOptions {
	return LegacyInviteOptions{Enabled: true, MinPaidAmount: 300}
}

// ---------------------------------------------------------------- tests

// TestScopedVerifyCodeKey 钉死验证码的命名空间拼法。
// 领码和注册用的是同一个邮箱却是两套流程，key 一旦重合，后发的码会覆盖前一个，
// 用户就会遇到「刚收到的验证码提示无效」。
func TestScopedVerifyCodeKey(t *testing.T) {
	t.Parallel()

	require.Equal(t, "legacy_invite:a@b.com", scopedVerifyCodeKey("legacy_invite", "a@b.com"))
	// 空 scope 退回旧行为（key 就是邮箱），保证注册流程的既有数据不受影响
	require.Equal(t, "a@b.com", scopedVerifyCodeKey("", "a@b.com"))
	require.Equal(t, "a@b.com", scopedVerifyCodeKey("   ", "a@b.com"))
}

// TestLegacyInviteService_IsEnabled 验证「缺任何一个依赖都算关闭」。
// 半配置状态下必须在入口就拒绝，而不是放进来最后报 500。
func TestLegacyInviteService_IsEnabled(t *testing.T) {
	t.Parallel()

	var nilSvc *LegacyInviteService
	require.False(t, nilSvc.IsEnabled())

	f := newLegacyInviteFixture(enabledOptions())
	require.True(t, f.svc.IsEnabled())
	require.Equal(t, LegacyInviteStatus{Enabled: true, MinPaidAmount: 300}, f.svc.Status())

	// 配置里关掉
	off := newLegacyInviteFixture(LegacyInviteOptions{Enabled: false, MinPaidAmount: 300})
	require.False(t, off.svc.IsEnabled())
	// 关闭时门槛不外泄，前端也用不上
	require.Equal(t, LegacyInviteStatus{Enabled: false}, off.svc.Status())

	// 开着但旧站库没接上（lookup 为 nil）——等价于不可用
	noLookup := newLegacyInviteFixture(enabledOptions())
	noLookup.svc.lookup = nil
	require.False(t, noLookup.svc.IsEnabled())

	noRedeem := newLegacyInviteFixture(enabledOptions())
	noRedeem.svc.redeemRepo = nil
	require.False(t, noRedeem.svc.IsEnabled())

	noEmail := newLegacyInviteFixture(enabledOptions())
	noEmail.svc.emailService = nil
	require.False(t, noEmail.svc.IsEnabled())
}

// TestNormalizeLegacyInviteEmail 验证归一化：唯一索引、Redis key、跨库查询三处必须同口径。
func TestNormalizeLegacyInviteEmail(t *testing.T) {
	t.Parallel()

	got, err := normalizeLegacyInviteEmail("  Foo@Example.COM ")
	require.NoError(t, err)
	require.Equal(t, "foo@example.com", got)

	for _, bad := range []string{"", "   ", "not-an-email"} {
		_, err := normalizeLegacyInviteEmail(bad)
		require.Error(t, err, "input %q should be rejected", bad)
	}
}

// TestLegacyInviteService_SendClaimCode_Guards 覆盖发信前的三道闸门。
// 重点是「已领过就不发信」：白发一封邮件最后仍然拒绝，是纯粹的骚扰。
func TestLegacyInviteService_SendClaimCode_Guards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	off := newLegacyInviteFixture(LegacyInviteOptions{Enabled: false})
	require.ErrorIs(t, off.svc.SendClaimCode(ctx, "a@b.com"), ErrLegacyInviteDisabled)

	f := newLegacyInviteFixture(enabledOptions())
	require.Error(t, f.svc.SendClaimCode(ctx, "not-an-email"))
	require.Zero(t, f.claims.getCalls, "非法邮箱不该产生任何仓储调用")

	claimed := newLegacyInviteFixture(enabledOptions())
	claimed.claims.existing = &LegacyInviteClaim{Email: "a@b.com", RedeemCode: "OLD"}
	require.ErrorIs(t, claimed.svc.SendClaimCode(ctx, "A@B.com"), ErrLegacyInviteAlreadyClaimed)

	// 仓储报错要向上传播，不能被吞成「可以发信」
	broken := newLegacyInviteFixture(enabledOptions())
	sentinel := errors.New("db down")
	broken.claims.getErr = sentinel
	require.ErrorIs(t, broken.svc.SendClaimCode(ctx, "a@b.com"), sentinel)
}

// TestLegacyInviteService_Claim_Guards 覆盖领取前的闸门：功能关闭、邮箱非法、验证码不对。
func TestLegacyInviteService_Claim_Guards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	off := newLegacyInviteFixture(LegacyInviteOptions{Enabled: false})
	_, err := off.svc.Claim(ctx, "a@b.com", "123456", "1.2.3.4")
	require.ErrorIs(t, err, ErrLegacyInviteDisabled)

	f := newLegacyInviteFixture(enabledOptions())
	_, err = f.svc.Claim(ctx, "bad", "123456", "")
	require.Error(t, err)

	// 没发过码就来领 → 验证码无效，且绝不能触碰旧站库
	noCode := newLegacyInviteFixture(enabledOptions())
	_, err = noCode.svc.Claim(ctx, "a@b.com", "123456", "")
	require.ErrorIs(t, err, ErrInvalidVerifyCode)
	require.Zero(t, noCode.lookup.callCount)

	// 码不匹配
	wrong := newLegacyInviteFixture(enabledOptions())
	wrong.seedVerifyCode("a@b.com", "123456")
	_, err = wrong.svc.Claim(ctx, "a@b.com", "000000", "")
	require.ErrorIs(t, err, ErrInvalidVerifyCode)
	require.Zero(t, wrong.lookup.callCount)
}

// TestLegacyInviteService_Claim_NotEligible 验证不达标的两种形态都拒发：
// 旧站压根没这个邮箱，以及有账号但消费没到门槛。
func TestLegacyInviteService_Claim_NotEligible(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	missing := newLegacyInviteFixture(enabledOptions())
	missing.seedVerifyCode("a@b.com", "123456")
	missing.lookup.user = nil
	_, err := missing.svc.Claim(ctx, "a@b.com", "123456", "")
	require.ErrorIs(t, err, ErrLegacyInviteNotEligible)
	require.Empty(t, missing.redeems.created, "不达标不应生成邀请码")

	// 差一分钱也不放行
	short := newLegacyInviteFixture(enabledOptions())
	short.seedVerifyCode("a@b.com", "123456")
	short.lookup.user = &LegacyPaidUser{UserID: 7, Email: "a@b.com", PaidAmount: 299.99}
	_, err = short.svc.Claim(ctx, "a@b.com", "123456", "")
	require.ErrorIs(t, err, ErrLegacyInviteNotEligible)

	// 正好等于门槛应当放行——「满 300」包含 300 本身
	exact := newLegacyInviteFixture(enabledOptions())
	exact.seedVerifyCode("a@b.com", "123456")
	exact.lookup.user = &LegacyPaidUser{UserID: 7, Email: "a@b.com", PaidAmount: 300}
	result, err := exact.svc.Claim(ctx, "a@b.com", "123456", "")
	require.NoError(t, err)
	require.NotEmpty(t, result.InvitationCode)
}

// TestLegacyInviteService_Claim_LookupFailure 验证旧站库不可达时报的是「暂时查不了」
// 而不是「不达标」——后者会让本来符合条件的用户以为自己没资格而放弃。
func TestLegacyInviteService_Claim_LookupFailure(t *testing.T) {
	t.Parallel()

	f := newLegacyInviteFixture(enabledOptions())
	f.seedVerifyCode("a@b.com", "123456")
	f.lookup.err = errors.New("connection refused")

	_, err := f.svc.Claim(context.Background(), "a@b.com", "123456", "")
	require.ErrorIs(t, err, ErrLegacyInviteLookupFailed)
	require.Empty(t, f.redeems.created)
}

// TestLegacyInviteService_Claim_Success 覆盖完整成功路径，
// 并检查落库的各字段确实来自旧站查询结果而不是请求参数。
func TestLegacyInviteService_Claim_Success(t *testing.T) {
	t.Parallel()

	f := newLegacyInviteFixture(enabledOptions())
	f.seedVerifyCode("user@example.com", "654321")
	f.lookup.user = &LegacyPaidUser{UserID: 42, Email: "user@example.com", PaidAmount: 512.5}

	// 大小写和空白都应被归一化后再用于查询与落库
	result, err := f.svc.Claim(context.Background(), "  User@Example.com ", "654321", "9.9.9.9")
	require.NoError(t, err)
	require.False(t, result.AlreadyClaimed)
	require.NotEmpty(t, result.InvitationCode)
	require.Nil(t, result.ExpiresAt, "CodeExpiresDays 为 0 时邀请码不设过期")

	require.Equal(t, "user@example.com", f.lookup.lastEmail)

	require.Len(t, f.redeems.created, 1)
	created := f.redeems.created[0]
	require.Equal(t, RedeemTypeInvitation, created.Type)
	require.Equal(t, StatusUnused, created.Status)
	require.Equal(t, result.InvitationCode, created.Code)

	require.Len(t, f.claims.created, 1)
	claim := f.claims.created[0]
	require.Equal(t, "user@example.com", claim.Email)
	require.Equal(t, int64(42), claim.LegacyUserID)
	require.InDelta(t, 512.5, claim.PaidAmount, 0.001)
	require.Equal(t, "9.9.9.9", claim.ClaimedIP)
	require.Equal(t, result.InvitationCode, claim.RedeemCode)

	// 验证码用过即焚，同一个码不能领第二次
	require.Contains(t, f.cache.deleted, scopedVerifyCodeKey(legacyInviteVerifyScope, "user@example.com"))
}

// TestLegacyInviteService_Claim_WithExpiry 验证配置了有效期时邀请码带上过期时间。
func TestLegacyInviteService_Claim_WithExpiry(t *testing.T) {
	t.Parallel()

	f := newLegacyInviteFixture(LegacyInviteOptions{Enabled: true, MinPaidAmount: 300, CodeExpiresDays: 7})
	f.seedVerifyCode("a@b.com", "123456")
	f.lookup.user = &LegacyPaidUser{UserID: 1, PaidAmount: 300}

	result, err := f.svc.Claim(context.Background(), "a@b.com", "123456", "")
	require.NoError(t, err)
	require.NotNil(t, result.ExpiresAt)
	require.True(t, result.ExpiresAt.After(time.Now().AddDate(0, 0, 6)))
	require.True(t, result.ExpiresAt.Before(time.Now().AddDate(0, 0, 8)))
	require.Equal(t, result.ExpiresAt, f.redeems.created[0].ExpiresAt)
}

// TestLegacyInviteService_Claim_ReturnsExistingCode 验证「已领过」时把原来那个码还回去。
//
// 这一步必须发生在验证码校验之后：验证码已经被消费掉了，此时若直接报错，
// 用户既拿不到新码、也看不到旧码，只能反复重来。
func TestLegacyInviteService_Claim_ReturnsExistingCode(t *testing.T) {
	t.Parallel()

	f := newLegacyInviteFixture(enabledOptions())
	f.seedVerifyCode("a@b.com", "123456")
	f.claims.existing = &LegacyInviteClaim{Email: "a@b.com", RedeemCode: "EXISTING-CODE"}

	result, err := f.svc.Claim(context.Background(), "a@b.com", "123456", "")
	require.NoError(t, err)
	require.True(t, result.AlreadyClaimed)
	require.Equal(t, "EXISTING-CODE", result.InvitationCode)
	require.Zero(t, f.lookup.callCount, "已领过就不必再查旧站")
	require.Empty(t, f.redeems.created, "已领过不应再生成新码")
}

// TestLegacyInviteService_Claim_ConcurrentDuplicate 验证并发下的最后一道闸门。
//
// 两个请求同时通过了「没领过」的检查，唯一索引会让后落库的那个失败；
// 此时必须把先落库的那个码还给用户，而不是把刚生成的新码也发出去——
// 否则「每人一个码」在并发下就被击穿了。
func TestLegacyInviteService_Claim_ConcurrentDuplicate(t *testing.T) {
	t.Parallel()

	f := newLegacyInviteFixture(enabledOptions())
	f.seedVerifyCode("a@b.com", "123456")
	f.lookup.user = &LegacyPaidUser{UserID: 1, PaidAmount: 400}

	// 换成一个会模拟竞态的仓储：首次查询查无记录（并发窗口内赢家还没落库），
	// Create 撞唯一冲突，再查就能查到赢家那条。
	f.svc.claimRepo = &legacyClaimRepoRacingStub{
		winner:    &LegacyInviteClaim{Email: "a@b.com", RedeemCode: "WINNER-CODE"},
		createErr: ErrLegacyInviteAlreadyClaimed,
	}

	result, err := f.svc.Claim(context.Background(), "a@b.com", "123456", "")
	require.NoError(t, err)
	require.True(t, result.AlreadyClaimed)
	require.Equal(t, "WINNER-CODE", result.InvitationCode)
}

// legacyClaimRepoRacingStub 模拟并发：首次查询查无记录，Create 撞唯一冲突，再查就查到赢家。
type legacyClaimRepoRacingStub struct {
	winner    *LegacyInviteClaim
	createErr error
	getCalls  int
}

func (s *legacyClaimRepoRacingStub) GetByEmail(context.Context, string) (*LegacyInviteClaim, error) {
	s.getCalls++
	if s.getCalls == 1 {
		return nil, nil
	}
	return s.winner, nil
}

func (s *legacyClaimRepoRacingStub) Create(context.Context, *LegacyInviteClaim) error {
	return s.createErr
}

// TestProvideLegacyInviteOptions 验证配置到选项的搬运，以及 nil 配置不 panic。
func TestProvideLegacyInviteOptions(t *testing.T) {
	t.Parallel()

	require.Equal(t, LegacyInviteOptions{}, ProvideLegacyInviteOptions(nil))

	cfg := &config.Config{}
	cfg.LegacyInvite = config.LegacyInviteConfig{
		Enabled:         true,
		MinPaidAmount:   300,
		CodeExpiresDays: 30,
	}
	require.Equal(t, LegacyInviteOptions{Enabled: true, MinPaidAmount: 300, CodeExpiresDays: 30},
		ProvideLegacyInviteOptions(cfg))
}
