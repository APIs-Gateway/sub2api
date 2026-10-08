package provider

import (
	"context"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func easyPaySignedNotifyFixture(params map[string]string) url.Values {
	q := url.Values{}
	for key, value := range params {
		q.Set(key, value)
	}
	q.Set("sign", easyPaySign(params, "MERCHANT_SECRET_KEY"))
	q.Set("sign_type", signTypeMD5)
	return q
}

func easyPayNotifyFixtureParams() map[string]string {
	return map[string]string{"pid": "1000", "trade_no": "PAID123", "out_trade_no": "ORDER123", "type": "alipay", "name": "balance recharge", "money": "650.00", "trade_status": tradeStatusSuccess}
}

func TestEasyPayNotifyBoundary_RejectsUnknownFields(t *testing.T) {
	for _, key := range []string{"notify_url", "return_url", "cid", "device", "clientip", "evil", "", "STATUS"} {
		for _, value := range []string{"", "x"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				params := easyPayNotifyFixtureParams()
				params[key] = value
				q := easyPaySignedNotifyFixture(params)
				_, err := easyPayPoCProvider().VerifyNotification(context.Background(), q.Encode(), nil)
				require.ErrorContains(t, err, "unexpected notify param")
			})
		}
	}
}

func TestEasyPayNotifyBoundary_RejectsDuplicateFields(t *testing.T) {
	for _, key := range []string{"pid", "trade_no", "out_trade_no", "type", "name", "money", "trade_status", "status", "param", "sign", "sign_type"} {
		for _, second := range []string{"same", "different"} {
			t.Run(key+"/"+second, func(t *testing.T) {
				params := easyPayNotifyFixtureParams()
				params["status"], params["param"] = "1", "opaque"
				q := easyPaySignedNotifyFixture(params)
				value := q.Get(key)
				if second == "different" {
					value += "other"
				}
				q.Add(key, value)
				_, err := easyPayPoCProvider().VerifyNotification(context.Background(), q.Encode(), nil)
				require.ErrorContains(t, err, "ambiguous duplicate notify param")
			})
		}
	}
}

func TestEasyPayNotifyBoundary_PreservesForkDialects(t *testing.T) {
	for _, tc := range []struct {
		name, tradeStatus, status, expected string
	}{
		{"standard", tradeStatusSuccess, "", payment.ProviderStatusSuccess},
		{"keying", "", "1", payment.ProviderStatusSuccess},
		{"both_success", tradeStatusSuccess, "1", payment.ProviderStatusSuccess},
		{"trade_status_precedes_keying", "TRADE_CLOSED", "1", payment.ProviderStatusFailed},
		{"pending", "", "0", payment.ProviderStatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := easyPayNotifyFixtureParams()
			delete(params, "trade_status")
			if tc.tradeStatus != "" {
				params["trade_status"] = tc.tradeStatus
			}
			if tc.status != "" {
				params["status"] = tc.status
			}
			params["param"] = "opaque & = 中文"
			q := easyPaySignedNotifyFixture(params)
			n, err := easyPayPoCProvider().VerifyNotification(context.Background(), q.Encode(), nil)
			require.NoError(t, err)
			require.Equal(t, tc.expected, n.Status)
			require.Equal(t, "ORDER123", n.OrderID)
			require.Equal(t, "PAID123", n.TradeNo)
			require.Equal(t, 650.0, n.Amount)
			require.Equal(t, "1000", n.Metadata["pid"])
		})
	}
}
