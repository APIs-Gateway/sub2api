//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type breakerTestEnv struct {
	mr  *miniredis.Miniredis
	rdb *redis.Client
	now time.Time
	cfg BreakerConfig
}

func newBreakerTestEnv(t *testing.T) *breakerTestEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &breakerTestEnv{
		mr:  mr,
		rdb: rdb,
		now: time.Unix(1_800_000_000, 0),
		cfg: BreakerConfigFromSettings(DefaultGroupFallbackSettings()),
	}
}

// breaker 返回一个共享同一 Redis 与同一时钟的熔断器实例（可以多次调用模拟不同 Key / 不同实例）。
func (e *breakerTestEnv) breaker() *GroupChainBreaker {
	b := NewGroupChainBreaker(e.rdb)
	b.now = func() time.Time { return e.now }
	return b
}

func (e *breakerTestEnv) advance(d time.Duration) {
	e.now = e.now.Add(d)
	e.mr.FastForward(d)
}

var testBreakerKey = BreakerKey{Platform: PlatformOpenAI, GroupID: 7, Family: "gpt-6-sol"}

func failN(b *GroupChainBreaker, cfg BreakerConfig, key BreakerKey, userID int64, n int) {
	for i := 0; i < n; i++ {
		b.RecordFailure(context.Background(), key, cfg, userID, "")
	}
}

func TestBreakerConfigFromSettings_UsesPR1Settings(t *testing.T) {
	cfg := BreakerConfigFromSettings(GroupFallbackSettings{
		BreakerWindowMS: 1000, BreakerThreshold: 4, BreakerOpenTTLMS: 2000, BreakerMinDistinctUser: 2,
	})
	require.Equal(t, time.Second, cfg.Window)
	require.Equal(t, 2*time.Second, cfg.OpenTTL)
	require.Equal(t, 4, cfg.Threshold)
	require.Equal(t, 2, cfg.MinDistinctUsers)

	def := BreakerConfigFromSettings(DefaultGroupFallbackSettings())
	require.Equal(t, 30*time.Second, def.Window)
	require.Equal(t, 30*time.Second, def.OpenTTL)
	require.Equal(t, 5, def.Threshold)
	require.Equal(t, 3, def.MinDistinctUsers)
}

func TestBreaker_SingleUserCannotOpen(t *testing.T) {
	e := newBreakerTestEnv(t)
	b := e.breaker()
	failN(b, e.cfg, testBreakerKey, 1, 200)

	adm := b.Admit(context.Background(), testBreakerKey, e.cfg)
	require.True(t, adm.Allowed)
	require.Equal(t, BreakerStateClosed, adm.State)
}

func TestBreaker_ThreeDistinctUsersOpen(t *testing.T) {
	e := newBreakerTestEnv(t)
	b := e.breaker()
	ctx := context.Background()

	failN(b, e.cfg, testBreakerKey, 1, 3)
	failN(b, e.cfg, testBreakerKey, 2, 1)
	require.True(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed, "4 次失败、2 个用户：不应打开")

	failN(b, e.cfg, testBreakerKey, 3, 1) // 第 5 次失败、第 3 个用户
	adm := b.Admit(ctx, testBreakerKey, e.cfg)
	require.False(t, adm.Allowed)
	require.Equal(t, BreakerStateOpen, adm.State)
}

func TestBreaker_ThresholdMetButUsersNotEnoughThenThirdUserOpens(t *testing.T) {
	e := newBreakerTestEnv(t)
	b := e.breaker()
	ctx := context.Background()

	failN(b, e.cfg, testBreakerKey, 1, 6)
	failN(b, e.cfg, testBreakerKey, 2, 6)
	require.True(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed, "12 次失败但只有 2 个用户")

	failN(b, e.cfg, testBreakerKey, 3, 1)
	require.False(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed)
}

func TestBreaker_UnknownUserIsNotCounted(t *testing.T) {
	e := newBreakerTestEnv(t)
	b := e.breaker()
	failN(b, e.cfg, testBreakerKey, 0, 50)
	failN(b, e.cfg, testBreakerKey, -1, 50)
	require.True(t, b.Admit(context.Background(), testBreakerKey, e.cfg).Allowed)
	hash, _, _ := testBreakerKey.redisKeys()
	require.False(t, e.mr.Exists(hash))
}

func TestBreaker_FailuresOutsideWindowDoNotAccumulate(t *testing.T) {
	e := newBreakerTestEnv(t)
	b := e.breaker()
	ctx := context.Background()

	failN(b, e.cfg, testBreakerKey, 1, 2)
	failN(b, e.cfg, testBreakerKey, 2, 1)
	failN(b, e.cfg, testBreakerKey, 3, 1)
	e.advance(e.cfg.Window + time.Second)

	failN(b, e.cfg, testBreakerKey, 4, 1) // 新窗口的第 1 次失败
	require.True(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed)

	// 新窗口内凑够 5 次、3 个用户才会打开
	failN(b, e.cfg, testBreakerKey, 5, 2)
	failN(b, e.cfg, testBreakerKey, 6, 2)
	require.False(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed)
}

func TestBreaker_FullCycleClosedOpenHalfOpenClosed(t *testing.T) {
	e := newBreakerTestEnv(t)
	b := e.breaker()
	ctx := context.Background()

	// closed
	adm := b.Admit(ctx, testBreakerKey, e.cfg)
	require.True(t, adm.Allowed)
	require.Empty(t, adm.ProbeToken)

	// closed -> open
	failN(b, e.cfg, testBreakerKey, 1, 3)
	failN(b, e.cfg, testBreakerKey, 2, 1)
	failN(b, e.cfg, testBreakerKey, 3, 1)
	adm = b.Admit(ctx, testBreakerKey, e.cfg)
	require.False(t, adm.Allowed)
	require.Equal(t, BreakerStateOpen, adm.State)

	// open 期间的失败不延长、不改变状态
	e.advance(10 * time.Second)
	failN(b, e.cfg, testBreakerKey, 4, 10)
	require.False(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed)

	// open 到期 -> half-open：恰好一个探测被放行
	e.advance(e.cfg.OpenTTL - 10*time.Second)
	probe := b.Admit(ctx, testBreakerKey, e.cfg)
	require.True(t, probe.Allowed)
	require.NotEmpty(t, probe.ProbeToken)
	require.Equal(t, BreakerStateHalfOpen, probe.State)

	other := b.Admit(ctx, testBreakerKey, e.cfg)
	require.False(t, other.Allowed, "探测在途时其它请求被跳过")
	require.Equal(t, BreakerStateHalfOpen, other.State)

	// 无令牌的失败不能改变 half-open 状态
	b.RecordFailure(ctx, testBreakerKey, e.cfg, 9, "")
	require.False(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed)

	// 错误令牌的成功不起作用
	b.RecordProbeSuccess(ctx, testBreakerKey, e.cfg, "not-the-token")
	require.False(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed)

	// 探测成功 -> closed
	b.RecordProbeSuccess(ctx, testBreakerKey, e.cfg, probe.ProbeToken)
	adm = b.Admit(ctx, testBreakerKey, e.cfg)
	require.True(t, adm.Allowed)
	require.Equal(t, BreakerStateClosed, adm.State)
	require.Empty(t, adm.ProbeToken)

	// closed 之后计数从零开始：单个用户仍打不开
	failN(b, e.cfg, testBreakerKey, 1, 20)
	require.True(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed)
}

func openBreaker(t *testing.T, e *breakerTestEnv, b *GroupChainBreaker, key BreakerKey) {
	t.Helper()
	failN(b, e.cfg, key, 1, 3)
	failN(b, e.cfg, key, 2, 1)
	failN(b, e.cfg, key, 3, 1)
	require.False(t, b.Admit(context.Background(), key, e.cfg).Allowed)
}

func TestBreaker_ProbeFailureReopens(t *testing.T) {
	e := newBreakerTestEnv(t)
	b := e.breaker()
	ctx := context.Background()
	openBreaker(t, e, b, testBreakerKey)

	e.advance(e.cfg.OpenTTL)
	probe := b.Admit(ctx, testBreakerKey, e.cfg)
	require.True(t, probe.Allowed)
	require.NotEmpty(t, probe.ProbeToken)

	b.RecordFailure(ctx, testBreakerKey, e.cfg, 1, probe.ProbeToken)
	adm := b.Admit(ctx, testBreakerKey, e.cfg)
	require.False(t, adm.Allowed)
	require.Equal(t, BreakerStateOpen, adm.State)

	// 重新 open 后再等一个 open_ttl 才会再次探测
	e.advance(e.cfg.OpenTTL - time.Second)
	require.False(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed)
	e.advance(time.Second)
	require.True(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed)
}

func TestBreaker_ReleasedProbeAllowsNextProbe(t *testing.T) {
	e := newBreakerTestEnv(t)
	b := e.breaker()
	ctx := context.Background()
	openBreaker(t, e, b, testBreakerKey)

	e.advance(e.cfg.OpenTTL)
	probe := b.Admit(ctx, testBreakerKey, e.cfg)
	require.NotEmpty(t, probe.ProbeToken)
	require.False(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed)

	b.ReleaseProbe(ctx, testBreakerKey, "wrong-token")
	require.False(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed, "错误令牌不能释放别人的探测")

	b.ReleaseProbe(ctx, testBreakerKey, probe.ProbeToken)
	again := b.Admit(ctx, testBreakerKey, e.cfg)
	require.True(t, again.Allowed)
	require.NotEmpty(t, again.ProbeToken)
	require.NotEqual(t, probe.ProbeToken, again.ProbeToken)
}

func TestBreaker_AbandonedProbeExpires(t *testing.T) {
	e := newBreakerTestEnv(t)
	b := e.breaker()
	ctx := context.Background()
	openBreaker(t, e, b, testBreakerKey)

	e.advance(e.cfg.OpenTTL)
	probe := b.Admit(ctx, testBreakerKey, e.cfg)
	require.NotEmpty(t, probe.ProbeToken)

	e.advance(e.cfg.ProbeTTL + time.Second) // 探测令牌 10 秒后过期
	next := b.Admit(ctx, testBreakerKey, e.cfg)
	require.True(t, next.Allowed)
	require.NotEmpty(t, next.ProbeToken)

	// 过期的旧探测事后上报成功，不能把状态改成 closed
	b.RecordProbeSuccess(ctx, testBreakerKey, e.cfg, probe.ProbeToken)
	require.False(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed)
}

func TestBreaker_SharedAcrossKeysAndInstances(t *testing.T) {
	e := newBreakerTestEnv(t)
	instanceA := e.breaker() // 例如 Key A 的请求落在实例 A
	instanceB := e.breaker() // Key B 的请求落在实例 B
	ctx := context.Background()

	failN(instanceA, e.cfg, testBreakerKey, 1, 3)
	failN(instanceB, e.cfg, testBreakerKey, 2, 1)
	failN(instanceA, e.cfg, testBreakerKey, 3, 1)

	require.False(t, instanceA.Admit(ctx, testBreakerKey, e.cfg).Allowed)
	require.False(t, instanceB.Admit(ctx, testBreakerKey, e.cfg).Allowed)
}

func TestBreaker_IsolatedByFamilyAndGroup(t *testing.T) {
	e := newBreakerTestEnv(t)
	b := e.breaker()
	ctx := context.Background()
	openBreaker(t, e, b, testBreakerKey)

	otherFamily := BreakerKey{Platform: PlatformOpenAI, GroupID: 7, Family: "gpt-5.4"}
	otherGroup := BreakerKey{Platform: PlatformOpenAI, GroupID: 8, Family: "gpt-6-sol"}
	require.True(t, b.Admit(ctx, otherFamily, e.cfg).Allowed)
	require.True(t, b.Admit(ctx, otherGroup, e.cfg).Allowed)
	require.False(t, b.Admit(ctx, testBreakerKey, e.cfg).Allowed)

	// 另一个族的失败也不会叠加到已有族上
	failN(b, e.cfg, otherFamily, 1, 4)
	failN(b, e.cfg, otherFamily, 2, 1)
	require.True(t, b.Admit(ctx, otherFamily, e.cfg).Allowed, "2 个用户的失败不足以打开")
}

func TestBreaker_InvalidKeyIsIgnored(t *testing.T) {
	e := newBreakerTestEnv(t)
	b := e.breaker()
	ctx := context.Background()
	for _, key := range []BreakerKey{
		{Platform: PlatformOpenAI, GroupID: 7}, // 模型族未命中：Family 为空，不进熔断
		{Platform: PlatformOpenAI, GroupID: 0, Family: "x"},
		{GroupID: 7, Family: "x"},
	} {
		failN(b, e.cfg, key, 1, 10)
		failN(b, e.cfg, key, 2, 10)
		failN(b, e.cfg, key, 3, 10)
		require.True(t, b.Admit(ctx, key, e.cfg).Allowed)
	}
	require.Empty(t, e.mr.Keys())
}

func TestBreaker_FailOpenWhenRedisDown(t *testing.T) {
	e := newBreakerTestEnv(t)
	b := e.breaker()
	ctx := context.Background()
	openBreaker(t, e, b, testBreakerKey)

	e.mr.Close()

	adm := b.Admit(ctx, testBreakerKey, e.cfg)
	require.True(t, adm.Allowed, "Redis 故障必须按 closed 放行")
	require.True(t, adm.FailOpen)
	require.Empty(t, adm.ProbeToken)
	require.NotPanics(t, func() {
		b.RecordFailure(ctx, testBreakerKey, e.cfg, 1, "")
		b.RecordProbeSuccess(ctx, testBreakerKey, e.cfg, "t")
		b.ReleaseProbe(ctx, testBreakerKey, "t")
	})
}

func TestBreaker_NilClientFailsOpen(t *testing.T) {
	b := NewGroupChainBreaker(nil)
	adm := b.Admit(context.Background(), testBreakerKey, BreakerConfigFromSettings(DefaultGroupFallbackSettings()))
	require.True(t, adm.Allowed)

	var nilBreaker *GroupChainBreaker
	require.True(t, nilBreaker.Admit(context.Background(), testBreakerKey, BreakerConfig{}).Allowed)
}

func TestBreaker_RedisKeysShareHashTag(t *testing.T) {
	hash, users, probe := testBreakerKey.redisKeys()
	require.Equal(t, "fbchain:cb:{openai:7:gpt-6-sol}", hash)
	require.Equal(t, hash+":users", users)
	require.Equal(t, hash+":probe", probe)
}
