package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	NotificationEmailEventAuthVerifyCode              = "auth.verify_code"
	NotificationEmailEventAuthPasswordReset           = "auth.password_reset"
	NotificationEmailEventNotificationEmailVerifyCode = "notification_email.verify_code"
	NotificationEmailEventSubscriptionPurchaseSuccess = "subscription.purchase_success"
	NotificationEmailEventSubscriptionExpiryReminder  = "subscription.expiry_reminder"
	NotificationEmailEventBalanceLow                  = "balance.low"
	NotificationEmailEventBalanceRechargeSuccess      = "balance.recharge_success"
	NotificationEmailEventAccountQuotaAlert           = "account.quota_alert"
	NotificationEmailEventContentModerationViolation  = "content_moderation.violation_notice"
	NotificationEmailEventContentModerationDisabled   = "content_moderation.account_disabled"
	NotificationEmailEventCyberPolicyNotice           = "content_moderation.cyber_policy_notice"
	NotificationEmailEventOpsAlert                    = "ops.alert"
	NotificationEmailEventOpsScheduledReport          = "ops.scheduled_report"

	notificationEmailTemplateKeyPrefix        = "notification_email_template:"
	notificationEmailPreferenceKeyPrefix      = "notification_email_preference:"
	notificationEmailDeliveryKeyPrefix        = "notification_email_delivery:"
	notificationEmailLocaleUserKeyPrefix      = "notification_email_locale:user:"
	notificationEmailLocaleEmailKeyPrefix     = "notification_email_locale:email:"
	notificationEmailUnsubscribeSecretKey     = "notification_email_unsubscribe_secret"
	notificationEmailLocaleEnglish            = "en"
	notificationEmailLocaleSimplifiedChinese  = "zh-CN"
	notificationEmailLocaleTraditionalChinese = "zh-HK"
	notificationEmailLocaleLegacyChinese      = "zh"
	notificationEmailMaxSubjectLength         = 200
	notificationEmailMaxHTMLLength            = 30000
	notificationEmailUnsubscribeTTL           = 365 * 24 * time.Hour
)

var (
	notificationEmailPlaceholderPattern = regexp.MustCompile(`{{\s*([a-zA-Z][a-zA-Z0-9_]*)\s*}}`)
	notificationEmailLocales            = []string{notificationEmailLocaleSimplifiedChinese, notificationEmailLocaleTraditionalChinese, notificationEmailLocaleEnglish}
	notificationEmailCommonPlaceholders = []string{"site_name", "recipient_name", "recipient_email"}
)

type NotificationEmailService struct {
	settingRepo  SettingRepository
	emailService *EmailService
}

type NotificationEmailEventInfo struct {
	Event        string   `json:"event"`
	Label        string   `json:"label"`
	Description  string   `json:"description"`
	Category     string   `json:"category"`
	Optional     bool     `json:"optional"`
	Placeholders []string `json:"placeholders"`
}

type NotificationEmailTemplate struct {
	Event        string     `json:"event"`
	Locale       string     `json:"locale"`
	Subject      string     `json:"subject"`
	HTML         string     `json:"html"`
	IsCustom     bool       `json:"is_custom"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
	Placeholders []string   `json:"placeholders"`
}

type NotificationEmailPreview struct {
	Subject string `json:"subject"`
	HTML    string `json:"html"`
}

type NotificationEmailPreviewInput struct {
	Event     string            `json:"event"`
	Locale    string            `json:"locale"`
	Subject   string            `json:"subject"`
	HTML      string            `json:"html"`
	Variables map[string]string `json:"variables,omitempty"`
}

type NotificationEmailSendInput struct {
	Event            string
	Locale           string
	RecipientEmail   string
	RecipientName    string
	UserID           int64
	SourceType       string
	SourceID         string
	ReminderKey      string
	Variables        map[string]string
	RawHTMLVariables map[string]string
}

type NotificationEmailUnsubscribeResult struct {
	Event string `json:"event"`
	Email string `json:"email"`
	Done  bool   `json:"done"`
}

type notificationEmailStoredTemplate struct {
	Subject   string    `json:"subject"`
	HTML      string    `json:"html"`
	UpdatedAt time.Time `json:"updated_at"`
}

type notificationEmailOfficialTemplate struct {
	Subject string
	HTML    string
}

type notificationEmailTemplateError struct {
	Err error
}

func (e notificationEmailTemplateError) Error() string {
	return e.Err.Error()
}

func (e notificationEmailTemplateError) Unwrap() error {
	return e.Err
}

type notificationEmailConfigError struct {
	Err error
}

func (e notificationEmailConfigError) Error() string {
	return e.Err.Error()
}

func (e notificationEmailConfigError) Unwrap() error {
	return e.Err
}

type notificationEmailDeliveryError struct {
	Err error
}

func (e notificationEmailDeliveryError) Error() string {
	return e.Err.Error()
}

func (e notificationEmailDeliveryError) Unwrap() error {
	return e.Err
}

type notificationEmailUnsubscribeClaims struct {
	Email string `json:"email"`
	Event string `json:"event"`
	Exp   int64  `json:"exp"`
}

func NewNotificationEmailService(settingRepo SettingRepository, emailService *EmailService) *NotificationEmailService {
	svc := &NotificationEmailService{settingRepo: settingRepo, emailService: emailService}
	if emailService != nil {
		emailService.SetNotificationEmailService(svc)
	}
	return svc
}

func notificationEmailTemplateErr(err error) error {
	if err == nil {
		return nil
	}
	return notificationEmailTemplateError{Err: err}
}

func notificationEmailConfigErr(err error) error {
	if err == nil {
		return nil
	}
	return notificationEmailConfigError{Err: err}
}

func notificationEmailDeliveryErr(err error) error {
	if err == nil {
		return nil
	}
	return notificationEmailDeliveryError{Err: err}
}

func shouldFallbackNotificationEmail(err error) bool {
	if err == nil {
		return false
	}
	var templateErr notificationEmailTemplateError
	if errors.As(err, &templateErr) {
		return true
	}
	var configErr notificationEmailConfigError
	return errors.As(err, &configErr)
}

func isNotificationEmailDeliveryError(err error) bool {
	var deliveryErr notificationEmailDeliveryError
	return errors.As(err, &deliveryErr)
}

func (s *NotificationEmailService) ListEventInfos() []NotificationEmailEventInfo {
	infos := make([]NotificationEmailEventInfo, 0, len(notificationEmailEventDefinitions))
	for _, event := range notificationEmailEventOrder {
		info := notificationEmailEventDefinitions[event]
		info.Placeholders = append([]string(nil), info.Placeholders...)
		infos = append(infos, info)
	}
	return infos
}

func (s *NotificationEmailService) SupportedLocales() []string {
	return append([]string(nil), notificationEmailLocales...)
}

func (s *NotificationEmailService) ListTemplates(ctx context.Context) ([]NotificationEmailTemplate, error) {
	items := make([]NotificationEmailTemplate, 0, len(notificationEmailEventOrder)*len(notificationEmailLocales))
	for _, event := range notificationEmailEventOrder {
		for _, locale := range notificationEmailLocales {
			tmpl, err := s.GetTemplate(ctx, event, locale)
			if err != nil {
				return nil, err
			}
			items = append(items, tmpl)
		}
	}
	return items, nil
}

func (s *NotificationEmailService) GetTemplate(ctx context.Context, event, locale string) (NotificationEmailTemplate, error) {
	info, normalizedEvent, err := s.eventInfo(event)
	if err != nil {
		return NotificationEmailTemplate{}, err
	}
	normalizedLocale := normalizeNotificationLocale(locale)
	official, ok := notificationEmailOfficialTemplates[normalizedEvent][normalizedLocale]
	if !ok {
		return NotificationEmailTemplate{}, fmt.Errorf("official template not found for %s/%s", normalizedEvent, normalizedLocale)
	}

	tmpl := NotificationEmailTemplate{
		Event:        normalizedEvent,
		Locale:       normalizedLocale,
		Subject:      official.Subject,
		HTML:         official.HTML,
		Placeholders: append([]string(nil), info.Placeholders...),
	}

	var raw string
	var foundCustom bool
	for _, key := range notificationEmailTemplateLookupKeys(normalizedEvent, normalizedLocale) {
		value, err := s.settingRepo.GetValue(ctx, key)
		if err != nil {
			if errors.Is(err, ErrSettingNotFound) {
				continue
			}
			return NotificationEmailTemplate{}, err
		}
		if strings.TrimSpace(value) == "" {
			return tmpl, nil
		}
		raw = value
		foundCustom = true
		break
	}
	if !foundCustom {
		return tmpl, nil
	}

	var stored notificationEmailStoredTemplate
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return NotificationEmailTemplate{}, fmt.Errorf("decode email template override: %w", err)
	}
	if err := validateNotificationEmailTemplate(normalizedEvent, stored.Subject, stored.HTML); err != nil {
		return NotificationEmailTemplate{}, err
	}
	tmpl.Subject = stored.Subject
	tmpl.HTML = stored.HTML
	tmpl.IsCustom = true
	updatedAt := stored.UpdatedAt
	tmpl.UpdatedAt = &updatedAt
	return tmpl, nil
}

func (s *NotificationEmailService) UpdateTemplate(ctx context.Context, event, locale, subject, htmlBody string) (NotificationEmailTemplate, error) {
	_, normalizedEvent, err := s.eventInfo(event)
	if err != nil {
		return NotificationEmailTemplate{}, err
	}
	normalizedLocale := normalizeNotificationLocale(locale)
	if err := validateNotificationEmailTemplate(normalizedEvent, subject, htmlBody); err != nil {
		return NotificationEmailTemplate{}, err
	}
	stored := notificationEmailStoredTemplate{
		Subject:   strings.TrimSpace(subject),
		HTML:      htmlBody,
		UpdatedAt: time.Now().UTC(),
	}
	payload, err := json.Marshal(stored)
	if err != nil {
		return NotificationEmailTemplate{}, err
	}
	if err := s.settingRepo.Set(ctx, notificationEmailTemplateKey(normalizedEvent, normalizedLocale), string(payload)); err != nil {
		return NotificationEmailTemplate{}, err
	}
	return s.GetTemplate(ctx, normalizedEvent, normalizedLocale)
}

func (s *NotificationEmailService) RestoreOfficialTemplate(ctx context.Context, event, locale string) (NotificationEmailTemplate, error) {
	_, normalizedEvent, err := s.eventInfo(event)
	if err != nil {
		return NotificationEmailTemplate{}, err
	}
	normalizedLocale := normalizeNotificationLocale(locale)
	for _, key := range notificationEmailTemplateLookupKeys(normalizedEvent, normalizedLocale) {
		if err := s.settingRepo.Delete(ctx, key); err != nil && !errors.Is(err, ErrSettingNotFound) {
			return NotificationEmailTemplate{}, err
		}
	}
	return s.GetTemplate(ctx, normalizedEvent, normalizedLocale)
}

func (s *NotificationEmailService) PreviewTemplate(ctx context.Context, input NotificationEmailPreviewInput) (NotificationEmailPreview, error) {
	_, normalizedEvent, err := s.eventInfo(input.Event)
	if err != nil {
		return NotificationEmailPreview{}, err
	}
	normalizedLocale := normalizeNotificationLocale(input.Locale)
	subject := input.Subject
	htmlBody := input.HTML
	if strings.TrimSpace(subject) == "" || strings.TrimSpace(htmlBody) == "" {
		tmpl, err := s.GetTemplate(ctx, normalizedEvent, normalizedLocale)
		if err != nil {
			return NotificationEmailPreview{}, err
		}
		if strings.TrimSpace(subject) == "" {
			subject = tmpl.Subject
		}
		if strings.TrimSpace(htmlBody) == "" {
			htmlBody = tmpl.HTML
		}
	}
	if err := validateNotificationEmailTemplate(normalizedEvent, subject, htmlBody); err != nil {
		return NotificationEmailPreview{}, err
	}
	variables := s.sampleVariables(ctx, normalizedEvent, normalizedLocale)
	for key, value := range input.Variables {
		variables[key] = value
	}
	return renderNotificationEmail(normalizedEvent, subject, htmlBody, variables, nil)
}

func (s *NotificationEmailService) Send(ctx context.Context, input NotificationEmailSendInput) error {
	info, normalizedEvent, err := s.eventInfo(input.Event)
	if err != nil {
		return notificationEmailTemplateErr(err)
	}
	recipient := strings.TrimSpace(input.RecipientEmail)
	if recipient == "" {
		return nil
	}
	if info.Optional {
		unsubscribed, err := s.IsUnsubscribed(ctx, recipient, normalizedEvent)
		if err != nil {
			return err
		}
		if unsubscribed {
			slog.Info("notification email suppressed by unsubscribe preference", "event", normalizedEvent, "recipient_hash", notificationEmailHash(recipient))
			return nil
		}
	}

	locale, hasExplicitLocale := normalizeNotificationLocaleHint(input.Locale)
	if hasExplicitLocale {
		s.RememberRecipientLocale(ctx, input.UserID, recipient, input.Locale)
	} else {
		locale = s.ResolveRecipientLocale(ctx, input.UserID, recipient)
	}
	tmpl, err := s.GetTemplate(ctx, normalizedEvent, locale)
	if err != nil {
		return notificationEmailTemplateErr(err)
	}
	variables := s.runtimeVariables(ctx, normalizedEvent, locale, input)
	rendered, err := renderNotificationEmail(normalizedEvent, tmpl.Subject, tmpl.HTML, variables, input.RawHTMLVariables)
	if err != nil {
		return notificationEmailTemplateErr(err)
	}

	deliveryKey := notificationEmailDeliveryKey(normalizedEvent, input.SourceType, input.SourceID, recipient, input.ReminderKey)
	if deliveryKey != "" {
		sent, err := s.deliveryExists(ctx, deliveryKey, legacyNotificationEmailDeliveryKey(normalizedEvent, input.SourceType, input.SourceID, recipient, input.ReminderKey))
		if err != nil {
			return err
		}
		if sent {
			return nil
		}
	}

	if s.emailService == nil {
		return notificationEmailConfigErr(errors.New("email service is not configured"))
	}
	if err := s.emailService.SendEmail(ctx, recipient, rendered.Subject, rendered.HTML); err != nil {
		return notificationEmailDeliveryErr(err)
	}
	if deliveryKey != "" {
		if err := s.settingRepo.Set(ctx, deliveryKey, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return nil
}

func (s *NotificationEmailService) RememberRecipientLocale(ctx context.Context, userID int64, email, acceptLanguage string) {
	locale, ok := normalizeNotificationLocaleHint(acceptLanguage)
	if !ok || s == nil || s.settingRepo == nil {
		return
	}
	if userID > 0 {
		_ = s.settingRepo.Set(ctx, notificationEmailLocaleUserKeyPrefix+strconv.FormatInt(userID, 10), locale)
	}
	if emailHash := notificationEmailHash(email); emailHash != "" {
		_ = s.settingRepo.Set(ctx, notificationEmailLocaleEmailKeyPrefix+emailHash, locale)
	}
}

func (s *NotificationEmailService) ResolveRecipientLocale(ctx context.Context, userID int64, email string) string {
	if s == nil || s.settingRepo == nil {
		return normalizeNotificationLocale(defaultLocaleFallback)
	}
	if userID > 0 {
		if locale, err := s.settingRepo.GetValue(ctx, notificationEmailLocaleUserKeyPrefix+strconv.FormatInt(userID, 10)); err == nil && strings.TrimSpace(locale) != "" {
			return normalizeNotificationLocale(locale)
		}
	}
	if emailHash := notificationEmailHash(email); emailHash != "" {
		if locale, err := s.settingRepo.GetValue(ctx, notificationEmailLocaleEmailKeyPrefix+emailHash); err == nil && strings.TrimSpace(locale) != "" {
			return normalizeNotificationLocale(locale)
		}
	}
	return s.resolveSiteDefaultLocale(ctx)
}

func (s *NotificationEmailService) resolveSiteDefaultLocale(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return normalizeNotificationLocale(defaultLocaleFallback)
	}
	if locale, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultLocale); err == nil && strings.TrimSpace(locale) != "" {
		return normalizeNotificationLocale(locale)
	}
	return normalizeNotificationLocale(defaultLocaleFallback)
}

func (s *NotificationEmailService) IsUnsubscribed(ctx context.Context, email, event string) (bool, error) {
	info, normalizedEvent, err := s.eventInfo(event)
	if err != nil {
		return false, err
	}
	if !info.Optional {
		return false, nil
	}
	for _, key := range []string{notificationEmailPreferenceKey(normalizedEvent, email), legacyNotificationEmailPreferenceKey(normalizedEvent, email)} {
		if strings.TrimSpace(key) == "" {
			continue
		}
		value, err := s.settingRepo.GetValue(ctx, key)
		if err == nil {
			return strings.EqualFold(strings.TrimSpace(value), "unsubscribed"), nil
		}
		if !errors.Is(err, ErrSettingNotFound) {
			return false, err
		}
	}
	return false, nil
}

func (s *NotificationEmailService) Unsubscribe(ctx context.Context, token string) (NotificationEmailUnsubscribeResult, error) {
	claims, err := s.parseUnsubscribeToken(ctx, token)
	if err != nil {
		return NotificationEmailUnsubscribeResult{}, err
	}
	info, normalizedEvent, err := s.eventInfo(claims.Event)
	if err != nil {
		return NotificationEmailUnsubscribeResult{}, err
	}
	if !info.Optional {
		return NotificationEmailUnsubscribeResult{}, fmt.Errorf("%s is transactional and cannot be unsubscribed", normalizedEvent)
	}
	if err := s.settingRepo.Set(ctx, notificationEmailPreferenceKey(normalizedEvent, claims.Email), "unsubscribed"); err != nil {
		return NotificationEmailUnsubscribeResult{}, err
	}
	return NotificationEmailUnsubscribeResult{Event: normalizedEvent, Email: claims.Email, Done: true}, nil
}

func (s *NotificationEmailService) eventInfo(event string) (NotificationEmailEventInfo, string, error) {
	normalized := strings.ToLower(strings.TrimSpace(event))
	info, ok := notificationEmailEventDefinitions[normalized]
	if !ok {
		return NotificationEmailEventInfo{}, "", fmt.Errorf("unsupported email template event: %s", event)
	}
	return info, normalized, nil
}

func (s *NotificationEmailService) sampleVariables(ctx context.Context, event, locale string) map[string]string {
	info := notificationEmailEventDefinitions[event]
	variables := make(map[string]string, len(info.Placeholders))
	for key, value := range notificationEmailSampleVariables(locale) {
		variables[key] = value
	}
	variables["site_name"] = s.siteName(ctx)
	if variables["unsubscribe_url"] == "" && info.Optional {
		variables["unsubscribe_url"] = "https://example.com/unsubscribe"
	}
	return variables
}

func (s *NotificationEmailService) runtimeVariables(ctx context.Context, event, locale string, input NotificationEmailSendInput) map[string]string {
	variables := s.sampleVariables(ctx, event, locale)
	for key, value := range input.Variables {
		variables[key] = value
	}
	variables["site_name"] = s.siteName(ctx)
	variables["recipient_email"] = input.RecipientEmail
	if strings.TrimSpace(input.RecipientName) != "" {
		variables["recipient_name"] = input.RecipientName
	}
	if notificationEmailEventDefinitions[event].Optional {
		if unsubscribeURL, err := s.buildUnsubscribeURL(ctx, input.RecipientEmail, event); err == nil {
			variables["unsubscribe_url"] = unsubscribeURL
		}
	}
	return variables
}

func (s *NotificationEmailService) siteName(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return defaultSiteName
	}
	name, err := s.settingRepo.GetValue(ctx, SettingKeySiteName)
	if err != nil || strings.TrimSpace(name) == "" {
		return defaultSiteName
	}
	return strings.TrimSpace(name)
}

func (s *NotificationEmailService) baseURL(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return ""
	}
	for _, key := range []string{SettingKeyAPIBaseURL, SettingKeyFrontendURL} {
		value, err := s.settingRepo.GetValue(ctx, key)
		if err == nil && strings.TrimSpace(value) != "" {
			return strings.TrimRight(strings.TrimSpace(value), "/")
		}
	}
	return ""
}

func (s *NotificationEmailService) buildUnsubscribeURL(ctx context.Context, email, event string) (string, error) {
	token, err := s.createUnsubscribeToken(ctx, email, event)
	if err != nil {
		return "", err
	}
	path := "/api/v1/settings/email-unsubscribe?token=" + url.QueryEscape(token)
	baseURL := s.baseURL(ctx)
	if baseURL == "" {
		return path, nil
	}
	return baseURL + path, nil
}

func (s *NotificationEmailService) createUnsubscribeToken(ctx context.Context, email, event string) (string, error) {
	secret, err := s.unsubscribeSecret(ctx)
	if err != nil {
		return "", err
	}
	claims := notificationEmailUnsubscribeClaims{Email: strings.TrimSpace(email), Event: event, Exp: time.Now().Add(notificationEmailUnsubscribeTTL).Unix()}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signature := signNotificationEmailToken(secret, encodedPayload)
	return encodedPayload + "." + signature, nil
}

func (s *NotificationEmailService) parseUnsubscribeToken(ctx context.Context, token string) (notificationEmailUnsubscribeClaims, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return notificationEmailUnsubscribeClaims{}, errors.New("invalid unsubscribe token")
	}
	secret, err := s.unsubscribeSecret(ctx)
	if err != nil {
		return notificationEmailUnsubscribeClaims{}, err
	}
	expected := signNotificationEmailToken(secret, parts[0])
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return notificationEmailUnsubscribeClaims{}, errors.New("invalid unsubscribe token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return notificationEmailUnsubscribeClaims{}, errors.New("invalid unsubscribe token payload")
	}
	var claims notificationEmailUnsubscribeClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return notificationEmailUnsubscribeClaims{}, errors.New("invalid unsubscribe token payload")
	}
	if strings.TrimSpace(claims.Email) == "" || strings.TrimSpace(claims.Event) == "" {
		return notificationEmailUnsubscribeClaims{}, errors.New("invalid unsubscribe token claims")
	}
	if claims.Exp <= time.Now().Unix() {
		return notificationEmailUnsubscribeClaims{}, errors.New("unsubscribe token expired")
	}
	return claims, nil
}

func (s *NotificationEmailService) unsubscribeSecret(ctx context.Context) (string, error) {
	secret, err := s.settingRepo.GetValue(ctx, notificationEmailUnsubscribeSecretKey)
	if err == nil && strings.TrimSpace(secret) != "" {
		return strings.TrimSpace(secret), nil
	}
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	secret = base64.RawURLEncoding.EncodeToString(buf)
	if err := s.settingRepo.Set(ctx, notificationEmailUnsubscribeSecretKey, secret); err != nil {
		return "", err
	}
	return secret, nil
}

func (s *NotificationEmailService) deliveryExists(ctx context.Context, keys ...string) (bool, error) {
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		_, err := s.settingRepo.GetValue(ctx, key)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, ErrSettingNotFound) {
			return false, err
		}
	}
	return false, nil
}

func validateNotificationEmailTemplate(event, subject, htmlBody string) error {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return errors.New("email subject cannot be empty")
	}
	if len([]rune(subject)) > notificationEmailMaxSubjectLength {
		return fmt.Errorf("email subject cannot exceed %d characters", notificationEmailMaxSubjectLength)
	}
	if strings.TrimSpace(htmlBody) == "" {
		return errors.New("email html cannot be empty")
	}
	if len([]byte(htmlBody)) > notificationEmailMaxHTMLLength {
		return fmt.Errorf("email html cannot exceed %d bytes", notificationEmailMaxHTMLLength)
	}
	allowed := notificationEmailAllowedPlaceholderSet(event)
	for _, placeholder := range notificationEmailPlaceholdersIn(subject + "\n" + htmlBody) {
		if _, ok := allowed[placeholder]; !ok {
			return fmt.Errorf("unsupported placeholder {{%s}} for event %s", placeholder, event)
		}
	}
	return nil
}

func renderNotificationEmail(event, subject, htmlBody string, variables map[string]string, rawHTMLVariables map[string]string) (NotificationEmailPreview, error) {
	if err := validateNotificationEmailTemplate(event, subject, htmlBody); err != nil {
		return NotificationEmailPreview{}, err
	}
	renderedSubject, err := renderNotificationEmailString(event, subject, variables, nil, false)
	if err != nil {
		return NotificationEmailPreview{}, err
	}
	renderedHTML, err := renderNotificationEmailString(event, htmlBody, variables, rawHTMLVariables, true)
	if err != nil {
		return NotificationEmailPreview{}, err
	}
	return NotificationEmailPreview{Subject: sanitizeEmailHeader(renderedSubject), HTML: renderedHTML}, nil
}

func renderNotificationEmailString(event, raw string, variables map[string]string, rawHTMLVariables map[string]string, escapeHTML bool) (string, error) {
	allowed := notificationEmailAllowedPlaceholderSet(event)
	var renderErr error
	rendered := notificationEmailPlaceholderPattern.ReplaceAllStringFunc(raw, func(match string) string {
		if renderErr != nil {
			return ""
		}
		parts := notificationEmailPlaceholderPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return ""
		}
		name := parts[1]
		if _, ok := allowed[name]; !ok {
			renderErr = fmt.Errorf("unsupported placeholder {{%s}} for event %s", name, event)
			return ""
		}
		value := variables[name]
		if escapeHTML && notificationEmailRawHTMLAllowed(event, name) {
			if rawHTMLVariables != nil {
				if rawValue, ok := rawHTMLVariables[name]; ok {
					return rawValue
				}
			}
		}
		if strings.HasSuffix(name, "_url") && !isSafeNotificationEmailURL(value) {
			value = ""
		}
		if escapeHTML {
			return html.EscapeString(value)
		}
		return sanitizeEmailHeader(value)
	})
	if renderErr != nil {
		return "", renderErr
	}
	return rendered, nil
}

func notificationEmailRawHTMLAllowed(event, placeholder string) bool {
	return event == NotificationEmailEventOpsScheduledReport && placeholder == "report_html"
}

func notificationEmailAllowedPlaceholderSet(event string) map[string]struct{} {
	info := notificationEmailEventDefinitions[event]
	allowed := make(map[string]struct{}, len(info.Placeholders))
	for _, placeholder := range info.Placeholders {
		allowed[placeholder] = struct{}{}
	}
	return allowed
}

func notificationEmailPlaceholdersIn(raw string) []string {
	matches := notificationEmailPlaceholderPattern.FindAllStringSubmatch(raw, -1)
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		if _, exists := seen[match[1]]; exists {
			continue
		}
		seen[match[1]] = struct{}{}
		out = append(out, match[1])
	}
	return out
}

func normalizeNotificationLocale(raw string) string {
	if locale, ok := normalizeNotificationLocaleHint(raw); ok {
		return locale
	}
	return defaultLocaleFallback
}

func normalizeNotificationLocaleHint(raw string) (string, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return "", false
	}
	for _, part := range strings.Split(trimmed, ",") {
		tag := strings.TrimSpace(strings.Split(part, ";")[0])
		switch {
		case tag == "zh-hk" || tag == "zh-tw" || tag == "zh-mo" || tag == "zh-hant" || strings.HasPrefix(tag, "zh-hant-"):
			return notificationEmailLocaleTraditionalChinese, true
		case tag == "zh-cn" || tag == "zh-sg" || tag == "zh-hans" || strings.HasPrefix(tag, "zh-hans-") || tag == notificationEmailLocaleLegacyChinese || tag == "cn" || strings.HasPrefix(tag, "zh"):
			return notificationEmailLocaleSimplifiedChinese, true
		case tag == notificationEmailLocaleEnglish || strings.HasPrefix(tag, "en-"):
			return notificationEmailLocaleEnglish, true
		}
	}
	return "", false
}

func isNotificationChineseLocale(locale string) bool {
	normalized := normalizeNotificationLocale(locale)
	return normalized == notificationEmailLocaleSimplifiedChinese || normalized == notificationEmailLocaleTraditionalChinese
}

func isNotificationTraditionalLocale(locale string) bool {
	return normalizeNotificationLocale(locale) == notificationEmailLocaleTraditionalChinese
}

func notificationEmailTemplateKey(event, locale string) string {
	return notificationEmailTemplateKeyPrefix + event + ":" + locale
}

func notificationEmailTemplateLookupKeys(event, locale string) []string {
	normalizedLocale := normalizeNotificationLocale(locale)
	keys := []string{notificationEmailTemplateKey(event, normalizedLocale)}
	if normalizedLocale == notificationEmailLocaleSimplifiedChinese {
		keys = append(keys, notificationEmailTemplateKey(event, notificationEmailLocaleLegacyChinese))
	}
	return keys
}

func notificationEmailPreferenceKey(event, email string) string {
	if strings.TrimSpace(event) == "" || strings.TrimSpace(email) == "" {
		return ""
	}
	identity := strings.TrimSpace(event) + "\x00" + strings.ToLower(strings.TrimSpace(email))
	return notificationEmailPreferenceKeyPrefix + "v2:" + notificationEmailHash(identity)
}

func legacyNotificationEmailPreferenceKey(event, email string) string {
	return notificationEmailPreferenceKeyPrefix + event + ":" + notificationEmailHash(email)
}

func notificationEmailDeliveryKey(event, sourceType, sourceID, recipient, reminderKey string) string {
	if strings.TrimSpace(sourceType) == "" || strings.TrimSpace(sourceID) == "" || strings.TrimSpace(recipient) == "" {
		return ""
	}
	identity := strings.Join([]string{
		strings.ToLower(strings.TrimSpace(event)),
		safeNotificationEmailKeyPart(sourceType),
		safeNotificationEmailKeyPart(sourceID),
		strings.ToLower(strings.TrimSpace(recipient)),
		safeNotificationEmailKeyPart(reminderKey),
	}, "\x00")
	return notificationEmailDeliveryKeyPrefix + "v2:" + notificationEmailHash(identity)
}

func legacyNotificationEmailDeliveryKey(event, sourceType, sourceID, recipient, reminderKey string) string {
	if strings.TrimSpace(sourceType) == "" || strings.TrimSpace(sourceID) == "" || strings.TrimSpace(recipient) == "" {
		return ""
	}
	parts := []string{notificationEmailDeliveryKeyPrefix, event, ":", safeNotificationEmailKeyPart(sourceType), ":", safeNotificationEmailKeyPart(sourceID), ":", notificationEmailHash(recipient)}
	if strings.TrimSpace(reminderKey) != "" {
		parts = append(parts, ":", safeNotificationEmailKeyPart(reminderKey))
	}
	return strings.Join(parts, "")
}

func notificationEmailHash(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:])
}

func safeNotificationEmailKeyPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			_, _ = builder.WriteRune(r)
		} else {
			_, _ = builder.WriteRune('_')
		}
	}
	return builder.String()
}

func signNotificationEmailToken(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func isSafeNotificationEmailURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return true
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return false
	}
	if parsed.IsAbs() {
		scheme := strings.ToLower(parsed.Scheme)
		return scheme == "http" || scheme == "https" || scheme == "mailto"
	}
	return strings.HasPrefix(trimmed, "/")
}

func notificationEmailSampleVariables(locale string) map[string]string {
	if isNotificationChineseLocale(locale) {
		variables := map[string]string{
			"site_name":           defaultSiteName,
			"recipient_name":      "张三",
			"recipient_email":     "user@example.com",
			"verification_code":   "123456",
			"expires_in_minutes":  "15",
			"reset_url":           "https://example.com/reset-password?token=preview",
			"subscription_group":  "Claude Pro",
			"subscription_days":   "30",
			"expiry_time":         "2026-06-18 12:00",
			"days_remaining":      "3",
			"current_balance":     "12.34",
			"threshold":           "20.00",
			"recharge_url":        "https://example.com/recharge",
			"recharge_amount":     "50.00",
			"order_id":            "1024",
			"unsubscribe_url":     "https://example.com/unsubscribe",
			"account_id":          "1001",
			"account_name":        "openai-main",
			"platform":            "openai",
			"quota_dimension":     "每日额度",
			"quota_used":          "80.00",
			"quota_limit":         "100.00",
			"quota_remaining":     "20.00",
			"quota_threshold":     "20%",
			"triggered_at":        "2026-05-20 12:00:00",
			"group_name":          "默认分组",
			"moderation_category": "violence",
			"moderation_score":    "0.982",
			"violation_count":     "2",
			"ban_threshold":       "3",
			"rule_name":           "错误率过高",
			"severity":            "critical",
			"alert_status":        "firing",
			"metric_type":         "error_rate",
			"operator":            ">=",
			"metric_value":        "12.50",
			"threshold_value":     "10.00",
			"alert_description":   "最近 10 分钟错误率超过阈值",
			"report_name":         "日报",
			"report_type":         "daily_summary",
			"report_start_time":   "2026-05-19 12:00",
			"report_end_time":     "2026-05-20 12:00",
			"report_html":         "<h2>日报</h2><p>请求量：1024</p>",
		}
		if isNotificationTraditionalLocale(locale) {
			for key, value := range variables {
				variables[key] = traditionalizeNotificationEmailText(value)
			}
		}
		return variables
	}
	return map[string]string{
		"site_name":           defaultSiteName,
		"recipient_name":      "Alex",
		"recipient_email":     "user@example.com",
		"verification_code":   "123456",
		"expires_in_minutes":  "15",
		"reset_url":           "https://example.com/reset-password?token=preview",
		"subscription_group":  "Claude Pro",
		"subscription_days":   "30",
		"expiry_time":         "2026-06-18 12:00",
		"days_remaining":      "3",
		"current_balance":     "12.34",
		"threshold":           "20.00",
		"recharge_url":        "https://example.com/recharge",
		"recharge_amount":     "50.00",
		"order_id":            "1024",
		"unsubscribe_url":     "https://example.com/unsubscribe",
		"account_id":          "1001",
		"account_name":        "openai-main",
		"platform":            "openai",
		"quota_dimension":     "Daily quota",
		"quota_used":          "80.00",
		"quota_limit":         "100.00",
		"quota_remaining":     "20.00",
		"quota_threshold":     "20%",
		"triggered_at":        "2026-05-20 12:00:00",
		"group_name":          "Default group",
		"moderation_category": "violence",
		"moderation_score":    "0.982",
		"violation_count":     "2",
		"ban_threshold":       "3",
		"rule_name":           "High error rate",
		"severity":            "critical",
		"alert_status":        "firing",
		"metric_type":         "error_rate",
		"operator":            ">=",
		"metric_value":        "12.50",
		"threshold_value":     "10.00",
		"alert_description":   "Error rate exceeded threshold in the last 10 minutes.",
		"report_name":         "Daily summary",
		"report_type":         "daily_summary",
		"report_start_time":   "2026-05-19 12:00",
		"report_end_time":     "2026-05-20 12:00",
		"report_html":         "<h2>Daily summary</h2><p>Requests: 1024</p>",
	}
}

var notificationEmailEventOrder = []string{
	NotificationEmailEventAuthVerifyCode,
	NotificationEmailEventAuthPasswordReset,
	NotificationEmailEventNotificationEmailVerifyCode,
	NotificationEmailEventSubscriptionPurchaseSuccess,
	NotificationEmailEventSubscriptionExpiryReminder,
	NotificationEmailEventBalanceLow,
	NotificationEmailEventBalanceRechargeSuccess,
	NotificationEmailEventAccountQuotaAlert,
	NotificationEmailEventContentModerationViolation,
	NotificationEmailEventContentModerationDisabled,
	NotificationEmailEventCyberPolicyNotice,
	NotificationEmailEventOpsAlert,
	NotificationEmailEventOpsScheduledReport,
}

var notificationEmailEventDefinitions = map[string]NotificationEmailEventInfo{
	NotificationEmailEventAuthVerifyCode: {
		Event:        NotificationEmailEventAuthVerifyCode,
		Label:        "Email verification code",
		Description:  "Sent for registration, email binding, OAuth pending email, and TOTP verification flows.",
		Category:     "auth",
		Optional:     false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...), "verification_code", "expires_in_minutes"),
	},
	NotificationEmailEventAuthPasswordReset: {
		Event:        NotificationEmailEventAuthPasswordReset,
		Label:        "Password reset",
		Description:  "Sent when a user requests a password reset link.",
		Category:     "auth",
		Optional:     false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...), "reset_url", "expires_in_minutes"),
	},
	NotificationEmailEventNotificationEmailVerifyCode: {
		Event:        NotificationEmailEventNotificationEmailVerifyCode,
		Label:        "Notification email verification code",
		Description:  "Sent when a user verifies an extra notification email address.",
		Category:     "auth",
		Optional:     false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...), "verification_code", "expires_in_minutes"),
	},
	NotificationEmailEventSubscriptionPurchaseSuccess: {
		Event:        NotificationEmailEventSubscriptionPurchaseSuccess,
		Label:        "Subscription purchase success",
		Description:  "Sent after a subscription purchase is fulfilled.",
		Category:     "subscription",
		Optional:     false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...), "subscription_group", "subscription_days", "expiry_time", "order_id"),
	},
	NotificationEmailEventSubscriptionExpiryReminder: {
		Event:        NotificationEmailEventSubscriptionExpiryReminder,
		Label:        "Subscription expiry reminder",
		Description:  "Optional reminder sent before an active subscription expires.",
		Category:     "subscription",
		Optional:     true,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...), "subscription_group", "expiry_time", "days_remaining", "unsubscribe_url"),
	},
	NotificationEmailEventBalanceLow: {
		Event:        NotificationEmailEventBalanceLow,
		Label:        "Low balance alert",
		Description:  "Optional alert sent when balance crosses the configured low-balance threshold.",
		Category:     "billing",
		Optional:     true,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...), "current_balance", "threshold", "recharge_url", "unsubscribe_url"),
	},
	NotificationEmailEventBalanceRechargeSuccess: {
		Event:        NotificationEmailEventBalanceRechargeSuccess,
		Label:        "Balance recharge success",
		Description:  "Sent after a balance recharge order is fulfilled.",
		Category:     "billing",
		Optional:     false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...), "recharge_amount", "current_balance", "order_id"),
	},
	NotificationEmailEventAccountQuotaAlert: {
		Event:       NotificationEmailEventAccountQuotaAlert,
		Label:       "Account quota alert",
		Description: "Sent to configured admin notification emails when an upstream account quota threshold is crossed.",
		Category:    "admin",
		Optional:    false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
			"account_id", "account_name", "platform", "quota_dimension", "quota_used", "quota_limit", "quota_remaining", "quota_threshold"),
	},
	NotificationEmailEventContentModerationViolation: {
		Event:       NotificationEmailEventContentModerationViolation,
		Label:       "Risk control violation notice",
		Description: "Sent to users when a request triggers content moderation/risk control rules.",
		Category:    "risk_control",
		Optional:    false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
			"triggered_at", "group_name", "moderation_category", "moderation_score", "violation_count", "ban_threshold"),
	},
	NotificationEmailEventContentModerationDisabled: {
		Event:       NotificationEmailEventContentModerationDisabled,
		Label:       "Risk control account disabled",
		Description: "Sent to users when content moderation automatically disables their account.",
		Category:    "risk_control",
		Optional:    false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
			"triggered_at", "group_name", "moderation_category", "moderation_score", "violation_count", "ban_threshold"),
	},
	NotificationEmailEventCyberPolicyNotice: {
		Event:       NotificationEmailEventCyberPolicyNotice,
		Label:       "Cyber policy notice",
		Description: "Sent to users when an upstream request is blocked by cyber-security policy (cyber_policy).",
		Category:    "risk_control",
		Optional:    false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
			"triggered_at", "model", "group_name", "upstream_message"),
	},
	NotificationEmailEventOpsAlert: {
		Event:       NotificationEmailEventOpsAlert,
		Label:       "Ops alert",
		Description: "Sent to configured operations recipients when an ops alert rule fires.",
		Category:    "ops",
		Optional:    false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
			"rule_name", "severity", "alert_status", "metric_type", "operator", "metric_value", "threshold_value", "triggered_at", "alert_description"),
	},
	NotificationEmailEventOpsScheduledReport: {
		Event:       NotificationEmailEventOpsScheduledReport,
		Label:       "Ops scheduled report",
		Description: "Sent to configured operations recipients for scheduled daily/weekly/error/account-health reports.",
		Category:    "ops",
		Optional:    false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
			"report_name", "report_type", "report_start_time", "report_end_time", "report_html"),
	},
}

var notificationEmailOfficialTemplates = map[string]map[string]notificationEmailOfficialTemplate{
	NotificationEmailEventAuthVerifyCode: {
		notificationEmailLocaleEnglish: {
			Subject: "[{{site_name}}] Email verification code",
			HTML: notificationEmailCard("#4f46e5", "Email verification code", `
<p>Hello {{recipient_name}},</p>
<p>Your verification code is:</p>
<div class="code">{{verification_code}}</div>
<p>This code expires in <strong>{{expires_in_minutes}}</strong> minutes.</p>
<p>If you did not request this code, please ignore this email.</p>`),
		},
		notificationEmailLocaleSimplifiedChinese: {
			Subject: "[{{site_name}}] 邮箱验证码",
			HTML: notificationEmailCard("#4f46e5", "邮箱验证码", `
<p>{{recipient_name}}，您好：</p>
<p>您的验证码是：</p>
<div class="code">{{verification_code}}</div>
<p>验证码将在 <strong>{{expires_in_minutes}}</strong> 分钟后失效。</p>
<p>如果不是您本人操作，请忽略此邮件。</p>`),
		},
	},
	NotificationEmailEventAuthPasswordReset: {
		notificationEmailLocaleEnglish: {
			Subject: "[{{site_name}}] Password reset request",
			HTML: notificationEmailCard("#7c3aed", "Password reset", `
<p>Hello {{recipient_name}},</p>
<p>We received a request to reset your password. Click the button below to set a new password.</p>
<p><a class="button" href="{{reset_url}}">Reset password</a></p>
<p>This link expires in <strong>{{expires_in_minutes}}</strong> minutes.</p>
<p class="muted">If the button does not work, copy this link into your browser:<br>{{reset_url}}</p>
<p>If you did not request this, you can safely ignore this email.</p>`),
		},
		notificationEmailLocaleSimplifiedChinese: {
			Subject: "[{{site_name}}] 密码重置请求",
			HTML: notificationEmailCard("#7c3aed", "密码重置", `
<p>{{recipient_name}}，您好：</p>
<p>我们收到了您的密码重置请求，请点击下方按钮设置新密码。</p>
<p><a class="button" href="{{reset_url}}">重置密码</a></p>
<p>此链接将在 <strong>{{expires_in_minutes}}</strong> 分钟后失效。</p>
<p class="muted">如果按钮无法点击，请复制以下链接到浏览器中打开：<br>{{reset_url}}</p>
<p>如果不是您本人操作，请忽略此邮件。</p>`),
		},
	},
	NotificationEmailEventNotificationEmailVerifyCode: {
		notificationEmailLocaleEnglish: {
			Subject: "[{{site_name}}] Notification email verification code",
			HTML: notificationEmailCard("#0ea5e9", "Notification email verification", `
<p>Hello {{recipient_name}},</p>
<p>You are adding this address as an extra notification email.</p>
<p>Your verification code is:</p>
<div class="code">{{verification_code}}</div>
<p>This code expires in <strong>{{expires_in_minutes}}</strong> minutes.</p>
<p>If you did not request this code, please ignore this email.</p>`),
		},
		notificationEmailLocaleSimplifiedChinese: {
			Subject: "[{{site_name}}] 通知邮箱验证码",
			HTML: notificationEmailCard("#0ea5e9", "通知邮箱验证", `
<p>{{recipient_name}}，您好：</p>
<p>您正在添加额外的通知邮箱，请输入以下验证码完成验证。</p>
<div class="code">{{verification_code}}</div>
<p>验证码将在 <strong>{{expires_in_minutes}}</strong> 分钟后失效。</p>
<p>如果不是您本人操作，请忽略此邮件。</p>`),
		},
	},
	NotificationEmailEventSubscriptionPurchaseSuccess: {
		notificationEmailLocaleEnglish: {
			Subject: "[{{site_name}}] Subscription purchase successful",
			HTML: notificationEmailCard("#2563eb", "Subscription activated", `
<p>Hello {{recipient_name}},</p>
<p>Your subscription for <strong>{{subscription_group}}</strong> has been activated for <strong>{{subscription_days}}</strong> days.</p>
<p>Expiry time: <strong>{{expiry_time}}</strong></p>
<p>Order ID: {{order_id}}</p>`),
		},
		notificationEmailLocaleSimplifiedChinese: {
			Subject: "[{{site_name}}] 订阅购买成功",
			HTML: notificationEmailCard("#2563eb", "订阅已开通", `
<p>{{recipient_name}}，您好：</p>
<p>您的 <strong>{{subscription_group}}</strong> 订阅已成功开通，有效期 <strong>{{subscription_days}}</strong> 天。</p>
<p>到期时间：<strong>{{expiry_time}}</strong></p>
<p>订单号：{{order_id}}</p>`),
		},
	},
	NotificationEmailEventSubscriptionExpiryReminder: {
		notificationEmailLocaleEnglish: {
			Subject: "[{{site_name}}] Subscription expires in {{days_remaining}} day(s)",
			HTML: notificationEmailCard("#f97316", "Subscription expiry reminder", `
<p>Hello {{recipient_name}},</p>
<p>Your <strong>{{subscription_group}}</strong> subscription will expire in <strong>{{days_remaining}}</strong> day(s).</p>
<p>Expiry time: <strong>{{expiry_time}}</strong></p>
<p class="muted"><a href="{{unsubscribe_url}}">Unsubscribe from optional subscription reminders</a></p>`),
		},
		notificationEmailLocaleSimplifiedChinese: {
			Subject: "[{{site_name}}] 订阅将在 {{days_remaining}} 天后到期",
			HTML: notificationEmailCard("#f97316", "订阅到期提醒", `
<p>{{recipient_name}}，您好：</p>
<p>您的 <strong>{{subscription_group}}</strong> 订阅将在 <strong>{{days_remaining}}</strong> 天后到期。</p>
<p>到期时间：<strong>{{expiry_time}}</strong></p>
<p class="muted"><a href="{{unsubscribe_url}}">退订此类订阅提醒</a></p>`),
		},
	},
	NotificationEmailEventBalanceLow: {
		notificationEmailLocaleEnglish: {
			Subject: "[{{site_name}}] Low balance alert",
			HTML: notificationEmailCard("#d97706", "Low balance alert", `
<p>Hello {{recipient_name}},</p>
<p>Your current balance is <strong>${{current_balance}}</strong>, below the configured alert threshold of <strong>${{threshold}}</strong>.</p>
<p>Please recharge in time to avoid service interruption.</p>
<p><a class="button" href="{{recharge_url}}">Recharge now</a></p>
<p class="muted"><a href="{{unsubscribe_url}}">Unsubscribe from optional balance alerts</a></p>`),
		},
		notificationEmailLocaleSimplifiedChinese: {
			Subject: "[{{site_name}}] 余额不足提醒",
			HTML: notificationEmailCard("#d97706", "余额不足提醒", `
<p>{{recipient_name}}，您好：</p>
<p>您当前余额为 <strong>${{current_balance}}</strong>，已低于提醒阈值 <strong>${{threshold}}</strong>。</p>
<p>请及时充值以免服务中断。</p>
<p><a class="button" href="{{recharge_url}}">立即充值</a></p>
<p class="muted"><a href="{{unsubscribe_url}}">退订此类余额提醒</a></p>`),
		},
	},
	NotificationEmailEventBalanceRechargeSuccess: {
		notificationEmailLocaleEnglish: {
			Subject: "[{{site_name}}] Balance recharge successful",
			HTML: notificationEmailCard("#16a34a", "Recharge successful", `
<p>Hello {{recipient_name}},</p>
<p>Your balance recharge of <strong>${{recharge_amount}}</strong> has been completed.</p>
<p>Current balance: <strong>${{current_balance}}</strong></p>
<p>Order ID: {{order_id}}</p>`),
		},
		notificationEmailLocaleSimplifiedChinese: {
			Subject: "[{{site_name}}] 余额充值成功",
			HTML: notificationEmailCard("#16a34a", "余额充值成功", `
<p>{{recipient_name}}，您好：</p>
<p>您的余额充值 <strong>${{recharge_amount}}</strong> 已完成。</p>
<p>当前余额：<strong>${{current_balance}}</strong></p>
			<p>订单号：{{order_id}}</p>`),
		},
	},
	NotificationEmailEventAccountQuotaAlert: {
		notificationEmailLocaleEnglish: {
			Subject: "[{{site_name}}] Account quota alert - {{account_name}}",
			HTML: notificationEmailCard("#dc2626", "Account quota alert", `
<p>The upstream account <strong>{{account_name}}</strong> has crossed its configured quota alert threshold.</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>Account ID</td><td>{{account_id}}</td></tr>
  <tr><td>Platform</td><td>{{platform}}</td></tr>
  <tr><td>Dimension</td><td>{{quota_dimension}}</td></tr>
  <tr><td>Used / Limit</td><td>{{quota_used}} / {{quota_limit}}</td></tr>
  <tr><td>Remaining</td><td>{{quota_remaining}}</td></tr>
  <tr><td>Threshold</td><td>{{quota_threshold}}</td></tr>
</table>`),
		},
		notificationEmailLocaleSimplifiedChinese: {
			Subject: "[{{site_name}}] 账号限额告警 - {{account_name}}",
			HTML: notificationEmailCard("#dc2626", "账号限额告警", `
<p>上游账号 <strong>{{account_name}}</strong> 已触发配置的额度告警阈值。</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>账号 ID</td><td>{{account_id}}</td></tr>
  <tr><td>平台</td><td>{{platform}}</td></tr>
  <tr><td>维度</td><td>{{quota_dimension}}</td></tr>
  <tr><td>已用 / 限额</td><td>{{quota_used}} / {{quota_limit}}</td></tr>
  <tr><td>剩余额度</td><td>{{quota_remaining}}</td></tr>
  <tr><td>告警阈值</td><td>{{quota_threshold}}</td></tr>
</table>`),
		},
	},
	NotificationEmailEventContentModerationViolation: {
		notificationEmailLocaleEnglish: {
			Subject: "[{{site_name}}] Risk control notice",
			HTML: notificationEmailCard("#ef4444", "Risk control notice", `
<p>Hello {{recipient_name}},</p>
<p>Your API request triggered the platform content moderation/risk-control policy.</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>Triggered at</td><td>{{triggered_at}}</td></tr>
  <tr><td>Group</td><td>{{group_name}}</td></tr>
  <tr><td>Category / Score</td><td>{{moderation_category}} / {{moderation_score}}</td></tr>
  <tr><td>Violation count</td><td>{{violation_count}} / {{ban_threshold}}</td></tr>
</table>
<p>Please review your request content to avoid future service interruptions.</p>`),
		},
		notificationEmailLocaleSimplifiedChinese: {
			Subject: "[{{site_name}}] 账户风控提醒",
			HTML: notificationEmailCard("#ef4444", "账户风控提醒", `
<p>{{recipient_name}}，您好：</p>
<p>您的 API 请求触发了平台内容审核/风控策略。</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>触发时间</td><td>{{triggered_at}}</td></tr>
  <tr><td>所属分组</td><td>{{group_name}}</td></tr>
  <tr><td>命中类别 / 分数</td><td>{{moderation_category}} / {{moderation_score}}</td></tr>
  <tr><td>累计触发次数</td><td>{{violation_count}} / {{ban_threshold}}</td></tr>
</table>
<p>请检查请求内容，避免后续服务受到影响。</p>`),
		},
	},
	NotificationEmailEventContentModerationDisabled: {
		notificationEmailLocaleEnglish: {
			Subject: "[{{site_name}}] Account disabled by risk control",
			HTML: notificationEmailCard("#b91c1c", "Account disabled", `
<p>Hello {{recipient_name}},</p>
<p>Your account has repeatedly triggered platform content moderation/risk-control rules and has been automatically disabled.</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>Disabled at</td><td>{{triggered_at}}</td></tr>
  <tr><td>Group</td><td>{{group_name}}</td></tr>
  <tr><td>Category / Score</td><td>{{moderation_category}} / {{moderation_score}}</td></tr>
  <tr><td>Violation count</td><td>{{violation_count}} / {{ban_threshold}}</td></tr>
</table>
<p>Please contact the administrator if you need to appeal or restore access.</p>`),
		},
		notificationEmailLocaleSimplifiedChinese: {
			Subject: "[{{site_name}}] 账户已被禁用",
			HTML: notificationEmailCard("#b91c1c", "账户已被禁用", `
<p>{{recipient_name}}，您好：</p>
<p>您的账户在统计周期内多次触发平台内容审核/风控规则，系统已自动禁用该账户。</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>禁用时间</td><td>{{triggered_at}}</td></tr>
  <tr><td>所属分组</td><td>{{group_name}}</td></tr>
  <tr><td>命中类别 / 分数</td><td>{{moderation_category}} / {{moderation_score}}</td></tr>
  <tr><td>累计触发次数</td><td>{{violation_count}} / {{ban_threshold}}</td></tr>
</table>
<p>如需申诉或恢复账号，请联系平台管理员处理。</p>`),
		},
	},
	NotificationEmailEventCyberPolicyNotice: {
		notificationEmailLocaleEnglish: {
			Subject: "[{{site_name}}] Cyber-security policy notice",
			HTML: notificationEmailCard("#ef4444", "Cyber-security policy notice", `
<p>Hello {{recipient_name}},</p>
<p>Your request was blocked by the upstream provider's cyber-security policy.</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>Triggered at</td><td>{{triggered_at}}</td></tr>
  <tr><td>Model</td><td>{{model}}</td></tr>
  <tr><td>Group</td><td>{{group_name}}</td></tr>
  <tr><td>Upstream message</td><td>{{upstream_message}}</td></tr>
</table>
<p>If you believe this is a mistake, try rephrasing your request, or apply for authorized security access.</p>`),
		},
		notificationEmailLocaleSimplifiedChinese: {
			Subject: "[{{site_name}}] 网络安全策略拦截提醒",
			HTML: notificationEmailCard("#ef4444", "网络安全策略拦截提醒", `
<p>{{recipient_name}}，您好：</p>
<p>您的请求被上游服务商的网络安全策略（cyber policy）拦截。</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>触发时间</td><td>{{triggered_at}}</td></tr>
  <tr><td>模型</td><td>{{model}}</td></tr>
  <tr><td>所属分组</td><td>{{group_name}}</td></tr>
  <tr><td>上游说明</td><td>{{upstream_message}}</td></tr>
</table>
<p>如认为系误判，可调整请求措辞后重试，或申请获得授权的安全访问权限。</p>`),
		},
	},
	NotificationEmailEventOpsAlert: {
		notificationEmailLocaleEnglish: {
			Subject: "[Ops Alert][{{severity}}] {{rule_name}}",
			HTML: notificationEmailCard("#ea580c", "Ops alert", `
<p><strong>Rule</strong>: {{rule_name}}</p>
<p><strong>Severity</strong>: {{severity}}</p>
<p><strong>Status</strong>: {{alert_status}}</p>
<p><strong>Metric</strong>: {{metric_type}} {{operator}} {{metric_value}} (threshold {{threshold_value}})</p>
<p><strong>Fired at</strong>: {{triggered_at}}</p>
<p><strong>Description</strong>: {{alert_description}}</p>`),
		},
		notificationEmailLocaleSimplifiedChinese: {
			Subject: "[运维告警][{{severity}}] {{rule_name}}",
			HTML: notificationEmailCard("#ea580c", "运维告警", `
<p><strong>规则</strong>：{{rule_name}}</p>
<p><strong>严重级别</strong>：{{severity}}</p>
<p><strong>状态</strong>：{{alert_status}}</p>
<p><strong>指标</strong>：{{metric_type}} {{operator}} {{metric_value}}（阈值 {{threshold_value}}）</p>
<p><strong>触发时间</strong>：{{triggered_at}}</p>
<p><strong>说明</strong>：{{alert_description}}</p>`),
		},
	},
	NotificationEmailEventOpsScheduledReport: {
		notificationEmailLocaleEnglish: {
			Subject: "[Ops Report] {{report_name}}",
			HTML: notificationEmailCard("#0891b2", "Ops report", `
<p><strong>Report</strong>: {{report_name}}</p>
<p><strong>Type</strong>: {{report_type}}</p>
<p><strong>Range</strong>: {{report_start_time}} - {{report_end_time}}</p>
<div>{{report_html}}</div>`),
		},
		notificationEmailLocaleSimplifiedChinese: {
			Subject: "[运维报表] {{report_name}}",
			HTML: notificationEmailCard("#0891b2", "运维报表", `
<p><strong>报表</strong>：{{report_name}}</p>
<p><strong>类型</strong>：{{report_type}}</p>
<p><strong>时间范围</strong>：{{report_start_time}} - {{report_end_time}}</p>
<div>{{report_html}}</div>`),
		},
	},
}

func init() {
	completeNotificationEmailOfficialTemplates()
}

func completeNotificationEmailOfficialTemplates() {
	for _, byLocale := range notificationEmailOfficialTemplates {
		if _, exists := byLocale[notificationEmailLocaleTraditionalChinese]; exists {
			continue
		}
		simplified, ok := byLocale[notificationEmailLocaleSimplifiedChinese]
		if !ok {
			continue
		}
		byLocale[notificationEmailLocaleTraditionalChinese] = notificationEmailOfficialTemplate{
			Subject: traditionalizeNotificationEmailText(simplified.Subject),
			HTML:    traditionalizeNotificationEmailText(simplified.HTML),
		}
	}
}

func notificationEmailCard(accent, title, content string) string {
	return `<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <style>
    body { margin: 0; padding: 40px 16px; background: #f0eee6; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; color: #191917; -webkit-font-smoothing: antialiased; }
    .container { max-width: 560px; margin: 0 auto; background: #ffffff; border: 1px solid #e4ded2; border-radius: 10px; overflow: hidden; }
    .accent { height: 3px; background: ` + accent + `; }
    .body { padding: 36px 40px 32px; }
    .body h1 { margin: 0 0 22px; font-family: Georgia, "Times New Roman", serif; font-size: 23px; font-weight: 600; line-height: 1.3; color: #191917; }
    .body p { margin: 0 0 14px; font-size: 15px; line-height: 1.75; color: #33312c; }
    .body strong { color: #191917; }
    .button { display: inline-block; margin: 6px 0; padding: 11px 22px; border-radius: 8px; background: #191917; color: #ffffff !important; text-decoration: none; font-weight: 600; font-size: 14px; }
    .code { margin: 22px 0; padding: 18px; text-align: center; background: #f5f3ec; border: 1px solid #e4ded2; border-radius: 8px; font-size: 30px; font-weight: 700; letter-spacing: 10px; color: #191917; font-variant-numeric: tabular-nums; }
    .muted { color: #8c877d; font-size: 13px; }
    .muted a { color: #8c877d; }
    .footer { padding: 18px 40px; border-top: 1px solid #efeadf; background: #ffffff; color: #a8a39a; font-size: 12px; }
  </style>
</head>
<body>
  <div class="container">
    <div class="accent"></div>
    <div class="body">
      <h1>` + title + `</h1>
      ` + content + `
    </div>
    <div class="footer">This email was sent by {{site_name}}. Please do not reply directly.</div>
  </div>
</body>
</html>`
}

var notificationEmailTraditionalReplacer = strings.NewReplacer(
	"邮箱", "郵箱",
	"邮件", "郵件",
	"验证码", "驗證碼",
	"验证", "驗證",
	"密码", "密碼",
	"请求", "請求",
	"订阅", "訂閱",
	"购买", "購買",
	"开通", "開通",
	"成功", "成功",
	"有效期", "有效期",
	"到期", "到期",
	"订单号", "訂單號",
	"余额", "餘額",
	"充值", "充值",
	"账号", "帳號",
	"账户", "帳戶",
	"风控", "風控",
	"限额", "限額",
	"告警", "告警",
	"平台", "平台",
	"额度", "額度",
	"每日", "每日",
	"每周", "每週",
	"总", "總",
	"已用", "已用",
	"剩余", "剩餘",
	"阈值", "閾值",
	"触发", "觸發",
	"所属", "所屬",
	"分组", "分組",
	"类别", "類別",
	"次数", "次數",
	"禁用", "停用",
	"网络", "網絡",
	"安全", "安全",
	"策略", "策略",
	"拦截", "攔截",
	"提醒", "提醒",
	"运维", "運維",
	"规则", "規則",
	"严重级别", "嚴重級別",
	"状态", "狀態",
	"指标", "指標",
	"说明", "說明",
	"报表", "報表",
	"类型", "類型",
	"时间", "時間",
	"范围", "範圍",
	"请求量", "請求量",
	"默认", "預設",
	"如果不是您本人操作", "如果不是您本人操作",
	"请", "請",
	"您", "您",
	"这", "這",
	"将", "將",
	"后", "後",
	"无法", "無法",
	"点击", "點擊",
	"以下", "以下",
	"浏览器", "瀏覽器",
	"打开", "打開",
	"当前", "目前",
	"低于", "低於",
	"及时", "及時",
	"以免", "以免",
	"服务", "服務",
	"中断", "中斷",
	"立即", "立即",
	"退订", "退訂",
	"此类", "此類",
	"上游", "上游",
	"配置", "設定",
	"命中", "命中",
	"累计", "累計",
	"检查", "檢查",
	"后续", "後續",
	"影响", "影響",
	"统计", "統計",
	"周期", "週期",
	"系统", "系統",
	"自动", "自動",
	"申诉", "申訴",
	"恢复", "恢復",
	"处理", "處理",
	"服务商", "服務商",
	"认为", "認為",
	"误判", "誤判",
	"调整", "調整",
	"措辞", "措辭",
	"重试", "重試",
	"申请", "申請",
	"获得", "獲得",
	"授权", "授權",
	"访问", "存取",
	"高", "高",
	"错误率", "錯誤率",
	"最近", "最近",
	"分钟", "分鐘",
	"超过", "超過",
	"日报", "日報",
	"中文", "中文",
	"张三", "張三",
)

func traditionalizeNotificationEmailText(value string) string {
	if value == "" {
		return value
	}
	return notificationEmailTraditionalReplacer.Replace(value)
}

func localizeChineseNotificationEmailText(locale, value string) string {
	if isNotificationTraditionalLocale(locale) {
		return traditionalizeNotificationEmailText(value)
	}
	return value
}
