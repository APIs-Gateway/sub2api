//go:build integration

package repository

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	userhandler "github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const easyPayBoundaryTestKey = "isolated-easypay-fixture-key"

type easyPayBoundaryLoadBalancer struct {
	payment.LoadBalancer
	config     map[string]string
	instanceID int64
}

func (b easyPayBoundaryLoadBalancer) GetInstanceConfig(_ context.Context, id int64) (map[string]string, error) {
	if id != b.instanceID {
		return nil, fmt.Errorf("unexpected fixture instance %d", id)
	}
	copyConfig := make(map[string]string, len(b.config))
	for k, v := range b.config {
		copyConfig[k] = v
	}
	return copyConfig, nil
}

type easyPayBoundaryHTTPFixture struct {
	h          *balanceFulfillmentHarness
	handler    *userhandler.PaymentWebhookHandler
	order      *dbent.PaymentOrder
	provider   *provider.EasyPay
	queryCalls atomic.Int64
	badQuery   atomic.Bool
}

func newEasyPayBoundaryHTTPFixture(t *testing.T, upstreamStatus int, money, upstreamOrder, snapshotMerchant string, queryFailure bool) *easyPayBoundaryHTTPFixture {
	t.Helper()
	f := &easyPayBoundaryHTTPFixture{}
	outTradeNo := "easypay_boundary_" + uuid.NewString()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.queryCalls.Add(1)
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/api.php" || q.Get("act") != "order" || q.Get("pid") != "fixture-merchant" || q.Get("key") != easyPayBoundaryTestKey || q.Get("out_trade_no") != outTradeNo {
			f.badQuery.Store(true)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if queryFailure {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream unavailable"))
			return
		}
		returnedOrder := outTradeNo
		if upstreamOrder != "" {
			returnedOrder = upstreamOrder
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "status": upstreamStatus, "money": money, "trade_no": "AUTHORITATIVE_PAID_TRADE", "out_trade_no": returnedOrder})
	}))
	t.Cleanup(upstream.Close)
	c := testEntClient(t)
	inst, err := c.PaymentProviderInstance.Create().SetProviderKey(payment.TypeEasyPay).SetName(uuid.NewString()).SetConfig("{}").SetSupportedTypes("alipay").SetPaymentMode("popup").SetEnabled(true).Save(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.PaymentProviderInstance.DeleteOneID(inst.ID).Exec(context.Background())) })
	f.h = newBalanceFulfillmentHarness(t, NewRedeemCache(testRedis(t)))
	u := mustCreateUser(t, c, &service.User{Email: uuid.NewString() + "@example.com", Username: "easypay signing boundary", Balance: 7})
	if snapshotMerchant == "" {
		snapshotMerchant = "fixture-merchant"
	}
	instanceID := strconv.FormatInt(inst.ID, 10)
	f.order, err = c.PaymentOrder.Create().SetUserID(u.ID).SetUserEmail(u.Email).SetUserName(u.Username).
		SetAmount(80).SetPayAmount(88).SetFeeRate(10).SetRechargeCode(strings.ReplaceAll(uuid.NewString(), "-", "")).SetOutTradeNo(outTradeNo).
		SetPaymentType(payment.TypeAlipay).SetProviderKey(payment.TypeEasyPay).SetProviderInstanceID(instanceID).
		SetPaymentTradeNo("").SetOrderType(payment.OrderTypeBalance).SetStatus(service.OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("site.example.com").
		SetProviderSnapshot(map[string]any{"schema_version": 2, "provider_instance_id": instanceID, "provider_key": payment.TypeEasyPay, "payment_mode": "popup", "merchant_id": snapshotMerchant, "currency": "CNY"}).Save(context.Background())
	require.NoError(t, err)
	f.h.trackFixture(f.order.ID, u.ID, f.order.RechargeCode)
	config := map[string]string{"pid": "fixture-merchant", "pkey": easyPayBoundaryTestKey, "apiBase": upstream.URL, "notifyUrl": "https://site.example.com/api/v1/payment/webhook/easypay", "returnUrl": "https://site.example.com/payment/result", "paymentMode": "popup"}
	f.provider, err = provider.NewEasyPay(instanceID, config)
	require.NoError(t, err)
	svc := service.NewPaymentService(c, payment.NewRegistry(), easyPayBoundaryLoadBalancer{config: config, instanceID: inst.ID}, f.h.redeemSvc, nil, service.NewPaymentConfigService(c, nil, nil), NewUserRepository(c, integrationDB), nil, nil)
	f.handler = userhandler.NewPaymentWebhookHandler(svc, nil)
	return f
}

func (f *easyPayBoundaryHTTPFixture) notify(t *testing.T, method, raw string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	path := "/api/v1/payment/webhook/easypay"
	if method == http.MethodGet {
		path += "?" + raw
	}
	c.Request = httptest.NewRequest(method, path, strings.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f.handler.EasyPayNotify(c)
	require.False(t, f.badQuery.Load(), "actual QueryOrder must use the pinned merchant and order")
	return rec
}

func (f *easyPayBoundaryHTTPFixture) assertUncredited(t *testing.T) {
	t.Helper()
	o, err := f.h.client.PaymentOrder.Get(context.Background(), f.order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusPending, o.Status)
	require.Empty(t, o.PaymentTradeNo)
	require.Nil(t, o.PaidAt)
	require.Equal(t, 7.0, balanceForUser(t, f.h.client, o.UserID))
	require.Zero(t, rechargeSuccessAuditCount(t, f.h.client, o.ID))
	_, err = f.h.redeemRepo.GetByCode(context.Background(), o.RechargeCode)
	require.ErrorIs(t, err, service.ErrRedeemCodeNotFound)
}

// The attacker uses only the public signed popup URL, never the merchant key.
// This intentionally models a malicious historical URL issued before stripping
// client return queries, not a full CreateOrder HTTP installation flow.
func (f *easyPayBoundaryHTTPFixture) replay(t *testing.T) string {
	t.Helper()
	returnURL := "https://site.example.com/payment/result?order_id=" + strconv.FormatInt(f.order.ID, 10) + "&out_trade_no=" + f.order.OutTradeNo + "&status=success&trade_status=TRADE_SUCCESS"
	issued, err := f.provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: f.order.OutTradeNo, Amount: "88.00", PaymentType: payment.TypeAlipay, Subject: "balance recharge", ReturnURL: returnURL})
	require.NoError(t, err)
	parsed, err := url.Parse(issued.PayURL)
	require.NoError(t, err)
	q := parsed.Query()
	q.Set("return_url", strings.TrimSuffix(q.Get("return_url"), "&trade_status=TRADE_SUCCESS"))
	q.Set("trade_status", "TRADE_SUCCESS")
	return q.Encode()
}

func (f *easyPayBoundaryHTTPFixture) genuine(keying bool) string {
	q := url.Values{"pid": {"fixture-merchant"}, "trade_no": {"CALLBACK_TRADE"}, "out_trade_no": {f.order.OutTradeNo}, "type": {payment.TypeAlipay}, "name": {"balance recharge"}, "money": {"88.00"}, "param": {"opaque & = 中文"}}
	if keying {
		q.Set("status", "1")
	} else {
		q.Set("trade_status", "TRADE_SUCCESS")
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+q.Get(k))
	}
	digest := md5.Sum([]byte(strings.Join(parts, "&") + easyPayBoundaryTestKey))
	q.Set("sign", hex.EncodeToString(digest[:]))
	q.Set("sign_type", "MD5")
	return q.Encode()
}

func TestEasyPaySignReuseHTTP_RejectsHistoricalPopupReplayPostgres(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			f := newEasyPayBoundaryHTTPFixture(t, 0, "88.00", "", "", false)
			raw := f.replay(t)
			rec := f.notify(t, method, raw)
			f.assertUncredited(t)
			require.Equal(t, http.StatusBadRequest, rec.Code, "old verifier accepts replay but the fork's upstream-not-paid guard still refuses credit")
			require.Equal(t, "verify failed", rec.Body.String())
			require.Zero(t, f.queryCalls.Load(), "rejected signing domain must not trigger an upstream query")
		})
	}
}

func TestEasyPaySignReuseHTTP_UnpaidAndInvalidUpstreamRemainUncreditedPostgres(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		status                 int
		money, order, merchant string
		failure                bool
	}{
		{"pending", 0, "88.00", "", "", false},
		{"amount_mismatch", 1, "87.00", "", "", false},
		{"invalid_amount", 1, "NaN", "", "", false},
		{"foreign_order", 1, "88.00", "FOREIGN_ORDER", "", false},
		{"merchant_snapshot_mismatch", 1, "88.00", "", "other-merchant", false},
		{"query_failure", 1, "88.00", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEasyPayBoundaryHTTPFixture(t, tc.status, tc.money, tc.order, tc.merchant, tc.failure)
			rec := f.notify(t, http.MethodPost, f.genuine(false))
			f.assertUncredited(t)
			require.Equal(t, http.StatusInternalServerError, rec.Code)
			require.Equal(t, int64(1), f.queryCalls.Load())
		})
	}
}

func TestEasyPaySignReuseHTTP_PaidDialectCreditsOncePostgres(t *testing.T) {
	for _, keying := range []bool{false, true} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(fmt.Sprintf("keying_%t/%s", keying, method), func(t *testing.T) {
				f := newEasyPayBoundaryHTTPFixture(t, 1, "88.00", "", "", false)
				for n := 1; n <= 2; n++ {
					rec := f.notify(t, method, f.genuine(keying))
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Equal(t, "success", rec.Body.String())
					require.Equal(t, 87.0, balanceForUser(t, f.h.client, f.order.UserID), "credit immutable order amount 80, not callback/upstream paid amount 88")
					o, err := f.h.client.PaymentOrder.Get(context.Background(), f.order.ID)
					require.NoError(t, err)
					require.Equal(t, service.OrderStatusCompleted, o.Status)
					require.Equal(t, "AUTHORITATIVE_PAID_TRADE", o.PaymentTradeNo)
					require.NotNil(t, o.PaidAt)
					require.Equal(t, 1, rechargeSuccessAuditCount(t, f.h.client, o.ID))
					code, err := f.h.redeemRepo.GetByCode(context.Background(), o.RechargeCode)
					require.NoError(t, err)
					require.True(t, code.IsUsed())
					require.Equal(t, 80.0, code.Value)
					require.Equal(t, int64(n), f.queryCalls.Load())
				}
			})
		}
	}
}

func TestEasyPaySignReuseHTTP_PinnedInstanceMismatchDoesNotCreditPostgres(t *testing.T) {
	f := newEasyPayBoundaryHTTPFixture(t, 1, "88.00", "", "", false)
	snapshot := make(map[string]any, len(f.order.ProviderSnapshot))
	for key, value := range f.order.ProviderSnapshot {
		snapshot[key] = value
	}
	snapshot["provider_instance_id"] = "999999999"
	_, err := f.h.client.PaymentOrder.UpdateOneID(f.order.ID).SetProviderSnapshot(snapshot).Save(context.Background())
	require.NoError(t, err)
	rec := f.notify(t, http.MethodPost, f.genuine(false))
	// The existing handler acknowledges an unresolved provider to stop retries.
	// This is not signature acceptance: the pinned provider cannot be loaded.
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "success", rec.Body.String())
	f.assertUncredited(t)
	require.Zero(t, f.queryCalls.Load())
}
