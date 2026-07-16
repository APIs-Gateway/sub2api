package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTotpVerificationMethodRequiresAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/totp/verification-method", NewTotpHandler(&service.TotpService{}).GetVerificationMethod)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/totp/verification-method", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestTotpStepUpRejectsUnauthenticatedAndMalformedRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewTotpHandler(&service.TotpService{})
	router := gin.New()
	router.POST("/totp/step-up", handler.StepUp)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/totp/step-up", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	router = gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 1})
		c.Next()
	})
	router.POST("/totp/step-up", handler.StepUp)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/totp/step-up", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
