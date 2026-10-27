package service

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalizeReturnURLSigningBoundary(t *testing.T) {
	for _, query := range []string{"?trade_status=TRADE_SUCCESS", "?status=1&trade_status=TRADE_SUCCESS", "?trade_status=TRADE_SUCCESS&trade_status=TRADE_CLOSED", "?return_url=x&notify_url=y", "?opaque=%ZZ", "?", "?order_id=999&out_trade_no=attacker&resume_token=attacker#fragment"} {
		t.Run(query, func(t *testing.T) {
			canonical, err := CanonicalizeReturnURL("https://site.example.com/payment/result"+query, "site.example.com", "")
			require.NoError(t, err)
			require.Equal(t, "https://site.example.com/payment/result", canonical)
			built, err := buildPaymentReturnURL(canonical, 42, "SERVER_ORDER", "SERVER_TOKEN")
			require.NoError(t, err)
			parsed, err := url.Parse(built)
			require.NoError(t, err)
			require.Equal(t, url.Values{"order_id": {"42"}, "out_trade_no": {"SERVER_ORDER"}, "resume_token": {"SERVER_TOKEN"}, "status": {"success"}}, parsed.Query())
		})
	}
}
