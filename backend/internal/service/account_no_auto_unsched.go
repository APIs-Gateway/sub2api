package service

import "github.com/Wei-Shaw/sub2api/internal/config"

// accountNoAutoUnsched：账号在 gateway.no_auto_unsched_account_ids 名单内时，
// 流超时暂停和 OpenAI 单模型短冷却都不触发，账号始终留在调度里。
func accountNoAutoUnsched(cfg *config.Config, account *Account) bool {
	if cfg == nil || account == nil {
		return false
	}
	for _, id := range cfg.Gateway.NoAutoUnschedAccountIDs {
		if id == account.ID {
			return true
		}
	}
	return false
}
