package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SubscriptionPlan holds the schema definition for the SubscriptionPlan entity.
//
// 删除策略：硬删除
// SubscriptionPlan 使用硬删除而非软删除，原因如下：
//   - 套餐为管理员维护的商品配置，删除即表示下架移除
//   - 通过 for_sale 字段控制是否在售，删除仅用于彻底移除
//   - 已购买的订阅记录保存在 UserSubscription 中，不受套餐删除影响
type SubscriptionPlan struct {
	ent.Schema
}

func (SubscriptionPlan) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "subscription_plans"},
	}
}

func (SubscriptionPlan) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("group_id"),
		// daily_amount_usd：套餐每日发放额度 D（burn-down 模型 source of truth）。
		// 取代开通时从 group.daily_limit_usd 推导；迁移 155 从所挂 group 回填。
		field.Float("daily_amount_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Default(0),
		// platform：仅用于展示/筛选（取代当前从 group 派生 platform）；不构成路由或权限约束。
		field.String("platform").
			MaxLen(50).
			Default(""),
		field.String("name").
			MaxLen(100).
			NotEmpty(),
		field.String("description").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),
		field.Float("price").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}),
		field.Float("original_price").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).
			Optional().
			Nillable(),
		// currency：可选的 ISO 4217 展示标签，不参与支付扣款逻辑。
		field.String("currency").
			MaxLen(3).
			Default(""),
		field.Int("validity_days").
			Default(30),
		field.String("validity_unit").
			MaxLen(10).
			Default("day"),
		field.String("features").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),
		field.String("product_name").
			MaxLen(100).
			Default(""),
		field.Bool("for_sale").
			Default(true),
		field.Int("sort_order").
			Default(0),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (SubscriptionPlan) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("group_id"),
		index.Fields("for_sale"),
	}
}

// Edges 声明套餐的反向关联，使生成的 ent migrate schema 与 SQL 迁移里
// user_subscriptions.plan_id / redeem_codes.plan_id 的外键保持一致（可空边 → ON DELETE SET NULL）。
func (SubscriptionPlan) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("subscriptions", UserSubscription.Type),
		edge.To("redeem_codes", RedeemCode.Type),
	}
}
