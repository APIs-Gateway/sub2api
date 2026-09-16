package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// serverTimingResponseWriter defers writing the Server-Timing response
// header until the response is actually about to be committed (first
// Write/WriteHeaderNow/Flush call), so that handlers may keep adding timing
// spans up until the last possible moment.
type serverTimingResponseWriter struct {
	gin.ResponseWriter
	context *gin.Context
	once    sync.Once
}

// Unwrap lets Go's http.ResponseController see through the wrapper (e.g. for
// Hijack/SetWriteDeadline support used elsewhere in the stack).
func (w *serverTimingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// ServerTiming collects per-request timing spans and renders them as a
// Server-Timing response header (https://www.w3.org/TR/server-timing/).
//
// Scope: this middleware is intended to be mounted on the admin route group
// only (see routes.RegisterAdminRoutes) -- it is not applied to gateway or
// authenticated user web API routes. As defense in depth against future
// mounting mistakes, it additionally verifies the resolved caller has the
// admin role before it starts collecting or renders anything, so mounting it
// on the wrong route group degrades to a silent no-op rather than leaking
// timing information.
func ServerTiming() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}

		collector := servertiming.New(time.Now())
		c.Request = c.Request.WithContext(servertiming.WithCollector(c.Request.Context(), collector))
		writer := &serverTimingResponseWriter{
			ResponseWriter: c.Writer,
			context:        c,
		}
		c.Writer = writer
		c.Next()
		writer.finalize()
	}
}

func (w *serverTimingResponseWriter) WriteHeaderNow() {
	w.finalize()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *serverTimingResponseWriter) Write(data []byte) (int, error) {
	w.finalize()
	return w.ResponseWriter.Write(data)
}

func (w *serverTimingResponseWriter) WriteString(data string) (int, error) {
	w.finalize()
	return w.ResponseWriter.WriteString(data)
}

func (w *serverTimingResponseWriter) Flush() {
	w.finalize()
	w.ResponseWriter.Flush()
}

func (w *serverTimingResponseWriter) finalize() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		if value := serverTimingHeaderValue(w.context); value != "" {
			w.ResponseWriter.Header().Set(servertiming.HeaderName, value)
		}
	})
}

// serverTimingHeaderValue renders the timing header, gated on the caller
// actually being an authenticated admin. adminAuth (and AdminComplianceGuard)
// run ahead of this middleware in the admin route group, so role is already
// resolved by the time this executes.
func serverTimingHeaderValue(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	role, ok := GetUserRoleFromContext(c)
	if !ok || role != service.RoleAdmin {
		return ""
	}
	return servertiming.HeaderValue(c.Request.Context(), time.Now(), "")
}
