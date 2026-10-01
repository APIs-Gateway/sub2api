package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// 回退链熔断（设计 3.6）：按「分组 × 模型族」的三态熔断，状态在 Redis，跨 Key、跨实例共享。
//
// 只记录「账号侧 / 上游侧的容量失败」；哪些失败算数由调用方（ClassifyHopFailure 与 runner）决定，
// 这里只负责计数、状态机与原子性。熔断是尽力而为的优化：Redis 任何错误都按 closed 处理（fail-open）。
//
// Redis 键（花括号里是 hash tag，保证三个键落在同一个 slot，Lua 才能同时操作）：
//
//	fbchain:cb:{platform:groupID:family}          Hash：state / fail_count / window_start / opened_at
//	fbchain:cb:{platform:groupID:family}:users    Set：当前窗口内贡献过失败的不同用户
//	fbchain:cb:{platform:groupID:family}:probe    String：half-open 探测令牌（SET NX PX）
//
// 时间以调用方传入的毫秒时间戳为准（ARGV），不在 Lua 里取 TIME：测试里可以注入时钟，
// 多实例之间的时钟偏差（NTP 量级，毫秒）相对 30 秒窗口可以忽略。

const (
	groupChainBreakerKeyPrefix = "fbchain:cb:"
	// groupChainBreakerProbeTTL half-open 探测令牌有效期（设计 3.6：10 秒）。
	groupChainBreakerProbeTTL = 10 * time.Second
)

// BreakerState 熔断三态。
type BreakerState string

const (
	BreakerStateClosed   BreakerState = "closed"
	BreakerStateOpen     BreakerState = "open"
	BreakerStateHalfOpen BreakerState = "half_open"
)

// BreakerKey 熔断的作用域：分组 × 模型族。平台包含在键里只为避免跨平台撞键（链本身不跨平台）。
type BreakerKey struct {
	Platform string
	GroupID  int64
	Family   string
}

func (k BreakerKey) valid() bool {
	return k.Platform != "" && k.GroupID > 0 && k.Family != ""
}

func (k BreakerKey) redisKeys() (hash, users, probe string) {
	base := fmt.Sprintf("%s{%s:%d:%s}", groupChainBreakerKeyPrefix, k.Platform, k.GroupID, k.Family)
	return base, base + ":users", base + ":probe"
}

// BreakerConfig 熔断参数，由 GroupFallbackSettings 转换而来（入口每请求读一次设置，不在每次调用里读库）。
type BreakerConfig struct {
	Window           time.Duration
	OpenTTL          time.Duration
	ProbeTTL         time.Duration
	Threshold        int
	MinDistinctUsers int
}

// BreakerConfigFromSettings 取 breaker.window_ms / threshold / open_ttl_ms / min_distinct_users。
func BreakerConfigFromSettings(s GroupFallbackSettings) BreakerConfig {
	def := DefaultGroupFallbackSettings()
	cfg := BreakerConfig{
		Window:           time.Duration(s.BreakerWindowMS) * time.Millisecond,
		OpenTTL:          time.Duration(s.BreakerOpenTTLMS) * time.Millisecond,
		ProbeTTL:         groupChainBreakerProbeTTL,
		Threshold:        s.BreakerThreshold,
		MinDistinctUsers: s.BreakerMinDistinctUser,
	}
	if cfg.Window <= 0 {
		cfg.Window = time.Duration(def.BreakerWindowMS) * time.Millisecond
	}
	if cfg.OpenTTL <= 0 {
		cfg.OpenTTL = time.Duration(def.BreakerOpenTTLMS) * time.Millisecond
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = def.BreakerThreshold
	}
	if cfg.MinDistinctUsers <= 0 {
		cfg.MinDistinctUsers = def.BreakerMinDistinctUser
	}
	return cfg
}

// stateTTLMS 是 Hash 键自身的兜底过期：比任何一段状态持续时间都长，避免遗留键永久存在。
func (c BreakerConfig) stateTTLMS() int64 {
	longest := c.Window
	if c.OpenTTL > longest {
		longest = c.OpenTTL
	}
	if c.ProbeTTL > longest {
		longest = c.ProbeTTL
	}
	return (2*longest + time.Minute).Milliseconds()
}

// BreakerAdmission 是 Admit 的结果。
type BreakerAdmission struct {
	// Allowed 为 false 表示该跳应被跳过（open，或 half-open 且已有探测在途）。
	Allowed bool
	// ProbeToken 非空表示本请求被选为 half-open 探测，结束时必须用同一令牌上报成功、失败或释放。
	ProbeToken string
	State      BreakerState
	// FailOpen 为 true 表示 Redis 出错、按 closed 放行（用于指标）。
	FailOpen bool
}

// GroupChainBreakerGate 是 runner 依赖的熔断接口，便于测试替换。
type GroupChainBreakerGate interface {
	// Admit 在尝试一个非最后一跳之前调用。
	Admit(ctx context.Context, key BreakerKey, cfg BreakerConfig) BreakerAdmission
	// RecordFailure 记录一次容量失败。probeToken 非空时表示这是 half-open 探测的失败（重新 open）。
	RecordFailure(ctx context.Context, key BreakerKey, cfg BreakerConfig, userID int64, probeToken string)
	// RecordProbeSuccess 探测成功：half-open → closed。
	RecordProbeSuccess(ctx context.Context, key BreakerKey, cfg BreakerConfig, probeToken string)
	// ReleaseProbe 探测请求没有得出结论（非容量失败、被跳过、客户端断开等），归还令牌让下一个请求探测。
	ReleaseProbe(ctx context.Context, key BreakerKey, probeToken string)
}

// GroupChainBreaker 是 Redis 实现。
type GroupChainBreaker struct {
	rdb      redis.Scripter
	now      func() time.Time
	lastWarn atomic.Int64
}

var _ GroupChainBreakerGate = (*GroupChainBreaker)(nil)

// NewGroupChainBreaker 构造熔断器。rdb 为 nil 时所有调用都 fail-open。
func NewGroupChainBreaker(rdb *redis.Client) *GroupChainBreaker {
	b := &GroupChainBreaker{now: time.Now}
	if rdb != nil {
		b.rdb = rdb
	}
	return b
}

// 返回码：0 跳过（open 未到期）；1 放行（closed）；2 放行（被选为探测）；3 跳过（half-open，已有探测）。
//
// 原子性：整段在一次 EVAL 里完成，open → half-open 的迁移与探测令牌的 SET NX 同属一个脚本，
// 同一时刻只有一个调用者能拿到令牌。
var groupChainBreakerAdmitScript = redis.NewScript(`
local function num(v) return tonumber(v) or 0 end
local state = redis.call('HGET', KEYS[1], 'state')
if (not state) or state == 'closed' then
  return 1
end
local now = num(ARGV[1])
local open_ttl = num(ARGV[2])
local probe_ttl = num(ARGV[3])
local token = ARGV[4]
local state_ttl = num(ARGV[5])
if state == 'open' then
  local opened_at = num(redis.call('HGET', KEYS[1], 'opened_at'))
  if now < opened_at + open_ttl then
    return 0
  end
  redis.call('HSET', KEYS[1], 'state', 'half')
  redis.call('PEXPIRE', KEYS[1], state_ttl)
end
if redis.call('SET', KEYS[2], token, 'NX', 'PX', probe_ttl) then
  return 2
end
return 3
`)

// 返回码：0 无状态变化；1 本次失败使熔断 open；2 探测失败，重新 open。
//
// 计数窗口是滚动重置的固定窗口：窗口起点 window_start 之后 window 毫秒内累计 fail_count 与不同用户；
// 窗口过期后的第一次失败重置计数与用户集合并开新窗口。open 与 half-open 期间不累计。
var groupChainBreakerFailScript = redis.NewScript(`
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
  if token ~= '' and redis.call('GET', KEYS[3]) == token then
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

// 返回 1 表示 half-open → closed。令牌不匹配（已过期或被别的探测取代）时不改状态。
var groupChainBreakerSuccessScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'state') == 'half' and redis.call('GET', KEYS[3]) == ARGV[1] then
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

func (b *GroupChainBreaker) available() bool {
	return b != nil && b.rdb != nil
}

func (b *GroupChainBreaker) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *GroupChainBreaker) nowMS() int64 {
	return b.clock().UnixMilli()
}

// Admit 见 GroupChainBreakerGate。Redis 出错或键无效时返回 Allowed（fail-open）。
func (b *GroupChainBreaker) Admit(ctx context.Context, key BreakerKey, cfg BreakerConfig) BreakerAdmission {
	closed := BreakerAdmission{Allowed: true, State: BreakerStateClosed}
	if !b.available() || !key.valid() {
		return closed
	}
	hash, _, probe := key.redisKeys()
	token := newBreakerProbeToken()
	code, err := groupChainBreakerAdmitScript.Run(ctx, b.rdb, []string{hash, probe},
		b.nowMS(), cfg.OpenTTL.Milliseconds(), cfg.ProbeTTL.Milliseconds(), token, cfg.stateTTLMS()).Int()
	if err != nil {
		b.warn("admit", err)
		closed.FailOpen = true
		return closed
	}
	switch code {
	case 0:
		return BreakerAdmission{Allowed: false, State: BreakerStateOpen}
	case 2:
		return BreakerAdmission{Allowed: true, ProbeToken: token, State: BreakerStateHalfOpen}
	case 3:
		return BreakerAdmission{Allowed: false, State: BreakerStateHalfOpen}
	default:
		return closed
	}
}

// RecordFailure 见 GroupChainBreakerGate。userID <= 0 时不计入（无法去重，宁可少计）。
func (b *GroupChainBreaker) RecordFailure(ctx context.Context, key BreakerKey, cfg BreakerConfig, userID int64, probeToken string) {
	if !b.available() || !key.valid() {
		return
	}
	if userID <= 0 && probeToken == "" {
		return
	}
	hash, users, probe := key.redisKeys()
	user := fmt.Sprintf("%d", userID)
	if _, err := groupChainBreakerFailScript.Run(ctx, b.rdb, []string{hash, users, probe},
		b.nowMS(), cfg.Window.Milliseconds(), cfg.Threshold, cfg.OpenTTL.Milliseconds(),
		cfg.MinDistinctUsers, user, probeToken, cfg.stateTTLMS()).Int(); err != nil {
		b.warn("record_failure", err)
	}
}

// RecordProbeSuccess 见 GroupChainBreakerGate。
func (b *GroupChainBreaker) RecordProbeSuccess(ctx context.Context, key BreakerKey, _ BreakerConfig, probeToken string) {
	if !b.available() || !key.valid() || probeToken == "" {
		return
	}
	hash, users, probe := key.redisKeys()
	if _, err := groupChainBreakerSuccessScript.Run(ctx, b.rdb, []string{hash, users, probe}, probeToken).Int(); err != nil {
		b.warn("probe_success", err)
	}
}

// ReleaseProbe 见 GroupChainBreakerGate。
func (b *GroupChainBreaker) ReleaseProbe(ctx context.Context, key BreakerKey, probeToken string) {
	if !b.available() || !key.valid() || probeToken == "" {
		return
	}
	_, _, probe := key.redisKeys()
	if _, err := groupChainBreakerReleaseScript.Run(ctx, b.rdb, []string{probe}, probeToken).Int(); err != nil {
		b.warn("release_probe", err)
	}
}

// warn 限频记录 Redis 错误，避免 Redis 故障时每个请求刷一条日志。
func (b *GroupChainBreaker) warn(op string, err error) {
	now := b.clock().UnixNano()
	last := b.lastWarn.Load()
	if now-last < int64(10*time.Second) || !b.lastWarn.CompareAndSwap(last, now) {
		return
	}
	slog.Warn("group chain breaker redis error, failing open", "op", op, "error", err)
}

func newBreakerProbeToken() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}
