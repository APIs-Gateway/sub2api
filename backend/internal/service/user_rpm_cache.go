package service

import "context"

// UserRPMCache 用户/分组级 RPM 计数器接口。
//
// 与账号级 RPMCache 的区别：
//   - RPMCache    —— 按外部 AI provider 账号聚合（key: rpm:{accountID}:{min}）。
//   - UserRPMCache —— 按用户或 (用户, 分组) 聚合，杜绝"同一用户创建多个 API Key 绕过 RPM"的路径。
//     key 形如 rpm:ug:{userID}:{groupID}:{min} 或 rpm:u:{userID}:{min}。
type UserRPMCache interface {
	// IncrementUserGroupRPM 原子递增 (user, group) 级分钟计数并返回最新值。
	// 用于分组 rpm_limit 与 user-group rpm_override 两种命中分支。
	IncrementUserGroupRPM(ctx context.Context, userID, groupID int64) (count int, err error)

	// IncrementUserRPM 原子递增用户级分钟计数并返回最新值。
	// 用于用户全局 rpm_limit 兜底分支（分组未设且无 override 时）。
	IncrementUserRPM(ctx context.Context, userID int64) (count int, err error)

	// GetUserGroupRPM 获取 (user, group) 当前分钟已用 RPM（只读，不递增）。
	GetUserGroupRPM(ctx context.Context, userID, groupID int64) (count int, err error)

	// GetUserRPM 获取用户当前分钟已用 RPM（只读，不递增）。
	GetUserRPM(ctx context.Context, userID int64) (count int, err error)
}

// UserGroupRPMSlotCounter 是 UserRPMCache 的可选扩展，给回退链的「退回一次计数」用：
// 递增时同时返回这次递增落在的分钟槽，退回时只对那个槽减 1，而不是对「退回时的当前分钟」减 1
// （否则跨分钟结束的一跳会给新一分钟凭空多出额度，审查 S5）。
// 做成独立接口，是为了不强迫已有实现与测试替身新增方法，调用方用类型断言判断是否支持：
// 不支持时回退链路径照常用 IncrementUserGroupRPM 计数，只是没有办法精确退回，静默少退一次（偏保守）。
type UserGroupRPMSlotCounter interface {
	// IncrementUserGroupRPMSlot 与 IncrementUserGroupRPM 语义相同，另返回这次递增落在的分钟槽 slot。
	IncrementUserGroupRPMSlot(ctx context.Context, userID, groupID int64) (count int, slot int64, err error)

	// DecrementUserGroupRPMSlot 尽力而为地把 (user, group) 在指定分钟槽的计数减 1，不会减到负数，
	// 也不会新建 key；不读取当前时间。
	DecrementUserGroupRPMSlot(ctx context.Context, userID, groupID, slot int64) error
}
