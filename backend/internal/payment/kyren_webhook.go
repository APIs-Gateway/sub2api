package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Kyren Pay 原生 webhook(区别于 easypay 兼容 notify 回调):退款生效时 Kyren 发 order.refunded 事件,
// 应用据此关订阅卡 / 对账。Kyren 的 api.php?act=refund 兼容端点官方不支持,故退款只能走「Kyren 控制台
// 发起 → order.refunded webhook 确认」这条路。文档:docs.kyren.top/zh/webhooks/{overview,signatures,events}。

// Kyren webhook 事件类型。
const (
	KyrenEventOrderPaid     = "order.paid"
	KyrenEventOrderRefunded = "order.refunded"
)

// Kyren 退款状态(order.refunded 的 refund_status)。
const (
	KyrenRefundStatusPartial = "PARTIAL"
	KyrenRefundStatusFull    = "FULL"
)

// kyrenWebhookMaxSkewMs 是签名时间戳允许的最大偏移(防重放),与官方建议一致:5 分钟。
const kyrenWebhookMaxSkewMs int64 = 5 * 60 * 1000

// kyrenSignaturePrefix 是 X-Kyren-Signature 头的固定前缀(sha256=<hex>)。
const kyrenSignaturePrefix = "sha256="

// KyrenWebhookEvent 是 Kyren webhook 的事件信封。data 用 RawMessage 延迟解析,按 type 再解具体载荷。
type KyrenWebhookEvent struct {
	ID        string          `json:"id"`   // evt_xxx,用于去重(同一事件可能因重试被推多次)
	Type      string          `json:"type"` // order.paid / order.refunded
	CreatedAt int64           `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}

// KyrenRefundData 是 order.refunded 事件的 data 载荷。
type KyrenRefundData struct {
	OrderID        string            `json:"order_id"`        // Kyren 订单标识(对 epay 兼容订单的取值需以真实 webhook 校准)
	RefundID       string            `json:"refund_id"`       // Kyren 退款流水号
	RefundStatus   string            `json:"refund_status"`   // PARTIAL | FULL
	Amount         string            `json:"amount"`          // 本次退款额
	RefundedAmount string            `json:"refunded_amount"` // 累计已退额
	OriginalAmount string            `json:"original_amount"` // 原订单额
	Reason         string            `json:"reason"`
	Metadata       map[string]string `json:"metadata"`
}

// VerifyKyrenWebhookSignature 校验 Kyren webhook 签名(HMAC-SHA256)。
//
//	待签名串 = timestampHeader + "." + rawBody
//	expected = "sha256=" + hex(HMAC_SHA256(待签名串, secret))
//
// 校验 expected 与 signatureHeader 恒时相等;并拒绝时间戳偏移超过 5 分钟的请求(防重放)。
// nowMs 为当前 Unix 毫秒(显式入参便于单测)。
func VerifyKyrenWebhookSignature(rawBody []byte, signatureHeader, timestampHeader, secret string, nowMs int64) error {
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("kyren webhook secret not configured")
	}
	signatureHeader = strings.TrimSpace(signatureHeader)
	if signatureHeader == "" {
		return fmt.Errorf("missing X-Kyren-Signature header")
	}
	timestampHeader = strings.TrimSpace(timestampHeader)
	if timestampHeader == "" {
		return fmt.Errorf("missing X-Kyren-Timestamp header")
	}
	ts, err := strconv.ParseInt(timestampHeader, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid X-Kyren-Timestamp: %w", err)
	}
	// 防重放:|now - ts| > 5min 拒绝(abs,兼顾两端时钟漂移)。
	skew := nowMs - ts
	if skew < 0 {
		skew = -skew
	}
	if skew > kyrenWebhookMaxSkewMs {
		return fmt.Errorf("kyren webhook timestamp outside allowed window (skew=%dms)", skew)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	// hash.Hash.Write 文档保证永不返回 error;显式忽略以过 errcheck。
	_, _ = mac.Write([]byte(timestampHeader))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(rawBody)
	expected := kyrenSignaturePrefix + hex.EncodeToString(mac.Sum(nil))

	// 恒时比较(长度不同 hmac.Equal 直接 false,不泄露长度差异时序)。
	if !hmac.Equal([]byte(expected), []byte(signatureHeader)) {
		return fmt.Errorf("kyren webhook signature mismatch")
	}
	return nil
}

// ParseKyrenWebhookEvent 解析事件信封。
func ParseKyrenWebhookEvent(rawBody []byte) (*KyrenWebhookEvent, error) {
	var evt KyrenWebhookEvent
	if err := json.Unmarshal(rawBody, &evt); err != nil {
		return nil, fmt.Errorf("parse kyren webhook event: %w", err)
	}
	if strings.TrimSpace(evt.Type) == "" {
		return nil, fmt.Errorf("kyren webhook event missing type")
	}
	if strings.TrimSpace(evt.ID) == "" {
		return nil, fmt.Errorf("kyren webhook event missing id")
	}
	return &evt, nil
}

// RefundData 解出 order.refunded 的退款载荷;非退款事件返回错误。
func (e *KyrenWebhookEvent) RefundData() (*KyrenRefundData, error) {
	if e == nil || e.Type != KyrenEventOrderRefunded {
		return nil, fmt.Errorf("event is not %s", KyrenEventOrderRefunded)
	}
	var data KyrenRefundData
	if err := json.Unmarshal(e.Data, &data); err != nil {
		return nil, fmt.Errorf("parse kyren refund data: %w", err)
	}
	if strings.TrimSpace(data.OrderID) == "" {
		return nil, fmt.Errorf("kyren refund data missing order_id")
	}
	return &data, nil
}

// IsFullRefund 判断是否全额退款(refund_status=FULL,大小写不敏感)。
func (d *KyrenRefundData) IsFullRefund() bool {
	return d != nil && strings.EqualFold(strings.TrimSpace(d.RefundStatus), KyrenRefundStatusFull)
}
