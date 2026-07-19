package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIngressRejectReasonRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	_, ok := GetIngressRejectReason(c)
	require.False(t, ok)

	MarkIngressRejected(c, IngressRejectInvalidAPIKey)
	reason, ok := GetIngressRejectReason(c)
	require.True(t, ok)
	require.Equal(t, IngressRejectInvalidAPIKey, reason)
}

func TestMarkIngressRejectedIgnoresEmptyReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	MarkIngressRejected(c, "")
	_, ok := GetIngressRejectReason(c)
	require.False(t, ok)
}
