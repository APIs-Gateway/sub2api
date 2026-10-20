package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ChannelMonitorGroupStats 按 (group_id, bucket_start, bucket_minutes) 维度聚合的渠道监控分组真实成功率统计。
// 每个 10 分钟桶为一行，记录该分组的真实流量成功/失败计数和成功率。
// 写入路径完全脱离请求主链，由 service.ChannelMonitorStatsAggregationTask 后台定时任务填充。
type ChannelMonitorGroupStats struct {
	ent.Schema
}

func (ChannelMonitorGroupStats) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "channel_monitor_group_stats"},
	}
}

func (ChannelMonitorGroupStats) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("group_id").
			Comment("分组 ID，关联 groups 表"),
		field.Time("bucket_start").
			Comment("时间桶起点（精确到分钟，date_trunc('minute', ...)）"),
		field.Int16("bucket_minutes").
			Default(10).
			Comment("桶大小（分钟），默认 10"),
		field.Int("success_count").
			Default(0).
			Comment("usage_logs 写入行数（排除探测 key + nginx 伪造响应）"),
		field.Int("error_5xx_count").
			Default(0).
			Comment("ops_error_logs 中 status_code>=500 且非排除类"),
		field.Int("total_count").
			Default(0).
			Comment("success_count + error_5xx_count"),
		field.Float("success_rate").
			Optional().
			Nillable().
			Comment("success_count / total_count，NULL when total=0"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

func (ChannelMonitorGroupStats) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("group_id", "bucket_start", "bucket_minutes").Unique(),
		index.Fields("group_id", "bucket_start").
			Annotations(entsql.DescColumns("bucket_start")),
		index.Fields("bucket_start").
			Annotations(entsql.DescColumns("bucket_start")),
	}
}
