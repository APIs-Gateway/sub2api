package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// 旧站付费用户领码流程的错误。
//
// 这些错误会原样映射成前端可辨识的 reason，所以措辞要能直接指导用户下一步动作：
// 「没达标」和「暂时查不了」必须区分开——后者是本站或旧站的故障，让用户稍后重试即可，
// 如果统一报成「没达标」，用户会以为自己不符合条件而放弃。
var (
	ErrLegacyInviteDisabled       = infraerrors.NotFound("LEGACY_INVITE_DISABLED", "legacy invite claim is not available")
	ErrLegacyInviteNotEligible    = infraerrors.Forbidden("LEGACY_INVITE_NOT_ELIGIBLE", "this account has not reached the required amount on the legacy site")
	ErrLegacyInviteAlreadyClaimed = infraerrors.Conflict("LEGACY_INVITE_ALREADY_CLAIMED", "an invite code has already been issued for this email")
	ErrLegacyInviteLookupFailed   = infraerrors.ServiceUnavailable("LEGACY_INVITE_LOOKUP_FAILED", "cannot verify the legacy account right now, please try again later")
)

// legacyInviteVerifyScope 是领码验证码在 Redis 里的命名空间。
//
// 必须和注册验证码分开：领码填的是**旧站**邮箱，而这个邮箱通常还没在本站注册，
// 用户很可能先领码、紧接着拿码去注册，两个流程会在几分钟内先后用到同一个邮箱。
// 共用一个 key 会让后发的验证码把前一个覆盖掉。
const legacyInviteVerifyScope = "legacy_invite"

// LegacyPaidUser 是旧站库里查出来的付费画像。
type LegacyPaidUser struct {
	UserID int64
	Email  string
	// PaidAmount 是实付金额合计（已扣除退款），单位与旧站 payment_orders.pay_amount 一致。
	PaidAmount float64
	// UsageCost 是累计用量消费（USD），口径为旧站 usage_logs.actual_cost 合计。
	//
	// 查不到（旧站库没给这张表的读权限、或查询本身失败）时为 0，
	// 达标判定会自动退化成只看 PaidAmount，不会因此把人误拒成「查询失败」。
	UsageCost float64
}

// LegacyPaidLookup 只读地查询旧站的付费情况。
//
// 实现方连的是旧站数据库，因此约定：查询失败一律返回 error，绝不用「查不到」冒充「不达标」。
type LegacyPaidLookup interface {
	// FindPaidUser 返回该邮箱在旧站的付费与用量合计；旧站没有这个邮箱时返回 (nil, nil)。
	FindPaidUser(ctx context.Context, email string) (*LegacyPaidUser, error)
	// Ping 用于健康检查，判断旧站库当前是否可达。
	Ping(ctx context.Context) error
}

// LegacyInviteClaim 是一次领取记录。
type LegacyInviteClaim struct {
	ID           int64
	Email        string
	LegacyUserID int64
	PaidAmount   float64
	// UsageCost 是领取当时算出的旧站累计用量消费（USD）。
	// 与 PaidAmount 一起留档，才能事后看出这个人是靠哪条口径达标的。
	UsageCost  float64
	RedeemCode string
	ClaimedIP  string
	CreatedAt  time.Time
}

// LegacyInviteClaimRepository 存取本站这一侧的发放流水。
type LegacyInviteClaimRepository interface {
	// GetByEmail 返回该邮箱已有的领取记录；没有则返回 (nil, nil)。
	GetByEmail(ctx context.Context, email string) (*LegacyInviteClaim, error)
	// Create 落库一条领取记录。邮箱重复时必须返回 ErrLegacyInviteAlreadyClaimed，
	// 这是并发下的最后一道闸门。
	Create(ctx context.Context, claim *LegacyInviteClaim) error
}

// LegacyInviteOptions 是功能的静态配置快照，来自 config.LegacyInviteConfig。
type LegacyInviteOptions struct {
	Enabled       bool
	MinPaidAmount float64
	// MinUsageCost 是第二条达标口径（USD 用量消费），与 MinPaidAmount 是「或」的关系。
	// 0 表示这条口径没开。
	MinUsageCost    float64
	CodeExpiresDays int
}

// LegacyInviteService 实现「旧站付费用户领取本站一次性邀请码」。
//
// 完整流程分两步，中间隔着一次邮箱所有权证明：
//  1. SendClaimCode：确认功能开着、邮箱没领过，然后往这个邮箱发一个验证码。
//     注意此时**不**查旧站是否达标——达标与否属于用户的消费隐私，
//     在证明邮箱归属之前就告诉调用方，等于开放一个「谁在旧站花过 300」的枚举接口。
//  2. Claim：校验验证码（证明邮箱确实是他的），再查旧站达标情况，
//     达标就生成一个一次性邀请码并记账。
//
// 达标有两条并列的口径（见 isEligible）：旧站实付金额，或者旧站累计用量消费。
type LegacyInviteService struct {
	lookup       LegacyPaidLookup
	claimRepo    LegacyInviteClaimRepository
	redeemRepo   RedeemCodeRepository
	emailService *EmailService
	settingSvc   *SettingService
	opts         LegacyInviteOptions
}

// NewLegacyInviteService 构造服务。lookup 为 nil（未配置旧站库）时功能自动关闭。
func NewLegacyInviteService(
	lookup LegacyPaidLookup,
	claimRepo LegacyInviteClaimRepository,
	redeemRepo RedeemCodeRepository,
	emailService *EmailService,
	settingSvc *SettingService,
	opts LegacyInviteOptions,
) *LegacyInviteService {
	return &LegacyInviteService{
		lookup:       lookup,
		claimRepo:    claimRepo,
		redeemRepo:   redeemRepo,
		emailService: emailService,
		settingSvc:   settingSvc,
		opts:         opts,
	}
}

// LegacyInviteStatus 是给前端渲染领码页用的元信息。
type LegacyInviteStatus struct {
	Enabled       bool    `json:"enabled"`
	MinPaidAmount float64 `json:"min_paid_amount"`
	// MinUsageCost 为 0 时前端只展示实付金额那一条门槛。
	MinUsageCost float64 `json:"min_usage_cost"`
}

// LegacyInviteClaimResult 是领码成功后的返回。
type LegacyInviteClaimResult struct {
	InvitationCode string     `json:"invitation_code"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	// AlreadyClaimed 为 true 表示这个码是之前就发过的同一个，不是新生成的。
	// 让用户能重新看到自己的码，而不是因为「已领过」被彻底挡在门外找不回来。
	AlreadyClaimed bool `json:"already_claimed"`
}

// IsEnabled 返回功能是否可用。缺少任何一个依赖都视为关闭，
// 避免半配置状态下把请求放进来最后才报 500。
func (s *LegacyInviteService) IsEnabled() bool {
	return s != nil &&
		s.opts.Enabled &&
		s.lookup != nil &&
		s.claimRepo != nil &&
		s.redeemRepo != nil &&
		s.emailService != nil
}

// Status 返回领码页需要的元信息。
func (s *LegacyInviteService) Status() LegacyInviteStatus {
	if !s.IsEnabled() {
		return LegacyInviteStatus{Enabled: false}
	}
	return LegacyInviteStatus{
		Enabled:       true,
		MinPaidAmount: s.opts.MinPaidAmount,
		MinUsageCost:  s.opts.MinUsageCost,
	}
}

// isEligible 判定这个旧站账号够不够格领码。
//
// 两条口径是「或」的关系：实付金额达标，或者用量消费达标，满足任意一条即可。
// 订阅制用户的实付人民币可能不高，但按量折算的消费额很大，只看实付会把他们挡在门外。
//
// MinUsageCost 为 0 表示这条口径没开，必须先判正数——否则「用量 0 >= 门槛 0」
// 会把所有人都放进来。MinPaidAmount 不加这层保护：它是主口径，
// 把它显式设成 0 本来就等于「谁都放行」。
func (s *LegacyInviteService) isEligible(paid *LegacyPaidUser) bool {
	if s == nil || paid == nil {
		return false
	}
	if paid.PaidAmount >= s.opts.MinPaidAmount {
		return true
	}
	return s.opts.MinUsageCost > 0 && paid.UsageCost >= s.opts.MinUsageCost
}

// SendClaimCode 向旧站邮箱发送领码验证码。
func (s *LegacyInviteService) SendClaimCode(ctx context.Context, email string, locale ...string) error {
	if !s.IsEnabled() {
		return ErrLegacyInviteDisabled
	}

	normalized, err := normalizeLegacyInviteEmail(email)
	if err != nil {
		return err
	}

	// 已经领过就别再发信了：这个提示只会回给知道该邮箱的人，
	// 而「已领过」本身不涉及消费金额，不构成隐私泄露。
	existing, err := s.claimRepo.GetByEmail(ctx, normalized)
	if err != nil {
		return err
	}
	if existing != nil {
		return ErrLegacyInviteAlreadyClaimed
	}

	siteName := "Sub2API"
	if s.settingSvc != nil {
		siteName = s.settingSvc.GetSiteName(ctx)
	}
	return s.emailService.SendScopedVerifyCode(ctx, legacyInviteVerifyScope, normalized, siteName, locale...)
}

// Claim 校验验证码并发放一次性邀请码。
func (s *LegacyInviteService) Claim(ctx context.Context, email, verifyCode, clientIP string) (*LegacyInviteClaimResult, error) {
	if !s.IsEnabled() {
		return nil, ErrLegacyInviteDisabled
	}

	normalized, err := normalizeLegacyInviteEmail(email)
	if err != nil {
		return nil, err
	}

	if err := s.emailService.VerifyScopedCode(ctx, legacyInviteVerifyScope, normalized, strings.TrimSpace(verifyCode)); err != nil {
		return nil, err
	}

	// 验证码已经被消费掉了，所以这里要先把「之前领过」的情况原样还给用户，
	// 否则他会既拿不到新码、也看不到旧码。
	if existing, err := s.claimRepo.GetByEmail(ctx, normalized); err != nil {
		return nil, err
	} else if existing != nil {
		return &LegacyInviteClaimResult{InvitationCode: existing.RedeemCode, AlreadyClaimed: true}, nil
	}

	paid, err := s.lookup.FindPaidUser(ctx, normalized)
	if err != nil {
		// 旧站库不可达时明确报「暂时查不了」，不要退化成「不达标」。
		return nil, ErrLegacyInviteLookupFailed
	}
	if !s.isEligible(paid) {
		return nil, ErrLegacyInviteNotEligible
	}

	code, err := GenerateRedeemCode()
	if err != nil {
		return nil, err
	}

	var expiresAt *time.Time
	if s.opts.CodeExpiresDays > 0 {
		expiry := time.Now().AddDate(0, 0, s.opts.CodeExpiresDays)
		expiresAt = &expiry
	}

	redeemCode := &RedeemCode{
		Code:      code,
		Type:      RedeemTypeInvitation,
		Status:    StatusUnused,
		ExpiresAt: expiresAt,
		Notes:     "legacy paid user claim",
	}
	if err := s.redeemRepo.Create(ctx, redeemCode); err != nil {
		return nil, err
	}

	claim := &LegacyInviteClaim{
		Email:        normalized,
		LegacyUserID: paid.UserID,
		PaidAmount:   paid.PaidAmount,
		UsageCost:    paid.UsageCost,
		RedeemCode:   code,
		ClaimedIP:    clientIP,
	}
	if err := s.claimRepo.Create(ctx, claim); err != nil {
		// 并发下另一个请求抢先落了库。此时本次多生成的邀请码不再交给用户，
		// 而是把先前那一个还回去，保证「每人一个码」不被并发击穿。
		if errors.Is(err, ErrLegacyInviteAlreadyClaimed) {
			if existing, lookupErr := s.claimRepo.GetByEmail(ctx, normalized); lookupErr == nil && existing != nil {
				return &LegacyInviteClaimResult{InvitationCode: existing.RedeemCode, AlreadyClaimed: true}, nil
			}
		}
		return nil, err
	}

	return &LegacyInviteClaimResult{InvitationCode: code, ExpiresAt: expiresAt}, nil
}

// normalizeLegacyInviteEmail 统一小写并去空白。
// 领码和查旧站都用这个结果，保证唯一索引、Redis key、跨库查询三处口径一致。
func normalizeLegacyInviteEmail(email string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || !strings.Contains(normalized, "@") {
		return "", infraerrors.BadRequest("INVALID_EMAIL", "invalid email address")
	}
	return normalized, nil
}

// ProvideLegacyInviteOptions 从配置里取出功能所需的静态参数。
//
// 单独抽一个 provider 是为了让 service 层只依赖一个扁平的选项结构，
// 而不是把整个 *config.Config 拖进来——后者会让单元测试不得不构造一大坨无关配置。
func ProvideLegacyInviteOptions(cfg *config.Config) LegacyInviteOptions {
	if cfg == nil {
		return LegacyInviteOptions{}
	}
	return LegacyInviteOptions{
		Enabled:         cfg.LegacyInvite.Enabled,
		MinPaidAmount:   cfg.LegacyInvite.MinPaidAmount,
		MinUsageCost:    cfg.LegacyInvite.MinUsageCost,
		CodeExpiresDays: cfg.LegacyInvite.CodeExpiresDays,
	}
}
