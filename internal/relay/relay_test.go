package relay

import (
	"bytes"
	"context"
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
	// Start mock SMTP server listener
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on mock port: %v", err)
	}
	defer l.Close()

	port := l.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Simple SMTP conversation simulation
		_, _ = conn.Write([]byte("220 mock.smtp Service Ready\r\n"))

		buf := make([]byte, 1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			cmd := string(buf[:n])
			if bytes.HasPrefix(buf[:n], []byte("EHLO")) || bytes.HasPrefix(buf[:n], []byte("HELO")) {
				_, _ = conn.Write([]byte("250-mock.smtp\r\n250 HELP\r\n"))
			} else if bytes.HasPrefix(buf[:n], []byte("MAIL FROM:")) {
				_, _ = conn.Write([]byte("250 2.1.0 Ok\r\n"))
			} else if bytes.HasPrefix(buf[:n], []byte("RCPT TO:")) {
				_, _ = conn.Write([]byte("250 2.1.5 Ok\r\n"))
			} else if bytes.HasPrefix(buf[:n], []byte("DATA")) {
				_, _ = conn.Write([]byte("354 End data with <CR><LF>.<CR><LF>\r\n"))
			} else if bytes.Contains(buf[:n], []byte("\r\n.\r\n")) || cmd == ".\r\n" {
				_, _ = conn.Write([]byte("250 2.0.0 Ok: queued\r\n"))
			} else if bytes.HasPrefix(buf[:n], []byte("QUIT")) {
				_, _ = conn.Write([]byte("221 2.0.0 Bye\r\n"))
				return
			}
		}
	}()

	cfg := &config.RelayConfig{
		Host:     "127.0.0.1",
		Port:     port,
		TLSType:  "NONE",
		AuthType: "NONE",
	}

	client := NewClient(cfg, metrics.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.Send(ctx, "sender@local.test", []string{"receiver@remote.test"}, []byte("Subject: Mock Relay\r\n\r\nRelayed content"))
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
