package schema

import (
	"time"

	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/domain"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UserSubscription holds the schema definition for the UserSubscription entity.
type UserSubscription struct {
	ent.Schema
}

func (UserSubscription) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "user_subscriptions"},
	}
}

func (UserSubscription) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (UserSubscription) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.Int64("group_id"),
		// plan_id：订阅来源套餐（burn-down 一等关联）。可空：存量按 group 直接分配、
		// 或所挂 group 无对应 plan 的卡为 NULL。Phase 1 起新发放写入；group_id 暂保留照写。
		field.Int64("plan_id").
			Optional().
			Nillable(),

		field.Time("starts_at").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("expires_at").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("status").
			MaxLen(20).
			Default(domain.SubscriptionStatusActive),

		field.Time("daily_window_start").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("weekly_window_start").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("monthly_window_start").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		field.Float("daily_usage_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Default(0),
		field.Float("weekly_usage_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Default(0),
		field.Float("monthly_usage_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Default(0),

		// Burn-down 计费模型字段：开通时一次性把 G=D×days 打入用户余额，
		// 每张订阅作为独立 burn-down 账户，消费/清扣按本卡 consumed/clawed 核算。
		// remaining = granted_total_usd - consumed_usd - clawed_usd
		field.Float("granted_total_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Default(0),
		field.Float("daily_amount_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Default(0),
		field.Float("consumed_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Default(0),
		field.Float("clawed_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Default(0),
		field.Int("last_clawback_day").
			Default(0),
		// 本卡「最多往后透支天数」：累计被扣最多到 (已过天数+N)×D，由用户在「我的订阅」自助设置。
		// nil = 透支关闭。
		field.Int("max_overdraft_days").
			Optional().
			Nillable(),
		// 本卡累计预支天数（求和、封顶 MaxSubscriptionOverdraftUses）：每天突破当日 D 的天数累加，
		// 达上限后自动关闭本卡透支（max_overdraft_days→NULL）。
		field.Int("total_overdraft_count").
			Default(0),
		// 本卡当前 burn-down 日内已消费额度，配合 daily_spent_day 实现「每日限速 D、不跨天结转、透支前移周期」。
		field.Float("daily_spent_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Default(0),
		// daily_spent_usd 对应的日历天 N（自激活起跨过的东八区午夜数）。读写时若 ≠ 当前 N 即视为 0（惰性重置）。
		// 默认 -1 表示「未初始化」——任何真实日历天 N≥0 都不等于它，避免与「day0 已消费 0」混淆。
		field.Int("daily_spent_day").
			Default(-1),

		// ── Per-day 每日额度模型字段（per-day redesign，加性引入；逐步取代上方 burn-down 窗口）──
		// 套餐余额只存 today_remaining（今日剩余，官方刀，1:1 扣减、永不为负）+ today_day（它属于
		// 哪个东八区自然日序号）。跨天且 today ≤ expire_day 才惰性覆盖成 D；today > expire_day 置 0
		// 并标 expired。无发放/撤回/清扣动作、零后台任务。服务区间用绝对自然日序号 [start_day, expire_day]：
		// expire_day = 最后发放 D 的自然日（含），每透支一次 expire_day−=1；today > expire_day 即到期。
		// start_day/expire_day/today_day 同为「东八区绝对日序号」= floor((unix+8h)/86400)，
		// 可直接做 expire_day−today 等算术（与上方 daily_spent_day 的「相对激活天数」口径不同）。
		field.Float("today_remaining").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Default(0),
		// today_remaining 对应的东八区自然日序号；读写时 ≠ 当前日即惰性覆盖。-1=未初始化。
		field.Int("today_day").
			Default(-1),
		// 激活当天的东八区自然日序号。
		field.Int("start_day").
			Default(0),
		// 最后发放 D 的东八区自然日序号（含）；无透支时 = start_day+T−1，每透支 −1。
		field.Int("expire_day").
			Default(0),

		// 清扣时钟起点（按 Asia/Shanghai 从此算第 N 个日历天）。
		// 为 nil 时回退到 starts_at；存量回填时设为 NOW() 以对剩余期重新计时。
		field.Time("activated_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		field.Int64("assigned_by").
			Optional().
			Nillable(),
		field.Time("assigned_at").
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("notes").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
	}
}

func (UserSubscription) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("subscriptions").
			Field("user_id").
			Unique().
			Required(),
		edge.From("group", Group.Type).
			Ref("subscriptions").
			Field("group_id").
			Unique().
			Required(),
		// plan：订阅来源套餐（可空边 → 生成 ON DELETE SET NULL，与迁移 155 一致）。
		edge.From("plan", SubscriptionPlan.Type).
			Ref("subscriptions").
			Field("plan_id").
			Unique(),
		edge.From("assigned_by_user", User.Type).
			Ref("assigned_subscriptions").
			Field("assigned_by").
			Unique(),
		edge.To("usage_logs", UsageLog.Type),
	}
}

func (UserSubscription) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id"),
		index.Fields("group_id"),
		index.Fields("status"),
		index.Fields("expires_at"),
		// 活跃订阅查询复合索引（线上由 SQL 迁移创建部分索引，schema 仅用于模型可读性对齐）
		index.Fields("user_id", "status", "expires_at"),
		index.Fields("assigned_by"),
		index.Fields("plan_id"),
		// 唯一约束通过部分索引实现（WHERE deleted_at IS NULL），支持软删除后重新订阅
		// 见迁移文件 016_soft_delete_partial_unique_indexes.sql
		index.Fields("user_id", "group_id"),
		index.Fields("deleted_at"),
	}
}
