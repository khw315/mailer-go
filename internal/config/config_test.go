package config

import (
	"net"
	"os"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Server.ListenAddr != ":2525" {
		t.Errorf("expected default listen addr :2525, got %s", cfg.Server.ListenAddr)
	}
	if err := cfg.Compile(); err != nil {
		t.Fatalf("failed to compile default config: %v", err)
	}

	// Test default allowed networks (loopback and private networks)
	allowedIPs := []string{"127.0.0.1", "10.0.1.5", "172.16.0.10", "192.168.1.100", "::1"}
	for _, ipStr := range allowedIPs {
		ip := net.ParseIP(ipStr)
		if !cfg.IsIPAllowed(ip) {
			t.Errorf("expected IP %s to be allowed in default config", ipStr)
		}
	}

	// Public IP should not be allowed by default
	publicIP := net.ParseIP("8.8.8.8")
	if cfg.IsIPAllowed(publicIP) {
		t.Errorf("expected public IP %s to NOT be allowed by default", publicIP)
	}
}

func TestLoadFromEnv(t *testing.T) {
	os.Setenv("SERVER_LISTEN_ADDR", "0.0.0.0:25")
	os.Setenv("RELAY_HOST", "smtp.example.com")
	os.Setenv("RELAY_PORT", "465")
	os.Setenv("RELAY_USER", "user@example.com")
	os.Setenv("RELAY_PASSWORD", "secret123")
	os.Setenv("RELAY_TLS_TYPE", "TLS")
	os.Setenv("ALLOWED_NETWORKS", "10.10.0.0/16, 192.168.50.0/24")
	os.Setenv("INBOUND_USERS", "admin:pass123, app:secret456")
	os.Setenv("QUEUE_MAX_RETRIES", "10")
	defer func() {
		os.Unsetenv("SERVER_LISTEN_ADDR")
		os.Unsetenv("RELAY_HOST")
		os.Unsetenv("RELAY_PORT")
		os.Unsetenv("RELAY_USER")
		os.Unsetenv("RELAY_PASSWORD")
		os.Unsetenv("RELAY_TLS_TYPE")
		os.Unsetenv("ALLOWED_NETWORKS")
		os.Unsetenv("INBOUND_USERS")
		os.Unsetenv("QUEUE_MAX_RETRIES")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.Server.ListenAddr != "0.0.0.0:25" {
		t.Errorf("got listen addr %s, expected 0.0.0.0:25", cfg.Server.ListenAddr)
	}
	if cfg.Relay.Host != "smtp.example.com" || cfg.Relay.Port != 465 {
		t.Errorf("got relay %s:%d, expected smtp.example.com:465", cfg.Relay.Host, cfg.Relay.Port)
	}
	if cfg.Relay.Username != "user@example.com" || cfg.Relay.Password != "secret123" {
		t.Errorf("got relay credentials %s:%s", cfg.Relay.Username, cfg.Relay.Password)
	}
	if cfg.Relay.TLSType != "TLS" {
		t.Errorf("got TLS type %s, expected TLS", cfg.Relay.TLSType)
	}
	if cfg.Queue.MaxRetries != 10 {
		t.Errorf("got max retries %d, expected 10", cfg.Queue.MaxRetries)
	}

	// Verify allowed networks
	if !cfg.IsIPAllowed(net.ParseIP("10.10.1.20")) {
		t.Errorf("expected 10.10.1.20 to be allowed")
	}
	if cfg.IsIPAllowed(net.ParseIP("192.168.1.1")) {
		t.Errorf("expected 192.168.1.1 to NOT be allowed with overridden networks")
	}

	// Verify inbound authentication
	if !cfg.AuthenticateInbound("admin", "pass123") {
		t.Errorf("expected admin:pass123 to authenticate successfully")
	}
	if !cfg.AuthenticateInbound("app", "secret456") {
		t.Errorf("expected app:secret456 to authenticate successfully")
	}
	if cfg.AuthenticateInbound("admin", "wrong") {
		t.Errorf("expected invalid password to fail")
	}
	if cfg.AuthenticateInbound("unknown", "pass123") {
		t.Errorf("expected unknown user to fail")
	}
}

func TestDurationJSON(t *testing.T) {
	d := Duration{Duration: 5 * time.Minute}
	bytes, err := d.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}
	var d2 Duration
	if err := d2.UnmarshalJSON(bytes); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}
	if d.Duration != d2.Duration {
		t.Errorf("expected %v, got %v", d.Duration, d2.Duration)
	}
}
