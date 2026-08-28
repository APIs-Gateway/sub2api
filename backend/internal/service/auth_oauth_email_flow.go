package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// SignupSourceEmail 是账号密码注册对应的来源标识。
const SignupSourceEmail = "email"

// SignupSources 列出所有可独立控制的注册来源，取值与 users.signup_source 的落库值一致。
// 它同时是 normalizeOAuthSignupSource 的白名单，避免两处列表各自漂移。
var SignupSources = []string{SignupSourceEmail, "github", "google", "linuxdo", "wechat", "oidc", "dingtalk"}

// SignupSourceEnabledSettingKey 返回某注册来源的开关设置键（auth_source_<source>_signup_enabled）。
// 传入的未知来源会被归一成 email，与实际落库的 signup_source 保持一致。
func SignupSourceEnabledSettingKey(source string) string {
	return SettingKeySignupSourcePrefix + normalizeOAuthSignupSource(source) + SettingKeySignupSourceSuffix
}

func normalizeOAuthSignupSource(signupSource string) string {
	signupSource = strings.TrimSpace(strings.ToLower(signupSource))
	if signupSource == "" {
		return SignupSourceEmail
	}
	for _, known := range SignupSources {
		if known == signupSource {
			return signupSource
		}
	}
	return SignupSourceEmail
}

// SendPendingOAuthVerifyCode sends a local verification code for pending OAuth
// account-creation flows without relying on the public registration gate.
func (s *AuthService) SendPendingOAuthVerifyCode(ctx context.Context, email string, locale ...string) (*SendVerifyCodeResult, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return nil, ErrEmailVerifyRequired
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, ErrEmailVerifyRequired
	}
	if isReservedEmail(email) {
		return nil, ErrEmailReserved
	}
	if s == nil || s.emailService == nil {
		return nil, ErrServiceUnavailable
	}

	siteName := "Sub2API"
	if s.settingService != nil {
		siteName = s.settingService.GetSiteName(ctx)
	}
	if err := s.emailService.SendVerifyCode(ctx, email, siteName, firstEmailLocale(locale)); err != nil {
		return nil, err
	}
	return &SendVerifyCodeResult{
		Countdown: int(verifyCodeCooldown / time.Second),
	}, nil
}

// validateOAuthRegistrationInvitation 校验 OAuth 注册的准入条件。
//
// 第二个返回值是「真正生效的邀请人邀请码」：注册码不可用但邀请人邀请码放行了这次注册时，
// 用户填在注册码栏里的值会被提升成推荐码，调用方必须用它去绑定邀请关系，
// 否则人进来了却没挂到邀请人名下，邀请奖励就发不出去。
func (s *AuthService) validateOAuthRegistrationInvitation(ctx context.Context, invitationCode, affiliateCode string) (*RedeemCode, string, error) {
	effectiveAffiliateCode := strings.TrimSpace(affiliateCode)
	if s == nil || s.settingService == nil || !s.settingService.IsInvitationCodeEnabled(ctx) {
		return nil, effectiveAffiliateCode, nil
	}
	if s.redeemRepo == nil && s.oauthEmailFlowClient(ctx) == nil {
		return nil, effectiveAffiliateCode, ErrServiceUnavailable
	}

	invitationCode = strings.TrimSpace(invitationCode)
	admitByAffiliate := func(admitErr error) (*RedeemCode, string, error) {
		promoted, quotaErr := s.admitSignupByAffiliateCode(ctx, effectiveAffiliateCode, invitationCode)
		if quotaErr != nil {
			return nil, effectiveAffiliateCode, quotaErr
		}
		if promoted == "" {
			return nil, effectiveAffiliateCode, admitErr
		}
		return nil, promoted, nil
	}

	if invitationCode == "" {
		return admitByAffiliate(ErrInvitationCodeRequired)
	}

	redeemCode, err := s.loadOAuthRegistrationInvitation(ctx, invitationCode)
	if err != nil {
		return admitByAffiliate(ErrInvitationCodeInvalid)
	}
	if redeemCode.Type != RedeemTypeInvitation || !redeemCode.CanUse() {
		return admitByAffiliate(ErrInvitationCodeInvalid)
	}
	return redeemCode, effectiveAffiliateCode, nil
}

// VerifyOAuthEmailCode verifies the locally entered email verification code for
// third-party signup and binding flows. This is intentionally independent from
// the global registration email verification toggle.
func (s *AuthService) VerifyOAuthEmailCode(ctx context.Context, email, verifyCode string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	verifyCode = strings.TrimSpace(verifyCode)

	if email == "" {
		return ErrEmailVerifyRequired
	}
	if verifyCode == "" {
		return ErrEmailVerifyRequired
	}
	if s == nil || s.emailService == nil {
		return ErrServiceUnavailable
	}
	return s.emailService.VerifyCode(ctx, email, verifyCode)
}

// RegisterOAuthEmailAccount creates a local account from a third-party first
// login after the user has verified a local email address.
func (s *AuthService) RegisterOAuthEmailAccount(
	ctx context.Context,
	email string,
	password string,
	verifyCode string,
	invitationCode string,
	signupSource string,
	affiliateCode string,
) (*TokenPair, *User, error) {
	if s == nil {
		return nil, nil, ErrServiceUnavailable
	}
	if s.settingService == nil || (!s.settingService.IsRegistrationEnabled(ctx) && !s.canBypassRegistrationDisabledForOAuth(ctx, signupSource)) {
		return nil, nil, ErrRegDisabled
	}
	// 该第三方渠道被单独关闭注册时，即便总闸开着也不放行
	if !s.settingService.IsSignupSourceEnabled(ctx, signupSource) {
		return nil, nil, ErrSignupSourceDisabled
	}

	email = strings.TrimSpace(strings.ToLower(email))
	if isReservedEmail(email) {
		return nil, nil, ErrEmailReserved
	}
	if err := s.validateRegistrationEmailPolicy(ctx, email); err != nil {
		slog.Error("oauth email register: policy rejected", "email", email, "error", err.Error())
		return nil, nil, err
	}
	if err := s.VerifyOAuthEmailCode(ctx, email, verifyCode); err != nil {
		slog.Error("oauth email register: verify code failed", "email", email, "error", err.Error())
		return nil, nil, err
	}

	if _, _, err := s.validateOAuthRegistrationInvitation(ctx, invitationCode, affiliateCode); err != nil {
		slog.Error("oauth email register: invitation failed", "email", email, "error", err.Error())
		return nil, nil, err
	}

	existsEmail, err := s.userRepo.ExistsByEmail(ctx, email)
	if err != nil {
		slog.Error("oauth email register: ExistsByEmail failed", "email", email, "error", err.Error())
		return nil, nil, ErrServiceUnavailable
	}
	if existsEmail {
		return nil, nil, ErrEmailExists
	}

	hashedPassword, err := s.HashPassword(password)
	if err != nil {
		return nil, nil, fmt.Errorf("hash password: %w", err)
	}

	signupSource = normalizeOAuthSignupSource(signupSource)
	grantPlan := s.resolveSignupGrantPlan(ctx, signupSource)

	user := &User{
		Email:        email,
		PasswordHash: hashedPassword,
		Role:         RoleUser,
		Balance:      grantPlan.Balance,
		Concurrency:  grantPlan.Concurrency,
		Status:       StatusActive,
		SignupSource: signupSource,
	}

	if err := s.userRepo.Create(ctx, user); err != nil {
		if errors.Is(err, ErrEmailExists) {
			return nil, nil, ErrEmailExists
		}
		slog.Error("oauth email register: userRepo.Create failed", "email", email, "signup_source", signupSource, "error", err.Error())
		return nil, nil, ErrServiceUnavailable
	}

	tokenPair, err := s.GenerateTokenPair(ctx, user, "")
	if err != nil {
		_ = s.RollbackOAuthEmailAccountCreation(ctx, user.ID, "")
		return nil, nil, fmt.Errorf("generate token pair: %w", err)
	}
	return tokenPair, user, nil
}

// RegisterVerifiedOAuthEmailAccount creates a local account from an OAuth
// provider that has already returned a verified email address.
func (s *AuthService) RegisterVerifiedOAuthEmailAccount(
	ctx context.Context,
	email string,
	password string,
	invitationCode string,
	signupSource string,
	affiliateCode string,
) (*TokenPair, *User, error) {
	if s == nil {
		return nil, nil, ErrServiceUnavailable
	}
	if s.settingService == nil || (!s.settingService.IsRegistrationEnabled(ctx) && !s.canBypassRegistrationDisabledForOAuth(ctx, signupSource)) {
		return nil, nil, ErrRegDisabled
	}
	// 该第三方渠道被单独关闭注册时，即便总闸开着也不放行
	if !s.settingService.IsSignupSourceEnabled(ctx, signupSource) {
		return nil, nil, ErrSignupSourceDisabled
	}

	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || len(email) > 255 {
		return nil, nil, ErrEmailVerifyRequired
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, nil, ErrEmailVerifyRequired
	}
	if isReservedEmail(email) {
		return nil, nil, ErrEmailReserved
	}
	if err := s.validateRegistrationEmailPolicy(ctx, email); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(password) == "" {
		return nil, nil, infraerrors.BadRequest("PASSWORD_REQUIRED", "password is required")
	}
	if _, _, err := s.validateOAuthRegistrationInvitation(ctx, invitationCode, affiliateCode); err != nil {
		return nil, nil, err
	}

	existsEmail, err := s.userRepo.ExistsByEmail(ctx, email)
	if err != nil {
		return nil, nil, ErrServiceUnavailable
	}
	if existsEmail {
		return nil, nil, ErrEmailExists
	}

	hashedPassword, err := s.HashPassword(password)
	if err != nil {
		return nil, nil, fmt.Errorf("hash password: %w", err)
	}

	signupSource = normalizeOAuthSignupSource(signupSource)
	grantPlan := s.resolveSignupGrantPlan(ctx, signupSource)
	var defaultRPMLimit int
	if s.settingService != nil {
		defaultRPMLimit = s.settingService.GetDefaultUserRPMLimit(ctx)
	}
	user := &User{
		Email:        email,
		PasswordHash: hashedPassword,
		Role:         RoleUser,
		Balance:      grantPlan.Balance,
		Concurrency:  grantPlan.Concurrency,
		RPMLimit:     defaultRPMLimit,
		Status:       StatusActive,
		SignupSource: signupSource,
	}

	if err := s.userRepo.Create(ctx, user); err != nil {
		if errors.Is(err, ErrEmailExists) {
			return nil, nil, ErrEmailExists
		}
		return nil, nil, ErrServiceUnavailable
	}

	tokenPair, err := s.GenerateTokenPair(ctx, user, "")
	if err != nil {
		_ = s.RollbackOAuthEmailAccountCreation(ctx, user.ID, "")
		return nil, nil, fmt.Errorf("generate token pair: %w", err)
	}
	return tokenPair, user, nil
}

// FinalizeOAuthEmailAccount applies invitation usage and normal signup bootstrap
// only after the pending OAuth flow has fully reached its last reversible step.
func (s *AuthService) FinalizeOAuthEmailAccount(
	ctx context.Context,
	user *User,
	invitationCode string,
	signupSource string,
	affiliateCode string,
) error {
	if s == nil || user == nil || user.ID <= 0 {
		return ErrServiceUnavailable
	}

	signupSource = normalizeOAuthSignupSource(signupSource)
	invitationRedeemCode, effectiveAffiliateCode, err := s.validateOAuthRegistrationInvitation(ctx, invitationCode, affiliateCode)
	if err != nil {
		return err
	}
	if invitationRedeemCode != nil {
		if err := s.useOAuthRegistrationInvitation(ctx, invitationRedeemCode.ID, user.ID); err != nil {
			return ErrInvitationCodeInvalid
		}
	}

	s.updateOAuthSignupSource(ctx, user.ID, signupSource)
	grantPlan := s.resolveSignupGrantPlan(ctx, signupSource)
	s.assignSubscriptions(ctx, user.ID, grantPlan.Subscriptions, "auto assigned by signup defaults")
	// snapshot user × platform quota（fail-open）
	_ = s.snapshotPlatformQuotaDefaults(ctx, user.ID, &grantPlan)
	return s.bindOAuthAffiliate(ctx, user.ID, effectiveAffiliateCode)
}

// RollbackOAuthEmailAccountCreation removes a partially-created local account
// and restores any invitation code already consumed by that account.
func (s *AuthService) RollbackOAuthEmailAccountCreation(ctx context.Context, userID int64, invitationCode string) error {
	if s == nil || s.userRepo == nil || userID <= 0 {
		return ErrServiceUnavailable
	}
	if err := s.restoreOAuthRegistrationInvitation(ctx, invitationCode, userID); err != nil {
		return err
	}
	if err := s.userRepo.Delete(ctx, userID); err != nil {
		return fmt.Errorf("delete created oauth user: %w", err)
	}
	return nil
}

func (s *AuthService) restoreOAuthRegistrationInvitation(ctx context.Context, invitationCode string, userID int64) error {
	if s == nil || s.settingService == nil || !s.settingService.IsInvitationCodeEnabled(ctx) {
		return nil
	}
	if s.redeemRepo == nil && s.oauthEmailFlowClient(ctx) == nil {
		return ErrServiceUnavailable
	}

	invitationCode = strings.TrimSpace(invitationCode)
	if invitationCode == "" || userID <= 0 {
		return nil
	}

	redeemCode, err := s.loadOAuthRegistrationInvitation(ctx, invitationCode)
	if err != nil {
		if errors.Is(err, ErrRedeemCodeNotFound) {
			return nil
		}
		return fmt.Errorf("load invitation code: %w", err)
	}
	if redeemCode.Type != RedeemTypeInvitation || redeemCode.Status != StatusUsed || redeemCode.UsedBy == nil || *redeemCode.UsedBy != userID {
		return nil
	}

	redeemCode.Status = StatusUnused
	redeemCode.UsedBy = nil
	redeemCode.UsedAt = nil
	if err := s.updateOAuthRegistrationInvitation(ctx, redeemCode); err != nil {
		return fmt.Errorf("restore invitation code: %w", err)
	}
	return nil
}

func (s *AuthService) oauthEmailFlowClient(ctx context.Context) *dbent.Client {
	if s == nil || s.entClient == nil {
		return nil
	}
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return s.entClient
}

func (s *AuthService) loadOAuthRegistrationInvitation(ctx context.Context, invitationCode string) (*RedeemCode, error) {
	if client := s.oauthEmailFlowClient(ctx); client != nil {
		entity, err := client.RedeemCode.Query().Where(redeemcode.CodeEQ(invitationCode)).Only(ctx)
		if err != nil {
			if dbent.IsNotFound(err) {
				return nil, ErrRedeemCodeNotFound
			}
			return nil, err
		}
		return &RedeemCode{
			ID:           entity.ID,
			Code:         entity.Code,
			Type:         entity.Type,
			Value:        entity.Value,
			Status:       entity.Status,
			UsedBy:       entity.UsedBy,
			UsedAt:       entity.UsedAt,
			Notes:        oauthEmailFlowStringValue(entity.Notes),
			CreatedAt:    entity.CreatedAt,
			ExpiresAt:    entity.ExpiresAt,
			GroupID:      entity.GroupID,
			ValidityDays: entity.ValidityDays,
		}, nil
	}
	return s.redeemRepo.GetByCode(ctx, invitationCode)
}

func (s *AuthService) useOAuthRegistrationInvitation(ctx context.Context, invitationID, userID int64) error {
	if client := s.oauthEmailFlowClient(ctx); client != nil {
		affected, err := client.RedeemCode.Update().
			Where(
				redeemcode.IDEQ(invitationID),
				redeemcode.StatusEQ(StatusUnused),
				redeemcode.Or(redeemcode.ExpiresAtIsNil(), redeemcode.ExpiresAtGT(time.Now().UTC())),
			).
			SetStatus(StatusUsed).
			SetUsedBy(userID).
			SetUsedAt(time.Now().UTC()).
			Save(ctx)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrRedeemCodeUsed
		}
		return nil
	}
	return s.redeemRepo.Use(ctx, invitationID, userID)
}

func (s *AuthService) updateOAuthRegistrationInvitation(ctx context.Context, code *RedeemCode) error {
	if code == nil {
		return nil
	}
	if client := s.oauthEmailFlowClient(ctx); client != nil {
		update := client.RedeemCode.UpdateOneID(code.ID).
			SetCode(code.Code).
			SetType(code.Type).
			SetValue(code.Value).
			SetStatus(code.Status).
			SetNotes(code.Notes).
			SetValidityDays(code.ValidityDays)
		if code.ExpiresAt != nil {
			update = update.SetExpiresAt(*code.ExpiresAt)
		} else {
			update = update.ClearExpiresAt()
		}
		if code.UsedBy != nil {
			update = update.SetUsedBy(*code.UsedBy)
		} else {
			update = update.ClearUsedBy()
		}
		if code.UsedAt != nil {
			update = update.SetUsedAt(*code.UsedAt)
		} else {
			update = update.ClearUsedAt()
		}
		if code.GroupID != nil {
			update = update.SetGroupID(*code.GroupID)
		} else {
			update = update.ClearGroupID()
		}
		_, err := update.Save(ctx)
		return err
	}
	return s.redeemRepo.Update(ctx, code)
}

func (s *AuthService) updateOAuthSignupSource(ctx context.Context, userID int64, signupSource string) {
	client := s.oauthEmailFlowClient(ctx)
	if client == nil || userID <= 0 || strings.TrimSpace(signupSource) == "" {
		return
	}
	_ = client.User.UpdateOneID(userID).SetSignupSource(signupSource).Exec(ctx)
}

func oauthEmailFlowStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ValidatePasswordCredentials checks the local password without completing the
// login flow. This is used by pending third-party account adoption flows before
// the external identity has been bound.
func (s *AuthService) ValidatePasswordCredentials(ctx context.Context, email, password string) (*User, error) {
	if s == nil {
		return nil, ErrServiceUnavailable
	}

	user, err := s.userRepo.GetByEmail(ctx, strings.TrimSpace(strings.ToLower(email)))
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, ErrServiceUnavailable
	}
	if !user.IsActive() {
		return nil, ErrUserNotActive
	}
	if !s.CheckPassword(password, user.PasswordHash) {
		return nil, ErrInvalidCredentials
	}
	return user, nil
}

// RecordSuccessfulLogin updates last-login activity after a non-standard login
// flow finishes with a real session.
func (s *AuthService) RecordSuccessfulLogin(ctx context.Context, userID int64) {
	if s != nil && s.userRepo != nil && userID > 0 {
		user, err := s.userRepo.GetByID(ctx, userID)
		if err == nil && user != nil && !isReservedEmail(user.Email) {
			s.backfillEmailIdentityOnSuccessfulLogin(ctx, user)
		}
	}
	s.touchUserLogin(ctx, userID)
}
