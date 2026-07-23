package routes

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// compositeTargetPlatformMiddleware resolves the concrete provider from a
// JSON model field before route selection and restores the body for handlers.
func compositeTargetPlatformMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey, ok := middleware.GetAPIKeyFromContext(c)
		if !ok || apiKey == nil || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformComposite {
			c.Next()
			return
		}
		if c.Request == nil || c.Request.Method == http.MethodGet {
			c.Next()
			return
		}

		body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
		if err != nil {
			status := http.StatusBadRequest
			message := "Failed to read request body"
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				status = http.StatusRequestEntityTooLarge
				message = "Request body is too large"
			}
			c.JSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "message": message}})
			c.Abort()
			return
		}
		resetCompositeRequestBody(c, body)

		model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
		if platform, ok := service.DetectModelPlatform(model); ok {
			c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), platform))
		}
		c.Next()
	}
}

// compositeImplicitTargetPlatformMiddleware handles native Gemini routes whose
// model is encoded in the URL instead of the request body.
func compositeImplicitTargetPlatformMiddleware(platform string) gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey, ok := middleware.GetAPIKeyFromContext(c)
		if ok && apiKey != nil && apiKey.Group != nil && apiKey.Group.Platform == service.PlatformComposite && c.Request != nil {
			c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), platform))
		}
		c.Next()
	}
}

func resetCompositeRequestBody(c *gin.Context, body []byte) {
	if c == nil || c.Request == nil {
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	c.Request.Header.Set("Content-Length", strconv.Itoa(len(body)))
}
