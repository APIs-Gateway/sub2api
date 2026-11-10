package repository

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbpredicate "github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/internal/service"

	entsql "entgo.io/ent/dialect/sql"
)

// 「不自动暂停调度」账号（extra.no_auto_unsched=true 或 gateway.no_auto_unsched_account_ids 名单内）：
//
//   - 读侧：所有「可调度账号」列表查询（调度快照 / Redis 桶重建 / 回源）对它忽略因出错自动产生的状态
//     （temp_unschedulable_until / overload_until / rate_limit_reset_at / 自动写入的 status=error），
//     否则账号一冷却就连候选池都进不去（表现为 no available accounts ... pool=0）；
//     手动停用（status=disabled、人工关 schedulable）与过期照常生效。
//   - 写侧：因上游失败自动产生的冷却/置错（SetError / SetRateLimited / SetOverloaded /
//     SetTempUnschedulable / SetModelRateLimit）直接不落库，避免失败风暴期间每个失败请求都写库 + 发 outbox 事件。
//     管理员手动操作（service.WithManualAccountState）不受影响。

// noAutoUnschedExpr returns a SQL boolean expression that is true for exempt accounts.
// extraCol / idCol are the (already quoted/qualified) column references to use.
func noAutoUnschedExpr(extraCol, idCol string) string {
	expr := fmt.Sprintf("COALESCE(%s, '{}'::jsonb) @> '{\"%s\": true}'::jsonb", extraCol, service.AccountExtraKeyNoAutoUnsched)
	ids := service.NoAutoUnschedConfigIDs()
	if len(ids) == 0 {
		return expr
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return fmt.Sprintf("(%s OR %s IN (%s))", expr, idCol, strings.Join(parts, ", "))
}

// noAutoUnschedRawSQL is noAutoUnschedExpr for hand-written SQL, with an optional table alias.
func noAutoUnschedRawSQL(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return noAutoUnschedExpr(prefix+"extra", prefix+"id")
}

// noAutoUnschedPredicate matches exempt accounts.
func noAutoUnschedPredicate() dbpredicate.Account {
	return dbpredicate.Account(func(s *entsql.Selector) {
		s.Where(entsql.ExprP(noAutoUnschedExpr(s.C(dbaccount.FieldExtra), s.C(dbaccount.FieldID))))
	})
}

// notNoAutoUnschedPredicate matches accounts that are NOT exempt (used to gate auto-pause writes).
func notNoAutoUnschedPredicate() dbpredicate.Account {
	return dbaccount.Not(noAutoUnschedPredicate())
}

// schedulableStatePredicate 是所有「可调度账号」列表查询共用的账号状态过滤：
// 带标记账号忽略三类冷却窗口和自动写入的 status=error，人工停用与过期仍然生效；
// 其余账号维持原口径（status=active、schedulable=true、未过期、不在冷却窗口内）。
// 分组、平台等非状态条件由调用方另行叠加。
func schedulableStatePredicate(now time.Time) dbpredicate.Account {
	return dbaccount.Or(
		// 带标记账号：忽略三类冷却窗口；status=error（只由失败处理自动写入，同时置 schedulable=false）
		// 视为自动停调度而放行；status=disabled / 人工关 schedulable / 过期仍然生效。
		dbaccount.And(
			noAutoUnschedPredicate(),
			dbaccount.Or(
				dbaccount.StatusEQ(service.StatusError),
				dbaccount.And(dbaccount.StatusEQ(service.StatusActive), dbaccount.SchedulableEQ(true)),
			),
			notExpiredPredicate(now),
		),
		dbaccount.And(
			dbaccount.StatusEQ(service.StatusActive),
			dbaccount.SchedulableEQ(true),
			tempUnschedulablePredicate(),
			notExpiredPredicate(now),
			dbaccount.Or(dbaccount.OverloadUntilIsNil(), dbaccount.OverloadUntilLTE(now)),
			dbaccount.Or(dbaccount.RateLimitResetAtIsNil(), dbaccount.RateLimitResetAtLTE(now)),
		),
	)
}

// logAutoPauseWriteSkipped records that an automatic pause write was suppressed for an exempt account.
func logAutoPauseWriteSkipped(id int64, action string) {
	service.LogNoAutoUnschedSkip(id, action, "automatic pause not recorded: account is exempt")
}

// extraNoAutoUnsched is the in-process equivalent of noAutoUnschedExpr for a raw extra JSON blob.
func extraNoAutoUnsched(extra []byte, id int64) bool {
	for _, configured := range service.NoAutoUnschedConfigIDs() {
		if configured == id {
			return true
		}
	}
	var record map[string]json.RawMessage
	if json.Unmarshal(extra, &record) != nil {
		return false
	}
	return string(record[service.AccountExtraKeyNoAutoUnsched]) == "true"
}
