//go:build integration

package repository

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Kyren order.refunded webhook 的 HTTP 端到端:验签 → 解析 → 关卡/对账 → 各响应码。
// 覆盖 PaymentWebhookHandler.KyrenRefundWebhook + PaymentService.KyrenWebhookSecret + configService 读密钥。

func signKyrenForTest(body []byte, tsMs int64, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strconv.FormatInt(tsMs, 10)))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func makeKyrenWebhookHandler(t *testing.T, secret string) *userhandler.PaymentWebhookHandler {
	t.Helper()
	client := testEntClient(t)
	settingRepo := NewSettingRepository(client)
	require.NoError(t, settingRepo.Set(context.Background(), service.SettingKyrenWebhookSecret, secret))
	groupRepo := NewGroupRepository(client, integrationDB)
	subSvc := service.NewSubscriptionService(
		groupRepo, NewUserSubscriptionRepository(client),
		nil, nil, nil, client, nil, nil,
	)
	configSvc := service.NewPaymentConfigService(client, settingRepo, nil)
	paySvc := service.NewPaymentService(
		client, nil, subscriptionOrderTestLoadBalancer{}, nil, subSvc, configSvc,
		NewUserRepository(client, integrationDB), groupRepo, nil,
	)
	return userhandler.NewPaymentWebhookHandler(paySvc, nil)
}

func postKyrenWebhook(t *testing.T, h *userhandler.PaymentWebhookHandler, body []byte, sig, ts string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/payment/webhook/kyren", bytes.NewReader(body))
	if sig != "" {
		c.Request.Header.Set("X-Kyren-Signature", sig)
	}
	if ts != "" {
		c.Request.Header.Set("X-Kyren-Timestamp", ts)
	}
	h.KyrenRefundWebhook(c)
	return rec
}

func TestKyrenWebhookHTTP_ValidRefundClosesCardPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	secret := "whsec_" + uuid.NewString()
	h := makeKyrenWebhookHandler(t, secret)

	user := mustCreateUser(t, client, &service.User{
		Email:    fmt.Sprintf("kyren-http-%s@example.com", uuid.NewString()),
		Username: "kyren-http",
	})
	group := mustCreateGroup(t, client, &service.Group{Name: "kyren-http-" + uuid.NewString()})
	today := service.TodayEastDayNumber()
	d := 10.0
	w, m := service.DeriveWindowCaps(d, 30)
	card := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyAmountUSD:  d,
		DailyLimitUSD:   &d,
		WeeklyLimitUSD:  &w,
		MonthlyLimitUSD: &m,
		TodayRemaining:  d,
		TodayDay:        today,
		StartDay:        today - 5,
		ExpireDay:       today + 25,
		ExpiresAt:       service.ExpireDayToExpiresAt(today + 25),
		Status:          service.SubscriptionStatusActive,
	})
	outTradeNo := "kyren_http_" + uuid.NewString()
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).SetUserEmail(user.Email).SetUserName(user.Username).
		SetAmount(300).SetPayAmount(300).SetFeeRate(0).
		SetRechargeCode("KH-" + uuid.NewString()).SetOutTradeNo(outTradeNo).
		SetPaymentType(payment.TypeEasyPay).SetPaymentTradeNo("trade-" + uuid.NewString()).
		SetOrderType(payment.OrderTypeSubscription).SetStatus(service.OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").SetSrcHost("api.example.com").SetProviderKey(payment.TypeEasyPay).
		SetSubscriptionDays(30).
		SetProviderSnapshot(map[string]any{
			"schema_version": 2, "provider_key": payment.TypeEasyPay,
			"subscription": map[string]any{"daily_amount_usd": d, "validity_days": 30.0, "subscription_id": card.ID},
		}).Save(ctx)
	require.NoError(t, err)

	body := []byte(fmt.Sprintf(`{"id":"evt_%s","type":"order.refunded","created_at":%d,"data":{"order_id":%q,"refund_id":"refund_x","refund_status":"PARTIAL","amount":"180.00","refunded_amount":"180.00","original_amount":"300.00"}}`,
		uuid.NewString(), time.Now().UnixMilli(), outTradeNo))
	now := time.Now().UnixMilli()
	rec := postKyrenWebhook(t, h, body, signKyrenForTest(body, now, secret), strconv.FormatInt(now, 10))
	require.Equalf(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	gotCard, err := NewUserSubscriptionRepository(client).GetByID(ctx, card.ID)
	require.NoError(t, err)
	require.NotEqual(t, service.SubscriptionStatusActive, gotCard.Status, "退款 webhook 应关卡")
	gotOrder, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusRefunded, gotOrder.Status)
}

func TestKyrenWebhookHTTP_BadSignatureRejectedPostgres(t *testing.T) {
	secret := "whsec_" + uuid.NewString()
	h := makeKyrenWebhookHandler(t, secret)
	body := []byte(`{"id":"evt_1","type":"order.refunded","data":{"order_id":"x"}}`)
	now := time.Now().UnixMilli()
	// 用错误密钥签 → 验签失败 → 401。
	rec := postKyrenWebhook(t, h, body, signKyrenForTest(body, now, "wrong_secret"), strconv.FormatInt(now, 10))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestKyrenWebhookHTTP_UnknownOrderAcksPostgres(t *testing.T) {
	secret := "whsec_" + uuid.NewString()
	h := makeKyrenWebhookHandler(t, secret)
	body := []byte(fmt.Sprintf(`{"id":"evt_%s","type":"order.refunded","data":{"order_id":"nope_%s"}}`, uuid.NewString(), uuid.NewString()))
	now := time.Now().UnixMilli()
	// 未知订单:验签通过但找不到订单 → ack 200 停止 Kyren 重推。
	rec := postKyrenWebhook(t, h, body, signKyrenForTest(body, now, secret), strconv.FormatInt(now, 10))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestKyrenWebhookHTTP_NonRefundEventIgnoredPostgres(t *testing.T) {
	secret := "whsec_" + uuid.NewString()
	h := makeKyrenWebhookHandler(t, secret)
	body := []byte(fmt.Sprintf(`{"id":"evt_%s","type":"order.paid","data":{"order_id":"x"}}`, uuid.NewString()))
	now := time.Now().UnixMilli()
	// 非退款事件(order.paid 走 easypay notify)→ ack 200 忽略。
	rec := postKyrenWebhook(t, h, body, signKyrenForTest(body, now, secret), strconv.FormatInt(now, 10))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestKyrenWebhookHTTP_BadJSONRejectedPostgres(t *testing.T) {
	secret := "whsec_" + uuid.NewString()
	h := makeKyrenWebhookHandler(t, secret)
	body := []byte(`{not valid json`)
	now := time.Now().UnixMilli()
	// 验签通过(签的就是这段坏 JSON)但解析失败 → 400。
	rec := postKyrenWebhook(t, h, body, signKyrenForTest(body, now, secret), strconv.FormatInt(now, 10))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestKyrenWebhookHTTP_RefundMissingOrderIDRejectedPostgres(t *testing.T) {
	secret := "whsec_" + uuid.NewString()
	h := makeKyrenWebhookHandler(t, secret)
	// order.refunded 但 data 缺 order_id → RefundData 报错 → 400。
	body := []byte(fmt.Sprintf(`{"id":"evt_%s","type":"order.refunded","data":{"refund_id":"x"}}`, uuid.NewString()))
	now := time.Now().UnixMilli()
	rec := postKyrenWebhook(t, h, body, signKyrenForTest(body, now, secret), strconv.FormatInt(now, 10))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// config 往返:UpdatePaymentConfig 设密钥 → GetKyrenWebhookSecret 读到;不带该字段的更新不清空。
func TestPaymentConfig_KyrenWebhookSecretRoundTripPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	configSvc := service.NewPaymentConfigService(client, NewSettingRepository(client), nil)

	secret := "whsec_" + uuid.NewString()
	require.NoError(t, configSvc.UpdatePaymentConfig(ctx, service.UpdatePaymentConfigRequest{KyrenWebhookSecret: &secret}))
	require.Equal(t, secret, configSvc.GetKyrenWebhookSecret(ctx))

	// 不带 kyren_webhook_secret 的配置保存,不应把已设密钥清空。
	enabled := true
	require.NoError(t, configSvc.UpdatePaymentConfig(ctx, service.UpdatePaymentConfigRequest{Enabled: &enabled}))
	require.Equal(t, secret, configSvc.GetKyrenWebhookSecret(ctx), "未带该字段的更新不应清空密钥")
}
