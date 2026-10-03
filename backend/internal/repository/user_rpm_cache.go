package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// 用户/分组级 RPM 计数器 Redis 实现。
//
// 设计说明：
//   - key 形式：rpm:ug:{uid}:{gid}:{minute}、rpm:u:{uid}:{minute}
//   - 时间来源：rdb.Time()（Redis 服务端时间），避免多实例时钟漂移。
//   - 原子操作：TxPipeline (MULTI/EXEC) 执行 INCR+EXPIRE，兼容 Redis Cluster。
//   - TTL：120s，覆盖当前分钟窗口 + 少量冗余。
//   - 返回值语义：超限判断由调用方（billing_cache_service.checkRPM）与 RPMLimit 比较完成。
const (
	userGroupRPMKeyPrefix = "rpm:ug:"
	userRPMKeyPrefix      = "rpm:u:"

	userRPMKeyTTL = 120 * time.Second
)

type userRPMCacheImpl struct {
	rdb *redis.Client
}

// NewUserRPMCache 创建用户/分组级 RPM 计数器。
func NewUserRPMCache(rdb *redis.Client) service.UserRPMCache {
	return &userRPMCacheImpl{rdb: rdb}
}

// minuteTS 获取当前 Redis 服务端分钟时间戳。
func (c *userRPMCacheImpl) minuteTS(ctx context.Context) (int64, error) {
	t, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return 0, fmt.Errorf("redis TIME: %w", err)
	}
	return t.Unix() / 60, nil
}

// atomicIncr 原子 INCR+EXPIRE。
func (c *userRPMCacheImpl) atomicIncr(ctx context.Context, key string) (int, error) {
	pipe := c.rdb.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, userRPMKeyTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("user rpm increment: %w", err)
	}
	return int(incr.Val()), nil
}

// IncrementUserGroupRPM 递增 (user, group) 分钟计数。
func (c *userRPMCacheImpl) IncrementUserGroupRPM(ctx context.Context, userID, groupID int64) (int, error) {
	minute, err := c.minuteTS(ctx)
	if err != nil {
		return 0, err
	}
	key := fmt.Sprintf("%s%d:%d:%d", userGroupRPMKeyPrefix, userID, groupID, minute)
	return c.atomicIncr(ctx, key)
}

// IncrementUserRPM 递增用户分钟计数。
func (c *userRPMCacheImpl) IncrementUserRPM(ctx context.Context, userID int64) (int, error) {
	minute, err := c.minuteTS(ctx)
	if err != nil {
		return 0, err
	}
	key := fmt.Sprintf("%s%d:%d", userRPMKeyPrefix, userID, minute)
	return c.atomicIncr(ctx, key)
}

// GetUserGroupRPM 获取 (user, group) 当前分钟已用 RPM（只读）。
func (c *userRPMCacheImpl) GetUserGroupRPM(ctx context.Context, userID, groupID int64) (int, error) {
	minute, err := c.minuteTS(ctx)
	if err != nil {
		return 0, err
	}
	key := fmt.Sprintf("%s%d:%d:%d", userGroupRPMKeyPrefix, userID, groupID, minute)
	val, err := c.rdb.Get(ctx, key).Int()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("user group rpm get: %w", err)
	}
	return val, nil
}

// GetUserRPM 获取用户当前分钟已用 RPM（只读）。
func (c *userRPMCacheImpl) GetUserRPM(ctx context.Context, userID int64) (int, error) {
	minute, err := c.minuteTS(ctx)
	if err != nil {
		return 0, err
	}
	key := fmt.Sprintf("%s%d:%d", userRPMKeyPrefix, userID, minute)
	val, err := c.rdb.Get(ctx, key).Int()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("user rpm get: %w", err)
	}
	return val, nil
}

// userGroupRPMDecrScript 只在 key 存在且大于 0 时递减，避免过期或并发后出现负数。
var userGroupRPMDecrScript = redis.NewScript(`
local v = redis.call('GET', KEYS[1])
if v and tonumber(v) > 0 then
  return redis.call('DECR', KEYS[1])
end
return 0
`)

var _ service.UserGroupRPMSlotCounter = (*userRPMCacheImpl)(nil)

// userGroupRPMSlotKey 是 (user, group) 在某个分钟槽上的计数 key，格式与 IncrementUserGroupRPM 一致。
func userGroupRPMSlotKey(userID, groupID, minute int64) string {
	return fmt.Sprintf("%s%d:%d:%d", userGroupRPMKeyPrefix, userID, groupID, minute)
}

// IncrementUserGroupRPMSlot 递增 (user, group) 分钟计数，并返回这次递增落在的分钟槽，
// 供回退链的 ticket 在该跳没有真正服务时对同一个槽退回一次（审查 S5）。
func (c *userRPMCacheImpl) IncrementUserGroupRPMSlot(ctx context.Context, userID, groupID int64) (int, int64, error) {
	minute, err := c.minuteTS(ctx)
	if err != nil {
		return 0, 0, err
	}
	count, err := c.atomicIncr(ctx, userGroupRPMSlotKey(userID, groupID, minute))
	if err != nil {
		return 0, 0, err
	}
	return count, minute, nil
}

// DecrementUserGroupRPMSlot 把 (user, group) 在指定分钟槽的计数减 1。
// 只动传入的槽，不读取当前时间：跨分钟结束的一跳不会减到新一分钟的计数上。
// Lua 保证 key 不存在或计数为 0 时不递减（不会出现负数，也不会新建没有 TTL 的 key）。
func (c *userRPMCacheImpl) DecrementUserGroupRPMSlot(ctx context.Context, userID, groupID, slot int64) error {
	key := userGroupRPMSlotKey(userID, groupID, slot)
	if err := userGroupRPMDecrScript.Run(ctx, c.rdb, []string{key}).Err(); err != nil && err != redis.Nil {
		return fmt.Errorf("user group rpm decrement: %w", err)
	}
	return nil
}
