//go:build unit

package service

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newSMTPTestCert 生成 127.0.0.1/localhost 的自签证书及其信任池。
func newSMTPTestCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, pool
}

// fakeSMTPServer 是覆盖三种连接形态的最小 SMTP 服务器：
// 隐式 TLS（465 语义）、明文+STARTTLS（587 语义）、纯明文。
type fakeSMTPServer struct {
	listener          net.Listener
	tlsConfig         *tls.Config
	advertiseStartTLS bool

	// 故障注入开关，供覆盖 email_service.go 里各条错误分支的测试使用。
	rejectMail            bool // MAIL FROM 回错误响应
	rejectRcpt            bool // RCPT TO 回错误响应
	closeOnData           bool // 回复 354 后立即断连，模拟写正文失败
	rejectAuth            bool // AUTH 回错误响应
	failStartTLSHandshake bool // STARTTLS 回 220 后不完成真实握手，直接断连

	mu       sync.Mutex
	commands []string
	conns    atomic.Int64
	wg       sync.WaitGroup
}

func startFakeSMTPServer(t *testing.T, implicitTLS, advertiseStartTLS bool) (*fakeSMTPServer, int) {
	t.Helper()
	cert, pool := newSMTPTestCert(t)
	prevPool := smtpTestRootCAs
	smtpTestRootCAs = pool
	t.Cleanup(func() { smtpTestRootCAs = prevPool })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &fakeSMTPServer{
		listener:          listener,
		tlsConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		advertiseStartTLS: advertiseStartTLS,
	}
	if implicitTLS {
		srv.listener = tls.NewListener(listener, srv.tlsConfig)
	}
	t.Cleanup(func() {
		_ = srv.listener.Close()
		srv.wg.Wait()
	})

	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		for {
			conn, err := srv.listener.Accept()
			if err != nil {
				return
			}
			srv.conns.Add(1)
			srv.wg.Add(1)
			go func() {
				defer srv.wg.Done()
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				srv.serve(conn, srv.advertiseStartTLS)
			}()
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	return srv, port
}

func (srv *fakeSMTPServer) record(cmd string) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	srv.commands = append(srv.commands, cmd)
}

func (srv *fakeSMTPServer) sawCommand(prefix string) bool {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for _, cmd := range srv.commands {
		if strings.HasPrefix(strings.ToUpper(cmd), prefix) {
			return true
		}
	}
	return false
}

func (srv *fakeSMTPServer) serve(conn net.Conn, allowStartTLS bool) {
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	writeLine := func(line string) bool {
		if _, err := writer.WriteString(line + "\r\n"); err != nil {
			return false
		}
		return writer.Flush() == nil
	}
	if !writeLine("220 fake.test ESMTP ready") {
		return
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		srv.record(cmd)
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			ok := writeLine("250-fake.test")
			if allowStartTLS {
				ok = ok && writeLine("250-STARTTLS")
			}
			if !(ok && writeLine("250-AUTH PLAIN LOGIN") && writeLine("250 8BITMIME")) {
				return
			}
		case upper == "STARTTLS" && allowStartTLS:
			if !writeLine("220 2.0.0 ready to start TLS") {
				return
			}
			if srv.failStartTLSHandshake {
				// 通告支持 STARTTLS 并接受命令，但拒绝完成真实握手——
				// 模拟握手阶段失败（证书/协议问题等），而非拒绝命令本身。
				return
			}
			tlsConn := tls.Server(conn, srv.tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			srv.serveUpgraded(tlsConn)
			return
		case strings.HasPrefix(upper, "AUTH"):
			if srv.rejectAuth {
				if !writeLine("535 5.7.8 authentication failed") {
					return
				}
				break
			}
			if !writeLine("235 2.7.0 authentication successful") {
				return
			}
		case strings.HasPrefix(upper, "MAIL"):
			if srv.rejectMail {
				if !writeLine("550 5.1.0 sender rejected") {
					return
				}
				break
			}
			if !writeLine("250 ok") {
				return
			}
		case strings.HasPrefix(upper, "RCPT"):
			if srv.rejectRcpt {
				if !writeLine("550 5.1.1 recipient rejected") {
					return
				}
				break
			}
			if !writeLine("250 ok") {
				return
			}
		case upper == "DATA":
			if srv.closeOnData {
				// 回复 354 后立即断连,不再读取正文——模拟消息体写入阶段连接中断。
				// 用 SetLinger(0) 触发 RST 而非平滑 FIN，让客户端后续的
				// 写入/关闭操作能可靠地观察到连接已失效，而不是被内核缓冲掩盖。
				_ = writeLine("354 go ahead")
				if tcpConn, ok := conn.(*net.TCPConn); ok {
					_ = tcpConn.SetLinger(0)
				}
				return
			}
			if !writeLine("354 go ahead") {
				return
			}
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
			}
			if !writeLine("250 message accepted") {
				return
			}
		case upper == "QUIT":
			_ = writeLine("221 bye")
			return
		default:
			if !writeLine("250 ok") {
				return
			}
		}
	}
}

// serveUpgraded 复用命令循环处理 STARTTLS 升级后的会话（升级后不再提供 STARTTLS）。
func (srv *fakeSMTPServer) serveUpgraded(conn net.Conn) {
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	// net/smtp 在 StartTLS 成功后会重新发送 EHLO，直接进入命令循环即可。
	srv.serveCommands(reader, writer)
}

func (srv *fakeSMTPServer) serveCommands(reader *bufio.Reader, writer *bufio.Writer) {
	writeLine := func(line string) bool {
		if _, err := writer.WriteString(line + "\r\n"); err != nil {
			return false
		}
		return writer.Flush() == nil
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		srv.record(cmd)
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			if !(writeLine("250-fake.test") && writeLine("250-AUTH PLAIN LOGIN") && writeLine("250 8BITMIME")) {
				return
			}
		case strings.HasPrefix(upper, "AUTH"):
			if !writeLine("235 2.7.0 authentication successful") {
				return
			}
		case strings.HasPrefix(upper, "MAIL"), strings.HasPrefix(upper, "RCPT"):
			if !writeLine("250 ok") {
				return
			}
		case upper == "DATA":
			if !writeLine("354 go ahead") {
				return
			}
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
			}
			if !writeLine("250 message accepted") {
				return
			}
		case upper == "QUIT":
			_ = writeLine("221 bye")
			return
		default:
			if !writeLine("250 ok") {
				return
			}
		}
	}
}

func smtpTestConfig(port int, useTLS bool) *SMTPConfig {
	return &SMTPConfig{
		Host:     "127.0.0.1",
		Port:     port,
		Username: "user",
		Password: "pass",
		From:     "noreply@example.com",
		FromName: "Test",
		UseTLS:   useTLS,
	}
}

// 465 语义：UseTLS=true + 隐式 TLS 服务器，原有路径保持可用。
func TestSMTPConnectionImplicitTLS(t *testing.T) {
	srv, port := startFakeSMTPServer(t, true, false)
	svc := &EmailService{}

	if err := svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, true)); err != nil {
		t.Fatalf("expected implicit TLS connection to succeed, got: %v", err)
	}
	if !srv.sawCommand("EHLO") {
		t.Fatal("expected server to receive EHLO")
	}
}

// 587 语义（#1470/#1488 核心场景）：UseTLS=true + 明文问候的 STARTTLS 服务器，
// 隐式 TLS 失败后必须自动降级为强制 STARTTLS 并成功。
func TestSMTPConnectionStartTLSFallbackWhenTLSEnabled(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, true)
	svc := &EmailService{}

	if err := svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, true)); err != nil {
		t.Fatalf("expected STARTTLS fallback to succeed, got: %v", err)
	}
	if !srv.sawCommand("STARTTLS") {
		t.Fatal("expected server to receive STARTTLS command")
	}
	if got := srv.conns.Load(); got < 2 {
		t.Fatalf("expected implicit TLS attempt before STARTTLS fallback (>=2 connections), got %d", got)
	}
}

// UseTLS=true 但服务器不支持 STARTTLS：必须报错，且绝不能把凭据发到明文连接上。
func TestSMTPConnectionMandatoryStartTLSRefusesPlaintext(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, false)
	svc := &EmailService{}

	err := svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, true))
	if err == nil {
		t.Fatal("expected error when server does not support STARTTLS")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("expected STARTTLS-related error, got: %v", err)
	}
	if srv.sawCommand("AUTH") {
		t.Fatal("credentials must not be sent over plaintext when TLS is required")
	}
}

// UseTLS=false + 服务器支持 STARTTLS：测试连接与发送路径一致，机会式升级后认证成功。
// 这是 #1488 评论"测试连接不成功，发送测试邮件实际上能发"的回归用例。
func TestSMTPConnectionOpportunisticStartTLSWhenTLSDisabled(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, true)
	svc := &EmailService{}

	if err := svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, false)); err != nil {
		t.Fatalf("expected opportunistic STARTTLS test connection to succeed, got: %v", err)
	}
	if !srv.sawCommand("STARTTLS") {
		t.Fatal("expected test connection to upgrade via STARTTLS like the send path")
	}
}

// UseTLS=false + 服务器不支持 STARTTLS：保持明文直连（既有行为不回归）。
func TestSMTPConnectionPlainWhenNoStartTLS(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, false)
	svc := &EmailService{}

	if err := svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, false)); err != nil {
		t.Fatalf("expected plain connection to succeed, got: %v", err)
	}
	if srv.sawCommand("STARTTLS") {
		t.Fatal("did not expect STARTTLS command when server does not advertise it")
	}
}

// 发送路径全流程：UseTLS=true + STARTTLS 服务器（587 语义）完整走完 MAIL/RCPT/DATA。
func TestSendEmailWithConfigStartTLSFallback(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, true)
	svc := &EmailService{}

	err := svc.SendEmailWithConfig(smtpTestConfig(port, true), "rcpt@example.com", "subject", "<p>body</p>")
	if err != nil {
		t.Fatalf("expected send via STARTTLS fallback to succeed, got: %v", err)
	}
	if !srv.sawCommand("STARTTLS") {
		t.Fatal("expected send path to upgrade via STARTTLS")
	}
	if !srv.sawCommand("DATA") {
		t.Fatal("expected send path to reach DATA")
	}
}

// 发送路径全流程：UseTLS=true + 隐式 TLS 服务器（465 语义）保持既有行为。
func TestSendEmailWithConfigImplicitTLS(t *testing.T) {
	srv, port := startFakeSMTPServer(t, true, false)
	svc := &EmailService{}

	err := svc.SendEmailWithConfig(smtpTestConfig(port, true), "rcpt@example.com", "subject", "<p>body</p>")
	if err != nil {
		t.Fatalf("expected send via implicit TLS to succeed, got: %v", err)
	}
	if !srv.sawCommand("DATA") {
		t.Fatal("expected send path to reach DATA")
	}
}

// buildSMTPMessage 失败（消息构造阶段）必须原样透传，不发起任何网络连接。
func TestSendEmailWithConfigPropagatesMessageBuildError(t *testing.T) {
	svc := &EmailService{}
	err := svc.SendEmailWithConfig(&SMTPConfig{Host: "smtp.example.com"}, "user@example.net", "subject", "body")
	if err == nil {
		t.Fatal("expected error when From address is missing")
	}
	if !strings.Contains(err.Error(), "invalid SMTP from address") {
		t.Fatalf("expected message-build error to propagate unwrapped, got: %v", err)
	}
}

// MAIL FROM 被服务器拒绝时必须原样透传错误。
func TestSendEmailWithConfigPropagatesMailCommandError(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, false)
	srv.rejectMail = true
	svc := &EmailService{}

	err := svc.SendEmailWithConfig(smtpTestConfig(port, false), "rcpt@example.com", "subject", "<p>body</p>")
	if err == nil {
		t.Fatal("expected MAIL FROM rejection to surface as an error")
	}
	if !strings.Contains(err.Error(), "smtp mail") {
		t.Fatalf("expected 'smtp mail' error, got: %v", err)
	}
}

// RCPT TO 被服务器拒绝时必须原样透传错误。
func TestSendEmailWithConfigPropagatesRcptCommandError(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, false)
	srv.rejectRcpt = true
	svc := &EmailService{}

	err := svc.SendEmailWithConfig(smtpTestConfig(port, false), "rcpt@example.com", "subject", "<p>body</p>")
	if err == nil {
		t.Fatal("expected RCPT TO rejection to surface as an error")
	}
	if !strings.Contains(err.Error(), "smtp rcpt") {
		t.Fatalf("expected 'smtp rcpt' error, got: %v", err)
	}
}

// DATA 阶段连接中断（写正文失败）必须原样透传错误，而不是被吞掉。
func TestSendEmailWithConfigPropagatesWriteError(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, false)
	srv.closeOnData = true
	svc := &EmailService{}

	err := svc.SendEmailWithConfig(smtpTestConfig(port, false), "rcpt@example.com", "subject", "<p>body</p>")
	if err == nil {
		t.Fatal("expected write failure after server drops the connection mid-DATA")
	}
}

// AUTH 被服务器拒绝时，TestSMTPConnectionWithConfig 必须原样透传错误。
func TestSMTPConnectionWithConfigPropagatesAuthError(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, false)
	srv.rejectAuth = true
	svc := &EmailService{}

	err := svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, false))
	if err == nil {
		t.Fatal("expected AUTH rejection to surface as an error")
	}
	if !strings.Contains(err.Error(), "smtp authentication failed") {
		t.Fatalf("expected 'smtp authentication failed' error, got: %v", err)
	}
}

// 服务器证书链不受信任时，隐式 TLS 拨号必须报出真实的证书错误（tls dial），
// 而不是被误判成 RecordHeaderError 并降级尝试 STARTTLS。
func TestConnectSMTPRejectsUntrustedCertificate(t *testing.T) {
	serverCert, _ := newSMTPTestCert(t)
	_, untrustedPool := newSMTPTestCert(t) // 与 serverCert 无关的另一份自签 CA

	prevPool := smtpTestRootCAs
	smtpTestRootCAs = untrustedPool
	t.Cleanup(func() { smtpTestRootCAs = prevPool })

	rawListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tlsListener := tls.NewListener(rawListener, &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS12,
	})
	t.Cleanup(func() { _ = tlsListener.Close() })
	go func() {
		for {
			conn, err := tlsListener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	port := rawListener.Addr().(*net.TCPAddr).Port
	svc := &EmailService{}
	err = svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, true))
	if err == nil {
		t.Fatal("expected certificate verification failure")
	}
	if !strings.Contains(err.Error(), "tls dial") {
		t.Fatalf("expected a 'tls dial' error (non-RecordHeaderError path), got: %v", err)
	}
}

// 明文连接建立后服务器不发送问候（直接断连），smtp.NewClient 读 banner 失败
// 必须原样透传，覆盖 connectSMTPStartTLS 里 newSMTPClient 的错误分支。
func TestConnectSMTPStartTLSPropagatesNewClientError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close() // 不发送 220 问候，直接断连
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	svc := &EmailService{}
	err = svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, false))
	if err == nil {
		t.Fatal("expected smtp client creation to fail without a greeting")
	}
	if !strings.Contains(err.Error(), "new smtp client") {
		t.Fatalf("expected 'new smtp client' error, got: %v", err)
	}
}

// 服务器通告并接受 STARTTLS 命令，但拒绝完成真实握手：client.StartTLS 的
// 错误必须原样透传，覆盖 connectSMTPStartTLS 里 StartTLS 失败的分支。
func TestConnectSMTPStartTLSPropagatesHandshakeError(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, true)
	srv.failStartTLSHandshake = true
	svc := &EmailService{}

	err := svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, false))
	if err == nil {
		t.Fatal("expected STARTTLS handshake failure to propagate")
	}
	if !strings.Contains(err.Error(), "starttls") {
		t.Fatalf("expected 'starttls' error, got: %v", err)
	}
}

// 目标端口没有任何服务监听（TCP 拨号本身失败）时，connectSMTPStartTLS 的
// dial 错误必须一路透传到 SendEmailWithConfig，两处 "if err != nil" 分支都
// 依赖这条路径才能被覆盖。
func TestSendEmailWithConfigPropagatesDialError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	svc := &EmailService{}
	err = svc.SendEmailWithConfig(smtpTestConfig(port, false), "rcpt@example.com", "subject", "<p>body</p>")
	if err == nil {
		t.Fatal("expected dialing a closed port to fail")
	}
	if !strings.Contains(err.Error(), "smtp dial") {
		t.Fatalf("expected 'smtp dial' error, got: %v", err)
	}
}

// DATA 阶段连接被 RST 且正文足够大（迫使 dotWriter 内部多次 flush）时，
// 失败必须发生在 w.Write 本身，而不仅仅是随后的 w.Close——覆盖两者各自
// 独立的错误分支。
func TestSendEmailWithConfigPropagatesLargeWriteError(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, false)
	srv.closeOnData = true
	svc := &EmailService{}

	largeBody := strings.Repeat("<p>filler content to force multiple dotWriter flushes</p>\n", 2000)
	err := svc.SendEmailWithConfig(smtpTestConfig(port, false), "rcpt@example.com", "subject", largeBody)
	if err == nil {
		t.Fatal("expected write failure after server RSTs the connection mid-DATA")
	}
}
