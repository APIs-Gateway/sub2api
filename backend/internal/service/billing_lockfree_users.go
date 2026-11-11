package service

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// 「计费免锁特权」名单。
//
// 特权存放在后台已有的「用户属性」里（user_attribute_definitions / user_attribute_values），
// 只有 admin 路由能改。一个用户有特权，当且仅当：
//   - 存在一条用户属性定义：key = BillingLockFreeAttributeKey、enabled = true、deleted_at IS NULL；
//   - 并且该用户在这条定义下的值，经 strings.TrimSpace 后与 "true" 大小写不敏感相等。
//
// 其他任何情况（定义不存在 / 被停用 / 被软删、值为空、"1"/"on"/"开启" 等）一律无特权。
//
// 有特权的用户：
//   - 下单前不做在途预占（不建 lease、不锁 users 行），改为一次不加锁的只读准入（余额 / 订阅卡限额，
//     见 billing_lockfree_admission.go）；exclusive 请求除外，照旧预占（见 applyInflightEstimate）；
//   - 结算命令带 BillingLockFree=true，仓储层在没有生效订阅卡时不再先锁 users 行。
//
// 没有特权的用户，行为与上游原版完全一致。
const (
	// BillingLockFreeAttributeKey 是用户属性定义里的 key。
	BillingLockFreeAttributeKey = "billing_lockfree"

	billingLockFreeRefreshInterval = 30 * time.Second
	billingLockFreeRefreshTimeout  = 5 * time.Second
	billingLockFreeLogComponent    = "service.billing_lockfree"
)

// BillingLockFreeAttributeValue 是「某个用户在 billing_lockfree 定义下的原始值」。
// 过滤条件（定义存在、enabled、未软删）由仓储的 SQL 负责；值的比较由本包的 isBillingLockFreeValue 统一负责，
// 这样「什么算 true」只有一处定义。
type BillingLockFreeAttributeValue struct {
	UserID int64
	Value  string
}

// BillingLockFreeUserLister 列出 billing_lockfree 定义（enabled 且未软删）下所有用户的原始值。
type BillingLockFreeUserLister interface {
	ListBillingLockFreeAttributeValues(ctx context.Context) ([]BillingLockFreeAttributeValue, error)
}

// IsBillingLockFreeAttributeKey 判断一个用户属性 key 是否就是特权属性（TrimSpace 后大小写不敏感）。
// 凡是会把「外部来源的值」（如第三方登录同步）写进管理员指定的属性 key 的代码，都必须先用它拦截：
// 否则外部用户能自己改出 "true" 拿到特权。
func IsBillingLockFreeAttributeKey(key string) bool {
	return strings.EqualFold(strings.TrimSpace(key), BillingLockFreeAttributeKey)
}

func isBillingLockFreeValue(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

// BillingLockFreeUsers 是有特权用户的内存名单。
//
// 请求路径（IsBillingLockFreeUser）只读内存，不查库、不等刷新：
//   - 距上次刷新超过 30 秒时，在后台起一次刷新（同一时刻最多一个），本次请求直接用当前名单；
//   - 刷新带 5 秒超时；失败时保留上一次的名单并打一条 warn（每次刷新最多一条，所以最多每 30 秒一条）；
//   - 从来没成功加载过时名单为空，即所有人无特权（fail-closed）。
//
// 刷新机制选「读时触发 + 后台 goroutine」而不是常驻 ticker：没有常驻 goroutine，
// 因此没有需要随服务关闭而回收的后台循环；每次刷新的 goroutine 最长存活 5 秒（查询超时），
// Close 还会取消它并等它退出。代价是长时间无流量后的第一个请求用的是旧名单，
// 对「授权/撤销」这种后台手工操作来说，晚一个请求生效没有影响。
//
// nil 解析器（依赖没注入）的 IsBillingLockFreeUser 恒为 false。
type BillingLockFreeUsers struct {
	lister   BillingLockFreeUserLister
	interval time.Duration
	timeout  time.Duration
	// elapsed 返回单调时钟读数；测试里可替换。
	elapsed func() time.Duration

	users atomic.Pointer[map[int64]struct{}]
	// lastRun 是最近一次刷新「开始」时的 elapsed()+1（0 表示从未刷新）。
	lastRun atomic.Int64
	running atomic.Bool

	baseCtx context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex // 保护 closed 与 wg.Add 的先后
	closed  bool
	wg      sync.WaitGroup
}

func NewBillingLockFreeUsers(lister BillingLockFreeUserLister) *BillingLockFreeUsers {
	start := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	return &BillingLockFreeUsers{
		lister:   lister,
		interval: billingLockFreeRefreshInterval,
		timeout:  billingLockFreeRefreshTimeout,
		elapsed:  func() time.Duration { return time.Since(start) },
		baseCtx:  ctx,
		cancel:   cancel,
	}
}

// IsBillingLockFreeUser 判断用户是否有特权。只读内存；名单过期时在后台触发刷新。
func (r *BillingLockFreeUsers) IsBillingLockFreeUser(userID int64) bool {
	if r == nil || userID <= 0 {
		return false
	}
	r.maybeRefresh()
	set := r.users.Load()
	if set == nil {
		return false
	}
	_, ok := (*set)[userID]
	return ok
}

// Prime 在启动时触发一次后台刷新（不阻塞），避免第一批请求都落在空名单上。
func (r *BillingLockFreeUsers) Prime() {
	if r != nil {
		r.maybeRefresh()
	}
}

func (r *BillingLockFreeUsers) stale(now int64) bool {
	last := r.lastRun.Load()
	return last == 0 || now-last >= int64(r.interval)
}

func (r *BillingLockFreeUsers) maybeRefresh() {
	if r.lister == nil {
		return
	}
	now := int64(r.elapsed()) + 1
	if !r.stale(now) {
		return
	}
	if !r.running.CompareAndSwap(false, true) {
		return
	}
	// 抢到「正在刷新」标记后再确认一次：上一轮刚好在我们读 lastRun 之后结束的话，不要紧接着再刷一轮。
	if !r.stale(now) {
		r.running.Store(false)
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		r.running.Store(false)
		return
	}
	r.wg.Add(1)
	r.mu.Unlock()
	r.lastRun.Store(now)
	go func() {
		defer r.wg.Done()
		defer r.running.Store(false)
		_ = r.Refresh(r.baseCtx)
	}()
}

// Refresh 同步刷新一次名单（带超时）。失败时保留旧名单、打一条 warn 并返回错误。
func (r *BillingLockFreeUsers) Refresh(ctx context.Context) error {
	if r == nil || r.lister == nil {
		return nil
	}
	// 同步调用（启动预加载、测试）也算一次刷新：此后 interval 内读路径不会再触发后台刷新。
	r.lastRun.Store(int64(r.elapsed()) + 1)
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	rows, err := r.lister.ListBillingLockFreeAttributeValues(ctx)
	if err != nil {
		logger.LegacyPrintf(billingLockFreeLogComponent, "Warning: refresh billing_lockfree user list failed, keeping previous list: %v", err)
		return err
	}
	set := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		if row.UserID > 0 && isBillingLockFreeValue(row.Value) {
			set[row.UserID] = struct{}{}
		}
	}
	if prev := r.users.Load(); prev == nil || len(*prev) != len(set) {
		logger.LegacyPrintf(billingLockFreeLogComponent, "billing_lockfree user list loaded: %d user(s)", len(set))
	}
	r.users.Store(&set)
	return nil
}

// Close 停止后续刷新，取消并等待正在进行的刷新退出。可重复调用。
func (r *BillingLockFreeUsers) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.cancel()
	r.wg.Wait()
}

// ProvideBillingLockFreeUsers 创建名单解析器并注入两个网关（与 ProvideStagedGroupPolicy 同一模式）：
// 两个网关共用同一个实例。必须在开始处理请求之前完成，所以放在依赖注入里。
func ProvideBillingLockFreeUsers(lister BillingLockFreeUserLister, gateway *GatewayService, openAIGateway *OpenAIGatewayService) *BillingLockFreeUsers {
	r := NewBillingLockFreeUsers(lister)
	gateway.SetBillingLockFreeUsers(r)
	openAIGateway.SetBillingLockFreeUsers(r)
	r.Prime()
	return r
}
