//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 计价口径单测（spec §3）：earning floor / 换套餐 ceil / clawback floor / 提现精确，禁 math.Round。

func TestComputeEarnPoints(t *testing.T) {
	cases := []struct {
		name              string
		amount, rate, peg float64
		want              int64
	}{
		{"标准 100×5%/0.01=500", 100, 5, 0.01, 500},
		{"小额 10×5%/0.01=50", 10, 5, 0.01, 50},
		{"floor 截断 9.99×5%/0.01=49.95→49", 9.99, 5, 0.01, 49},
		{"rate=0 → 0", 100, 0, 0.01, 0},
		{"amount=0 → 0", 0, 5, 0.01, 0},
		{"peg=0 → 0（防御）", 100, 5, 0, 0},
		{"100%返 100/0.01=10000", 100, 100, 0.01, 10000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, ComputeEarnPoints(c.amount, c.rate, c.peg))
		})
	}
}

func TestComputePlanPoints(t *testing.T) {
	cases := []struct {
		name       string
		price, peg float64
		want       int64
	}{
		{"整除 1/0.01=100", 1, 0.01, 100},
		{"ceil 进位 1.005/0.01=100.5→101", 1.005, 0.01, 101},
		{"ceil 任意余数 0.001/0.01=0.1→1", 0.001, 0.01, 1},
		{"price=0 → 0", 0, 0.01, 0},
		{"peg=0 → 0（防御）", 1, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, ComputePlanPoints(c.price, c.peg))
		})
	}
}

func TestComputeClawbackPoints(t *testing.T) {
	cases := []struct {
		name                         string
		earned                       int64
		refundAmount, originalAmount float64
		want                         int64
	}{
		{"全额退 → 精确反向", 500, 100, 100, 500},
		{"超额退（>原额）→ 仍 = earned", 500, 120, 100, 500},
		{"半额退 500×0.5=250", 500, 50, 100, 250},
		{"33% 退 500×0.33=165", 500, 33, 100, 165},
		{"0.5 边界 earned=1 ratio=0.5 → floor(0.5)=0（不得多撤）", 1, 50, 100, 0},
		{"0.5 边界 earned=3 ratio=0.5 → floor(1.5)=1", 3, 50, 100, 1},
		{"earned=0 → 0", 0, 50, 100, 0},
		{"refund=0 → 0", 500, 0, 100, 0},
		{"original=0 → 0（防御）", 500, 50, 0, 0},
		{"极小比例 floor 到 0", 500, 0.1, 100, 0}, // 500*0.1/100 = 0.5 → floor 0
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, ComputeClawbackPoints(c.earned, c.refundAmount, c.originalAmount))
		})
	}
}

func TestPointsToBalance(t *testing.T) {
	require.InDelta(t, 5.0, PointsToBalance(500, 0.01), 1e-9)
	require.InDelta(t, 0.0, PointsToBalance(0, 0.01), 1e-9)
	require.InDelta(t, 0.0, PointsToBalance(500, 0), 1e-9)
	require.InDelta(t, 1.23, PointsToBalance(123, 0.01), 1e-9)
}

func TestComputeWithdrawalAmounts(t *testing.T) {
	gross, fee, net := ComputeWithdrawalAmounts(1000, 0.01, 10)
	require.InDelta(t, 10.0, gross, 1e-9)
	require.InDelta(t, 1.0, fee, 1e-9)
	require.InDelta(t, 9.0, net, 1e-9)

	gross, fee, net = ComputeWithdrawalAmounts(1000, 0.01, 0)
	require.InDelta(t, 10.0, gross, 1e-9)
	require.InDelta(t, 0.0, fee, 1e-9)
	require.InDelta(t, 10.0, net, 1e-9)

	gross, fee, net = ComputeWithdrawalAmounts(0, 0.01, 10)
	require.InDelta(t, 0.0, gross, 1e-9)
	require.InDelta(t, 0.0, fee, 1e-9)
	require.InDelta(t, 0.0, net, 1e-9)
}
