//go:build unit

package admin

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/payment"
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
