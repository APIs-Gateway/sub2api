package service

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// AccountExtraKeyNoAutoUnsched is the per-account extra flag the admin account forms write.
//
// 「不自动暂停调度」账号：因上游失败而自动产生的任何停调度/冷却都不影响它被选中——
// 429 / 529 / 401 / 403 / 临时不可调度规则 / 流超时 / 单模型限流 / 运行时冷却 / 传输错误隔离 /
// 代理流隔离，以及由这些原因自动置的 status=error。典型用途是只有一个号的专用分组：
// 号被冷却后整组只会变成 503「no available accounts」，白白拦掉冷却期内本来能成功的请求。
//
// 开关只管「因出错自动产生的停调度」。仍然生效（保持原行为）的条件：管理员手动停用
// （status=disabled、status=active 下 schedulable=false）、账号过期自动暂停、API Key 配额耗尽、
// OpenAI 用量阈值自动暂停，以及分组/模型/渠道/能力匹配/并发上限这类非账号状态条件。
const AccountExtraKeyNoAutoUnsched = "no_auto_unsched"

// noAutoUnschedConfigIDs 是 gateway.no_auto_unsched_account_ids 的运行时副本。
//
// 账号级判断（Account.IsSchedulable、调度快照 SQL）拿不到 *config.Config，所以启动时由
// wire 注入一次（SetNoAutoUnschedConfigIDs）。配置在进程生命周期内不变，不需要热更新。
var noAutoUnschedConfigIDs atomic.Pointer[map[int64]struct{}]

// SetNoAutoUnschedConfigIDs installs the config-listed exempt account IDs (nil/empty clears).
func SetNoAutoUnschedConfigIDs(ids []int64) {
	if len(ids) == 0 {
		noAutoUnschedConfigIDs.Store(nil)
		return
	}
	set := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id > 0 {
			set[id] = struct{}{}
		}
	}
	if len(set) == 0 {
		noAutoUnschedConfigIDs.Store(nil)
		return
	}
	noAutoUnschedConfigIDs.Store(&set)
}

// NoAutoUnschedConfigIDs returns the config-listed exempt account IDs (unordered copy).
func NoAutoUnschedConfigIDs() []int64 {
	set := noAutoUnschedConfigIDs.Load()
	if set == nil {
		return nil
	}
	ids := make([]int64, 0, len(*set))
	for id := range *set {
		ids = append(ids, id)
	}
	return ids
}

func isNoAutoUnschedConfigID(id int64) bool {
	set := noAutoUnschedConfigIDs.Load()
	if set == nil {
		return false
	}
	_, ok := (*set)[id]
	return ok
}

// NoAutoUnsched reports whether automatic (upstream-failure driven) scheduling pauses must be
// ignored for this account: extra.no_auto_unsched=true or listed in gateway.no_auto_unsched_account_ids.
func (a *Account) NoAutoUnsched() bool {
	if a == nil {
		return false
	}
	return a.getExtraBool(AccountExtraKeyNoAutoUnsched) || isNoAutoUnschedConfigID(a.ID)
}

// accountNoAutoUnsched is NoAutoUnsched plus an explicit config (union of both sources). Call sites
// that hold a *config.Config use it so they also honor a config that was not installed globally.
func accountNoAutoUnsched(cfg *config.Config, account *Account) bool {
	if account == nil {
		return false
	}
	if account.NoAutoUnsched() {
		return true
	}
	if cfg == nil {
		return false
	}
	for _, id := range cfg.Gateway.NoAutoUnschedAccountIDs {
		if id == account.ID {
			return true
		}
	}
	return false
}

type manualAccountStateKey struct{}

// WithManualAccountState marks ctx as an administrator-initiated state change. Repository auto-pause
// gates (SetError and friends) skip the no_auto_unsched exemption for such writes so a manual stop
// always takes effect.
func WithManualAccountState(ctx context.Context) context.Context {
	return context.WithValue(ctx, manualAccountStateKey{}, true)
}

// IsManualAccountState reports whether ctx was marked by WithManualAccountState.
func IsManualAccountState(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	manual, _ := ctx.Value(manualAccountStateKey{}).(bool)
	return manual
}

const noAutoUnschedSkipLogInterval = 30 * time.Second

var noAutoUnschedSkipLogAt sync.Map // key: skipLogKey -> time.Time

type noAutoUnschedSkipLogKey struct {
	accountID int64
	action    string
}

// LogNoAutoUnschedSkip records (throttled to once per account/action per 30s) that an automatic
// pause was suppressed for an exempt account, so operators can still see the upstream failures.
func LogNoAutoUnschedSkip(accountID int64, action, detail string) {
	now := time.Now()
	key := noAutoUnschedSkipLogKey{accountID: accountID, action: action}
	if last, ok := noAutoUnschedSkipLogAt.Load(key); ok {
		if lastAt, ok := last.(time.Time); ok && now.Sub(lastAt) < noAutoUnschedSkipLogInterval {
			return
		}
	}
	noAutoUnschedSkipLogAt.Store(key, now)
	slog.Info("no_auto_unsched_skip", "account_id", accountID, "action", action, "detail", detail)
}
