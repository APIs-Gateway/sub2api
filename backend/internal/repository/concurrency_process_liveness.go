package repository

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	processHeartbeatKey           = "concurrency:process:heartbeat"
	processHeartbeatWindowSeconds = 60
)

var _ service.ProcessLivenessCache = (*concurrencyCache)(nil)

// Keep expired heartbeat records long enough to identify abandoned slots.
// Missing records are unknown processes, including older versions during an upgrade.
// A hostname cannot identify a dead process: multiple processes may share one host.
var processHeartbeatScript = redis.NewScript(`
	redis.replicate_commands()
	local now = tonumber(redis.call('TIME')[1])
	local retention = tonumber(ARGV[2])
	redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', '(' .. (now - retention))
	redis.call('ZADD', KEYS[1], now, ARGV[1])
	redis.call('EXPIRE', KEYS[1], retention)
	return 1
`)

func (c *concurrencyCache) HeartbeatProcess(ctx context.Context, requestPrefix string) error {
	if requestPrefix == "" {
		return nil
	}
	retention := c.slotTTLSeconds + processHeartbeatWindowSeconds
	return processHeartbeatScript.Run(ctx, c.rdb, []string{processHeartbeatKey}, requestPrefix, retention).Err()
}

// Only an expired, known heartbeat is evidence for startup removal. Unknown or
// newly registered peers survive even if they were absent from this snapshot.
func (c *concurrencyCache) staleProcessPrefixes(ctx context.Context, activePrefix string) ([]string, error) {
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME for process liveness: %w", err)
	}
	prefixes, err := c.rdb.ZRangeByScore(ctx, processHeartbeatKey, &redis.ZRangeBy{
		Min: "-inf",
		Max: "(" + strconv.FormatInt(now.Unix()-processHeartbeatWindowSeconds, 10),
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("read stale process heartbeats: %w", err)
	}
	stale := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		if prefix != "" && prefix != activePrefix {
			stale = append(stale, prefix)
		}
	}
	return stale, nil
}
