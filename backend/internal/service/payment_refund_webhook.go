package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
)

// Kyren Pay 原生退款 webhook(order.refunded)的处理。Kyren 的 api.php?act=refund 兼容端点官方不支持,
// 退款只能在 Kyren 控制台发起,生效后 Kyren 推 order.refunded;应用据此关订阅卡 + 标订单 refunded + 对账。
// 验签在 handler 层完成(payment.VerifyKyrenWebhookSignature),此处只处理已验签的载荷。

// SettingKyrenWebhookSecret 是 Kyren 原生 webhook 的验签密钥(全局 setting)。
// 用全局 setting 而非 provider config:验签发生在「信任 body 之前」,无法先按订单定位到具体加密实例
// (鸡生蛋);且 Kyren 一个商户=一个 webhook 密钥,全局更贴合。
const SettingKyrenWebhookSecret = "kyren_webhook_secret"

// kyrenRefundAuditAction 按事件 id 生成去重用的 audit action。同一事件(evt_xxx)因重试被多次推送时,
// 凭此审计记录幂等跳过。
func kyrenRefundAuditAction(eventID string) string {
	return "KYREN_REFUND_WEBHOOK:" + eventID
}

// KyrenWebhookSecret 读取 Kyren webhook 验签密钥。
func (s *PaymentService) KyrenWebhookSecret(ctx context.Context) string {
	if s.configService == nil {
		return ""
	}
	return s.configService.GetKyrenWebhookSecret(ctx)
}

// HandleKyrenRefundWebhook 处理【已验签】的 order.refunded 事件:
//
//	找订单(防御性匹配)→ 按 evt_id 幂等去重 → 订阅单退订关卡(无条件,不分 FULL/PARTIAL)
//	→ 标订单 refunded → 写审计。
//
// 返回 ErrOrderNotFound 让 handler ack 2xx(停止 Kyren 重推);其余 error 让 handler 回 5xx 触发重试。
func (s *PaymentService) HandleKyrenRefundWebhook(ctx context.Context, data *payment.KyrenRefundData, eventID string) error {
	if data == nil {
		return fmt.Errorf("nil kyren refund data")
	}
	order, err := s.findOrderForKyrenRefund(ctx, data)
	if err != nil {
		return err // ErrOrderNotFound 向上冒泡 → handler ack 2xx
	}

	// 幂等:同一事件已处理过 → 直接成功返回(Kyren 因重试可能重复推送)。
	action := kyrenRefundAuditAction(eventID)
	if s.hasAuditLog(ctx, order.ID, action) {
		return nil
	}

	// 订阅订单退款 = 退订:无条件关卡(不分 FULL/PARTIAL)。退款额本就是按「剩余使用时间 − 手续费」
	// 在 Kyren 侧算好的,partial 只代表金额扣了已用部分,不代表保留卡。
	if order.OrderType == payment.OrderTypeSubscription {
		if subID, ok := readSubscriptionSnapshotSubscriptionID(order); ok && subID > 0 && s.subscriptionSvc != nil {
			if closeErr := s.subscriptionSvc.closeSubscriptionForRefund(ctx, subID); closeErr != nil {
				// 卡已不存在/已关 → 容忍(幂等);其余错误返回触发重试,避免「退了款卡没关」。
				if !errors.Is(closeErr, ErrSubscriptionNotFound) {
					return fmt.Errorf("close subscription card on kyren refund (order %d, sub %d): %w", order.ID, subID, closeErr)
				}
			}
		}
	}

	// 标订单 refunded(仅从可退状态转;已是 refunded 则 Update 命中 0 行、不报错 → 幂等)。
	if _, err := s.entClient.PaymentOrder.Update().
		Where(
			paymentorder.IDEQ(order.ID),
			paymentorder.StatusIn(OrderStatusCompleted, OrderStatusRefundRequested, OrderStatusRefundFailed, OrderStatusRefunding),
		).
		SetStatus(OrderStatusRefunded).
		Save(ctx); err != nil {
		return fmt.Errorf("mark order %d refunded: %w", order.ID, err)
	}

	s.writeAuditLog(ctx, order.ID, action, "kyren_webhook", map[string]any{
		"event_id":        eventID,
		"refund_id":       data.RefundID,
		"refund_status":   data.RefundStatus,
		"amount":          data.Amount,
		"refunded_amount": data.RefundedAmount,
		"original_amount": data.OriginalAmount,
		"reason":          data.Reason,
	})
	return nil
}

// findOrderForKyrenRefund 把 webhook 的 order_id 对回应用订单。
// 【防御性三路兜底】文档未明确 epay 兼容订单的 data.order_id 取值(=out_trade_no / trade_no / Kyren 原生 id),
// 故依次比 OutTradeNo → PaymentTradeNo,并尝试 metadata.out_trade_no;都不中记日志告警(待真实 webhook 校准)。
func (s *PaymentService) findOrderForKyrenRefund(ctx context.Context, data *payment.KyrenRefundData) (*dbent.PaymentOrder, error) {
	seen := map[string]bool{}
	candidates := []string{strings.TrimSpace(data.OrderID)}
	if data.Metadata != nil {
		if mid := strings.TrimSpace(data.Metadata["out_trade_no"]); mid != "" {
			candidates = append(candidates, mid)
		}
	}
	for _, id := range candidates {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if o, err := s.entClient.PaymentOrder.Query().Where(paymentorder.OutTradeNo(id)).Only(ctx); err == nil {
			return o, nil
		}
		if o, err := s.entClient.PaymentOrder.Query().Where(paymentorder.PaymentTradeNo(id)).Only(ctx); err == nil {
			return o, nil
		}
	}
	slog.Warn("[Kyren Refund Webhook] no matching order",
		"order_id", data.OrderID, "refund_id", data.RefundID)
	return nil, ErrOrderNotFound
}
