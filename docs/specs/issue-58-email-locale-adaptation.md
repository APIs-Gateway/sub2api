# Issue 58: Email Locale Adaptation

## §1 Resolution Order

Notification emails must resolve locale in this order:

1. Valid explicit request locale, such as `Accept-Language`, payment locale, or another caller-supplied locale hint.
2. Remembered recipient locale by user id.
3. Remembered recipient locale by normalized email hash.
4. Site `default_locale`.
5. Fixed service fallback.

The current user model has no dedicated language-preference field, so the remembered recipient locale is the project-local representation of the user's email language preference.

## §2 Locale Normalization

`zh-CN`, `zh-Hans`, legacy `zh`, and other simplified/default Chinese language tags resolve to notification locale `zh-CN`.

`zh-HK`, `zh-TW`, `zh-MO`, `zh-Hant`, and other traditional Chinese language tags resolve to notification locale `zh-HK`.

`en`, `en-US`, and other English language tags resolve to notification locale `en`.

Unsupported non-empty language tags are not remembered as a preference and must continue down the resolution chain instead of silently overwriting the recipient with English.

## §3 Memory Refresh

Whenever a key email-triggering path receives a valid explicit locale, it refreshes recipient locale memory for the recipient email and, when available, user id.

This includes registration verification, password reset, TOTP verification, email identity binding, notification email verification, and payment/subscription requests.

## §4 Site Default

When no explicit or remembered locale exists, `SettingKeyDefaultLocale` controls the notification locale.

`zh-CN`, `zh-HK`, and `en` are preserved as notification locales. Missing or invalid setting values use the same default-locale fallback as site settings.

## §5 Legacy Fallback

If a notification template cannot be used and the code falls back to built-in HTML/subject text, the built-in fallback must use the same resolved locale as the templated path.

Fallback text should be single-locale text rather than bilingual mixed text.

## §6 Tests

Unit tests must cover:

1. `Accept-Language` normalization.
2. Remembered locale by user id and email hash.
3. Site default locale fallback.
4. Legacy fallback locale consistency for auth and balance emails.

## §7 Conformance Check

1. §1 is implemented in `NotificationEmailService.ResolveRecipientLocale` and `EmailService.resolveLegacyEmailLocale`.
2. §2 is implemented by `normalizeNotificationLocaleHint`, which preserves supported frontend locales and rejects unsupported hints instead of storing them as English.
3. §3 is implemented by `NotificationEmailService.Send`, `EmailService` fallback resolution, TOTP verification, email identity binding, notification email verification, and the existing payment order locale memory path.
4. §4 is implemented by site default fallback through `SettingKeyDefaultLocale`.
5. §5 is implemented for auth verification, password reset, notification email verification, balance/quota alerts, and content moderation/cyber-policy fallback subjects and bodies.
6. §6 is covered by service unit tests in `notification_email_service_test.go` and `balance_notify_email_body_test.go`.
