package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const accountTestLogKey = "sub2api.account_test.log_context"
const accountTestBackgroundKey = "sub2api.account_test.background"

type accountTestLogContext struct {
	accountID      int64
	platform       string
	requestedModel string
	model          string
	source         string
}

// Each invocation owns its metadata. The scheduled runner shares its service and
// parent context between concurrent accounts, so identity must never live there.
func beginAccountTestLogging(c *gin.Context, accountID int64, requestedModel string) {
	if c == nil {
		return
	}
	source := "manual"
	if background, _ := c.Get(accountTestBackgroundKey); background == true {
		source = "background"
	}
	c.Set(accountTestLogKey, accountTestLogContext{accountID: accountID, requestedModel: requestedModel, source: source})
}

func getAccountTestLogContext(c *gin.Context) accountTestLogContext {
	if c != nil {
		if value, ok := c.Get(accountTestLogKey); ok {
			if metadata, ok := value.(accountTestLogContext); ok {
				return metadata
			}
		}
	}
	return accountTestLogContext{}
}

func setAccountTestLogPlatform(c *gin.Context, platform string) {
	if c == nil {
		return
	}
	metadata := getAccountTestLogContext(c)
	metadata.platform = platform
	c.Set(accountTestLogKey, metadata)
}

func setAccountTestLogModel(c *gin.Context, model string) {
	if c == nil {
		return
	}
	metadata := getAccountTestLogContext(c)
	metadata.model = model
	c.Set(accountTestLogKey, metadata)
}

func accountTestFailureLogger(c *gin.Context) *zap.Logger {
	var ctx context.Context
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	metadata := getAccountTestLogContext(c)
	fields := []zap.Field{zap.String("component", "service.account_test")}
	if metadata.accountID > 0 {
		fields = append(fields, zap.Int64("account_id", metadata.accountID))
	}
	if metadata.platform != "" {
		fields = append(fields, zap.String("platform", metadata.platform))
	}
	if metadata.requestedModel != "" {
		fields = append(fields, zap.String("requested_model", metadata.requestedModel))
	}
	if metadata.model != "" {
		fields = append(fields, zap.String("model", metadata.model))
	}
	if metadata.source != "" {
		fields = append(fields, zap.String("test_source", metadata.source))
	}
	return logger.FromContext(ctx).With(fields...)
}

func accountTestBackgroundContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	requestID := uuid.NewString()
	requestLogger := logger.FromContext(ctx).With(zap.String("request_id", requestID), zap.String("client_request_id", ""))
	ctx = context.WithValue(ctx, ctxkey.RequestID, requestID)
	return logger.IntoContext(ctx, requestLogger)
}
