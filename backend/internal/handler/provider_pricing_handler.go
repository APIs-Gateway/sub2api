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
	quoter               *service.PriceQuoter
}

func NewProviderPricingHandler(paymentConfigService *service.PaymentConfigService, pricingService *service.PricingService, settingService *service.SettingService, groupRepo service.GroupRepository, quoter *service.PriceQuoter) *ProviderPricingHandler {
	return &ProviderPricingHandler{
		paymentConfigService: paymentConfigService,
		pricingService:       pricingService,
		settingService:       settingService,
		groupRepo:            groupRepo,
		quoter:               quoter,
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

	// 一个空接口值（groupRepo 为 nil 的场景）不能直接传进去，否则 ListActive 会对 nil 解引用。
	var lister service.HvoyProviderGroupLister
	if h.groupRepo != nil {
		lister = h.groupRepo
	}
	resp, err := h.pricingService.BuildHvoyProviderPricingQuoted(
		c.Request.Context(),
		h.quoter,
		lister,
		cfg.BalanceRechargeMultiplier,
		h.settingService.GetSiteName(c.Request.Context()),
		h.settingService.GetFrontendURL(c.Request.Context()),
		time.Now(),
	)
	if err != nil {
		c.JSON(http.StatusOK, service.HvoyProviderPricingResponse{
			SchemaVersion: service.HvoyProviderPricingSchemaVersion,
			Success:       false,
			Message:       "failed to load groups",
		})
		return
	}
	c.JSON(http.StatusOK, resp)
}
