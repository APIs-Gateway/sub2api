package payment

import (
	"strings"
	"testing"
)

// 退款幂等键的安全属性：同订单同额多次调用必须【完全相同】（否则重试会双重退款）。
func TestDeterministicRefundNo(t *testing.T) {
	a := DeterministicRefundNo("ORD123", "9.90")
	b := DeterministicRefundNo("ORD123", "9.90")
	if a != b {
		t.Fatalf("同订单同额必须确定性相同：%q != %q", a, b)
	}
	// 不同退款额 → 不同键（允许同单分次退款各自幂等）。
	if c := DeterministicRefundNo("ORD123", "5.00"); c == a {
		t.Fatalf("不同退款额应得到不同键，却相同：%q", c)
	}
	// 不同订单 → 不同键。
	if d := DeterministicRefundNo("ORD999", "9.90"); d == a {
		t.Fatalf("不同订单应得到不同键，却相同：%q", d)
	}
	// 形如 {orderID}-refund-{hex}，且足够短（≤64，支付宝/微信 out_request_no/out_refund_no 上限）。
	if !strings.HasPrefix(a, "ORD123-refund-") {
		t.Fatalf("前缀不符：%q", a)
	}
	if len(a) > 64 {
		t.Fatalf("退款号超 64 长度上限：len=%d %q", len(a), a)
	}
}
