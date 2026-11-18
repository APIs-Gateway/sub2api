package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"golang.org/x/net/http2"
)

type codexModelsOpaqueTransportError struct{ cause error }

func (e *codexModelsOpaqueTransportError) Error() string { return "opaque transport wrapper" }
func (e *codexModelsOpaqueTransportError) Unwrap() error { return e.cause }

// Deliberately the same exported name and field shape, but a different package.
// A shape-only or Error-string recognizer must not accept this provider.
type GoAwayError struct {
	LastStreamID uint32
	ErrCode      uint32
	DebugData    string
}

func (GoAwayError) Error() string { return "opaque lookalike" }

type codexModelsCyclicTransportError struct{}

func (*codexModelsCyclicTransportError) Error() string   { return "opaque cyclic wrapper" }
func (e *codexModelsCyclicTransportError) Unwrap() error { return e }

type codexModelsNilTerminalError struct{ message string }

func (e codexModelsNilTerminalError) Error() string { return e.message }
func (codexModelsNilTerminalError) Unwrap() error   { return nil }

// Produce the error from actual TLS/ALPN HTTP/2 frames. LastStreamID is the
// received active request, so the transport cannot mask it by resending an
// unaccepted request. Closing after GOAWAY makes each real provider expose its
// terminal typed error; EOF, deadline and net.OpError alone are not proof.
func codexModelsWireGoAway(t *testing.T, private bool) error {
	t.Helper()
	seed := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := seed.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(seed.Certificate())
	seed.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{certificate}, NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	peerResult := make(chan error, 1)
	accepted := make(chan uint32, 1)
	joined := make(chan struct{})
	ownedConn := make(chan net.Conn, 1)
	go func() {
		defer close(joined)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			peerResult <- acceptErr
			return
		}
		ownedConn <- conn
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			peerResult <- err
			return
		}
		preface := make([]byte, len(http2.ClientPreface))
		if _, err := io.ReadFull(conn, preface); err != nil {
			peerResult <- err
			return
		}
		if string(preface) != http2.ClientPreface {
			peerResult <- errors.New("missing real HTTP/2 preface")
			return
		}
		tlsConn, ok := conn.(*tls.Conn)
		if !ok || tlsConn.ConnectionState().NegotiatedProtocol != "h2" {
			peerResult <- errors.New("missing real h2 ALPN")
			return
		}
		framer := http2.NewFramer(conn, conn)
		if err := framer.WriteSettings(); err != nil {
			peerResult <- err
			return
		}
		for {
			frame, err := framer.ReadFrame()
			if err != nil {
				peerResult <- err
				return
			}
			switch frame := frame.(type) {
			case *http2.SettingsFrame:
				if !frame.IsAck() {
					if err := framer.WriteSettingsAck(); err != nil {
						peerResult <- err
						return
					}
				}
			case *http2.HeadersFrame:
				if !frame.HeadersEnded() {
					peerResult <- errors.New("unexpected split test request headers")
					return
				}
				accepted <- frame.StreamID
				peerResult <- framer.WriteGoAway(frame.StreamID, http2.ErrCodeNo, []byte("wire-produced-goaway"))
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case conn := <-ownedConn:
			_ = conn.Close()
		default:
		}
		select {
		case <-joined:
		case <-time.After(6 * time.Second):
			t.Error("owned GOAWAY peer did not join")
		}
	})
	endpoint := "https://" + listener.Addr().String() + "/models"
	clientTLS := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	var resultErr error
	if private {
		client := req.NewClient().SetTLSClientConfig(clientTLS).SetTimeout(4 * time.Second).EnableForceHTTP2()
		defer client.GetClient().CloseIdleConnections()
		response, err := client.R().SetRetryCount(0).Get(endpoint)
		if response != nil && response.Response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		resultErr = err
	} else {
		protocols := new(http.Protocols)
		protocols.SetHTTP1(true)
		protocols.SetHTTP2(true)
		transport := &http.Transport{TLSClientConfig: clientTLS, Protocols: protocols, HTTP2: &http.HTTP2Config{}}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
		response, err := client.Get(endpoint)
		if response != nil {
			_ = response.Body.Close()
		}
		resultErr = err
	}
	select {
	case peerErr := <-peerResult:
		if peerErr != nil {
			t.Fatalf("GOAWAY peer failed: %v", peerErr)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("GOAWAY peer did not finish")
	}
	var streamID uint32
	select {
	case streamID = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("peer accepted no request stream")
	}
	if resultErr == nil {
		t.Fatal("actual GOAWAY request unexpectedly succeeded")
	}
	nodes, valid := codexModelsTransportErrorNodes(resultErr)
	if !valid {
		t.Fatalf("invalid actual error tree: %T", resultErr)
	}
	wantPackage, wantName := "net/http", "http2GoAwayError"
	if private {
		wantPackage, wantName = "github.com/imroc/req/v3/internal/http2", "GoAwayError"
	}
	for _, node := range nodes {
		value := reflect.ValueOf(node)
		for value.Kind() == reflect.Pointer {
			value = value.Elem()
		}
		typ := value.Type()
		if typ.PkgPath() != wantPackage || typ.Name() != wantName {
			continue
		}
		if !isPinnedCodexModelsGoAwayType(typ) {
			t.Fatalf("actual pinned GOAWAY field shape changed: %v", typ)
		}
		if value.FieldByName("LastStreamID").Uint() != uint64(streamID) || value.FieldByName("ErrCode").Uint() != 0 || value.FieldByName("DebugData").String() != "wire-produced-goaway" {
			t.Fatalf("actual GOAWAY fields do not match the accepted wire stream: %v", node)
		}
		return node
	}
	t.Fatalf("missing real %s.%s terminal error; got %T: %v", wantPackage, wantName, resultErr, resultErr)
	return nil
}

func testCodexModelsGoAwayWrappers(t *testing.T, real error) {
	t.Helper()
	value := reflect.ValueOf(real)
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	pointer := reflect.New(value.Type())
	pointer.Elem().Set(value)
	pointerError, ok := pointer.Interface().(error)
	if !ok {
		t.Fatal("actual GOAWAY pointer does not implement error")
	}
	nilTarget, ok := reflect.Zero(pointer.Type()).Interface().(error)
	if !ok {
		t.Fatal("actual nil GOAWAY pointer does not implement error")
	}
	for name, wrapped := range map[string]error{
		"opaque":             &codexModelsOpaqueTransportError{cause: real},
		"double opaque":      &codexModelsOpaqueTransportError{cause: &codexModelsOpaqueTransportError{cause: real}},
		"pointer opaque":     &codexModelsOpaqueTransportError{cause: pointerError},
		"url opaque":         &url.Error{Op: "Get", URL: "https://upstream.example/models", Err: &codexModelsOpaqueTransportError{cause: real}},
		"joined shared node": errors.Join(&codexModelsOpaqueTransportError{cause: real}, &codexModelsOpaqueTransportError{cause: real}),
	} {
		t.Run(name, func(t *testing.T) {
			if !isCodexModelsGoAwayError(wrapped) || !isRetryableCodexModelsManifestTransportError(wrapped) {
				t.Fatalf("opaque real GOAWAY was lost: %T", wrapped)
			}
			if isRetryableCodexModelsManifestTransportError(errors.Join(wrapped, context.Canceled)) {
				t.Fatal("GOAWAY overrode cancellation priority")
			}
		})
	}
	for name, malformed := range map[string]error{
		"wrong provider same fields": GoAwayError{LastStreamID: 1},
		"nil actual provider":        nilTarget,
		"opaque nil actual provider": &codexModelsOpaqueTransportError{cause: nilTarget},
		"nil wrong provider":         (*GoAwayError)(nil),
		"wrapped nil":                &codexModelsOpaqueTransportError{cause: (*GoAwayError)(nil)},
		"joined nil":                 errors.Join(real, (*GoAwayError)(nil)),
		"nil unwrap":                 &url.Error{Op: "Get", URL: "https://upstream.example/models"},
		"cycle":                      &codexModelsCyclicTransportError{},
	} {
		t.Run(name, func(t *testing.T) {
			if isCodexModelsGoAwayError(malformed) || isRetryableCodexModelsManifestTransportError(malformed) {
				t.Fatalf("malformed/wrong provider was accepted: %T", malformed)
			}
		})
	}
	deep := real
	for i := 0; i < 4096; i++ {
		deep = &codexModelsOpaqueTransportError{cause: deep}
	}
	if isCodexModelsGoAwayError(deep) || isRetryableCodexModelsManifestTransportError(deep) {
		t.Fatal("unbounded error chain was accepted")
	}
	if isRetryableCodexModelsManifestTransportError(errors.Join(&codexModelsCyclicTransportError{}, context.Canceled)) {
		t.Fatal("cyclic joined cancellation was accepted")
	}
	shared := real
	for i := 0; i < 12; i++ {
		shared = errors.Join(shared, shared)
	}
	if isCodexModelsGoAwayError(shared) || isRetryableCodexModelsManifestTransportError(shared) {
		t.Fatal("exponentially expanded shared error graph exceeded the bound")
	}
	var nilCause error
	for name, terminal := range map[string]error{
		"legacy nil terminal": codexModelsNilTerminalError{message: "http2: goaway"},
		"fmt nil terminal":    fmt.Errorf("http2: goaway: %w", nilCause),
	} {
		t.Run(name, func(t *testing.T) {
			if isCodexModelsGoAwayError(terminal) || !isRetryableCodexModelsManifestTransportError(terminal) {
				t.Fatal("legal nil terminal changed the retained text fallback")
			}
			if isRetryableCodexModelsManifestTransportError(errors.Join(terminal, context.Canceled)) {
				t.Fatal("legal terminal overrode cancellation")
			}
		})
	}
	plainTerminal := codexModelsNilTerminalError{message: "ordinary terminal"}
	if isCodexModelsGoAwayError(plainTerminal) || isRetryableCodexModelsManifestTransportError(plainTerminal) {
		t.Fatal("ordinary nil terminal became retryable")
	}
	withTerminal := errors.Join(&codexModelsOpaqueTransportError{cause: real}, plainTerminal)
	if !isCodexModelsGoAwayError(withTerminal) || !isRetryableCodexModelsManifestTransportError(withTerminal) {
		t.Fatal("legal nil terminal hid actual GOAWAY")
	}
	if isRetryableCodexModelsManifestTransportError(errors.Join(withTerminal, context.Canceled)) {
		t.Fatal("joined terminal overrode cancellation priority")
	}
	if fmt.Sprint((&codexModelsOpaqueTransportError{cause: real})) != "opaque transport wrapper" {
		t.Fatal("test wrapper accidentally exposes a legacy matching message")
	}
}
