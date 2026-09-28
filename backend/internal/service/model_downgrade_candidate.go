package service

import "context"

// IsOpenAIAccountUsableForDowngradeGuard applies the persistent model and
// quota filters used by OpenAI scheduling when preserving a fallback account.
func IsOpenAIAccountUsableForDowngradeGuard(ctx context.Context, account *Account, requestedModel string) bool {
	if account == nil || account.Platform != PlatformOpenAI ||
		!account.IsSchedulableForModelWithContext(ctx, requestedModel) ||
		!account.IsModelSupported(requestedModel) {
		return false
	}
	paused, _ := shouldAutoPauseOpenAIAccountByQuota(ctx, account)
	return !paused
}
