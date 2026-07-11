package handler

import (
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type ProviderPricingHandler struct {
	paymentConfigService *service.PaymentConfigService
	pricingService       *service.PricingService
	settingService       *service.SettingService
}

func NewProviderPricingHandler(paymentConfigService *service.PaymentConfigService, pricingService *service.PricingService, settingService *service.SettingService) *ProviderPricingHandler {
	return &ProviderPricingHandler{
		paymentConfigService: paymentConfigService,
		pricingService:       pricingService,
		settingService:       settingService,
	}
}

func (h *ProviderPricingHandler) GetPricing(c *gin.Context) {
	cfg, err := h.paymentConfigService.GetPaymentConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, service.HvoyProviderPricingResponse{
			SchemaVersion: service.HvoyProviderPricingSchemaVersion,
			Success:       false,
			Message:       "failed to load payment config",
		})
		return
	}

	resp := h.pricingService.BuildHvoyProviderPricing(
		cfg.BalanceRechargeMultiplier,
		h.settingService.GetSiteName(c.Request.Context()),
		h.settingService.GetFrontendURL(c.Request.Context()),
		time.Now(),
	)
	c.JSON(http.StatusOK, resp)
}
