package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// runServerTimingRequest builds a tiny single-route engine with ServerTiming
// mounted, optionally seeds ContextKeyUserRole (mimicking what adminAuth
// would have already resolved by the time this middleware runs), and drives
// handler through it.
func runServerTimingRequest(t *testing.T, role string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	engine := gin.New()
	engine.Use(ServerTiming())
	engine.Any("/*path", func(c *gin.Context) {
		if role != "" {
			c.Set(string(ContextKeyUserRole), role)
		}
		handler(c)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/dashboard", nil)
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestServerTimingEmitsHeaderForAdminRole(t *testing.T) {
	recorder := runServerTimingRequest(t, "admin", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	header := recorder.Header().Get(servertiming.HeaderName)
	if header == "" {
		t.Fatal("expected Server-Timing header for admin-role request, got none")
	}
	if !strings.Contains(header, "total;dur=") || !strings.Contains(header, `cache;desc="bypass"`) {
		t.Fatalf("incomplete timing header: %q", header)
	}
}

func TestServerTimingWithholdsHeaderForNonAdminRole(t *testing.T) {
	tests := []struct {
		name string
		role string
	}{
		{name: "user role", role: "user"},
		{name: "no role resolved", role: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := runServerTimingRequest(t, tt.role, func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"ok": true})
			})
			if got := recorder.Header().Get(servertiming.HeaderName); got != "" {
				t.Fatalf("unexpected Server-Timing header for role %q: %q", tt.role, got)
			}
		})
	}
}

func TestServerTimingCollectorIsRequestScoped(t *testing.T) {
	active := false
	recorder := runServerTimingRequest(t, "admin", func(c *gin.Context) {
		active = servertiming.Active(c.Request.Context())
		c.Status(http.StatusNoContent)
	})
	if !active {
		t.Fatal("collector was not attached to the request context")
	}
	if recorder.Header().Get(servertiming.HeaderName) == "" {
		t.Fatal("timing header missing from status-only response")
	}
}

func TestServerTimingFinalizesBeforeEarlyCommit(t *testing.T) {
	recorder := runServerTimingRequest(t, "admin", func(c *gin.Context) {
		c.Status(http.StatusAccepted)
		c.Writer.WriteHeaderNow()
	})
	if got := recorder.Header().Get(servertiming.HeaderName); got == "" {
		t.Fatal("timing header was not written before response commit")
	}
}

func TestServerTimingFinalizesOnFlush(t *testing.T) {
	recorder := runServerTimingRequest(t, "admin", func(c *gin.Context) {
		c.Writer.Flush()
	})
	if got := recorder.Header().Get(servertiming.HeaderName); got == "" {
		t.Fatal("timing header was not written before stream flush")
	}
}

func TestServerTimingStatusResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "not modified", status: http.StatusNotModified},
		{name: "internal error", status: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := runServerTimingRequest(t, "admin", func(c *gin.Context) {
				c.Status(tt.status)
			})
			if recorder.Code != tt.status {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.status)
			}
			if got := recorder.Header().Get(servertiming.HeaderName); got == "" {
				t.Fatalf("timing header missing from status %d response", tt.status)
			}
		})
	}
}

func TestServerTimingResponseWriterUnwraps(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	baseWriter := c.Writer
	writer := &serverTimingResponseWriter{ResponseWriter: baseWriter}
	if got := writer.Unwrap(); got != baseWriter {
		t.Fatalf("Unwrap() = %T, want original Gin writer", got)
	}
}

func TestServerTimingHeaderValueNilSafety(t *testing.T) {
	if got := serverTimingHeaderValue(nil); got != "" {
		t.Fatalf("serverTimingHeaderValue(nil) = %q, want empty", got)
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if got := serverTimingHeaderValue(c); got != "" {
		t.Fatalf("serverTimingHeaderValue with nil request = %q, want empty", got)
	}
}

// TestServerTimingNotMountedOnOtherRouteGroups verifies, at the routing
// level, that a group without ServerTiming registered never receives the
// header even when nothing else distinguishes the two requests -- i.e. the
// admin-only activation is a property of where the middleware is mounted,
// not of any request content.
func TestServerTimingNotMountedOnOtherRouteGroups(t *testing.T) {
	engine := gin.New()
	v1 := engine.Group("/api/v1")

	admin := v1.Group("/admin")
	admin.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyUserRole), "admin")
		c.Next()
	})
	admin.Use(ServerTiming())
	admin.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	user := v1.Group("/user")
	user.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyUserRole), "admin") // even if somehow admin-scoped, header must not appear
		c.Next()
	})
	user.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	adminRecorder := httptest.NewRecorder()
	engine.ServeHTTP(adminRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/ping", nil))
	if adminRecorder.Header().Get(servertiming.HeaderName) == "" {
		t.Fatal("expected Server-Timing header on the admin group route")
	}

	userRecorder := httptest.NewRecorder()
	engine.ServeHTTP(userRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/user/ping", nil))
	if got := userRecorder.Header().Get(servertiming.HeaderName); got != "" {
		t.Fatalf("user route group must not receive Server-Timing header, got %q", got)
	}
}
