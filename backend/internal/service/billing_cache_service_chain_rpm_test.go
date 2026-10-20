//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// userRPMDecrCacheStub 在 userRPMCacheStub 之上实现可选的 UserGroupRPMSlotCounter：
// 带槽递增复用 userRPMCacheStub 的计数序列，并返回当前的 slot；退回时记录收到的槽。
type userRPMDecrCacheStub struct {
	*userRPMCacheStub
	decrCalls     int32
	slotIncrCalls int32
	decrErr       error

	mu        sync.Mutex
	slot      int64   // 下一次带槽递增落在的分钟槽，测试可修改以模拟跨分钟
	decrSlots []int64 // 每次退回收到的槽
}

func (s *userRPMDecrCacheStub) IncrementUserGroupRPMSlot(ctx context.Context, userID, groupID int64) (int, int64, error) {
	atomic.AddInt32(&s.slotIncrCalls, 1)
	count, err := s.userRPMCacheStub.IncrementUserGroupRPM(ctx, userID, groupID)
	s.mu.Lock()
	slot := s.slot
	s.mu.Unlock()
	return count, slot, err
}

func (s *userRPMDecrCacheStub) DecrementUserGroupRPMSlot(_ context.Context, _, _ int64, slot int64) error {
	atomic.AddInt32(&s.decrCalls, 1)
	s.mu.Lock()
	s.decrSlots = append(s.decrSlots, slot)
	s.mu.Unlock()
	return s.decrErr
}

func (s *userRPMDecrCacheStub) setSlot(slot int64) {
	s.mu.Lock()
	s.slot = slot
	s.mu.Unlock()
}

func (s *userRPMDecrCacheStub) releasedSlots() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.decrSlots...)
}

func newDecrStub(counts ...int) *userRPMDecrCacheStub {
	return &userRPMDecrCacheStub{userRPMCacheStub: &userRPMCacheStub{userGroupCounts: counts}}
}

// ---- 无链回归：checkRPM 的行为被断言锁住 ----

// 无链 Key 仍走原 checkRPM：分组超限时用户层不计数（「放在最后以避免为注定失败的请求增加计数」），
// 超限的那一次递增也不会被退回。
func TestChainRPM_NoChainCheckRPMUnchanged_GroupExceededDoesNotTouchUserLayer(t *testing.T) {
	cache := newDecrStub(1, 2, 3)
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})
	user := &User{ID: 1, RPMLimit: 100}
	group := &Group{ID: 10, RPMLimit: 2}

	require.NoError(t, svc.checkRPM(context.Background(), user, group))
	require.NoError(t, svc.checkRPM(context.Background(), user, group))
	require.ErrorIs(t, svc.checkRPM(context.Background(), user, group), ErrGroupRPMExceeded)

	require.EqualValues(t, 3, atomic.LoadInt32(&cache.userGroupCalls))
	require.EqualValues(t, 2, atomic.LoadInt32(&cache.userCalls), "分组超限时不得再给用户层计数")
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.decrCalls), "无链路径从不退回计数")
}

// 无链：快照里的 override 优先，不查库；快照为空才查库。
func TestChainRPM_NoChainCheckRPMUnchanged_SnapshotOverrideBeforeDB(t *testing.T) {
	dbOverride := 100
	snapOverride := 1
	repo := &rpmOverrideRepoStub{override: &dbOverride}
	cache := newDecrStub(2)
	svc := newBillingServiceForRPM(t, cache, repo)
	user := &User{ID: 1, UserGroupRPMOverride: &snapOverride}
	group := &Group{ID: 10, RPMLimit: 100}

	require.ErrorIs(t, svc.checkRPM(context.Background(), user, group), ErrGroupRPMExceeded)
	require.EqualValues(t, 0, atomic.LoadInt32(&repo.calls), "快照有 override 时不应查库")

	// 快照为空：查库。
	user2 := &User{ID: 1}
	cache2 := newDecrStub(1)
	repo2 := &rpmOverrideRepoStub{override: &snapOverride}
	svc2 := newBillingServiceForRPM(t, cache2, repo2)
	require.NoError(t, svc2.checkRPM(context.Background(), user2, group))
	require.EqualValues(t, 1, atomic.LoadInt32(&repo2.calls))
}

// 无链：group 为 nil 时只检查用户层。
func TestChainRPM_NoChainCheckRPMUnchanged_NilGroupOnlyUserLayer(t *testing.T) {
	cache := &userRPMCacheStub{userCounts: []int{1, 2}}
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})
	user := &User{ID: 1, RPMLimit: 1}

	require.NoError(t, svc.checkRPM(context.Background(), user, nil))
	require.ErrorIs(t, svc.checkRPM(context.Background(), user, nil), ErrUserRPMExceeded)
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userGroupCalls))
}

// 无链：两层都不限时一次计数都不发生。
func TestChainRPM_NoChainCheckRPMUnchanged_NoLimitsNoCounting(t *testing.T) {
	cache := &userRPMCacheStub{}
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})
	require.NoError(t, svc.checkRPM(context.Background(), &User{ID: 1}, &Group{ID: 10}))
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userGroupCalls))
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userCalls))
}

// 无链：Redis 故障 fail-open。
func TestChainRPM_NoChainCheckRPMUnchanged_RedisErrorFailsOpen(t *testing.T) {
	cache := &userRPMCacheStub{userGroupErr: errors.New("redis down"), userErr: errors.New("redis down")}
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})
	require.NoError(t, svc.checkRPM(context.Background(), &User{ID: 1, RPMLimit: 1}, &Group{ID: 10, RPMLimit: 1}))
}

// ---- 用户层 ----

func TestChainRPM_UserLayerOnly(t *testing.T) {
	cache := &userRPMCacheStub{userCounts: []int{1, 2}}
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})
	user := &User{ID: 1, RPMLimit: 1, UserGroupRPMOverride: intPtr(1)}

	require.NoError(t, svc.checkUserRPMLayer(context.Background(), user))
	require.ErrorIs(t, svc.checkUserRPMLayer(context.Background(), user), ErrUserRPMExceeded)
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userGroupCalls), "用户层不碰分组计数")

	// 没有用户级限额：不计数。
	cache2 := &userRPMCacheStub{}
	svc2 := newBillingServiceForRPM(t, cache2, &rpmOverrideRepoStub{})
	require.NoError(t, svc2.checkUserRPMLayer(context.Background(), &User{ID: 1}))
	require.EqualValues(t, 0, atomic.LoadInt32(&cache2.userCalls))

	// nil 安全。
	var nilSvc *BillingCacheService
	require.NoError(t, nilSvc.checkUserRPMLayer(context.Background(), user))
}

// ---- 分组层（每跳） ----

// override 一律按 (user, hop 分组) 从 DB 读，不使用快照里属于原分组的覆盖值。
func TestChainRPM_HopGroupLayerReadsOverrideFromDBNotSnapshot(t *testing.T) {
	dbOverride := 1
	repo := &rpmOverrideRepoStub{override: &dbOverride}
	cache := newDecrStub(2)
	svc := newBillingServiceForRPM(t, cache, repo)
	// 快照里的 override=100 属于原分组，不能用于 hop 分组。
	user := &User{ID: 1, UserGroupRPMOverride: intPtr(100)}
	group := &Group{ID: 20, RPMLimit: 1000}

	ticket, err := svc.CheckGroupRPMForHop(context.Background(), user, group)
	require.ErrorIs(t, err, ErrGroupRPMExceeded)
	require.NotNil(t, ticket, "超限时递增已经发生，应返回 ticket")
	require.EqualValues(t, 1, atomic.LoadInt32(&repo.calls))
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.userGroupCalls))
}

// hop 分组的 group.rpm_limit 对回退流量真正计数并限流（B2：不能只读）。
func TestChainRPM_HopGroupLayerCountsAndLimitsFallbackTraffic(t *testing.T) {
	cache := newDecrStub(1, 2, 3)
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})
	user := &User{ID: 1}
	group := &Group{ID: 20, RPMLimit: 2}

	for i := 0; i < 2; i++ {
		ticket, err := svc.CheckGroupRPMForHop(context.Background(), user, group)
		require.NoError(t, err)
		require.NotNil(t, ticket)
	}
	ticket, err := svc.CheckGroupRPMForHop(context.Background(), user, group)
	require.ErrorIs(t, err, ErrGroupRPMExceeded)
	require.NotNil(t, ticket)
	require.EqualValues(t, 3, atomic.LoadInt32(&cache.userGroupCalls), "每次都对 hop 分组计数")
}

// 分组层不触碰用户层计数（用户层只在入口做一次）。
func TestChainRPM_HopGroupLayerDoesNotTouchUserCounter(t *testing.T) {
	cache := newDecrStub(1)
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})
	_, err := svc.CheckGroupRPMForHop(context.Background(), &User{ID: 1, RPMLimit: 1}, &Group{ID: 20, RPMLimit: 5})
	require.NoError(t, err)
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userCalls))
}

// override=0 免检；没有任何限额；Redis 故障：都不产生 ticket。
func TestChainRPM_HopGroupLayerNoTicketCases(t *testing.T) {
	zero := 0
	cache := newDecrStub()
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{override: &zero})
	ticket, err := svc.CheckGroupRPMForHop(context.Background(), &User{ID: 1}, &Group{ID: 20, RPMLimit: 1})
	require.NoError(t, err)
	require.Nil(t, ticket, "override=0 免检，不计数")
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userGroupCalls))

	cache2 := newDecrStub()
	svc2 := newBillingServiceForRPM(t, cache2, &rpmOverrideRepoStub{})
	ticket, err = svc2.CheckGroupRPMForHop(context.Background(), &User{ID: 1}, &Group{ID: 20})
	require.NoError(t, err)
	require.Nil(t, ticket, "没有分组限额不计数")
	require.EqualValues(t, 0, atomic.LoadInt32(&cache2.userGroupCalls))

	cache3 := &userRPMDecrCacheStub{userRPMCacheStub: &userRPMCacheStub{userGroupErr: errors.New("redis down")}}
	svc3 := newBillingServiceForRPM(t, cache3, &rpmOverrideRepoStub{})
	ticket, err = svc3.CheckGroupRPMForHop(context.Background(), &User{ID: 1}, &Group{ID: 20, RPMLimit: 1})
	require.NoError(t, err, "Redis 故障 fail-open")
	require.Nil(t, ticket)

	// nil 安全。
	var nilSvc *BillingCacheService
	ticket, err = nilSvc.CheckGroupRPMForHop(context.Background(), &User{ID: 1}, &Group{ID: 20, RPMLimit: 1})
	require.NoError(t, err)
	require.Nil(t, ticket)
	ticket, err = svc.CheckGroupRPMForHop(context.Background(), nil, &Group{ID: 20, RPMLimit: 1})
	require.NoError(t, err)
	require.Nil(t, ticket)
}

// ticket.Release：只退回一次；缓存不支持退回时静默；nil ticket 安全。
func TestChainRPM_TicketReleaseOnceAndSafe(t *testing.T) {
	cache := newDecrStub(1)
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})
	ticket, err := svc.CheckGroupRPMForHop(context.Background(), &User{ID: 1}, &Group{ID: 20, RPMLimit: 5})
	require.NoError(t, err)
	require.NotNil(t, ticket)

	ticket.Release(context.Background())
	ticket.Release(context.Background())
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.decrCalls), "Release 幂等，只退回一次")

	// 已取消的 ctx 也要退回（客户端断开不应丢掉退回）。
	cache2 := newDecrStub(1)
	svc2 := newBillingServiceForRPM(t, cache2, &rpmOverrideRepoStub{})
	ticket2, _ := svc2.CheckGroupRPMForHop(context.Background(), &User{ID: 1}, &Group{ID: 20, RPMLimit: 5})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	ticket2.Release(cancelled)
	require.EqualValues(t, 1, atomic.LoadInt32(&cache2.decrCalls))

	// 缓存不支持退回：静默。
	plain := &userRPMCacheStub{userGroupCounts: []int{1}}
	svc3 := newBillingServiceForRPM(t, plain, &rpmOverrideRepoStub{})
	ticket3, _ := svc3.CheckGroupRPMForHop(context.Background(), &User{ID: 1}, &Group{ID: 20, RPMLimit: 5})
	require.NotNil(t, ticket3)
	require.NotPanics(t, func() { ticket3.Release(context.Background()) })

	// 退回失败：不 panic、不影响调用方。
	cache4 := newDecrStub(1)
	cache4.decrErr = errors.New("redis down")
	svc4 := newBillingServiceForRPM(t, cache4, &rpmOverrideRepoStub{})
	ticket4, _ := svc4.CheckGroupRPMForHop(context.Background(), &User{ID: 1}, &Group{ID: 20, RPMLimit: 5})
	require.NotPanics(t, func() { ticket4.Release(context.Background()) })

	var nilTicket *GroupRPMTicket
	require.NotPanics(t, func() { nilTicket.Release(context.Background()) })
}

// S5：退回对准递增时的分钟槽，而不是退回时的当前分钟；退回不再依赖「当前分钟」。
func TestChainRPM_TicketReleaseTargetsIncrementSlot(t *testing.T) {
	cache := newDecrStub(1)
	cache.setSlot(28_333_334)
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})

	ticket, err := svc.CheckGroupRPMForHop(context.Background(), &User{ID: 1}, &Group{ID: 20, RPMLimit: 5})
	require.NoError(t, err)
	require.NotNil(t, ticket)

	// 之后时间走到了下一分钟：新的递增会落在新槽，但这张 ticket 的退回必须仍对准旧槽。
	cache.setSlot(28_333_335)
	ticket.Release(context.Background())
	require.Equal(t, []int64{28_333_334}, cache.releasedSlots())
}

// S5：首跳在入口计数时同样记下分钟槽。
func TestChainRPM_EntryTicketReleaseTargetsIncrementSlot(t *testing.T) {
	cache := newDecrStub(1)
	cache.setSlot(100)
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})

	ticket, err := svc.checkChainEntryRPM(context.Background(), &User{ID: 1}, &Group{ID: 10, RPMLimit: 5})
	require.NoError(t, err)
	require.NotNil(t, ticket)
	cache.setSlot(101)
	ticket.Release(context.Background())
	require.Equal(t, []int64{100}, cache.releasedSlots())
}

// S5：无链路径（checkRPM）仍只用原来的 IncrementUserGroupRPM，不走带槽的版本；有链路径才走带槽版本。
func TestChainRPM_OnlyChainPathUsesSlotIncrement(t *testing.T) {
	cache := newDecrStub(1, 1)
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})
	group := &Group{ID: 10, RPMLimit: 5}

	require.NoError(t, svc.checkRPM(context.Background(), &User{ID: 1}, group))
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.userGroupCalls))
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.slotIncrCalls), "无链路径不得使用带槽递增")

	_, err := svc.CheckGroupRPMForHop(context.Background(), &User{ID: 1}, group)
	require.NoError(t, err)
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.slotIncrCalls))
}

// S5：带槽递增失败（Redis 故障）时 fail-open，不产生 ticket，也就不会有退回。
func TestChainRPM_SlotIncrementErrorFailsOpenWithoutTicket(t *testing.T) {
	cache := &userRPMDecrCacheStub{userRPMCacheStub: &userRPMCacheStub{userGroupErr: errors.New("redis down")}}
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})
	ticket, err := svc.CheckGroupRPMForHop(context.Background(), &User{ID: 1}, &Group{ID: 20, RPMLimit: 1})
	require.NoError(t, err)
	require.Nil(t, ticket)
}

// ---- 入口 RPM（S1）：首跳分组层先于用户层，与无链 checkRPM 的顺序一致 ----

// 首跳分组层超限：用户层不计数（与无链一致），ticket 随错误返回但不退回。
func TestChainRPM_EntryFirstHopExceededDoesNotCountUserLayer(t *testing.T) {
	cache := newDecrStub(2)
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})
	user := &User{ID: 1, RPMLimit: 100}

	ticket, err := svc.checkChainEntryRPM(context.Background(), user, &Group{ID: 10, RPMLimit: 1})
	require.ErrorIs(t, err, ErrGroupRPMExceeded)
	require.NotNil(t, ticket)
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.userGroupCalls))
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userCalls), "首跳分组超限时用户层不得计数")
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.decrCalls), "首跳超限保持计数，与无链一致")
}

// 首跳通过、用户层超限：返回 ErrUserRPMExceeded，首跳 ticket 一并返回，分组层计数保持（与无链一致）。
func TestChainRPM_EntryUserLayerExceededKeepsGroupCount(t *testing.T) {
	cache := &userRPMDecrCacheStub{userRPMCacheStub: &userRPMCacheStub{userGroupCounts: []int{1}, userCounts: []int{2}}}
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})

	ticket, err := svc.checkChainEntryRPM(context.Background(), &User{ID: 1, RPMLimit: 1}, &Group{ID: 10, RPMLimit: 5})
	require.ErrorIs(t, err, ErrUserRPMExceeded)
	require.NotNil(t, ticket)
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.userGroupCalls))
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.userCalls))
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.decrCalls))
}

// 全部通过：首跳计一次、用户层计一次，返回首跳 ticket，用于第 0 跳结束后的退回。
func TestChainRPM_EntryPassesReturnsFirstHopTicket(t *testing.T) {
	cache := newDecrStub(1)
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})
	ticket, err := svc.checkChainEntryRPM(context.Background(), &User{ID: 1, RPMLimit: 5}, &Group{ID: 10, RPMLimit: 5})
	require.NoError(t, err)
	require.NotNil(t, ticket)
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.userGroupCalls))
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.userCalls))
	ticket.Release(context.Background())
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.decrCalls))
}

// firstHop 为 nil 或没有分组限额：只做用户层，ticket 为 nil。
func TestChainRPM_EntryNoFirstHopLimitOnlyUserLayer(t *testing.T) {
	cache := newDecrStub()
	svc := newBillingServiceForRPM(t, cache, &rpmOverrideRepoStub{})
	user := &User{ID: 1, RPMLimit: 5}

	ticket, err := svc.checkChainEntryRPM(context.Background(), user, nil)
	require.NoError(t, err)
	require.Nil(t, ticket)
	ticket, err = svc.checkChainEntryRPM(context.Background(), user, &Group{ID: 10})
	require.NoError(t, err)
	require.Nil(t, ticket)
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userGroupCalls))
	require.EqualValues(t, 2, atomic.LoadInt32(&cache.userCalls))
}

// 与无链 checkRPM 的计数序列逐项一致（首跳 = 主分组、无 admin head 时）。
func TestChainRPM_EntryCountingMatchesNoChainCheckRPM(t *testing.T) {
	group := &Group{ID: 10, RPMLimit: 2}
	user := &User{ID: 1, RPMLimit: 100}

	noChain := newDecrStub(1, 2, 3)
	svcA := newBillingServiceForRPM(t, noChain, &rpmOverrideRepoStub{})
	chain := newDecrStub(1, 2, 3)
	svcB := newBillingServiceForRPM(t, chain, &rpmOverrideRepoStub{})

	for i := 0; i < 3; i++ {
		errA := svcA.checkRPM(context.Background(), user, group)
		_, errB := svcB.checkChainEntryRPM(context.Background(), user, group)
		require.Equal(t, errA, errB, "第 %d 次", i)
	}
	require.Equal(t, atomic.LoadInt32(&noChain.userGroupCalls), atomic.LoadInt32(&chain.userGroupCalls))
	require.Equal(t, atomic.LoadInt32(&noChain.userCalls), atomic.LoadInt32(&chain.userCalls))
}

// ---- BK-3：简易模式不检查任何 RPM ----

func TestChainRPM_SimpleModeSkipsAllRPMChecks(t *testing.T) {
	cache := newDecrStub(9, 9)
	svc := NewBillingCacheService(nil, nil, nil, nil, cache, &rpmOverrideRepoStub{}, &config.Config{RunMode: config.RunModeSimple}, nil, nil)
	t.Cleanup(svc.Stop)
	user := &User{ID: 1, RPMLimit: 1}
	group := &Group{ID: 10, RPMLimit: 1}

	ticket, err := svc.CheckGroupRPMForHop(context.Background(), user, group)
	require.NoError(t, err, "simple 模式下分组层不得返回 429")
	require.Nil(t, ticket)
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userGroupCalls))

	ticket, err = svc.CheckBillingEligibilityForChain(context.Background(), user, nil, group, nil, "")
	require.NoError(t, err)
	require.Nil(t, ticket)
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userGroupCalls))
	require.EqualValues(t, 0, atomic.LoadInt32(&cache.userCalls))
}
