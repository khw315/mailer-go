package server

import (
	"context"
	"net"
	"net/smtp"
	"testing"
	"time"

	"github.com/khw315/mailer-go/internal/config"
	"github.com/khw315/mailer-go/internal/metrics"
	"github.com/khw315/mailer-go/internal/queue"
)

type mockRelayer struct {
	lastFrom string
	lastTo   []string
	lastMsg  []byte
}

func (m *mockRelayer) Send(ctx context.Context, from string, to []string, msg []byte) error {
	m.lastFrom = from
	m.lastTo = to
	m.lastMsg = msg
	return nil
}

func setupTestServer(t *testing.T, allowedNetworks []string, inboundUsers []string) (*Server, string, *mockRelayer, func()) {
	cfg := config.DefaultConfig()
	cfg.Server.ListenAddr = "127.0.0.1:0"
	cfg.Server.AllowInsecureAuth = true
	cfg.Server.AllowedNetworks = allowedNetworks
	cfg.Server.InboundUsers = inboundUsers
	if err := cfg.Compile(); err != nil {
		t.Fatalf("failed to compile config: %v", err)
	}

	mockRelay := &mockRelayer{}
	m := metrics.New()
	q := queue.New(&cfg.Queue, mockRelay, m, nil)
	_ = q.Start(context.Background())

	srv, err := New(cfg, q, mockRelay, m, nil)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	go func() {
		_ = srv.Serve(listener)
	}()

	addr := listener.Addr().String()

	cleanup := func() {
		_ = srv.Close()
		_ = listener.Close()
		q.Stop()
	}

	return srv, addr, mockRelay, cleanup
}

func TestServerAllowedNetworkInbound(t *testing.T) {
	// Allow 127.0.0.0/8
	_, addr, mockRelay, cleanup := setupTestServer(t, []string{"127.0.0.0/8"}, nil)
	defer cleanup()

	client, err := smtp.Dial(addr)
	if err != nil {
		t.Fatalf("failed to dial SMTP server: %v", err)
	}
	defer client.Close()

	if err := client.Mail("app@internal.net"); err != nil {
		t.Fatalf("MAIL FROM failed: %v", err)
	}
	if err := client.Rcpt("user@example.com"); err != nil {
		t.Fatalf("RCPT TO failed: %v", err)
	}

	w, err := client.Data()
	if err != nil {
		t.Fatalf("DATA failed: %v", err)
	}
	_, _ = w.Write([]byte("Subject: Internal notification\r\n\r\nHello from allowed network"))
	if err := w.Close(); err != nil {
		t.Fatalf("closing data failed: %v", err)
	}
	_ = client.Quit()

	// Wait for queue delivery
	time.Sleep(100 * time.Millisecond)

	if mockRelay.lastFrom != "app@internal.net" {
		t.Errorf("expected from app@internal.net, got %s", mockRelay.lastFrom)
	}
	if len(mockRelay.lastTo) != 1 || mockRelay.lastTo[0] != "user@example.com" {
		t.Errorf("expected recipient user@example.com, got %v", mockRelay.lastTo)
	}
}

func TestServerDisallowedNetworkWithoutAuth(t *testing.T) {
	// Only allow 10.0.0.0/8, so 127.0.0.1 is disallowed
	_, addr, _, cleanup := setupTestServer(t, []string{"10.0.0.0/8"}, nil)
	defer cleanup()

	client, err := smtp.Dial(addr)
	if err != nil {
		t.Fatalf("failed to dial SMTP server: %v", err)
	}
	defer client.Close()

	err = client.Mail("unauthorized@external.com")
	if err == nil {
		t.Errorf("expected MAIL FROM to be rejected for unwhitelisted client, but succeeded")
	}
}

func TestServerAuthenticatedInbound(t *testing.T) {
	// Only allow 10.0.0.0/8, but configure inbound user credentials
	_, addr, mockRelay, cleanup := setupTestServer(t, []string{"10.0.0.0/8"}, []string{"maileruser:secret123"})
	defer cleanup()

	client, err := smtp.Dial(addr)
	if err != nil {
		t.Fatalf("failed to dial SMTP server: %v", err)
	}
	defer client.Close()

	auth := smtp.PlainAuth("", "maileruser", "secret123", "127.0.0.1")
	if err := client.Auth(auth); err != nil {
		t.Fatalf("Auth failed: %v", err)
	}

	if err := client.Mail("authuser@external.com"); err != nil {
		t.Fatalf("MAIL FROM failed after auth: %v", err)
	}
	if err := client.Rcpt("client@external.com"); err != nil {
		t.Fatalf("RCPT TO failed: %v", err)
	}

	w, err := client.Data()
	if err != nil {
		t.Fatalf("DATA failed: %v", err)
	}
	_, _ = w.Write([]byte("Subject: Authenticated email\r\n\r\nHello auth user"))
	_ = w.Close()
	_ = client.Quit()

	time.Sleep(100 * time.Millisecond)

	if mockRelay.lastFrom != "authuser@external.com" {
		t.Errorf("expected from authuser@external.com, got %s", mockRelay.lastFrom)
	}
}

func TestServerInvalidAuth(t *testing.T) {
	_, addr, _, cleanup := setupTestServer(t, []string{"10.0.0.0/8"}, []string{"maileruser:secret123"})
	defer cleanup()

	client, err := smtp.Dial(addr)
	if err != nil {
		t.Fatalf("failed to dial SMTP server: %v", err)
	}
	defer client.Close()

	auth := smtp.PlainAuth("", "maileruser", "wrongpassword", "127.0.0.1")
	err = client.Auth(auth)
	if err == nil {
		t.Errorf("expected Auth with wrong password to fail, but succeeded")
	}
}
