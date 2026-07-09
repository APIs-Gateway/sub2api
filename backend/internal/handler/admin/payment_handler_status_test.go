//go:build unit

package admin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOrderStatuses(t *testing.T) {
	require.Nil(t, parseOrderStatuses(""))
	require.Nil(t, parseOrderStatuses("COMPLETED"))
	require.Equal(t, []string{"REFUND_REQUESTED", "REFUNDING", "REFUNDED"}, parseOrderStatuses(" REFUND_REQUESTED,REFUNDING, , REFUNDED "))
}
