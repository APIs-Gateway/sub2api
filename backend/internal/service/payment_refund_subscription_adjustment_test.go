//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRefundSubscriptionAdjustmentSnapshotReference(t *testing.T) {
	for _, body := range []string{
		`{"subscriptionAdjustmentID":1,"SubscriptionAdjustmentID":0}`,
		`{"subscriptionAdjustmentID":1,"SubscriptionAdjustmentID":null}`,
		`{"SubscriptionAdjustmentID":1}`,
		`{"subscriptionAdjustmentID":1,"subscriptionAdjustmentID":0}`,
		`{"subscriptionAdjustmentID":null}`,
		`{"subscriptionAdjustmentID":0}`,
		`{"subscriptionAdjustmentID":-1}`,
		`{"subscriptionAdjustmentID":"1"}`,
		`{"subscriptionAdjustmentID":1.5}`,
		`{"subscriptionAdjustmentID":9223372036854775808}`,
	} {
		t.Run(body, func(t *testing.T) {
			var detail refundPendingAuditDetail
			require.Error(t, decodeRefundPendingSnapshot(body, &detail))
		})
	}
	var detail refundPendingAuditDetail
	require.NoError(t, decodeRefundPendingSnapshot(`{"subscriptionAdjustmentID":9007199254740993}`, &detail))
	require.Equal(t, int64(9007199254740993), detail.SubscriptionAdjustmentID, "must not round through a JSON float64")
	require.NoError(t, decodeRefundPendingSnapshot(`{"subscriptionAdjustmentID":9223372036854775807}`, &detail))
	require.Equal(t, int64(9223372036854775807), detail.SubscriptionAdjustmentID)
	detail = refundPendingAuditDetail{}
	require.NoError(t, decodeRefundPendingSnapshot(`{"deductionType":"subscription","subscriptionID":3}`, &detail))
	require.Zero(t, detail.SubscriptionAdjustmentID, "legacy absence remains legacy")
}
