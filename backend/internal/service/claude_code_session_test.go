package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClaudeCodeSessionIDFromHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.Empty(t, ClaudeCodeSessionIDFromHeader(nil))

	newContext := func(value string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		if value != "" {
			c.Request.Header.Set("X-Claude-Code-Session-Id", value)
		}
		return c
	}

	emptyRequest, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Empty(t, ClaudeCodeSessionIDFromHeader(emptyRequest))

	require.Empty(t, ClaudeCodeSessionIDFromHeader(newContext("")))
	require.Equal(t, "claude-session-001", ClaudeCodeSessionIDFromHeader(newContext("  claude-session-001  ")))
	require.Empty(t, ClaudeCodeSessionIDFromHeader(newContext("   ")))
	require.Empty(t, ClaudeCodeSessionIDFromHeader(newContext("bad\tsession")))
	require.Empty(t, ClaudeCodeSessionIDFromHeader(newContext("bad\x7fsession")))
	require.Empty(t, ClaudeCodeSessionIDFromHeader(newContext("bad\xffsession")))
	require.Equal(t, strings.Repeat("a", 255), ClaudeCodeSessionIDFromHeader(newContext(strings.Repeat("a", 255))))
	require.Empty(t, ClaudeCodeSessionIDFromHeader(newContext(strings.Repeat("a", 256))))
}
