package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// 回退链熔断的 Redis 实现（设计 3.6）。接口与纯逻辑在 service/group_chain_breaker.go。
//
// Redis 键（花括号里是 hash tag，保证三个键落在同一个 slot，Lua 才能同时操作）：
//
//	fbchain:cb:{platform:groupID:family}          Hash：state / fail_count / window_start / opened_at
//	fbchain:cb:{platform:groupID:family}:users    Set：当前窗口内贡献过失败的不同用户
//	fbchain:cb:{platform:groupID:family}:probe    String：half-open 探测令牌（SET NX PX，限制同一时刻只有一个探测）
//
// 探测令牌的形式是 "<opened_at>:<随机串>"，opened_at 是发出令牌时这一轮 half-open 对应的 Hash 字段。
// 成功 / 失败按「轮次」认领（令牌的 opened_at 等于当前 Hash 里的 opened_at），不要求 probe 键仍然存在：
// 探测成功要等整个响应写完才上报，可能远超 10 秒的令牌有效期。重新 open 会改写 opened_at，上一轮的探测随之失效。
//
// 时间以调用方传入的毫秒时间戳为准（ARGV），不在 Lua 里取 TIME：测试里可以注入时钟，
// 多实例之间的时钟偏差（NTP 量级，毫秒）相对 30 秒窗口可以忽略。

const (
	groupChainBreakerKeyPrefix = "fbchain:cb:"
	// groupChainBreakerRedisTimeout 单次 Redis 调用的上限：熔断是尽力而为的优化，Redis 变慢时宁可 fail-open。
	groupChainBreakerRedisTimeout = 100 * time.Millisecond
)

func groupChainBreakerKeys(k service.BreakerKey) (hash, users, probe string) {
	base := fmt.Sprintf("%s{%s:%d:%s}", groupChainBreakerKeyPrefix, k.Platform, k.GroupID, k.Family)
	return base, base + ":users", base + ":probe"
}

// 返回 {码, 令牌}：0 跳过（open 未到期）；1 放行（closed）；2 放行（被选为探测，带令牌）；3 跳过（half-open，已有探测）。
//
// 原子性：整段在一次 EVAL 里完成，open → half-open 的迁移与探测令牌的 SET NX 同属一个脚本，
// 同一时刻只有一个调用者能拿到令牌。
var groupChainBreakerAdmitScript = redis.NewScript(`
local function num(v) return tonumber(v) or 0 end
local state = redis.call('HGET', KEYS[1], 'state')
if (not state) or state == 'closed' then
  return {1, ''}
end
local now = num(ARGV[1])
local open_ttl = num(ARGV[2])
local probe_ttl = num(ARGV[3])
local token = ARGV[4]
local state_ttl = num(ARGV[5])
if state == 'open' then
  local opened_at = num(redis.call('HGET', KEYS[1], 'opened_at'))
  if now < opened_at + open_ttl then
    return {0, ''}
  end
  redis.call('HSET', KEYS[1], 'state', 'half')
  redis.call('PEXPIRE', KEYS[1], state_ttl)
end
local round = redis.call('HGET', KEYS[1], 'opened_at') or ''
local full = round .. ':' .. token
if redis.call('SET', KEYS[2], full, 'NX', 'PX', probe_ttl) then
  redis.call('PEXPIRE', KEYS[1], state_ttl)
  return {2, full}
end
return {3, ''}
`)

// 令牌是否属于当前这一轮 half-open：令牌以 "<当前 opened_at>:" 开头。
const groupChainBreakerRoundFunc = `
local function in_round(hash_key, token)
  local opened = redis.call('HGET', hash_key, 'opened_at')
  if (not opened) or token == '' then
    return false
  end
  return string.sub(token, 1, string.len(opened) + 1) == (opened .. ':')
end
`

// 返回 0 无状态变化；1 本次失败使熔断 open；2 本轮探测失败，重新 open。
//
// 计数窗口是滚动重置的固定窗口：窗口起点 window_start 之后 window 毫秒内累计 fail_count 与不同用户；
// 窗口过期后的第一次失败重置计数与用户集合并开新窗口。open 与 half-open 期间不累计。
var groupChainBreakerFailScript = redis.NewScript(groupChainBreakerRoundFunc + `
local function num(v) return tonumber(v) or 0 end
local now = num(ARGV[1])
local window = num(ARGV[2])
local threshold = num(ARGV[3])
local open_ttl = num(ARGV[4])
local min_users = num(ARGV[5])
local user = ARGV[6]
local token = ARGV[7]
local state_ttl = num(ARGV[8])
-- KEYS[1]=hash KEYS[2]=users KEYS[3]=probe
local state = redis.call('HGET', KEYS[1], 'state')
if state == 'half' then
  if in_round(KEYS[1], token) then
    redis.call('HSET', KEYS[1], 'state', 'open', 'opened_at', now, 'fail_count', 0)
    redis.call('HDEL', KEYS[1], 'window_start')
    redis.call('DEL', KEYS[2], KEYS[3])
    redis.call('PEXPIRE', KEYS[1], state_ttl)
    return 2
  end
  return 0
end
if state == 'open' then
  return 0
end
local ws = num(redis.call('HGET', KEYS[1], 'window_start'))
if ws == 0 or now - ws >= window then
  redis.call('HSET', KEYS[1], 'window_start', now, 'fail_count', 0)
  redis.call('DEL', KEYS[2])
end
local count = redis.call('HINCRBY', KEYS[1], 'fail_count', 1)
redis.call('SADD', KEYS[2], user)
redis.call('PEXPIRE', KEYS[2], window)
local users = redis.call('SCARD', KEYS[2])
if count >= threshold and users >= min_users then
  redis.call('HSET', KEYS[1], 'state', 'open', 'opened_at', now, 'fail_count', 0)
  redis.call('HDEL', KEYS[1], 'window_start')
  redis.call('DEL', KEYS[2])
  redis.call('PEXPIRE', KEYS[1], state_ttl)
  return 1
end
redis.call('PEXPIRE', KEYS[1], state_ttl)
return 0
`)

// 返回 1 表示 half-open → closed。令牌不属于当前这一轮（上一轮的探测）时不改状态；
// 属于本轮的探测即使令牌（probe 键）已过期，也能关闭熔断。
var groupChainBreakerSuccessScript = redis.NewScript(groupChainBreakerRoundFunc + `
if redis.call('HGET', KEYS[1], 'state') == 'half' and in_round(KEYS[1], ARGV[1]) then
  redis.call('HSET', KEYS[1], 'state', 'closed', 'fail_count', 0)
  redis.call('HDEL', KEYS[1], 'window_start', 'opened_at')
  redis.call('DEL', KEYS[2], KEYS[3])
  return 1
end
return 0
`)

var groupChainBreakerReleaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

// groupChainBreaker 是 service.GroupChainBreakerGate 的 Redis 实现。
type groupChainBreaker struct {
	rdb      redis.Scripter
	now      func() time.Time
	lastWarn atomic.Int64
}

var _ service.GroupChainBreakerGate = (*groupChainBreaker)(nil)

// NewGroupChainBreaker 构造熔断器。rdb 为 nil 时所有调用都 fail-open。
func NewGroupChainBreaker(rdb *redis.Client) service.GroupChainBreakerGate {
	return newGroupChainBreaker(rdb)
}

func newGroupChainBreaker(rdb *redis.Client) *groupChainBreaker {
	b := &groupChainBreaker{now: time.Now}
	if rdb != nil {
		b.rdb = rdb
	}
	return b
}

func (b *groupChainBreaker) available() bool {
	return b != nil && b.rdb != nil
}

func (b *groupChainBreaker) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *groupChainBreaker) nowMS() int64 {
	return b.clock().UnixMilli()
}

// callCtx 给单次 Redis 调用加短超时，并且不随请求 ctx 取消：
// 客户端在响应写完后断开，不应该让 RecordProbeSuccess / 5xx 确认这类上报丢失。
func callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), groupChainBreakerRedisTimeout)
}

// Admit 见 service.GroupChainBreakerGate。Redis 出错或键无效时返回 Allowed（fail-open）。
func (b *groupChainBreaker) Admit(ctx context.Context, key service.BreakerKey, cfg service.BreakerConfig) service.BreakerAdmission {
	closed := service.BreakerAdmission{Allowed: true, State: service.BreakerStateClosed}
	if !b.available() || !key.Valid() {
		return closed
	}
	hash, _, probe := groupChainBreakerKeys(key)
	cctx, cancel := callCtx(ctx)
	defer cancel()
	res, err := groupChainBreakerAdmitScript.Run(cctx, b.rdb, []string{hash, probe},
		b.nowMS(), cfg.OpenTTL.Milliseconds(), cfg.ProbeTTL.Milliseconds(), newBreakerProbeNonce(), cfg.StateTTLMS()).Slice()
	if err != nil || len(res) != 2 {
		if err == nil {
			err = fmt.Errorf("unexpected admit reply: %v", res)
		}
		b.warn("admit", err)
		closed.FailOpen = true
		return closed
	}
	code, _ := res[0].(int64)
	token, _ := res[1].(string)
	switch code {
	case 0:
		return service.BreakerAdmission{Allowed: false, State: service.BreakerStateOpen}
	case 2:
		if token == "" {
			closed.FailOpen = true
			return closed
		}
		return service.BreakerAdmission{Allowed: true, ProbeToken: token, State: service.BreakerStateHalfOpen}
	case 3:
		return service.BreakerAdmission{Allowed: false, State: service.BreakerStateHalfOpen}
	default:
		return closed
	}
}

// RecordFailure 见 service.GroupChainBreakerGate。userID <= 0 时不计入（无法去重，宁可少计）。
func (b *groupChainBreaker) RecordFailure(ctx context.Context, key service.BreakerKey, cfg service.BreakerConfig, userID int64, probeToken string) {
	if !b.available() || !key.Valid() {
		return
	}
	if userID <= 0 && probeToken == "" {
		return
	}
	hash, users, probe := groupChainBreakerKeys(key)
	cctx, cancel := callCtx(ctx)
	defer cancel()
	if _, err := groupChainBreakerFailScript.Run(cctx, b.rdb, []string{hash, users, probe},
		b.nowMS(), cfg.Window.Milliseconds(), cfg.Threshold, cfg.OpenTTL.Milliseconds(),
		cfg.MinDistinctUsers, strconv.FormatInt(userID, 10), probeToken, cfg.StateTTLMS()).Int(); err != nil {
		b.warn("record_failure", err)
	}
}

// RecordProbeSuccess 见 service.GroupChainBreakerGate。
func (b *groupChainBreaker) RecordProbeSuccess(ctx context.Context, key service.BreakerKey, _ service.BreakerConfig, probeToken string) {
	if !b.available() || !key.Valid() || probeToken == "" {
		return
	}
	hash, users, probe := groupChainBreakerKeys(key)
	cctx, cancel := callCtx(ctx)
	defer cancel()
	if _, err := groupChainBreakerSuccessScript.Run(cctx, b.rdb, []string{hash, users, probe}, probeToken).Int(); err != nil {
		b.warn("probe_success", err)
	}
}

// ReleaseProbe 见 service.GroupChainBreakerGate。
func (b *groupChainBreaker) ReleaseProbe(ctx context.Context, key service.BreakerKey, probeToken string) {
	if !b.available() || !key.Valid() || probeToken == "" {
		return
	}
	_, _, probe := groupChainBreakerKeys(key)
	cctx, cancel := callCtx(ctx)
	defer cancel()
	if _, err := groupChainBreakerReleaseScript.Run(cctx, b.rdb, []string{probe}, probeToken).Int(); err != nil {
		b.warn("release_probe", err)
	}
}

// warn 限频记录 Redis 错误，避免 Redis 故障时每个请求刷一条日志。
func (b *groupChainBreaker) warn(op string, err error) {
	now := b.clock().UnixNano()
	last := b.lastWarn.Load()
	if now-last < int64(10*time.Second) || !b.lastWarn.CompareAndSwap(last, now) {
		return
	}
	slog.Warn("group chain breaker redis error, failing open", "op", op, "error", err)
}

// newBreakerProbeNonce 生成令牌里的随机部分（完整令牌由 Lua 拼上当前轮次的 opened_at）。
func newBreakerProbeNonce() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(buf[:])
}
