package http2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/imroc/req/v3/internal/transport"
	xhttp2 "golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// No new flow types are referenced here: the same wire file is the old-version
// behavioral witness. A setup error, timeout or compilation failure is not red.
type reqSecurityEvent struct {
	kind xhttp2.FrameType
	id   uint32
	ack  bool
	code xhttp2.ErrCode
	data []byte
	err  error
}
type reqSecurityPeer struct {
	t      *testing.T
	cc     *ClientConn
	conn   net.Conn
	write  *xhttp2.Framer
	events chan reqSecurityEvent
	done   chan struct{}
}
type reqSecurityResult struct {
	response *http.Response
	body     []byte
	err      error
}
type reqSecurityCall struct {
	id     uint32
	body   *io.PipeWriter
	cancel context.CancelFunc
	result chan reqSecurityResult
	done   chan struct{}
}

func newReqSecurityPeer(t *testing.T, initial uint32) *reqSecurityPeer {
	t.Helper()
	client, server := net.Pipe()
	deadline := time.Now().Add(8 * time.Second)
	client.SetDeadline(deadline)
	server.SetDeadline(deadline)
	p := &reqSecurityPeer{t: t, conn: server, write: xhttp2.NewFramer(server, bytes.NewReader(nil)), events: make(chan reqSecurityEvent, 128), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		preface := make([]byte, len(xhttp2.ClientPreface))
		if _, err := io.ReadFull(server, preface); err != nil {
			p.events <- reqSecurityEvent{err: err}
			return
		}
		if string(preface) != xhttp2.ClientPreface {
			p.events <- reqSecurityEvent{err: fmt.Errorf("wrong client preface")}
			return
		}
		reader := xhttp2.NewFramer(io.Discard, server)
		for {
			f, err := reader.ReadFrame()
			if err != nil {
				p.events <- reqSecurityEvent{err: err}
				return
			}
			e := reqSecurityEvent{kind: f.Header().Type, id: f.Header().StreamID}
			switch f := f.(type) {
			case *xhttp2.SettingsFrame:
				e.ack = f.IsAck()
			case *xhttp2.PingFrame:
				e.ack = f.IsAck()
				e.data = append([]byte(nil), f.Data[:]...)
			case *xhttp2.DataFrame:
				e.data = append([]byte(nil), f.Data()...)
			case *xhttp2.GoAwayFrame:
				e.code = f.ErrCode
			case *xhttp2.RSTStreamFrame:
				e.code = f.ErrCode
			}
			select {
			case p.events <- e:
			case <-time.After(8 * time.Second):
				return
			}
		}
	}()
	t.Cleanup(func() {
		client.Close()
		server.Close()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			t.Error("peer reader did not join")
		}
		if p.cc != nil {
			select {
			case <-p.cc.readerDone:
			case <-time.After(2 * time.Second):
				t.Error("private transport reader did not join")
			}
		}
	})
	var err error
	p.cc, err = (&Transport{Options: &transport.Options{}}).newClientConn(client, false)
	if err != nil {
		t.Fatalf("private transport setup: %v", err)
	}
	p.must(p.write.WriteSettings(xhttp2.Setting{ID: xhttp2.SettingInitialWindowSize, Val: initial}))
	p.wait(func(e reqSecurityEvent) bool { return e.kind == xhttp2.FrameSettings && e.ack })
	return p
}
func (p *reqSecurityPeer) must(err error) {
	p.t.Helper()
	if err != nil {
		p.t.Fatalf("peer frame setup/write: %v", err)
	}
}
func (p *reqSecurityPeer) wait(match func(reqSecurityEvent) bool) reqSecurityEvent {
	p.t.Helper()
	guard := time.NewTimer(5 * time.Second)
	defer guard.Stop()
	for {
		select {
		case e := <-p.events:
			if e.err != nil {
				p.t.Fatalf("unexpected peer read error: %v", e.err)
			}
			if match(e) {
				return e
			}
		case <-guard.C:
			p.t.Fatal("wire fixture guard expired (not behavioral red evidence)")
		}
	}
}
func (p *reqSecurityPeer) settings(n uint32) {
	p.t.Helper()
	p.must(p.write.WriteSettings(xhttp2.Setting{ID: xhttp2.SettingInitialWindowSize, Val: n}))
	p.wait(func(e reqSecurityEvent) bool { return e.kind == xhttp2.FrameSettings && e.ack })
}
func (p *reqSecurityPeer) barrier() {
	p.t.Helper()
	key := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	p.must(p.write.WritePing(false, key))
	p.wait(func(e reqSecurityEvent) bool {
		return e.kind == xhttp2.FramePing && e.ack && bytes.Equal(e.data, key[:])
	})
}
func (p *reqSecurityPeer) start(method string, withBody bool) *reqSecurityCall {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	c := &reqSecurityCall{cancel: cancel, result: make(chan reqSecurityResult, 1), done: make(chan struct{})}
	var body io.Reader
	var reader *io.PipeReader
	if withBody {
		reader, c.body = io.Pipe()
		body = reader
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://fixture.invalid/security", body)
	if err != nil {
		p.t.Fatal(err)
	}
	go func() {
		defer close(c.done)
		res, err := p.cc.RoundTrip(req)
		var b []byte
		if err == nil {
			b, err = io.ReadAll(res.Body)
			res.Body.Close()
		}
		c.result <- reqSecurityResult{response: res, body: b, err: err}
	}()
	p.t.Cleanup(func() {
		cancel()
		if c.body != nil {
			c.body.Close()
			reader.Close()
		}
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
			p.t.Error("request/body reader did not join")
		}
	})
	c.id = p.wait(func(e reqSecurityEvent) bool { return e.kind == xhttp2.FrameHeaders }).id
	return c
}
func (p *reqSecurityPeer) sendByte(c *reqSecurityCall) {
	p.t.Helper()
	done := make(chan error, 1)
	go func() { _, err := c.body.Write([]byte("x")); done <- err }()
	p.t.Cleanup(func() {
		c.body.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			p.t.Error("body producer did not join")
		}
	})
}
func (p *reqSecurityPeer) response(id uint32, fields []hpack.HeaderField, body string) {
	p.t.Helper()
	var b bytes.Buffer
	enc := hpack.NewEncoder(&b)
	p.must(enc.WriteField(hpack.HeaderField{Name: ":status", Value: "200"}))
	for _, f := range fields {
		p.must(enc.WriteField(f))
	}
	p.must(p.write.WriteHeaders(xhttp2.HeadersFrameParam{StreamID: id, BlockFragment: b.Bytes(), EndHeaders: true, EndStream: body == ""}))
	if body != "" {
		p.must(p.write.WriteData(id, true, []byte(body)))
	}
}
func (p *reqSecurityPeer) result(c *reqSecurityCall) reqSecurityResult {
	p.t.Helper()
	select {
	case r := <-c.result:
		return r
	case <-time.After(5 * time.Second):
		p.t.Fatal("request result guard expired (not behavioral red evidence)")
		return reqSecurityResult{}
	}
}
func (p *reqSecurityPeer) readerFlowControlClose() {
	p.t.Helper()
	// Reader-origin ConnectionError follows the original upstream close path;
	// its buffered GOAWAY is not guaranteed on the wire. Require actual peer
	// closure AND the joined private reader's precise error, not a timeout.
	guard := time.NewTimer(5 * time.Second)
	defer guard.Stop()
	for {
		select {
		case e := <-p.events:
			if e.err == nil {
				continue
			}
			if !errors.Is(e.err, io.EOF) {
				p.t.Fatalf("reader flow error peer closure: %v", e.err)
			}
			select {
			case <-p.cc.readerDone:
			case <-guard.C:
				p.t.Fatal("private reader did not join")
			}
			var flowError ConnectionError
			if !errors.As(p.cc.readerErr, &flowError) || flowError != ConnectionError(ErrCodeFlowControl) {
				p.t.Fatalf("reader error type: %T %v", p.cc.readerErr, p.cc.readerErr)
			}
			return
		case <-guard.C:
			p.t.Fatal("reader closure guard expired (not behavioral red evidence)")
		}
	}
}

func TestReqHTTP2Security_LazySettingsOverflowGOAWAY(t *testing.T) {
	p := newReqSecurityPeer(t, 65535)
	c := p.start("POST", true)
	other := p.start("POST", true)
	p.must(p.write.WriteWindowUpdate(c.id, 1000))
	p.settings(math.MaxInt32 - 1000 + 1)
	p.sendByte(c)
	e := p.wait(func(e reqSecurityEvent) bool { return e.kind == xhttp2.FrameGoAway || e.kind == xhttp2.FrameData })
	if e.kind != xhttp2.FrameGoAway || e.code != xhttp2.ErrCodeFlowControl {
		t.Fatalf("SECURITY_WIRE_ASSERT: overflowing stream emitted %v code=%v data=%q; want GOAWAY FLOW_CONTROL_ERROR", e.kind, e.code, e.data)
	}
	if r := p.result(c); r.err == nil {
		t.Fatal("overflow request unexpectedly succeeded")
	}
	if r := p.result(other); r.err == nil {
		t.Fatal("another request survived connection overflow")
	}
}
func TestReqHTTP2Security_ConnectionWindowUpdateOverflow(t *testing.T) {
	p := newReqSecurityPeer(t, 0)
	a := p.start("POST", true)
	b := p.start("POST", true)
	p.sendByte(a)
	p.sendByte(b)
	p.must(p.write.WriteWindowUpdate(0, math.MaxInt32-65535))
	p.must(p.write.WriteWindowUpdate(0, 1))
	p.readerFlowControlClose()
	if r := p.result(a); r.err == nil {
		t.Fatal("first blocked writer survived connection error")
	}
	if r := p.result(b); r.err == nil {
		t.Fatal("second blocked writer survived connection error")
	}
}
func TestReqHTTP2Security_InvalidInitialWindowSetting(t *testing.T) {
	p := newReqSecurityPeer(t, 65535)
	p.must(p.write.WriteSettings(xhttp2.Setting{ID: xhttp2.SettingInitialWindowSize, Val: uint32(math.MaxInt32) + 1}))
	p.readerFlowControlClose()
}
func TestReqHTTP2Security_LegalSettingsBoundary(t *testing.T) {
	p := newReqSecurityPeer(t, 65535)
	c := p.start("POST", true)
	p.must(p.write.WriteWindowUpdate(c.id, 1000))
	p.settings(math.MaxInt32 - 1000)
	p.sendByte(c)
	e := p.wait(func(e reqSecurityEvent) bool { return e.kind == xhttp2.FrameData || e.kind == xhttp2.FrameGoAway })
	if e.kind != xhttp2.FrameData || e.id != c.id || string(e.data) != "x" {
		t.Fatalf("legal boundary: %+v", e)
	}
	c.body.Close()
	p.response(c.id, nil, "")
	if r := p.result(c); r.err != nil {
		t.Fatal(r.err)
	}
}
func TestReqHTTP2Security_StreamUpdateOverflowKeepsConnection(t *testing.T) {
	p := newReqSecurityPeer(t, 0)
	c := p.start("POST", true)
	p.must(p.write.WriteWindowUpdate(c.id, math.MaxInt32))
	p.must(p.write.WriteWindowUpdate(c.id, 1))
	e := p.wait(func(e reqSecurityEvent) bool { return e.kind == xhttp2.FrameRSTStream || e.kind == xhttp2.FrameGoAway })
	if e.kind != xhttp2.FrameRSTStream || e.id != c.id || e.code != xhttp2.ErrCodeFlowControl {
		t.Fatalf("stream overflow: %+v", e)
	}
	if r := p.result(c); r.err == nil {
		t.Fatal("overflow stream succeeded")
	}
	p.must(p.write.WriteWindowUpdate(c.id, 1))
	p.barrier()
	next := p.start("GET", false)
	p.response(next.id, nil, "ok")
	if r := p.result(next); r.err != nil || string(r.body) != "ok" {
		t.Fatalf("connection unusable after stream error: %+v", r)
	}
}
func TestReqHTTP2Security_LowerNegativeWindowAndNewStream(t *testing.T) {
	p := newReqSecurityPeer(t, 1)
	a := p.start("POST", true)
	p.sendByte(a)
	p.wait(func(e reqSecurityEvent) bool { return e.kind == xhttp2.FrameData && e.id == a.id })
	p.settings(0)
	p.sendByte(a)
	p.barrier()
	p.settings(1)
	b := p.start("POST", true)
	p.sendByte(b)
	e := p.wait(func(e reqSecurityEvent) bool { return e.kind == xhttp2.FrameData })
	if e.id != b.id {
		t.Fatalf("consumed stream sent while zero quota: %+v", e)
	}
	p.settings(2)
	e = p.wait(func(e reqSecurityEvent) bool { return e.kind == xhttp2.FrameData })
	if e.id != a.id || string(e.data) != "x" {
		t.Fatalf("raised window did not wake old stream: %+v", e)
	}
	a.body.Close()
	b.body.Close()
	p.response(a.id, nil, "")
	p.response(b.id, nil, "")
	if r := p.result(a); r.err != nil {
		t.Fatal(r.err)
	}
	if r := p.result(b); r.err != nil {
		t.Fatal(r.err)
	}
}
func TestReqHTTP2Security_ConcurrentCancelAndClosedUpdates(t *testing.T) {
	p := newReqSecurityPeer(t, 0)
	calls := []*reqSecurityCall{p.start("POST", true), p.start("POST", true), p.start("POST", true)}
	for _, c := range calls {
		p.sendByte(c)
	}
	for _, c := range calls {
		c.cancel()
	}
	seen := map[uint32]bool{}
	for len(seen) < len(calls) {
		e := p.wait(func(e reqSecurityEvent) bool { return e.kind == xhttp2.FrameRSTStream })
		seen[e.id] = true
	}
	for _, c := range calls {
		if r := p.result(c); r.err == nil {
			t.Fatal("canceled stream succeeded")
		}
		p.must(p.write.WriteWindowUpdate(c.id, 1))
	}
	p.barrier()
	next := p.start("GET", false)
	p.response(next.id, nil, "ok")
	if r := p.result(next); r.err != nil || string(r.body) != "ok" {
		t.Fatalf("connection after cancellation: %+v", r)
	}
}

func TestReqHTTP2Security_ResponseFramingHeaders(t *testing.T) {
	tests := []struct {
		name   string
		fields []hpack.HeaderField
		length int64
		wantCL []string
	}{
		{"connection", []hpack.HeaderField{{Name: "connection", Value: "keep-alive"}}, -1, nil},
		{"proxy_connection", []hpack.HeaderField{{Name: "proxy-connection", Value: "keep-alive"}}, -1, nil},
		{"keep_alive", []hpack.HeaderField{{Name: "keep-alive", Value: "timeout=5"}}, -1, nil},
		{"transfer_encoding", []hpack.HeaderField{{Name: "transfer-encoding", Value: "chunked"}}, -1, nil},
		{"upgrade", []hpack.HeaderField{{Name: "upgrade", Value: "h2c"}}, -1, nil},
		{"same_length", []hpack.HeaderField{{Name: "content-length", Value: "2"}, {Name: "content-length", Value: "2"}}, 2, []string{"2"}},
		{"different_length", []hpack.HeaderField{{Name: "content-length", Value: "2"}, {Name: "content-length", Value: "3"}}, -1, nil},
		{"invalid_length", []hpack.HeaderField{{Name: "content-length", Value: "x"}}, -1, nil},
		{"negative_length", []hpack.HeaderField{{Name: "content-length", Value: "-1"}}, -1, nil},
		{"overflow_length", []hpack.HeaderField{{Name: "content-length", Value: "9223372036854775808"}}, -1, nil},
		{"comma_length", []hpack.HeaderField{{Name: "content-length", Value: "2, 2"}}, -1, nil},
		{"empty_length", []hpack.HeaderField{{Name: "content-length", Value: ""}}, -1, nil},
		{"valid_length", []hpack.HeaderField{{Name: "content-length", Value: "2"}}, 2, []string{"2"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p := newReqSecurityPeer(t, 65535)
			c := p.start("GET", false)
			fields := append(append([]hpack.HeaderField(nil), test.fields...), hpack.HeaderField{Name: "x-unknown", Value: "preserved"})
			p.response(c.id, fields, "ok")
			r := p.result(c)
			if r.err != nil {
				t.Fatal(r.err)
			}
			for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Transfer-Encoding", "Upgrade"} {
				if v := r.response.Header.Values(name); len(v) != 0 {
					t.Fatalf("SECURITY_WIRE_ASSERT: forbidden %s survived: %q", name, v)
				}
			}
			if got := r.response.Header.Values("Content-Length"); strings.Join(got, "|") != strings.Join(test.wantCL, "|") || len(got) != len(test.wantCL) {
				t.Fatalf("SECURITY_WIRE_ASSERT: Content-Length got%q want%q", got, test.wantCL)
			}
			if r.response.ContentLength != test.length || string(r.body) != "ok" {
				t.Fatalf("length/body got%d %q", r.response.ContentLength, r.body)
			}
			if r.response.Header.Get("X-Unknown") != "preserved" {
				t.Fatal("unrelated response header lost")
			}
		})
	}
	t.Run("head_keeps_valid_length", func(t *testing.T) {
		p := newReqSecurityPeer(t, 65535)
		c := p.start("HEAD", false)
		p.response(c.id, []hpack.HeaderField{{Name: "content-length", Value: "2"}}, "")
		r := p.result(c)
		if r.err != nil || r.response.ContentLength != 2 || len(r.body) != 0 {
			t.Fatalf("HEAD: %+v", r)
		}
	})
	t.Run("end_stream_unknown_length_is_zero", func(t *testing.T) {
		p := newReqSecurityPeer(t, 65535)
		c := p.start("GET", false)
		p.response(c.id, nil, "")
		r := p.result(c)
		if r.err != nil || r.response.ContentLength != 0 {
			t.Fatalf("END_STREAM: %+v", r)
		}
	})
	t.Run("valid_body_and_trailer", func(t *testing.T) {
		p := newReqSecurityPeer(t, 65535)
		c := p.start("GET", false)
		var block bytes.Buffer
		encoder := hpack.NewEncoder(&block)
		p.must(encoder.WriteField(hpack.HeaderField{Name: ":status", Value: "200"}))
		p.must(encoder.WriteField(hpack.HeaderField{Name: "trailer", Value: "x-end"}))
		p.must(p.write.WriteHeaders(xhttp2.HeadersFrameParam{StreamID: c.id, EndHeaders: true, BlockFragment: block.Bytes()}))
		p.must(p.write.WriteData(c.id, false, []byte("ok")))
		block.Reset()
		p.must(encoder.WriteField(hpack.HeaderField{Name: "x-end", Value: "retained"}))
		p.must(p.write.WriteHeaders(xhttp2.HeadersFrameParam{StreamID: c.id, EndHeaders: true, EndStream: true, BlockFragment: block.Bytes()}))
		r := p.result(c)
		if r.err != nil || string(r.body) != "ok" || r.response.Trailer.Get("X-End") != "retained" {
			t.Fatalf("body/trailer lost: %+v", r)
		}
	})
}
