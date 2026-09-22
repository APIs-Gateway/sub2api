package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const stickySessionPrefix = "sticky_session:"
const openAIResponsesSessionWindowPrefix = "openai_responses_session_window:"

type gatewayCache struct {
	rdb *redis.Client
}

func NewGatewayCache(rdb *redis.Client) service.GatewayCache {
	return &gatewayCache{rdb: rdb}
}

// buildSessionKey 构建 session key，包含 groupID 实现分组隔离
// 格式: sticky_session:{groupID}:{sessionHash}
func buildSessionKey(groupID int64, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s", stickySessionPrefix, groupID, sessionHash)
}

func buildOpenAIResponsesSessionWindowKey(groupID int64, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s", openAIResponsesSessionWindowPrefix, groupID, sessionHash)
}

func (c *gatewayCache) GetSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Get(ctx, key).Int64()
}

func (c *gatewayCache) SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Set(ctx, key, accountID, ttl).Err()
}

func (c *gatewayCache) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Expire(ctx, key, ttl).Err()
}

// DeleteSessionAccountID 删除粘性会话与账号的绑定关系。
// 当检测到绑定的账号不可用（如状态错误、禁用、不可调度等）时调用，
// 以便下次请求能够重新选择可用账号。
//
// DeleteSessionAccountID removes the sticky session binding for the given session.
// Called when the bound account becomes unavailable (e.g., error status, disabled,
// or unschedulable), allowing subsequent requests to select a new available account.
func (c *gatewayCache) DeleteSessionAccountID(ctx context.Context, groupID int64, sessionHash string) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Del(ctx, key).Err()
}

var claimOpenAIResponsesSessionWindowScript = redis.NewScript(`
local previous = redis.call('GET', KEYS[1])
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return previous
`)

var compareAndRefreshOpenAIResponsesSessionWindowScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current == false or current ~= ARGV[1] then return 0 end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)

var compareAndDeleteOpenAIResponsesSessionWindowScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current == false or current ~= ARGV[1] then return 0 end
redis.call('DEL', KEYS[1])
return 1
`)

// ClaimOpenAIResponsesSessionWindow atomically publishes the current ingress
// owner and returns the previous one. The owner itself is intentionally opaque
// to Redis; conditional refresh/delete below prevents stale cleanup races.
func (c *gatewayCache) ClaimOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, owner []byte, ttl time.Duration) ([]byte, error) {
	if c == nil || c.rdb == nil { return nil, errors.New("gateway cache unavailable") }
	if len(owner) == 0 || strings.TrimSpace(sessionHash) == "" || ttl <= 0 { return nil, errors.New("invalid OpenAI Responses session-window claim") }
	value, err := claimOpenAIResponsesSessionWindowScript.Run(ctx, c.rdb, []string{buildOpenAIResponsesSessionWindowKey(groupID, sessionHash)}, owner, ttl.Milliseconds()).Result()
	if errors.Is(err, redis.Nil) || value == nil { return nil, nil }
	if err != nil { return nil, err }
	switch v := value.(type) {
	case string: return []byte(v), nil
	case []byte: return append([]byte(nil), v...), nil
	default: return nil, fmt.Errorf("unexpected OpenAI Responses session-window claim result %T", value)
	}
}

func (c *gatewayCache) CompareAndRefreshOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, expected []byte, ttl time.Duration) (bool, error) {
	if c == nil || c.rdb == nil { return false, errors.New("gateway cache unavailable") }
	if len(expected) == 0 || strings.TrimSpace(sessionHash) == "" || ttl <= 0 { return false, errors.New("invalid OpenAI Responses session-window refresh") }
	n, err := compareAndRefreshOpenAIResponsesSessionWindowScript.Run(ctx, c.rdb, []string{buildOpenAIResponsesSessionWindowKey(groupID, sessionHash)}, expected, ttl.Milliseconds()).Int()
	return n == 1, err
}

func (c *gatewayCache) CompareAndDeleteOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, expected []byte) (bool, error) {
	if c == nil || c.rdb == nil { return false, errors.New("gateway cache unavailable") }
	if len(expected) == 0 || strings.TrimSpace(sessionHash) == "" { return false, errors.New("invalid OpenAI Responses session-window delete") }
	n, err := compareAndDeleteOpenAIResponsesSessionWindowScript.Run(ctx, c.rdb, []string{buildOpenAIResponsesSessionWindowKey(groupID, sessionHash)}, expected).Int()
	return n == 1, err
}

var _ service.OpenAIWSSessionPreemptionCache = (*gatewayCache)(nil)

// Compile-time assertion: gatewayCache must implement CyberSessionBlockStore.
var _ service.CyberSessionBlockStore = (*gatewayCache)(nil)

const cyberSessionBlockPrefix = "cyber_session_block:"

// SetCyberSessionBlocked 把被 cyber_policy 命中的会话写入屏蔽表（TTL 自动过期）。
// 存储值 "1" 作为存在标记（IsCyberSessionBlocked 只检查 key 是否存在，不读值）。
func (c *gatewayCache) SetCyberSessionBlocked(ctx context.Context, key string, ttl time.Duration) error {
	return c.rdb.Set(ctx, cyberSessionBlockPrefix+key, "1", ttl).Err()
}

// IsCyberSessionBlocked 查询会话是否在屏蔽表中。
func (c *gatewayCache) IsCyberSessionBlocked(ctx context.Context, key string) (bool, error) {
	n, err := c.rdb.Exists(ctx, cyberSessionBlockPrefix+key).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
