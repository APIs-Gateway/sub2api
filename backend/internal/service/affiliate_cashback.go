package service

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var ErrAffiliateInviteCodeInvalid = infraerrors.BadRequest("AFFILIATE_INVITE_CODE_INVALID", "错误邀请码，请输入正确的邀请码")

const (
	SettingKeyAffiliateCashbackEnabled     = "affiliate_cashback_enabled"
	SettingKeyAffiliateCashbackRatePercent = "affiliate_cashback_rate_percent"
)

type AffiliateCashbackFaceValue struct {
	RedeemValue        float64   `json:"redeem_value"`
	CashbackBaseAmount float64   `json:"cashback_base_amount"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type AffiliateCashbackSettings struct {
	Enabled     bool                         `json:"enabled"`
	RatePercent float64                      `json:"rate_percent"`
	FaceValues  []AffiliateCashbackFaceValue `json:"face_values"`
}

type AffiliateCashbackSettingsInput struct {
	Enabled     bool                         `json:"enabled"`
	RatePercent float64                      `json:"rate_percent"`
	FaceValues  []AffiliateCashbackFaceValue `json:"face_values"`
}

type AffiliateCashbackRecord struct {
	LedgerID            int64     `json:"ledger_id"`
	InviterID           int64     `json:"inviter_id"`
	InviterEmail        string    `json:"inviter_email"`
	InviterUsername     string    `json:"inviter_username"`
	InviteeID           int64     `json:"invitee_id"`
	InviteeEmail        string    `json:"invitee_email"`
	InviteeUsername     string    `json:"invitee_username"`
	RedeemCodeID        *int64    `json:"redeem_code_id,omitempty"`
	RedeemCode          string    `json:"redeem_code,omitempty"`
	RedeemValue         float64   `json:"redeem_value"`
	CashbackBaseAmount  float64   `json:"cashback_base_amount"`
	CashbackRatePercent float64   `json:"cashback_rate_percent"`
	CashbackAmount      float64   `json:"cashback_amount"`
	InviterBalanceAfter *float64  `json:"inviter_balance_after,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

type UserInviteCashbackDetail struct {
	UserID              int64                     `json:"user_id"`
	AffCode             string                    `json:"aff_code"`
	InviterID           *int64                    `json:"inviter_id,omitempty"`
	InvitedCount        int                       `json:"invited_count"`
	TotalCashback       float64                   `json:"total_cashback"`
	CashbackEnabled     bool                      `json:"cashback_enabled"`
	CashbackRatePercent float64                   `json:"cashback_rate_percent"`
	Invitees            []AffiliateInvitee        `json:"invitees"`
	Records             []AffiliateCashbackRecord `json:"records"`
}

type AffiliateCashbackRepository interface {
	ListCashbackFaceValues(ctx context.Context) ([]AffiliateCashbackFaceValue, error)
	ReplaceCashbackFaceValues(ctx context.Context, entries []AffiliateCashbackFaceValue) error
	GetCashbackBaseAmount(ctx context.Context, redeemValue float64) (float64, bool, error)
	ApplyRedeemCashback(ctx context.Context, inviterID, inviteeUserID, redeemCodeID int64, redeemCode string, redeemValue, baseAmount, ratePercent, cashbackAmount float64) (bool, error)
	ListCashbackRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateCashbackRecord, int64, error)
	ListUserCashbackRecords(ctx context.Context, userID int64, limit int) ([]AffiliateCashbackRecord, error)
	GetUserCashbackTotal(ctx context.Context, userID int64) (float64, error)
}

func (s *SettingService) IsAffiliateCashbackEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateCashbackEnabled)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

func (s *SettingService) GetAffiliateCashbackRatePercent(ctx context.Context) float64 {
	if s == nil || s.settingRepo == nil {
		return AffiliateRebateRateDefault
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateCashbackRatePercent)
	if err != nil {
		return AffiliateRebateRateDefault
	}
	rate, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return AffiliateRebateRateDefault
	}
	return clampAffiliateRebateRate(rate)
}

func (s *SettingService) UpdateAffiliateCashbackSettings(ctx context.Context, repo AffiliateCashbackRepository, input AffiliateCashbackSettingsInput) (*AffiliateCashbackSettings, error) {
	if s == nil || s.settingRepo == nil || repo == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate cashback settings unavailable")
	}
	rate := clampAffiliateRebateRate(input.RatePercent)
	normalized := normalizeCashbackFaceValues(input.FaceValues)
	if err := repo.ReplaceCashbackFaceValues(ctx, normalized); err != nil {
		return nil, err
	}
	if err := s.settingRepo.SetMultiple(ctx, map[string]string{
		SettingKeyAffiliateCashbackEnabled:     strconv.FormatBool(input.Enabled),
		SettingKeyAffiliateCashbackRatePercent: strconv.FormatFloat(rate, 'f', 8, 64),
	}); err != nil {
		return nil, err
	}
	return s.GetAffiliateCashbackSettings(ctx, repo)
}

func (s *SettingService) GetAffiliateCashbackSettings(ctx context.Context, repo AffiliateCashbackRepository) (*AffiliateCashbackSettings, error) {
	if s == nil || repo == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate cashback settings unavailable")
	}
	entries, err := repo.ListCashbackFaceValues(ctx)
	if err != nil {
		return nil, err
	}
	return &AffiliateCashbackSettings{
		Enabled:     s.IsAffiliateCashbackEnabled(ctx),
		RatePercent: s.GetAffiliateCashbackRatePercent(ctx),
		FaceValues:  entries,
	}, nil
}

func normalizeCashbackFaceValues(entries []AffiliateCashbackFaceValue) []AffiliateCashbackFaceValue {
	out := make([]AffiliateCashbackFaceValue, 0, len(entries))
	seen := make(map[string]int, len(entries))
	for _, entry := range entries {
		redeemValue := roundTo(entry.RedeemValue, 8)
		baseAmount := roundTo(entry.CashbackBaseAmount, 8)
		if redeemValue <= 0 || baseAmount <= 0 || math.IsNaN(redeemValue) || math.IsNaN(baseAmount) || math.IsInf(redeemValue, 0) || math.IsInf(baseAmount, 0) {
			continue
		}
		key := strconv.FormatFloat(redeemValue, 'f', 8, 64)
		normalized := AffiliateCashbackFaceValue{
			RedeemValue:        redeemValue,
			CashbackBaseAmount: baseAmount,
		}
		if idx, ok := seen[key]; ok {
			out[idx] = normalized
			continue
		}
		seen[key] = len(out)
		out = append(out, normalized)
	}
	return out
}

func (s *AffiliateService) ValidateInviteCodeForSignup(ctx context.Context, rawCode string) error {
	code := strings.ToUpper(strings.TrimSpace(rawCode))
	if code == "" {
		return nil
	}
	if s == nil || s.repo == nil {
		return infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	if !isValidAffiliateCodeFormat(code) {
		return ErrAffiliateInviteCodeInvalid
	}
	summary, err := s.repo.GetAffiliateByCode(ctx, code)
	if err != nil {
		if errors.Is(err, ErrAffiliateProfileNotFound) {
			return ErrAffiliateInviteCodeInvalid
		}
		return err
	}
	if summary == nil || summary.UserID <= 0 {
		return ErrAffiliateInviteCodeInvalid
	}
	return nil
}

func (s *AffiliateService) BindInviterByCodeStrict(ctx context.Context, userID int64, rawCode string) error {
	code := strings.ToUpper(strings.TrimSpace(rawCode))
	if code == "" {
		return nil
	}
	if s == nil || s.repo == nil {
		return infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate service unavailable")
	}
	if !s.IsEnabled(ctx) && s.settingService != nil && !s.settingService.IsAffiliateCashbackEnabled(ctx) {
		return nil
	}
	if !isValidAffiliateCodeFormat(code) {
		return ErrAffiliateInviteCodeInvalid
	}

	selfSummary, err := s.repo.EnsureUserAffiliate(ctx, userID)
	if err != nil {
		return err
	}
	if selfSummary.InviterID != nil {
		return nil
	}

	inviterSummary, err := s.repo.GetAffiliateByCode(ctx, code)
	if err != nil {
		if errors.Is(err, ErrAffiliateProfileNotFound) {
			return ErrAffiliateInviteCodeInvalid
		}
		return err
	}
	if inviterSummary == nil || inviterSummary.UserID <= 0 || inviterSummary.UserID == userID {
		return ErrAffiliateInviteCodeInvalid
	}

	bound, err := s.repo.BindInviter(ctx, userID, inviterSummary.UserID)
	if err != nil {
		return err
	}
	if !bound {
		return ErrAffiliateInviteCodeInvalid
	}
	return nil
}

func (s *AffiliateService) AccrueCashbackForRedeem(ctx context.Context, inviteeUserID int64, redeemCode *RedeemCode) (float64, error) {
	if s == nil || s.repo == nil || inviteeUserID <= 0 || redeemCode == nil || redeemCode.ID <= 0 {
		return 0, nil
	}
	if redeemCode.Type != RedeemTypeBalance || redeemCode.Value <= 0 {
		return 0, nil
	}
	if s.settingService == nil || !s.settingService.IsAffiliateCashbackEnabled(ctx) {
		return 0, nil
	}
	cashbackRepo, ok := s.repo.(AffiliateCashbackRepository)
	if !ok {
		return 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate cashback repository unavailable")
	}
	inviteeSummary, err := s.repo.EnsureUserAffiliate(ctx, inviteeUserID)
	if err != nil {
		return 0, err
	}
	if inviteeSummary.InviterID == nil || *inviteeSummary.InviterID <= 0 {
		return 0, nil
	}
	baseAmount, found, err := cashbackRepo.GetCashbackBaseAmount(ctx, redeemCode.Value)
	if err != nil || !found || baseAmount <= 0 {
		return 0, err
	}
	rate := s.settingService.GetAffiliateCashbackRatePercent(ctx)
	cashbackAmount := roundTo(baseAmount*(rate/100), 8)
	if cashbackAmount <= 0 {
		return 0, nil
	}
	applied, err := cashbackRepo.ApplyRedeemCashback(ctx, *inviteeSummary.InviterID, inviteeUserID, redeemCode.ID, redeemCode.Code, redeemCode.Value, baseAmount, rate, cashbackAmount)
	if err != nil || !applied {
		return 0, err
	}
	s.invalidateAffiliateCaches(ctx, *inviteeSummary.InviterID)
	return cashbackAmount, nil
}

func (s *AffiliateService) GetInviteCashbackDetail(ctx context.Context, userID int64) (*UserInviteCashbackDetail, error) {
	summary, err := s.EnsureUserAffiliate(ctx, userID)
	if err != nil {
		return nil, err
	}
	invitees, err := s.listInvitees(ctx, userID)
	if err != nil {
		return nil, err
	}
	var records []AffiliateCashbackRecord
	var total float64
	if repo, ok := s.repo.(AffiliateCashbackRepository); ok {
		records, err = repo.ListUserCashbackRecords(ctx, userID, 100)
		if err != nil {
			return nil, err
		}
		total, err = repo.GetUserCashbackTotal(ctx, userID)
		if err != nil {
			return nil, err
		}
	}
	enabled := false
	rate := AffiliateRebateRateDefault
	if s.settingService != nil {
		enabled = s.settingService.IsAffiliateCashbackEnabled(ctx)
		rate = s.settingService.GetAffiliateCashbackRatePercent(ctx)
	}
	return &UserInviteCashbackDetail{
		UserID:              summary.UserID,
		AffCode:             summary.AffCode,
		InviterID:           summary.InviterID,
		InvitedCount:        summary.AffCount,
		TotalCashback:       total,
		CashbackEnabled:     enabled,
		CashbackRatePercent: rate,
		Invitees:            invitees,
		Records:             records,
	}, nil
}

func (s *AffiliateService) AdminGetCashbackSettings(ctx context.Context) (*AffiliateCashbackSettings, error) {
	repo, ok := s.repo.(AffiliateCashbackRepository)
	if !ok || s.settingService == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate cashback settings unavailable")
	}
	return s.settingService.GetAffiliateCashbackSettings(ctx, repo)
}

func (s *AffiliateService) AdminUpdateCashbackSettings(ctx context.Context, input AffiliateCashbackSettingsInput) (*AffiliateCashbackSettings, error) {
	repo, ok := s.repo.(AffiliateCashbackRepository)
	if !ok || s.settingService == nil {
		return nil, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate cashback settings unavailable")
	}
	return s.settingService.UpdateAffiliateCashbackSettings(ctx, repo, input)
}

func (s *AffiliateService) AdminListCashbackRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateCashbackRecord, int64, error) {
	repo, ok := s.repo.(AffiliateCashbackRepository)
	if !ok {
		return nil, 0, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "affiliate cashback repository unavailable")
	}
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	if filter.PageSize > 1000 {
		filter.PageSize = 1000
	}
	return repo.ListCashbackRecords(ctx, filter)
}
