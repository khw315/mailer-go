package relay

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/smtp"
	"testing"
	"time"

	"github.com/khw315/mailer-go/internal/config"
	"github.com/khw315/mailer-go/internal/metrics"
)

func TestInjectHeaders(t *testing.T) {
	cfg := &config.RelayConfig{
		AddHeaders: map[string]string{
			"X-Relayed-By": "mailer-go",
		},
	}
	client := NewClient(cfg, metrics.New(), nil)

	originalMsg := []byte("Subject: Test\r\n\r\nBody")
	injected := client.injectHeaders(originalMsg)

	if !bytes.Contains(injected, []byte("X-Relayed-By: mailer-go\r\n")) {
		t.Errorf("expected X-Relayed-By header, got: %s", string(injected))
	}

	// Should not duplicate if header already present
	alreadyHasHeader := []byte("X-Relayed-By: mailer-go\r\nSubject: Test\r\n\r\nBody")
	notReInjected := client.injectHeaders(alreadyHasHeader)
	if !bytes.Equal(alreadyHasHeader, notReInjected) {
		t.Errorf("expected no duplicate headers added")
	}
}

func TestRelaySendViaMockServer(t *testing.T) {
	l, port := startMockServer(t)
	defer l.Close()

	cfg := &config.RelayConfig{
		Host:     "127.0.0.1",
		Port:     port,
		TLSType:  "NONE",
		AuthType: "NONE",
	}

	client := NewClient(cfg, metrics.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Send(ctx, "sender@local.test", []string{"receiver@remote.test"}, []byte("Subject: Mock Relay\r\n\r\nRelayed content"))
	if err != nil {
		t.Fatalf("relay Send failed: %v", err)
	}
}

func TestLoginAuth(t *testing.T) {
	auth := &loginAuth{username: "user1", password: "pwd"}
	serverInfo := &smtp.ServerInfo{Name: "smtp.example.com", TLS: true, Auth: []string{"LOGIN"}}

	mech, initialResp, err := auth.Start(serverInfo)
	if err != nil || mech != "LOGIN" || len(initialResp) != 0 {
		t.Fatalf("Start failed: mech=%s, err=%v", mech, err)
	}

	// Server asks for Username
	resp, err := auth.Next([]byte("Username:"), true)
	if err != nil || string(resp) != "user1" {
		t.Errorf("expected user1 response, got %s (err: %v)", string(resp), err)
	}

	// Server asks for Password
	resp, err = auth.Next([]byte("Password:"), true)
	if err != nil || string(resp) != "pwd" {
		t.Errorf("expected pwd response, got %s (err: %v)", string(resp), err)
	}

	// Done
	resp, err = auth.Next(nil, false)
	if err != nil || resp != nil {
		t.Errorf("expected nil response at completion, got %v", resp)
	}
}

func startMockServer(t *testing.T) (net.Listener, int) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go handleMockConn(conn)
		}
	}()
	return l, port
}

func handleMockConn(c net.Conn) {
	defer c.Close()
	_, _ = c.Write([]byte("220 mock.smtp Service Ready\r\n"))
	buf := make([]byte, 1024)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		response, shouldQuit := mockSMTPResponse(buf[:n])
		if response != "" {
			_, _ = c.Write([]byte(response))
		}
		if shouldQuit {
			return
		}
	}
}

func mockSMTPResponse(data []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(data, []byte("EHLO")), bytes.HasPrefix(data, []byte("HELO")):
		return "250-mock.smtp\r\n250-AUTH PLAIN LOGIN\r\n250 HELP\r\n", false
	case bytes.HasPrefix(data, []byte("MAIL FROM:")):
		return "250 2.1.0 Ok\r\n", false
	case bytes.HasPrefix(data, []byte("RCPT TO:")):
		return "250 2.1.5 Ok\r\n", false
	case bytes.HasPrefix(data, []byte("DATA")):
		return "354 End data with <CR><LF>.<CR><LF>\r\n", false
	case bytes.Contains(data, []byte("\r\n.\r\n")), string(data) == ".\r\n":
		return "250 2.0.0 Ok: queued\r\n", false
	case bytes.HasPrefix(data, []byte("QUIT")):
		return "221 2.0.0 Bye\r\n", true
	default:
		return "250 Ok\r\n", false
	}
}

func TestMultiRelayFailover(t *testing.T) {
	// Server 1 is intentionally on a closed/invalid port to simulate failure
	l2, port2 := startMockServer(t)
	defer l2.Close()

	cfg := &config.RelayConfig{
		Strategy: "failover",
		Upstreams: []config.UpstreamRelay{
			{Name: "broken-primary", Host: "127.0.0.1", Port: 65432, TLSType: "NONE", AuthType: "NONE"},
			{Name: "working-secondary", Host: "127.0.0.1", Port: port2, TLSType: "NONE", AuthType: "NONE"},
		},
	}

	client := NewClient(cfg, metrics.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Send(ctx, "sender@test.local", []string{"recipient@test.local"}, []byte("Subject: Failover Test\r\n\r\nBody"))
	if err != nil {
		t.Fatalf("expected failover to succeed on second upstream, got err: %v", err)
	}
}

func TestRoundRobinRelay(t *testing.T) {
	l1, port1 := startMockServer(t)
	defer l1.Close()
	l2, port2 := startMockServer(t)
	defer l2.Close()

	cfg := &config.RelayConfig{
		Strategy: "round-robin",
		Upstreams: []config.UpstreamRelay{
			{Name: "server-1", Host: "127.0.0.1", Port: port1, TLSType: "NONE", AuthType: "NONE"},
			{Name: "server-2", Host: "127.0.0.1", Port: port2, TLSType: "NONE", AuthType: "NONE"},
		},
	}

	client := NewClient(cfg, metrics.New(), nil)
	ctx := context.Background()

	// Send twice to exercise round-robin index rotation
	for i := 0; i < 2; i++ {
		err := client.Send(ctx, "sender@test.local", []string{"rr@test.local"}, []byte("Subject: Round Robin\r\n\r\nBody"))
		if err != nil {
			t.Fatalf("round robin send iteration %d failed: %v", i, err)
		}
	}
}

func TestDomainRouting(t *testing.T) {
	lCorp, portCorp := startMockServer(t)
	defer lCorp.Close()

	cfg := &config.RelayConfig{
		DomainRoutes: map[string]string{
			"corp.internal": "corp",
			"backup.local":  fmt.Sprintf("127.0.0.1:%d", portCorp),
		},
		Upstreams: []config.UpstreamRelay{
			{Name: "corp", Host: "127.0.0.1", Port: portCorp, TLSType: "NONE", AuthType: "NONE"},
		},
	}

	client := NewClient(cfg, metrics.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Route by named upstream
	err := client.Send(ctx, "sender@test.local", []string{"alice@corp.internal"}, []byte("Subject: Internal\r\n\r\nInternal message"))
	if err != nil {
		t.Fatalf("domain routed send failed: %v", err)
	}

	// Route by host:port target
	err = client.Send(ctx, "sender@test.local", []string{"bob@backup.local"}, []byte("Subject: Backup\r\n\r\nBackup message"))
	if err != nil {
		t.Fatalf("domain host:port routed send failed: %v", err)
	}
}

func TestGroupRecipientsByDomain(t *testing.T) {
	valid := []string{"alice@example.com", "bob@example.com", "carol@other.org"}
	groups, err := groupRecipientsByDomain(valid)
	if err != nil || len(groups["example.com"]) != 2 || len(groups["other.org"]) != 1 {
		t.Errorf("unexpected domain grouping: %v, err: %v", groups, err)
	}

	invalid := []string{"not-an-email"}
	_, err = groupRecipientsByDomain(invalid)
	if err == nil {
		t.Errorf("expected error on invalid email address")
	}
}

func TestTryDeliverMX(t *testing.T) {
	l, port := startMockServer(t)
	defer l.Close()

	cfg := &config.RelayConfig{InsecureSkipVerify: true}
	client := NewClient(cfg, metrics.New(), nil)

	// Direct MX send to local server
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = client.tryDeliverMX(ctx, fmt.Sprintf("127.0.0.1:%d", port), "test.local", []string{"rcpt@test.local"}, "sender@test.local", []byte("Subject: Direct MX\r\n\r\nTest"))
}

func TestAuthenticateUpstreamModes(t *testing.T) {
	l, port := startMockServer(t)
	defer l.Close()

	// 1. PLAIN Auth
	cfgPlain := &config.RelayConfig{
		Host:     "127.0.0.1",
		Port:     port,
		Username: "user",
		Password: "pass",
		AuthType: "PLAIN",
		TLSType:  "NONE",
	}
	clientPlain := NewClient(cfgPlain, metrics.New(), nil)
	_ = clientPlain.Send(context.Background(), "a@b.c", []string{"d@e.f"}, []byte("Subject: Auth\r\n\r\nMsg"))

	// 2. LOGIN Auth
	cfgLogin := &config.RelayConfig{
		Host:     "127.0.0.1",
		Port:     port,
		Username: "user",
		Password: "pass",
		AuthType: "LOGIN",
		TLSType:  "NONE",
	}
	clientLogin := NewClient(cfgLogin, metrics.New(), nil)
	_ = clientLogin.Send(context.Background(), "a@b.c", []string{"d@e.f"}, []byte("Subject: Auth\r\n\r\nMsg"))

	// 3. AUTO Auth
	cfgAuto := &config.RelayConfig{
		Host:     "127.0.0.1",
		Port:     port,
		Username: "user",
		Password: "pass",
		AuthType: "AUTO",
		TLSType:  "NONE",
	}
	clientAuto := NewClient(cfgAuto, metrics.New(), nil)
	_ = clientAuto.Send(context.Background(), "a@b.c", []string{"d@e.f"}, []byte("Subject: Auth\r\n\r\nMsg"))
}

