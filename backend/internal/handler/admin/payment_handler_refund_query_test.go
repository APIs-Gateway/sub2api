//go:build unit

package admin

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func TestQueryAndFinalizeRefundHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := sql.Open("sqlite", "file:admin_payment_refund_query?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	user, err := client.User.Create().SetEmail("refund-query-admin@example.com").SetPasswordHash("hash").SetUsername("refund-query-admin").Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(10).
		SetPayAmount(10).
		SetFeeRate(0).
		SetRechargeCode("REFUND-QUERY-ADMIN").
		SetOutTradeNo("sub2_refund_query_admin").
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("pi_refund_query_admin").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(service.OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	h := NewPaymentHandler(service.NewPaymentService(client, payment.NewRegistry(), nil, nil, nil, nil, nil, nil, nil), nil)
	call := func(id string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/payment/orders/"+id+"/refund/query", nil)
		c.Params = gin.Params{{Key: "id", Value: id}}
		h.QueryAndFinalizeRefund(c)
		return rec
	}

	require.Equal(t, http.StatusBadRequest, call("abc").Code)
	require.Equal(t, http.StatusNotFound, call("987654").Code)
	rec := call(strconv.FormatInt(order.ID, 10))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "refund pending")
}

func TestResolvePendingRefundHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := sql.Open("sqlite", "file:admin_payment_refund_resolve?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	user, err := client.User.Create().SetEmail("refund-resolve-admin@example.com").SetPasswordHash("hash").SetUsername("refund-resolve-admin").Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(10).
		SetPayAmount(10).
		SetFeeRate(0).
		SetRechargeCode("REFUND-RESOLVE-ADMIN").
		SetOutTradeNo("sub2_refund_resolve_admin").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade_refund_resolve_admin").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(service.OrderStatusRefundPending).
		SetRefundAmount(10).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	h := NewPaymentHandler(service.NewPaymentService(client, payment.NewRegistry(), nil, nil, nil, nil, nil, nil, nil), nil)
	call := func(id, body string, adminID int64) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/payment/orders/"+id+"/refund/resolve", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: id}}
		if adminID > 0 {
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: adminID})
		}
		h.ResolvePendingRefund(c)
		return rec
	}
	id := strconv.FormatInt(order.ID, 10)

	require.Equal(t, http.StatusBadRequest, call("abc", `{}`, 0).Code)
	require.Equal(t, http.StatusBadRequest, call(id, `{`, 0).Code)
	rec := call(id, `{"outcome":"maybe"}`, 0)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "outcome must be")
	require.Equal(t, http.StatusNotFound, call("987654", `{"outcome":"failed"}`, 0).Code)

	rec = call(id, `{"outcome":"failed","note":"closed in console"}`, 9)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, service.OrderStatusRefundFailed, reloaded.Status)
	logs, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(id), paymentauditlog.ActionHasPrefix("REFUND_MANUAL_RESOLVED")).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Equal(t, "admin:9", logs[0].Operator)

	// No longer pending: resolving again is rejected.
	rec = call(id, `{"outcome":"succeeded"}`, 9)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "refund pending")
}
