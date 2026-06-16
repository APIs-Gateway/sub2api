//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIHomeExhausted(t *testing.T) {
	acct := &AccountSelectionResult{Account: &Account{ID: 1}}
	waitOnly := &AccountSelectionResult{WaitPlan: &AccountWaitPlan{}}
	empty := &AccountSelectionResult{}

	cases := []struct {
		name string
		sel  *AccountSelectionResult
		err  error
		want bool
	}{
		{"no-available error", nil, ErrNoAvailableAccounts, true},
		{"wrapped no-available error", nil, fmt.Errorf("%w supporting model", ErrNoAvailableAccounts), true},
		{"context canceled is not exhausted", nil, context.Canceled, false},
		{"nil selection no error", nil, nil, true},
		{"got account", acct, nil, false},
		{"wait plan only is not exhausted", waitOnly, nil, false},
		{"empty selection no wait plan", empty, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, openAIHomeExhausted(tc.sel, tc.err))
		})
	}
}

// stableChainGroupRepo 复用 stubGroupRepoForAvailable 的全套空实现，仅覆写 GetByIDLite。
type stableChainGroupRepo struct {
	*stubGroupRepoForAvailable
	groups map[int64]*Group
}

func (s *stableChainGroupRepo) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	g, ok := s.groups[id]
	if !ok {
		return nil, ErrGroupNotFound
	}
	return g, nil
}

func openaiGroup(id int64, fallback *int64) *Group {
	return &Group{ID: id, Platform: PlatformOpenAI, Status: StatusActive, StablePriorityFallbackGroupID: fallback}
}

func ptrI64(v int64) *int64 { return &v }

func newStableChainSvc(groups ...*Group) *OpenAIGatewayService {
	m := make(map[int64]*Group, len(groups))
	for _, g := range groups {
		m[g.ID] = g
	}
	return &OpenAIGatewayService{groupRepo: &stableChainGroupRepo{groups: m}}
}

func chainIDs(chain []*Group) []int64 {
	ids := make([]int64, 0, len(chain))
	for _, g := range chain {
		ids = append(ids, g.ID)
	}
	return ids
}

func TestResolveStableChain_LinearMultiTier(t *testing.T) {
	g0 := openaiGroup(1, ptrI64(2))
	g1 := openaiGroup(2, ptrI64(3))
	g2 := openaiGroup(3, nil)
	svc := newStableChainSvc(g0, g1, g2)
	require.Equal(t, []int64{2, 3}, chainIDs(svc.resolveStableChain(context.Background(), g0)))
}

func TestResolveStableChain_NoFallbackConfigured(t *testing.T) {
	g0 := openaiGroup(1, nil)
	svc := newStableChainSvc(g0)
	require.Empty(t, svc.resolveStableChain(context.Background(), g0))
}

func TestResolveStableChain_CycleGuard(t *testing.T) {
	// 1 -> 2 -> 1，环应被截断，不会无限循环。
	g0 := openaiGroup(1, ptrI64(2))
	g1 := openaiGroup(2, ptrI64(1))
	svc := newStableChainSvc(g0, g1)
	require.Equal(t, []int64{2}, chainIDs(svc.resolveStableChain(context.Background(), g0)))
}

func TestResolveStableChain_StopsAtNonOpenAI(t *testing.T) {
	g0 := openaiGroup(1, ptrI64(2))
	g1 := &Group{ID: 2, Platform: PlatformAnthropic, Status: StatusActive}
	svc := newStableChainSvc(g0, g1)
	require.Empty(t, svc.resolveStableChain(context.Background(), g0))
}

func TestResolveStableChain_SkipsInactiveButFollows(t *testing.T) {
	// 中间档位 inactive：不纳入链，但仍沿指针继续到下一个 active 档位。
	g0 := openaiGroup(1, ptrI64(2))
	g1 := &Group{ID: 2, Platform: PlatformOpenAI, Status: "inactive", StablePriorityFallbackGroupID: ptrI64(3)}
	g2 := openaiGroup(3, nil)
	svc := newStableChainSvc(g0, g1, g2)
	require.Equal(t, []int64{3}, chainIDs(svc.resolveStableChain(context.Background(), g0)))
}

func TestResolveStableChain_DepthCap(t *testing.T) {
	// 构造一条超过 StablePriorityMaxChainDepth 的长链，结果应被截断到上限。
	groups := []*Group{}
	for i := int64(1); i <= int64(StablePriorityMaxChainDepth)+3; i++ {
		groups = append(groups, openaiGroup(i, ptrI64(i+1)))
	}
	svc := newStableChainSvc(groups...)
	chain := svc.resolveStableChain(context.Background(), groups[0])
	require.Len(t, chain, StablePriorityMaxChainDepth)
}

func TestResolveStableChain_RingFromTopTier(t *testing.T) {
	// 互备环 cheap(1)->medium(2)->stable(3)->cheap(1)。
	// home=stable(3) 时应能爬到 [cheap, medium]（高档位挂掉也能路由到正常分组）。
	g1 := openaiGroup(1, ptrI64(2))
	g2 := openaiGroup(2, ptrI64(3))
	g3 := openaiGroup(3, ptrI64(1))
	svc := newStableChainSvc(g1, g2, g3)
	require.Equal(t, []int64{1, 2}, chainIDs(svc.resolveStableChain(context.Background(), g3)))
}

func TestResolveImageRateMultiplierFromFields(t *testing.T) {
	cases := []struct {
		name        string
		independent bool
		imageMult   float64
		effective   float64
		want        float64
	}{
		{"independent positive overrides effective", true, 1.5, 3.0, 1.5},
		{"independent negative means free", true, -1, 3.0, 0},
		{"not independent falls back to effective", false, 1.5, 3.0, 3.0},
		{"independent zero", true, 0, 3.0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, resolveImageRateMultiplierFromFields(tc.independent, tc.imageMult, tc.effective))
		})
	}
}

func TestAnnotateStableServed_CarriesServedImageRate(t *testing.T) {
	p1k, p2k, p4k := 0.5, 0.8, 1.2
	served := &Group{
		ID:                   2,
		Platform:             PlatformOpenAI,
		Status:               StatusActive,
		RateMultiplier:       3.0,
		ImageRateIndependent: true,
		ImageRateMultiplier:  2.5,
		ImagePrice1K:         &p1k,
		ImagePrice2K:         &p2k,
		ImagePrice4K:         &p4k,
	}
	// 兜底服务：决策须携带 served 组的费率、image 费率策略与图片基础单价（方案 Y）。
	var fallbackDec OpenAIAccountScheduleDecision
	annotateStableServed(&fallbackDec, StablePriorityModeFallback, true, served, 1)
	require.Equal(t, int64(2), fallbackDec.StableServedGroupID)
	require.Equal(t, 3.0, fallbackDec.StableServedRateMultiplier)
	require.True(t, fallbackDec.StableServedImageRateIndependent)
	require.Equal(t, 2.5, fallbackDec.StableServedImageRateMultiplier)
	require.Equal(t, &p1k, fallbackDec.StableServedImagePrice1K)
	require.Equal(t, &p2k, fallbackDec.StableServedImagePrice2K)
	require.Equal(t, &p4k, fallbackDec.StableServedImagePrice4K)

	// home（served=nil）：用 homeID，image 字段保持零值/nil，计费走 home 组策略。
	var homeDec OpenAIAccountScheduleDecision
	annotateStableServed(&homeDec, StablePriorityModeNormal, false, nil, 1)
	require.Equal(t, int64(1), homeDec.StableServedGroupID)
	require.False(t, homeDec.StableServedImageRateIndependent)
	require.Zero(t, homeDec.StableServedImageRateMultiplier)
	require.Nil(t, homeDec.StableServedImagePrice1K)
}

func TestValidateStablePriorityFallbackGroup_AllowsRing(t *testing.T) {
	ring := &groupRepoStubForFallbackCycle{groups: map[int64]*Group{
		1: openaiGroup(1, ptrI64(2)),
		2: openaiGroup(2, ptrI64(3)),
		3: openaiGroup(3, ptrI64(1)),
	}}
	svc := &adminServiceImpl{groupRepo: ring}
	// 设置 stable(3) -> cheap(1) 闭环，应被接受（允许互备环）。
	require.NoError(t, svc.validateStablePriorityFallbackGroup(context.Background(), 3, 1, PlatformOpenAI))
}

func TestValidateStablePriorityFallbackGroup_Rejects(t *testing.T) {
	repo := &groupRepoStubForFallbackCycle{groups: map[int64]*Group{
		2: {ID: 2, Platform: PlatformAnthropic, Status: StatusActive},
	}}
	svc := &adminServiceImpl{groupRepo: repo}
	// 非 openai home
	require.Error(t, svc.validateStablePriorityFallbackGroup(context.Background(), 1, 2, PlatformAnthropic))
	// 直接指向自己
	require.Error(t, svc.validateStablePriorityFallbackGroup(context.Background(), 3, 3, PlatformOpenAI))
	// 兜底链中存在非 openai 分组
	require.Error(t, svc.validateStablePriorityFallbackGroup(context.Background(), 1, 2, PlatformOpenAI))
}
