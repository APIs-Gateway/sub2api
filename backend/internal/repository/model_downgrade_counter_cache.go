package repository

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

var modelDowngradeCountScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return count
`)

type modelDowngradeCounterCache struct {
	rdb *redis.Client
}

func NewModelDowngradeCounterCache(rdb *redis.Client) service.ModelDowngradeCounterCache {
	return &modelDowngradeCounterCache{rdb: rdb}
}

func modelDowngradeCounterKey(accountID int64, model string) string {
	return fmt.Sprintf("model_downgrade:count:%d:%x", accountID, sha256.Sum256([]byte(model)))
}

func (c *modelDowngradeCounterCache) IncrementModelDowngradeCount(ctx context.Context, accountID int64, model string, windowMinutes int) (int64, error) {
	return modelDowngradeCountScript.Run(ctx, c.rdb, []string{modelDowngradeCounterKey(accountID, model)}, windowMinutes*60).Int64()
}

func (c *modelDowngradeCounterCache) ResetModelDowngradeCount(ctx context.Context, accountID int64, model string) error {
	return c.rdb.Del(ctx, modelDowngradeCounterKey(accountID, model)).Err()
}
