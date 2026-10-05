package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// A concurrency wait may already have committed a transport-only SSE ping.
// Only local audio rejects use this writer; provider errors retain their own
// policy and attribution. The caller proves no upstream dispatch took place.
func writeChatInputAudioError(c *gin.Context, message string) {
	if c.Request != nil && c.Request.Context().Err() != nil {
		return
	}
	if c.Writer.Written() && strings.HasPrefix(c.Writer.Header().Get("Content-Type"), "text/event-stream") {
		MarkResponseCommitted(c)
		MarkOpsStreamErrorValue(c, OpsStreamError{ErrType: "invalid_request_error", Code: "invalid_request_error", Message: message, IntendedStatus: http.StatusBadRequest, RequestScoped: true})
		if _, err := fmt.Fprint(c.Writer, buildChatStreamErrorSSE("invalid_request_error", message)); err != nil {
			return
		}
		c.Writer.Flush()
		return
	}
	writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", message)
}
