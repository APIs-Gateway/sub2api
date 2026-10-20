package service

import (
	"context"
	"testing"
)

// Kyren webhook 密钥读取的 nil 守卫(纯单测):未配置 configService/settingRepo 时返回空 → 验签必失败(安全默认)。
func TestKyrenWebhookSecret_NilGuards(t *testing.T) {
	ctx := context.Background()

	// PaymentService.configService 为 nil → 空。
	if got := (&PaymentService{}).KyrenWebhookSecret(ctx); got != "" {
		t.Fatalf("nil configService should yield empty secret, got %q", got)
	}
	// PaymentConfigService.settingRepo 为 nil → 空。
	if got := (&PaymentConfigService{}).GetKyrenWebhookSecret(ctx); got != "" {
		t.Fatalf("nil settingRepo should yield empty secret, got %q", got)
	}
	// nil 接收者 → 空(不 panic)。
	var nilCfg *PaymentConfigService
	if got := nilCfg.GetKyrenWebhookSecret(ctx); got != "" {
		t.Fatalf("nil receiver should yield empty secret, got %q", got)
	}
}

// kyrenRefundAuditAction 生成稳定的按事件去重键。
func TestKyrenRefundAuditAction(t *testing.T) {
	if got := kyrenRefundAuditAction("evt_abc"); got != "KYREN_REFUND_WEBHOOK:evt_abc" {
		t.Fatalf("unexpected audit action: %q", got)
	}
}
