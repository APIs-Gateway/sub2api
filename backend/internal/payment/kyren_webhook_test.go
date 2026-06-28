package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
)

const kyrenTestSecret = "whsec_test_kyren_123"

// signKyren 按 Kyren 官方算法生成合法签名头(测试辅助):sha256=hex(HMAC(ts + "." + body, secret))。
func signKyren(body []byte, tsMs int64, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(tsMs, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return kyrenSignaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyKyrenWebhookSignature_Valid(t *testing.T) {
	body := []byte(`{"id":"evt_1","type":"order.refunded","data":{}}`)
	now := int64(1_700_000_000_000)
	sig := signKyren(body, now, kyrenTestSecret)
	if err := VerifyKyrenWebhookSignature(body, sig, strconv.FormatInt(now, 10), kyrenTestSecret, now); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
}

func TestVerifyKyrenWebhookSignature_TamperedBody(t *testing.T) {
	body := []byte(`{"id":"evt_1","type":"order.refunded","data":{}}`)
	now := int64(1_700_000_000_000)
	sig := signKyren(body, now, kyrenTestSecret)
	// 改一个字节 → 验签必败(防篡改)。
	tampered := []byte(`{"id":"evt_1","type":"order.refunded","data":{"x":1}}`)
	if err := VerifyKyrenWebhookSignature(tampered, sig, strconv.FormatInt(now, 10), kyrenTestSecret, now); err == nil {
		t.Fatal("tampered body accepted")
	}
}

func TestVerifyKyrenWebhookSignature_WrongSecret(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	now := int64(1_700_000_000_000)
	sig := signKyren(body, now, kyrenTestSecret)
	if err := VerifyKyrenWebhookSignature(body, sig, strconv.FormatInt(now, 10), "whsec_other", now); err == nil {
		t.Fatal("signature from wrong secret accepted")
	}
}

func TestVerifyKyrenWebhookSignature_ReplayRejected(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	ts := int64(1_700_000_000_000)
	sig := signKyren(body, ts, kyrenTestSecret)
	// now 比签名时间晚 6 分钟 → 超出 5 分钟窗口 → 拒(防重放)。
	now := ts + 6*60*1000
	if err := VerifyKyrenWebhookSignature(body, sig, strconv.FormatInt(ts, 10), kyrenTestSecret, now); err == nil {
		t.Fatal("stale timestamp accepted (replay window not enforced)")
	}
	// 4 分钟内仍接受。
	if err := VerifyKyrenWebhookSignature(body, sig, strconv.FormatInt(ts, 10), kyrenTestSecret, ts+4*60*1000); err != nil {
		t.Fatalf("timestamp within window rejected: %v", err)
	}
}

func TestVerifyKyrenWebhookSignature_FutureTimestampRejected(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	ts := int64(1_700_000_000_000)
	sig := signKyren(body, ts, kyrenTestSecret)
	// 签名时间比 now 早(now 在签名时间之前 6 分钟)→ 负偏移也要按 abs 拒。
	now := ts - 6*60*1000
	if err := VerifyKyrenWebhookSignature(body, sig, strconv.FormatInt(ts, 10), kyrenTestSecret, now); err == nil {
		t.Fatal("future timestamp beyond window accepted")
	}
}

func TestVerifyKyrenWebhookSignature_Guards(t *testing.T) {
	body := []byte(`{}`)
	now := int64(1_700_000_000_000)
	nowStr := strconv.FormatInt(now, 10)
	good := signKyren(body, now, kyrenTestSecret)
	cases := []struct {
		name            string
		sig, ts, secret string
	}{
		{"empty secret", good, nowStr, ""},
		{"empty sig", "", nowStr, kyrenTestSecret},
		{"empty ts", good, "", kyrenTestSecret},
		{"non-numeric ts", good, "not-a-number", kyrenTestSecret},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := VerifyKyrenWebhookSignature(body, c.sig, c.ts, c.secret, now); err == nil {
				t.Fatalf("%s: expected error, got nil", c.name)
			}
		})
	}
}

func TestParseKyrenWebhookEvent_AndRefundData(t *testing.T) {
	raw := []byte(`{
		"id":"evt_refund123","type":"order.refunded","created_at":1736932600000,
		"data":{"order_id":"order_def456","refund_id":"refund_abc123","refund_status":"PARTIAL",
		"amount":"2.50","refunded_amount":"2.50","original_amount":"9.99","reason":"customer_request",
		"metadata":{"user_id":"u_123"}}}`)
	evt, err := ParseKyrenWebhookEvent(raw)
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	if evt.ID != "evt_refund123" || evt.Type != KyrenEventOrderRefunded {
		t.Fatalf("unexpected envelope: %+v", evt)
	}
	data, err := evt.RefundData()
	if err != nil {
		t.Fatalf("refund data: %v", err)
	}
	if data.OrderID != "order_def456" || data.RefundID != "refund_abc123" {
		t.Fatalf("unexpected refund data: %+v", data)
	}
	if data.IsFullRefund() {
		t.Fatal("PARTIAL should not be full refund")
	}
	if data.Metadata["user_id"] != "u_123" {
		t.Fatalf("metadata not parsed: %+v", data.Metadata)
	}
}

func TestKyrenRefundData_IsFullRefund(t *testing.T) {
	if !(&KyrenRefundData{RefundStatus: "FULL"}).IsFullRefund() {
		t.Fatal("FULL not recognized")
	}
	if !(&KyrenRefundData{RefundStatus: "full"}).IsFullRefund() {
		t.Fatal("case-insensitive FULL not recognized")
	}
	if (&KyrenRefundData{RefundStatus: "PARTIAL"}).IsFullRefund() {
		t.Fatal("PARTIAL wrongly full")
	}
}

func TestParseKyrenWebhookEvent_Guards(t *testing.T) {
	if _, err := ParseKyrenWebhookEvent([]byte(`{bad json`)); err == nil {
		t.Fatal("bad json accepted")
	}
	if _, err := ParseKyrenWebhookEvent([]byte(`{"id":"evt_1"}`)); err == nil {
		t.Fatal("missing type accepted")
	}
	if _, err := ParseKyrenWebhookEvent([]byte(`{"type":"order.paid"}`)); err == nil {
		t.Fatal("missing id accepted")
	}
	// 非退款事件取 RefundData 应报错。
	evt, _ := ParseKyrenWebhookEvent([]byte(`{"id":"evt_1","type":"order.paid","data":{}}`))
	if _, err := evt.RefundData(); err == nil {
		t.Fatal("order.paid RefundData should error")
	}
}
