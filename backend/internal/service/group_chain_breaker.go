package service

import (
	"context"
	"time"
)

// 回退链熔断（设计 3.6）：按「分组 × 模型族」的三态熔断，状态跨 Key、跨实例共享。
//
// 本文件只放接口、类型与纯逻辑（service 层不得依赖 redis，见 .golangci.yml 的 depguard）；
// Redis + Lua 实现在 internal/repository/group_chain_breaker_cache.go。
//
// 只记录「账号侧 / 上游侧的容量失败」；哪些失败算数由调用方（ClassifyHopFailure 与 runner）决定，
// 实现只负责计数、状态机与原子性。熔断是尽力而为的优化：存储出错一律按 closed 处理（fail-open）。

const (
	// groupChainBreakerProbeTTL half-open 同一时刻只放行一个探测的间隔（设计 3.6：10 秒）。
	// 它只限制「多久可以再发一个探测」，不限制探测成功多久之内必须上报：探测成功按 half-open 轮次认领。
	groupChainBreakerProbeTTL = 10 * time.Second
)

// BreakerState 熔断三态。
type BreakerState string

const (
	BreakerStateClosed   BreakerState = "closed"
	BreakerStateOpen     BreakerState = "open"
	BreakerStateHalfOpen BreakerState = "half_open" // Redis 里的 state 字段存的是 "half"
)

// BreakerKey 熔断的作用域：分组 × 模型族。平台包含在键里只为避免跨平台撞键（链本身不跨平台）。
type BreakerKey struct {
	Platform string
	GroupID  int64
	Family   string
}

// Valid 为 false（例如模型族未命中）时不进熔断。
func (k BreakerKey) Valid() bool {
	return k.Platform != "" && k.GroupID > 0 && k.Family != ""
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

// StateTTLMS 是状态键自身的兜底过期：比任何一段状态持续时间都长，避免遗留键永久存在。
func (c BreakerConfig) StateTTLMS() int64 {
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
	// RecordFailure 记录一次容量失败。probeToken 非空时表示这是 half-open 探测的失败（本轮探测失败则重新 open）。
	RecordFailure(ctx context.Context, key BreakerKey, cfg BreakerConfig, userID int64, probeToken string)
	// RecordProbeSuccess 探测成功：half-open → closed。
	// 令牌按 half-open 轮次认领：只要探测属于当前这一轮（令牌里带着该轮的 opened_at），即使令牌已过期
	// （成功要等整个响应写完才上报，可能远超探测令牌的 10 秒），也能关闭熔断；上一轮的探测不会误改状态。
	RecordProbeSuccess(ctx context.Context, key BreakerKey, cfg BreakerConfig, probeToken string)
	// ReleaseProbe 探测请求没有得出结论（非容量失败、被跳过、客户端断开等），归还令牌让下一个请求探测。
	ReleaseProbe(ctx context.Context, key BreakerKey, probeToken string)
}
