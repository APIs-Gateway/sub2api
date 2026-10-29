package req_test

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	req "github.com/imroc/req/v3"
)

type safariWrapperHello struct {
	ciphers []uint16
	protocols []string
}

type safariWrapperExchange struct {
	method string
	uri string
	protocol string
	header http.Header
	body string
	readError error
	writeError error
}

// These tests share the public default client and must remain sequential.
// Each case owns its TLS peer, client, complete exchange ledger and cleanup.
func TestReqSafariWrapperContract(t *testing.T) {
	for _, name := range []string{"global_safari", "instance_safari", "global_firefox_control"} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var hellos []safariWrapperHello
			var exchanges []safariWrapperExchange
			completed := make(chan struct{}, 4)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, readErr := io.ReadAll(io.LimitReader(r.Body, 65537))
				if len(body) > 65536 { readErr = fmt.Errorf("Safari peer entity exceeds fixture bound") }
				w.Header().Set("Content-Type", "text/plain")
				_, writeErr := io.WriteString(w, "safari-wrapper-peer-ok")
				e := safariWrapperExchange{r.Method, r.RequestURI, r.Proto, r.Header.Clone(), string(body), readErr, writeErr}
				mu.Lock(); exchanges = append(exchanges, e); mu.Unlock()
				select { case completed <- struct{}{}: default: }
			}))
			server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				mu.Lock()
				hellos = append(hellos, safariWrapperHello{append([]uint16(nil), hello.CipherSuites...), append([]string(nil), hello.SupportedProtos...)})
				mu.Unlock()
				return nil, nil
			}}
			server.StartTLS()
			t.Cleanup(func() {
				server.Close() // Join handlers before checking every recorded exchange.
				mu.Lock(); defer mu.Unlock()
				if len(hellos) != 1 || len(exchanges) != 1 { t.Errorf("Safari final peer ledger hellos=%d exchanges=%d, want 1/1", len(hellos), len(exchanges)) }
				for _, e := range exchanges { if e.readError != nil || e.writeError != nil { t.Errorf("Safari peer read=%v write=%v", e.readError, e.writeError) } }
				t.Logf("SAFARI_WRAPPER_PEER_COMPLETE hellos=%d exchanges=%d", len(hellos), len(exchanges))
			})
			previous := req.DefaultClient()
			client := req.C().SetProxy(nil).DisableAutoDecode().SetTimeout(5*time.Second)
			pool := x509.NewCertPool(); pool.AddCert(server.Certificate())
			client.GetTLSClientConfig().RootCAs = pool
			req.SetDefaultClient(client)
			t.Cleanup(func() { req.SetDefaultClient(previous); client.CloseIdleConnections(); if err := client.CloseDump(); err != nil { t.Errorf("Safari owned dump cleanup: %v", err) } })
			var configured *req.Client
			switch name {
			case "global_safari": configured = req.ImpersonateSafari()
			case "instance_safari": configured = client.ImpersonateSafari()
			case "global_firefox_control": configured = req.ImpersonateFirefox()
			}
			if configured != client || req.DefaultClient() != client { t.Fatal("impersonation replaced the active default client") }
			request := req.R()
			if name == "instance_safari" { request = client.R() }
			request.SetFileBytes("artifact", "wire.txt", []byte("safari-file-payload")).SetFormData(map[string]string{"field":"literal +& value"})
			response, err := request.Post(server.URL + "/safari-wire?source=contract")
			if err != nil || response == nil { t.Fatalf("real TLS request response=%v error=%v", response, err) }
			if response.StatusCode != 200 || response.String() != "safari-wrapper-peer-ok" { t.Fatalf("real TLS response status=%d body=%q", response.StatusCode, response.String()) }
			if response.Body != nil { if closeErr := response.Body.Close(); closeErr != nil { t.Errorf("Safari response body close: %v", closeErr) } }
			select { case <-completed: case <-time.After(6*time.Second): t.Fatal("Safari real peer handler did not complete") }
			mu.Lock()
			if len(hellos) != 1 || len(exchanges) != 1 { mu.Unlock(); t.Fatal("Safari request did not produce exactly one handshake and exchange") }
			hello, exchange := hellos[0], exchanges[0]
			mu.Unlock()
			if exchange.readError != nil || exchange.writeError != nil || exchange.method != "POST" || exchange.uri != "/safari-wire?source=contract" || exchange.protocol != "HTTP/1.1" { t.Fatalf("Safari real peer exchange=%+v", exchange) }
			wantUA := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Safari/605.1.15"
			wantLanguage, wantBoundary := "zh-CN,zh-Hans;q=0.9", "----WebKitFormBoundary"
			wantPrefix, wantGrease := []uint16{0x1301, 0x1302, 0x1303}, true
			if name == "global_firefox_control" {
				wantUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:120.0) Gecko/20100101 Firefox/120.0"
				wantLanguage, wantBoundary = "zh-CN,zh;q=0.8,zh-TW;q=0.7,zh-HK;q=0.5,en-US;q=0.3,en;q=0.2", "-------------------------"
				wantPrefix, wantGrease = []uint16{0x1301, 0x1303, 0x1302}, false
			}
			// Accumulate independent wire failures: the OLD global case must retain
			// both its wrong HTTP identity and wrong real ClientHello evidence.
			if exchange.header.Get("User-Agent") != wantUA || exchange.header.Get("Accept-Language") != wantLanguage { t.Errorf("SAFARI_WRAPPER_HTTP_IDENTITY gotUA=%q gotLanguage=%q wantUA=%q wantLanguage=%q", exchange.header.Get("User-Agent"), exchange.header.Get("Accept-Language"), wantUA, wantLanguage) }
			var nonGrease []uint16
			grease := false
			for _, cipher := range hello.ciphers { if cipher&0x0f0f == 0x0a0a && byte(cipher>>8) == byte(cipher) { grease = true } else { nonGrease = append(nonGrease, cipher) } }
			if len(nonGrease)<3 || !reflect.DeepEqual(nonGrease[:3], wantPrefix) || grease != wantGrease || !reflect.DeepEqual(hello.protocols, []string{"h2", "http/1.1"}) { t.Errorf("SAFARI_WRAPPER_CLIENT_HELLO ciphers=%x grease=%v ALPN=%q wantPrefix=%x wantGrease=%v", hello.ciphers, grease, hello.protocols, wantPrefix, wantGrease) }
			media, params, parseErr := mime.ParseMediaType(exchange.header.Get("Content-Type"))
			if parseErr != nil || media != "multipart/form-data" { t.Fatalf("multipart media=%q params=%v error=%v", media, params, parseErr) }
			boundary := params["boundary"]
			if !strings.HasPrefix(boundary,wantBoundary) || len(boundary)<=len(wantBoundary) { t.Errorf("SAFARI_WRAPPER_MULTIPART boundary=%q wantPrefix=%q", boundary, wantBoundary) }
			reader := multipart.NewReader(strings.NewReader(exchange.body), boundary)
			fields, files := 0, 0
			for {
				part, nextErr := reader.NextPart(); if nextErr == io.EOF { break }; if nextErr != nil { t.Fatal(nextErr) }
				body, readErr := io.ReadAll(part); closeErr := part.Close(); if readErr != nil || closeErr != nil { t.Fatalf("multipart read=%v close=%v", readErr, closeErr) }
				switch part.FormName() {
				case "field": fields++; if part.FileName()!="" || string(body)!="literal +& value" { t.Errorf("multipart field filename=%q body=%q",part.FileName(),body) }
				case "artifact": files++; if part.FileName()!="wire.txt" || string(body)!="safari-file-payload" { t.Errorf("multipart file filename=%q body=%q",part.FileName(),body) }
				default: t.Errorf("unexpected multipart part %v", part.Header)
				}
			}
			if fields!=1 || files!=1 { t.Errorf("multipart actual fields=%d files=%d, want1/1",fields,files) }
			t.Logf("SAFARI_WRAPPER_WIRE_OBSERVED case=%s UA=%q ciphers=%x grease=%v boundary=%q", name, exchange.header.Get("User-Agent"), hello.ciphers, grease, boundary)
		})
	}
}
