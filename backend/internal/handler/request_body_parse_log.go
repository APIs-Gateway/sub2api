package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

// logRequestBodyParseFailure records server-side diagnostics for request body
// parse failures while keeping client-visible responses generic and stable.
func logRequestBodyParseFailure(reqLog *zap.Logger, body []byte, err error) {
	if reqLog == nil {
		return
	}
	if err == nil {
		err = service.DescribeInvalidJSON(body)
	}

	reqLog.Warn("parse request body failed",
		zap.Error(err),
		zap.Int("body_len", len(body)),
	)
}
