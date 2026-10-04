package handler

import (
	"context"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type billingInflightGateway interface {
	ReserveBillingInflight(context.Context, service.BillingInflightRequest) (*service.BillingInflightLease, error)
}

func finishBillingInflightHTTP(c *gin.Context) {
	service.BillingInflightLeaseFromContext(c.Request.Context()).HandlerDone()
}

// Account selection and its effective pricing fields precede admission. A retry
// resizes the same logical lease, rather than consuming a second owner slot.
func reserveBillingInflightHTTP(c *gin.Context, gateway billingInflightGateway, request service.BillingInflightRequest, release func(), respond func(int, string, string)) bool {
	lease, err := gateway.ReserveBillingInflight(c.Request.Context(), request)
	if err != nil {
		if release != nil {
			release()
		}
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		respond(status, code, message)
		return false
	}
	if lease != nil {
		c.Request = c.Request.WithContext(service.WithBillingInflightLease(c.Request.Context(), lease))
		lease.MarkDispatched()
	}
	return true
}

func billingInflightForwardContext(c *gin.Context, ctx context.Context) context.Context {
	if lease := service.BillingInflightLeaseFromContext(c.Request.Context()); lease != nil {
		return service.WithBillingInflightLease(ctx, lease)
	}
	return ctx
}
