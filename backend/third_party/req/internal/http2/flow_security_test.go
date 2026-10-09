package http2

import (
	"math"
	"testing"
)

// These arithmetic tests exercise the actual private implementation. The wire
// witness lives in transport_security_test.go and also compiles with old req.
func TestReqHTTP2Security_FlowArithmetic(t *testing.T) {
	t.Run("shared_initial_and_new_stream", func(t *testing.T) {
		var conn connOutflow
		conn.init()
		a := outflow{conn: &conn}
		a.take(3)
		if !conn.changeInitialWindowSize(2) {
			t.Fatal("valid window rejected")
		}
		if n, ok := a.available(); !ok || n != -1 {
			t.Fatalf("consumed stream: %d %v", n, ok)
		}
		b := outflow{conn: &conn}
		if n, ok := b.available(); !ok || n != 2 {
			t.Fatalf("new stream: %d %v", n, ok)
		}
		if !conn.changeInitialWindowSize(7) {
			t.Fatal("valid window rejected")
		}
		if n, ok := a.available(); !ok || n != 4 {
			t.Fatalf("raised stream: %d %v", n, ok)
		}
		if a.delta != -3 || b.delta != 0 {
			t.Fatal("settings changed per-stream deltas")
		}
	})
	t.Run("stream_update_limit", func(t *testing.T) {
		var conn connOutflow
		conn.init()
		conn.changeInitialWindowSize(0)
		a := outflow{conn: &conn}
		if !a.add(math.MaxInt32) || a.add(1) {
			t.Fatal("stream overflow accepted")
		}
		if conn.flowErr {
			t.Fatal("stream update incorrectly poisoned connection")
		}
		if n, ok := a.available(); !ok || n != initialWindowSize {
			t.Fatalf("connection quota: %d %v", n, ok)
		}
	})
	t.Run("lazy_settings_overflow_is_connection_error", func(t *testing.T) {
		var conn connOutflow
		conn.init()
		a := outflow{conn: &conn}
		b := outflow{conn: &conn}
		if !a.add(1000) || !conn.changeInitialWindowSize(math.MaxInt32-1000+1) {
			t.Fatal("setup rejected")
		}
		if _, ok := a.available(); ok || !conn.flowErr {
			t.Fatal("lazy overflow accepted")
		}
		if _, ok := b.available(); ok {
			t.Fatal("another stream can send after connection error")
		}
	})
	t.Run("lazy_overflow_detected_on_update", func(t *testing.T) {
		var conn connOutflow
		conn.init()
		a := outflow{conn: &conn}
		a.add(1000)
		conn.changeInitialWindowSize(math.MaxInt32 - 999)
		if a.add(1) || !conn.flowErr {
			t.Fatal("existing overflow misclassified as stream error")
		}
	})
	t.Run("legal_maximum", func(t *testing.T) {
		var conn connOutflow
		conn.init()
		a := outflow{conn: &conn}
		a.add(1000)
		if !conn.changeInitialWindowSize(math.MaxInt32 - 1000) {
			t.Fatal("legal maximum rejected")
		}
		if n, ok := a.available(); !ok || n != initialWindowSize {
			t.Fatalf("available: %d %v", n, ok)
		}
		a.take(1)
		if a.delta != 999 || conn.n != initialWindowSize-1 {
			t.Fatal("send accounting changed")
		}
	})
	t.Run("invalid_setting", func(t *testing.T) {
		var conn connOutflow
		conn.init()
		if conn.changeInitialWindowSize(int64(math.MaxInt32)+1) || !conn.flowErr {
			t.Fatal("invalid setting accepted")
		}
	})
	t.Run("connection_update_overflow", func(t *testing.T) {
		var conn connOutflow
		conn.init()
		if !conn.add(math.MaxInt32-initialWindowSize) || conn.add(1) || !conn.flowErr {
			t.Fatal("connection update overflow accepted")
		}
	})
}
