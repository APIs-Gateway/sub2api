package repository

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newMiniredisUserRPMCache(t *testing.T) (service.UserGroupRPMSlotCounter, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	counter, ok := NewUserRPMCache(rdb).(service.UserGroupRPMSlotCounter)
	require.True(t, ok, "生产实现必须支持分钟槽计数，否则回退链退不回计数")
	return counter, mr
}

func userGroupSlotRedisKey(userID, groupID, slot int64) string {
	return "rpm:ug:" + strconv.FormatInt(userID, 10) + ":" + strconv.FormatInt(groupID, 10) + ":" + strconv.FormatInt(slot, 10)
}

// S5：退回只减递增时的那一分钟，跨分钟后不会减到新一分钟的计数上。
func TestUserRPMCache_DecrementSlotTargetsOriginalMinute(t *testing.T) {
	counter, mr := newMiniredisUserRPMCache(t)
	ctx := context.Background()

	t0 := time.Unix(1_700_000_040, 0) // 分钟对齐
	mr.SetTime(t0)
	count, slot, err := counter.IncrementUserGroupRPMSlot(ctx, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.EqualValues(t, t0.Unix()/60, slot)

	// 时间走到下一分钟，新的一跳在新槽上又计了一次。
	mr.SetTime(t0.Add(61 * time.Second))
	count2, slot2, err := counter.IncrementUserGroupRPMSlot(ctx, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 1, count2, "新的一分钟从 1 开始")
	require.Equal(t, slot+1, slot2)

	// 对上一分钟的槽退回：旧槽归零，新槽不受影响。
	require.NoError(t, counter.DecrementUserGroupRPMSlot(ctx, 1, 2, slot))
	oldVal, err := mr.Get(userGroupSlotRedisKey(1, 2, slot))
	require.NoError(t, err)
	require.Equal(t, "0", oldVal)
	newVal, err := mr.Get(userGroupSlotRedisKey(1, 2, slot2))
	require.NoError(t, err)
	require.Equal(t, "1", newVal, "跨分钟退回不得减到新一分钟的计数")
}

// 退回不会减到负数，也不会为不存在的槽新建 key；不同 (user, group) 互不影响。
func TestUserRPMCache_DecrementSlotNeverNegativeAndDoesNotCreateKeys(t *testing.T) {
	counter, mr := newMiniredisUserRPMCache(t)
	ctx := context.Background()
	mr.SetTime(time.Unix(1_700_000_040, 0))

	_, slot, err := counter.IncrementUserGroupRPMSlot(ctx, 1, 2)
	require.NoError(t, err)

	require.NoError(t, counter.DecrementUserGroupRPMSlot(ctx, 1, 2, slot))
	require.NoError(t, counter.DecrementUserGroupRPMSlot(ctx, 1, 2, slot), "重复退回也不报错")
	val, err := mr.Get(userGroupSlotRedisKey(1, 2, slot))
	require.NoError(t, err)
	require.Equal(t, "0", val, "不会减到负数")

	require.NoError(t, counter.DecrementUserGroupRPMSlot(ctx, 9, 9, slot+5))
	require.False(t, mr.Exists(userGroupSlotRedisKey(9, 9, slot+5)), "不存在的槽不会被新建")

	// 另一个 (user, group) 的计数不受影响。
	_, slotOther, err := counter.IncrementUserGroupRPMSlot(ctx, 1, 3)
	require.NoError(t, err)
	require.NoError(t, counter.DecrementUserGroupRPMSlot(ctx, 1, 2, slot))
	other, err := mr.Get(userGroupSlotRedisKey(1, 3, slotOther))
	require.NoError(t, err)
	require.Equal(t, "1", other)
}

// 带槽的递增与原 IncrementUserGroupRPM 共用同一个计数器。
func TestUserRPMCache_SlotIncrementSharesCounterWithPlainIncrement(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewUserRPMCache(rdb)
	counter, ok := cache.(service.UserGroupRPMSlotCounter)
	require.True(t, ok)
	ctx := context.Background()
	mr.SetTime(time.Unix(1_700_000_040, 0))

	plain, err := cache.IncrementUserGroupRPM(ctx, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 1, plain)
	withSlot, _, err := counter.IncrementUserGroupRPMSlot(ctx, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 2, withSlot)
	current, err := cache.GetUserGroupRPM(ctx, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 2, current)
}
