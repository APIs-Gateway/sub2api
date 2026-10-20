package repository

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// 稳定优先状态机的 Redis 落地。
//
// 设计约定：normal 态不落 key，仅 fallback 态存在 Hash：
//
//	stable_priority:openai:group:{homeGroupID} -> { mode, since, healthy_since }
//
// 所有状态翻转用 Lua CAS 保证并发安全（同一时刻只有一个请求完成 enter/revert）。
const stablePriorityKeyPrefix = "stable_priority:openai:group:"

var (
	// enterFallbackScript: 仅当当前非 fallback 时置 fallback。
	// KEYS[1]=stateKey  ARGV[1]=now  ARGV[2]=ttlSeconds
	// 返回 1=本次切换, 0=已是 fallback（幂等）
	stableEnterFallbackScript = redis.NewScript(`
local mode = redis.call('HGET', KEYS[1], 'mode')
if mode == 'fallback' then
	redis.call('EXPIRE', KEYS[1], tonumber(ARGV[2]))
	return 0
end
redis.call('HSET', KEYS[1], 'mode', 'fallback', 'since', ARGV[1], 'healthy_since', '0')
redis.call('EXPIRE', KEYS[1], tonumber(ARGV[2]))
return 1
`)

	// observeHomeHealthScript: 仅在 fallback 态(key 存在)维护 healthy_since。
	// KEYS[1]=stateKey  ARGV[1]=healthy("1"/"0")  ARGV[2]=now  ARGV[3]=ttlSeconds
	// 返回更新后的 healthy_since（无 key 返回 "0"）
	stableObserveHealthScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
	return '0'
end
local hs = tonumber(redis.call('HGET', KEYS[1], 'healthy_since') or '0') or 0
if ARGV[1] == '1' then
	if hs == 0 then
		hs = tonumber(ARGV[2])
		redis.call('HSET', KEYS[1], 'healthy_since', tostring(hs))
	end
else
	if hs ~= 0 then
		hs = 0
		redis.call('HSET', KEYS[1], 'healthy_since', '0')
	end
end
redis.call('EXPIRE', KEYS[1], tonumber(ARGV[3]))
return tostring(hs)
`)

	// revertScript: 仅当 fallback 且驻留/连续健康均达标时切回 normal（删 key）。
	// KEYS[1]=stateKey  ARGV[1]=now  ARGV[2]=minDwellNanos  ARGV[3]=revertStableNanos
	// 返回 1=本次切回, 0=条件未满足/非 fallback
	stableRevertScript = redis.NewScript(`
local mode = redis.call('HGET', KEYS[1], 'mode')
if mode ~= 'fallback' then return 0 end
local since = tonumber(redis.call('HGET', KEYS[1], 'since') or '0') or 0
local hs = tonumber(redis.call('HGET', KEYS[1], 'healthy_since') or '0') or 0
local now = tonumber(ARGV[1])
if hs == 0 then return 0 end
if (now - since) < tonumber(ARGV[2]) then return 0 end
if (now - hs) < tonumber(ARGV[3]) then return 0 end
redis.call('DEL', KEYS[1])
return 1
`)
)

type stablePriorityStateStore struct {
	rdb *redis.Client
}

// NewStablePriorityStateStore 创建稳定优先状态存储。
func NewStablePriorityStateStore(rdb *redis.Client) service.StablePriorityStateStore {
	return &stablePriorityStateStore{rdb: rdb}
}

func stablePriorityKey(homeGroupID int64) string {
	return stablePriorityKeyPrefix + strconv.FormatInt(homeGroupID, 10)
}

func stablePriorityTTLSeconds() int {
	return int(service.StablePriorityStateTTL.Seconds())
}

func (s *stablePriorityStateStore) Get(ctx context.Context, homeGroupID int64) (service.StablePriorityState, error) {
	if s == nil || s.rdb == nil {
		return service.StablePriorityState{Mode: service.StablePriorityModeNormal}, nil
	}
	fields, err := s.rdb.HGetAll(ctx, stablePriorityKey(homeGroupID)).Result()
	if err != nil && err != redis.Nil {
		return service.StablePriorityState{Mode: service.StablePriorityModeNormal}, err
	}
	if len(fields) == 0 || fields["mode"] == "" {
		return service.StablePriorityState{Mode: service.StablePriorityModeNormal}, nil
	}
	since, _ := strconv.ParseInt(fields["since"], 10, 64)
	hs, _ := strconv.ParseInt(fields["healthy_since"], 10, 64)
	return service.StablePriorityState{
		Mode:                fields["mode"],
		SinceUnixNano:       since,
		HomeHealthyUnixNano: hs,
	}, nil
}

func (s *stablePriorityStateStore) TryEnterFallback(ctx context.Context, homeGroupID int64, nowUnixNano int64) (bool, error) {
	if s == nil || s.rdb == nil {
		return false, nil
	}
	res, err := stableEnterFallbackScript.Run(ctx, s.rdb,
		[]string{stablePriorityKey(homeGroupID)},
		strconv.FormatInt(nowUnixNano, 10), stablePriorityTTLSeconds()).Int64()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

func (s *stablePriorityStateStore) ObserveHomeHealth(ctx context.Context, homeGroupID int64, healthy bool, nowUnixNano int64) (int64, error) {
	if s == nil || s.rdb == nil {
		return 0, nil
	}
	healthyArg := "0"
	if healthy {
		healthyArg = "1"
	}
	res, err := stableObserveHealthScript.Run(ctx, s.rdb,
		[]string{stablePriorityKey(homeGroupID)},
		healthyArg, strconv.FormatInt(nowUnixNano, 10), stablePriorityTTLSeconds()).Text()
	if err != nil {
		return 0, err
	}
	hs, parseErr := strconv.ParseInt(res, 10, 64)
	if parseErr != nil {
		return 0, fmt.Errorf("parse healthy_since %q: %w", res, parseErr)
	}
	return hs, nil
}

func (s *stablePriorityStateStore) TryRevert(ctx context.Context, homeGroupID int64, nowUnixNano, minDwellNanos, revertStableNanos int64) (bool, error) {
	if s == nil || s.rdb == nil {
		return false, nil
	}
	res, err := stableRevertScript.Run(ctx, s.rdb,
		[]string{stablePriorityKey(homeGroupID)},
		strconv.FormatInt(nowUnixNano, 10),
		strconv.FormatInt(minDwellNanos, 10),
		strconv.FormatInt(revertStableNanos, 10)).Int64()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}
