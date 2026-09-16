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
	groupRepo            service.GroupRepository
}

func NewProviderPricingHandler(paymentConfigService *service.PaymentConfigService, pricingService *service.PricingService, settingService *service.SettingService, groupRepo service.GroupRepository) *ProviderPricingHandler {
	return &ProviderPricingHandler{
		paymentConfigService: paymentConfigService,
		pricingService:       pricingService,
		settingService:       settingService,
		groupRepo:            groupRepo,
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

	groupMultipliers, err := service.LoadHvoyProviderGroupMultipliers(c.Request.Context(), h.groupRepo)
	if err != nil {
		c.JSON(http.StatusOK, service.HvoyProviderPricingResponse{
			SchemaVersion: service.HvoyProviderPricingSchemaVersion,
			Success:       false,
			Message:       "failed to load groups",
		})
		return
	}

	resp := h.pricingService.BuildHvoyProviderPricing(
		cfg.BalanceRechargeMultiplier,
		groupMultipliers,
		h.settingService.GetSiteName(c.Request.Context()),
		h.settingService.GetFrontendURL(c.Request.Context()),
		time.Now(),
	)
	c.JSON(http.StatusOK, resp)
}
