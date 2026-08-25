package config

import (
	"net"
	"os"
	"path/filepath"
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

	// nil IP
	if cfg.IsIPAllowed(nil) {
		t.Errorf("expected nil IP to NOT be allowed")
	}
}

func TestLoadFromEnv(t *testing.T) {
	os.Setenv("SERVER_LISTEN_ADDR", "0.0.0.0:25")
	os.Setenv("SERVER_HOSTNAME", "mail.example.org")
	os.Setenv("SERVER_READ_TIMEOUT", "30s")
	os.Setenv("SERVER_WRITE_TIMEOUT", "45s")
	os.Setenv("SERVER_MAX_MESSAGE_SIZE", "10485760")
	os.Setenv("SERVER_MAX_RECIPIENTS", "25")
	os.Setenv("SERVER_TLS_CERT", "/certs/cert.pem")
	os.Setenv("SERVER_TLS_KEY", "/certs/key.pem")
	os.Setenv("SERVER_ALLOW_INSECURE_AUTH", "true")
	os.Setenv("RELAY_HOST", "smtp.example.com")
	os.Setenv("RELAY_PORT", "465")
	os.Setenv("RELAY_USER", "user@example.com")
	os.Setenv("RELAY_PASSWORD", "secret123")
	os.Setenv("RELAY_TLS_TYPE", "TLS")
	os.Setenv("RELAY_AUTH_TYPE", "LOGIN")
	os.Setenv("RELAY_INSECURE_SKIP_VERIFY", "true")
	os.Setenv("SENDER_OVERRIDE", "no-reply@domain.com")
	os.Setenv("RELAY_STRATEGY", "ROUND-ROBIN")
	os.Setenv("ALLOWED_NETWORKS", "10.10.0.0/16, 192.168.50.0/24, 1.2.3.4, fe80::1")
	os.Setenv("INBOUND_USERS", "admin:pass123, app:secret456")
	os.Setenv("QUEUE_ENABLED", "true")
	os.Setenv("QUEUE_DIR", "/tmp/spool")
	os.Setenv("QUEUE_MAX_RETRIES", "10")
	os.Setenv("QUEUE_MAX_CONCURRENCY", "4")
	os.Setenv("QUEUE_BACKOFF_INTERVAL", "15s")
	os.Setenv("QUEUE_SCAN_INTERVAL", "30s")
	os.Setenv("HTTP_ENABLED", "true")
	os.Setenv("HTTP_LISTEN_ADDR", ":9090")
	os.Setenv("HTTP_DASHBOARD_ENABLED", "true")
	os.Setenv("LOG_LEVEL", "DEBUG")
	os.Setenv("LOG_FORMAT", "JSON")
	defer func() {
		os.Unsetenv("SERVER_LISTEN_ADDR")
		os.Unsetenv("SERVER_HOSTNAME")
		os.Unsetenv("SERVER_READ_TIMEOUT")
		os.Unsetenv("SERVER_WRITE_TIMEOUT")
		os.Unsetenv("SERVER_MAX_MESSAGE_SIZE")
		os.Unsetenv("SERVER_MAX_RECIPIENTS")
		os.Unsetenv("SERVER_TLS_CERT")
		os.Unsetenv("SERVER_TLS_KEY")
		os.Unsetenv("SERVER_ALLOW_INSECURE_AUTH")
		os.Unsetenv("RELAY_HOST")
		os.Unsetenv("RELAY_PORT")
		os.Unsetenv("RELAY_USER")
		os.Unsetenv("RELAY_PASSWORD")
		os.Unsetenv("RELAY_TLS_TYPE")
		os.Unsetenv("RELAY_AUTH_TYPE")
		os.Unsetenv("RELAY_INSECURE_SKIP_VERIFY")
		os.Unsetenv("SENDER_OVERRIDE")
		os.Unsetenv("RELAY_STRATEGY")
		os.Unsetenv("ALLOWED_NETWORKS")
		os.Unsetenv("INBOUND_USERS")
		os.Unsetenv("QUEUE_ENABLED")
		os.Unsetenv("QUEUE_DIR")
		os.Unsetenv("QUEUE_MAX_RETRIES")
		os.Unsetenv("QUEUE_MAX_CONCURRENCY")
		os.Unsetenv("QUEUE_BACKOFF_INTERVAL")
		os.Unsetenv("QUEUE_SCAN_INTERVAL")
		os.Unsetenv("HTTP_ENABLED")
		os.Unsetenv("HTTP_LISTEN_ADDR")
		os.Unsetenv("HTTP_DASHBOARD_ENABLED")
		os.Unsetenv("LOG_LEVEL")
		os.Unsetenv("LOG_FORMAT")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.Server.ListenAddr != "0.0.0.0:25" {
		t.Errorf("got listen addr %s, expected 0.0.0.0:25", cfg.Server.ListenAddr)
	}
	if cfg.Server.Hostname != "mail.example.org" {
		t.Errorf("got hostname %s, expected mail.example.org", cfg.Server.Hostname)
	}
	if cfg.Server.ReadTimeout.Duration != 30*time.Second {
		t.Errorf("got read timeout %v, expected 30s", cfg.Server.ReadTimeout.Duration)
	}
	if cfg.Server.WriteTimeout.Duration != 45*time.Second {
		t.Errorf("got write timeout %v, expected 45s", cfg.Server.WriteTimeout.Duration)
	}
	if cfg.Server.MaxMessageSize != 10485760 {
		t.Errorf("got max message size %d, expected 10485760", cfg.Server.MaxMessageSize)
	}
	if cfg.Server.MaxRecipients != 25 {
		t.Errorf("got max recipients %d, expected 25", cfg.Server.MaxRecipients)
	}
	if cfg.Server.TLSCertFile != "/certs/cert.pem" || cfg.Server.TLSKeyFile != "/certs/key.pem" {
		t.Errorf("unexpected TLS cert/key: %s / %s", cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile)
	}
	if !cfg.Server.AllowInsecureAuth {
		t.Errorf("expected AllowInsecureAuth to be true")
	}
	if cfg.Relay.Host != "smtp.example.com" || cfg.Relay.Port != 465 {
		t.Errorf("got relay %s:%d, expected smtp.example.com:465", cfg.Relay.Host, cfg.Relay.Port)
	}
	if cfg.Relay.Username != "user@example.com" || cfg.Relay.Password != "secret123" {
		t.Errorf("got relay credentials %s:%s", cfg.Relay.Username, cfg.Relay.Password)
	}
	if cfg.Relay.TLSType != "TLS" || cfg.Relay.AuthType != "LOGIN" {
		t.Errorf("got TLS %s, Auth %s", cfg.Relay.TLSType, cfg.Relay.AuthType)
	}
	if !cfg.Relay.InsecureSkipVerify {
		t.Errorf("expected InsecureSkipVerify to be true")
	}
	if cfg.Relay.SenderOverride != "no-reply@domain.com" {
		t.Errorf("got sender override %s", cfg.Relay.SenderOverride)
	}
	if cfg.Relay.Strategy != "round-robin" {
		t.Errorf("got relay strategy %s", cfg.Relay.Strategy)
	}
	if cfg.Queue.MaxRetries != 10 || cfg.Queue.MaxConcurrency != 4 {
		t.Errorf("got queue max retries %d, concurrency %d", cfg.Queue.MaxRetries, cfg.Queue.MaxConcurrency)
	}
	if cfg.HTTP.ListenAddr != ":9090" || !cfg.HTTP.DashboardEnabled {
		t.Errorf("got HTTP listen %s, dashboard %v", cfg.HTTP.ListenAddr, cfg.HTTP.DashboardEnabled)
	}
	if cfg.Logging.Level != "debug" || cfg.Logging.Format != "json" {
		t.Errorf("unexpected logging config: %+v", cfg.Logging)
	}

	// Verify allowed networks including single IP addresses
	if !cfg.IsIPAllowed(net.ParseIP("10.10.1.20")) {
		t.Errorf("expected 10.10.1.20 to be allowed")
	}
	if !cfg.IsIPAllowed(net.ParseIP("1.2.3.4")) {
		t.Errorf("expected single IPv4 1.2.3.4 to be allowed")
	}
	if !cfg.IsIPAllowed(net.ParseIP("fe80::1")) {
		t.Errorf("expected single IPv6 fe80::1 to be allowed")
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

	// Unmarshal from numeric nanoseconds
	var dNum Duration
	if err := dNum.UnmarshalJSON([]byte("120")); err != nil || dNum.Duration != 120 {
		t.Errorf("expected numeric nanoseconds unmarshal to 120, got %v (err: %v)", dNum.Duration, err)
	}

	// Unmarshal invalid string
	var dBad Duration
	if err := dBad.UnmarshalJSON([]byte(`"invalid-duration"`)); err == nil {
		t.Errorf("expected error unmarshaling invalid duration string")
	}
}

func TestNewFeaturesConfig(t *testing.T) {
	os.Setenv("RATE_LIMIT_ENABLED", "true")
	os.Setenv("RATE_LIMIT_MAX_PER_MINUTE", "300")
	os.Setenv("RATE_LIMIT_BURST", "50")
	os.Setenv("WEBHOOK_ENABLED", "true")
	os.Setenv("WEBHOOK_URL", "https://api.example.com/events")
	os.Setenv("WEBHOOK_SECRET", "supersecret")
	os.Setenv("WEBHOOK_TIMEOUT", "5s")
	os.Setenv("HTTP_API_KEY", "key-12345")
	os.Setenv("SERVER_REQUIRE_TLS", "true")
	os.Setenv("RELAY_DOMAIN_ROUTES", `{"corp.local":"internal.relay:25","gmail.com":"smtp.gmail.com:587"}`)
	os.Setenv("RELAY_UPSTREAMS", `[{"name":"primary","host":"mail.corp","port":587}]`)
	defer func() {
		os.Unsetenv("RATE_LIMIT_ENABLED")
		os.Unsetenv("RATE_LIMIT_MAX_PER_MINUTE")
		os.Unsetenv("RATE_LIMIT_BURST")
		os.Unsetenv("WEBHOOK_ENABLED")
		os.Unsetenv("WEBHOOK_URL")
		os.Unsetenv("WEBHOOK_SECRET")
		os.Unsetenv("WEBHOOK_TIMEOUT")
		os.Unsetenv("HTTP_API_KEY")
		os.Unsetenv("SERVER_REQUIRE_TLS")
		os.Unsetenv("RELAY_DOMAIN_ROUTES")
		os.Unsetenv("RELAY_UPSTREAMS")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if !cfg.RateLimit.Enabled || cfg.RateLimit.MaxPerMinute != 300 || cfg.RateLimit.Burst != 50 {
		t.Errorf("unexpected rate limit config: %+v", cfg.RateLimit)
	}
	if !cfg.Webhook.Enabled || cfg.Webhook.URL != "https://api.example.com/events" || cfg.Webhook.Secret != "supersecret" {
		t.Errorf("unexpected webhook config: %+v", cfg.Webhook)
	}
	if cfg.Webhook.Timeout.Duration != 5*time.Second {
		t.Errorf("unexpected webhook timeout: %+v", cfg.Webhook)
	}
	if cfg.HTTP.APIKey != "key-12345" {
		t.Errorf("expected API key key-12345, got %s", cfg.HTTP.APIKey)
	}
	if !cfg.Server.RequireTLS {
		t.Errorf("expected RequireTLS to be true")
	}
	if cfg.Relay.DomainRoutes["corp.local"] != "internal.relay:25" {
		t.Errorf("expected domain route for corp.local, got %v", cfg.Relay.DomainRoutes)
	}
	if len(cfg.Relay.Upstreams) == 0 || cfg.Relay.Upstreams[0].Host != "mail.corp" {
		t.Errorf("expected upstream mail.corp, got %+v", cfg.Relay.Upstreams)
	}
}

func TestLoadFromFile(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")

	jsonContent := `{
		"server": {
			"listen_addr": ":3535",
			"hostname": "file.hostname"
		},
		"relay": {
			"host": "relay.file.local",
			"port": 2525
		}
	}`

	if err := os.WriteFile(configPath, []byte(jsonContent), 0600); err != nil {
		t.Fatalf("failed to write test config file: %v", err)
	}

	os.Setenv("CONFIG_FILE", configPath)
	defer os.Unsetenv("CONFIG_FILE")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() from file failed: %v", err)
	}

	if cfg.Server.ListenAddr != ":3535" || cfg.Server.Hostname != "file.hostname" {
		t.Errorf("unexpected server config from file: %+v", cfg.Server)
	}
	if cfg.Relay.Host != "relay.file.local" || cfg.Relay.Port != 2525 {
		t.Errorf("unexpected relay config from file: %+v", cfg.Relay)
	}

	// Non-existent file
	os.Setenv("CONFIG_FILE", "/non/existent/file.json")
	_, err = Load()
	if err == nil {
		t.Errorf("expected error loading non-existent config file")
	}
}

func TestParseBool(t *testing.T) {
	cases := []struct {
		in       string
		fallback bool
		expected bool
	}{
		{"true", false, true},
		{"1", false, true},
		{"yes", false, true},
		{"false", true, false},
		{"0", true, false},
		{"no", true, false},
		{"invalid", true, true},
		{"invalid", false, false},
	}

	for _, c := range cases {
		res := parseBool(c.in, c.fallback)
		if res != c.expected {
			t.Errorf("parseBool(%q, %v) = %v, expected %v", c.in, c.fallback, res, c.expected)
		}
	}
}

func TestParseUpstreamsAndDomainRoutesStringFormat(t *testing.T) {
	// String format upstreams
	str := "smtp://user:pass@relay1.local:587, smtps://admin:secret@relay2.local:465, direct.local:25"
	upstreams := parseUpstreamsEnv(str)
	if len(upstreams) != 3 {
		t.Fatalf("expected 3 parsed upstreams, got %d", len(upstreams))
	}
	if upstreams[0].Host != "relay1.local" || upstreams[0].Port != 587 || upstreams[0].Username != "user" {
		t.Errorf("unexpected 1st upstream: %+v", upstreams[0])
	}
	if upstreams[1].Host != "relay2.local" || upstreams[1].Port != 465 || upstreams[1].TLSType != "TLS" {
		t.Errorf("unexpected 2nd upstream: %+v", upstreams[1])
	}

	// String format domain routes
	domainRoutesStr := "corp.internal=internal.relay:25, gmail.com->smtp.gmail.com:587"
	routes := parseDomainRoutesEnv(domainRoutesStr)
	if routes["corp.internal"] != "internal.relay:25" {
		t.Errorf("expected corp.internal route, got %v", routes["corp.internal"])
	}
	if routes["gmail.com"] != "smtp.gmail.com:587" {
		t.Errorf("expected gmail.com route, got %v", routes["gmail.com"])
	}
}

