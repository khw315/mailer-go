package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all configuration settings for mailer-go.
type Config struct {
	Server    ServerConfig    `json:"server"`
	Relay     RelayConfig     `json:"relay"`
	Queue     QueueConfig     `json:"queue"`
	HTTP      HTTPConfig      `json:"http"`
	RateLimit RateLimitConfig `json:"rate_limit"`
	Webhook   WebhookConfig   `json:"webhook"`
	Logging   LoggingConfig   `json:"logging"`
}

// ServerConfig defines the inbound SMTP server settings.
type ServerConfig struct {
	ListenAddr        string   `json:"listen_addr"`
	Hostname          string   `json:"hostname"`
	ReadTimeout       Duration `json:"read_timeout"`
	WriteTimeout      Duration `json:"write_timeout"`
	MaxMessageSize    int64    `json:"max_message_size"`
	MaxRecipients     int      `json:"max_recipients"`
	TLSCertFile       string   `json:"tls_cert_file"`
	TLSKeyFile        string   `json:"tls_key_file"`
	RequireTLS        bool     `json:"require_tls"`
	AllowInsecureAuth bool     `json:"allow_insecure_auth"`
	AllowedNetworks   []string `json:"allowed_networks"`
	InboundUsers      []string `json:"inbound_users"` // format "user:pass,user2:pass2"

	// Parsed runtime values
	ParsedNetworks  []*net.IPNet      `json:"-"`
	UserCredentials map[string]string `json:"-"`
}

// UpstreamRelay represents an individual upstream SMTP relay.
type UpstreamRelay struct {
	Name               string `json:"name"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	Password           string `json:"password"`
	AuthType           string `json:"auth_type"` // AUTO, PLAIN, LOGIN, NONE
	TLSType            string `json:"tls_type"`  // AUTO, STARTTLS, TLS, NONE
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
}

// RelayConfig defines outbound smart-host relay and routing settings.
type RelayConfig struct {
	Host               string            `json:"host"`
	Port               int               `json:"port"`
	Username           string            `json:"username"`
	Password           string            `json:"password"`
	AuthType           string            `json:"auth_type"` // AUTO, PLAIN, LOGIN, NONE
	TLSType            string            `json:"tls_type"`  // AUTO, STARTTLS, TLS (SMTPS), NONE
	InsecureSkipVerify bool              `json:"insecure_skip_verify"`
	SenderOverride     string            `json:"sender_override"`
	AddHeaders         map[string]string `json:"add_headers"`

	// Multi-relay and Smart Routing
	Upstreams    []UpstreamRelay   `json:"upstreams"`
	Strategy     string            `json:"strategy"` // failover, round-robin
	DomainRoutes map[string]string `json:"domain_routes"`
}

// QueueConfig defines email spooling and retry behavior.
type QueueConfig struct {
	Enabled        bool     `json:"enabled"`
	SpoolDir       string   `json:"spool_dir"`
	MaxRetries     int      `json:"max_retries"`
	RetryInterval  Duration `json:"retry_interval"`
	MaxConcurrency int      `json:"max_concurrency"`
}

// HTTPConfig defines the healthcheck, metrics, and REST API HTTP server settings.
type HTTPConfig struct {
	Enabled          bool   `json:"enabled"`
	ListenAddr       string `json:"listen_addr"`
	APIKey           string `json:"api_key"`
	DashboardEnabled bool   `json:"dashboard_enabled"`
}

// RateLimitConfig defines rate limiting parameters.
type RateLimitConfig struct {
	Enabled      bool `json:"enabled"`
	MaxPerMinute int  `json:"max_per_minute"`
	Burst        int  `json:"burst"`
}

// WebhookConfig defines event delivery webhook settings.
type WebhookConfig struct {
	Enabled bool     `json:"enabled"`
	URL     string   `json:"url"`
	Secret  string   `json:"secret"`
	Timeout Duration `json:"timeout"`
}

// LoggingConfig defines structured logging parameters.
type LoggingConfig struct {
	Level  string `json:"level"`  // debug, info, warn, error
	Format string `json:"format"` // json, text
}

// Duration is a wrapper around time.Duration for JSON parsing.
type Duration struct {
	time.Duration
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case float64:
		d.Duration = time.Duration(value)
		return nil
	case string:
		var err error
		d.Duration, err = time.ParseDuration(value)
		return err
	default:
		return fmt.Errorf("invalid duration: %v", v)
	}
}

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// Default RFC private and loopback network CIDRs
const (
	CIDRLoopbackIPv4  = "127.0.0.0/8"   // NOSONAR Loopback IPv4
	CIDRPrivateClassA = "10.0.0.0/8"    // NOSONAR RFC 1918 Class A
	CIDRPrivateClassB = "172.16.0.0/12" // NOSONAR RFC 1918 Class B
	CIDRPrivateClassC = "192.168.0.0/16"// NOSONAR RFC 1918 Class C
	CIDRLoopbackIPv6  = "::1/128"       // NOSONAR Loopback IPv6
	CIDRPrivateIPv6   = "fc00::/7"      // NOSONAR RFC 4193 Unique Local
	CIDRLinkLocalIPv6 = "fe80::/10"     // NOSONAR Link-Local IPv6
)

// DefaultConfig returns a Config with sensible default values.
func DefaultConfig() *Config {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "mailer-go"
	}

	return &Config{
		Server: ServerConfig{
			ListenAddr:        ":2525",
			Hostname:          hostname,
			ReadTimeout:       Duration{Duration: 60 * time.Second},
			WriteTimeout:      Duration{Duration: 60 * time.Second},
			MaxMessageSize:    25 * 1024 * 1024, // 25 MB
			MaxRecipients:     50,
			AllowInsecureAuth: false,
			RequireTLS:        false,
			AllowedNetworks: []string{
				CIDRLoopbackIPv4,
				CIDRPrivateClassA,
				CIDRPrivateClassB,
				CIDRPrivateClassC,
				CIDRLoopbackIPv6,
				CIDRPrivateIPv6,
				CIDRLinkLocalIPv6,
			},
			InboundUsers:    nil,
			ParsedNetworks:  nil,
			UserCredentials: make(map[string]string),
		},
		Relay: RelayConfig{
			Host:               "",
			Port:               587,
			Username:           "",
			Password:           "",
			AuthType:           "AUTO",
			TLSType:            "AUTO",
			InsecureSkipVerify: false,
			SenderOverride:     "",
			AddHeaders: map[string]string{
				"X-Relayed-By": "mailer-go",
			},
			Upstreams:    nil,
			Strategy:     "failover",
			DomainRoutes: make(map[string]string),
		},
		Queue: QueueConfig{
			Enabled:        true,
			SpoolDir:       "",
			MaxRetries:     5,
			RetryInterval:  Duration{Duration: 1 * time.Minute},
			MaxConcurrency: 10,
		},
		HTTP: HTTPConfig{
			Enabled:          true,
			ListenAddr:       ":8080",
			APIKey:           "",
			DashboardEnabled: true,
		},
		RateLimit: RateLimitConfig{
			Enabled:      false,
			MaxPerMinute: 120,
			Burst:        30,
		},
		Webhook: WebhookConfig{
			Enabled: false,
			URL:     "",
			Secret:  "",
			Timeout: Duration{Duration: 10 * time.Second},
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "text",
		},
	}
}

// Load loads configuration from environment variables, overriding defaults.
func Load() (*Config, error) {
	cfg := DefaultConfig()

	// Optionally load config file if specified
	if configFile := getEnv("CONFIG_FILE", ""); configFile != "" {
		if err := loadFromFile(configFile, cfg); err != nil {
			return nil, fmt.Errorf("failed to load config file: %w", err)
		}
	}

	loadServerEnv(&cfg.Server)
	loadRelayEnv(&cfg.Relay)
	loadQueueEnv(&cfg.Queue)
	loadHTTPEnv(&cfg.HTTP)
	loadRateLimitEnv(&cfg.RateLimit)
	loadWebhookEnv(&cfg.Webhook)
	loadLoggingEnv(&cfg.Logging)

	if err := cfg.Compile(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func loadServerEnv(s *ServerConfig) {
	loadServerNetworkEnv(s)
	loadServerLimitsEnv(s)
	loadServerSecurityEnv(s)
}

func loadServerNetworkEnv(s *ServerConfig) {
	if val := getEnv("SERVER_LISTEN_ADDR", getEnv("SMTP_PORT", "")); val != "" {
		if !strings.Contains(val, ":") {
			s.ListenAddr = ":" + val
		} else {
			s.ListenAddr = val
		}
	}
	if val := getEnv("SERVER_HOSTNAME", getEnv("MYHOSTNAME", "")); val != "" {
		s.Hostname = val
	}
	if val := getEnv("ALLOWED_NETWORKS", getEnv("MYNETWORKS", "")); val != "" {
		if parts := splitList(val); len(parts) > 0 {
			s.AllowedNetworks = parts
		}
	}
	if val := getEnv("INBOUND_USERS", getEnv("SMTP_USER_PASS", "")); val != "" {
		s.InboundUsers = splitList(val)
	}
}

func loadServerLimitsEnv(s *ServerConfig) {
	if val := getEnv("SERVER_READ_TIMEOUT", ""); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			s.ReadTimeout = Duration{Duration: d}
		}
	}
	if val := getEnv("SERVER_WRITE_TIMEOUT", ""); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			s.WriteTimeout = Duration{Duration: d}
		}
	}
	if val := getEnv("SERVER_MAX_MESSAGE_SIZE", getEnv("MESSAGE_SIZE_LIMIT", "")); val != "" {
		if size, err := strconv.ParseInt(val, 10, 64); err == nil {
			s.MaxMessageSize = size
		}
	}
	if val := getEnv("SERVER_MAX_RECIPIENTS", ""); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			s.MaxRecipients = n
		}
	}
}

func loadServerSecurityEnv(s *ServerConfig) {
	if val := getEnv("SERVER_TLS_CERT", ""); val != "" {
		s.TLSCertFile = val
	}
	if val := getEnv("SERVER_TLS_KEY", ""); val != "" {
		s.TLSKeyFile = val
	}
	if val := getEnv("SERVER_REQUIRE_TLS", ""); val != "" {
		s.RequireTLS = parseBool(val, s.RequireTLS)
	}
	if val := getEnv("SERVER_ALLOW_INSECURE_AUTH", ""); val != "" {
		s.AllowInsecureAuth = parseBool(val, s.AllowInsecureAuth)
	}
}

func loadRelayEnv(r *RelayConfig) {
	if val := getEnv("RELAY_HOST", getEnv("RELAYHOST", "")); val != "" {
		if host, portStr, err := net.SplitHostPort(val); err == nil {
			r.Host = host
			if port, err := strconv.Atoi(portStr); err == nil {
				r.Port = port
			}
		} else {
			r.Host = val
		}
	}
	if val := getEnv("RELAY_PORT", ""); val != "" {
		if p, err := strconv.Atoi(val); err == nil {
			r.Port = p
		}
	}
	if val := getEnv("RELAY_USER", getEnv("RELAY_USERNAME", getEnv("SMTP_USERNAME", ""))); val != "" {
		r.Username = val
	}
	if val := getEnv("RELAY_PASSWORD", getEnv("RELAY_PASS", getEnv("SMTP_PASSWORD", ""))); val != "" {
		r.Password = val
	}
	if val := getEnv("RELAY_AUTH_TYPE", ""); val != "" {
		r.AuthType = strings.ToUpper(val)
	}
	if val := getEnv("RELAY_TLS_TYPE", ""); val != "" {
		r.TLSType = strings.ToUpper(val)
	}
	if val := getEnv("RELAY_INSECURE_SKIP_VERIFY", ""); val != "" {
		r.InsecureSkipVerify = parseBool(val, r.InsecureSkipVerify)
	}
	if val := getEnv("SENDER_OVERRIDE", getEnv("MASQUERADE_DOMAINS", "")); val != "" {
		r.SenderOverride = val
	}
	if val := getEnv("RELAY_STRATEGY", ""); val != "" {
		r.Strategy = strings.ToLower(val)
	}
	if val := getEnv("RELAY_UPSTREAMS", ""); val != "" {
		r.Upstreams = parseUpstreamsEnv(val)
	}
	if val := getEnv("RELAY_DOMAIN_ROUTES", ""); val != "" {
		r.DomainRoutes = parseDomainRoutesEnv(val)
	}
}

func parseUpstreamsEnv(val string) []UpstreamRelay {
	val = strings.TrimSpace(val)
	if strings.HasPrefix(val, "[") {
		var list []UpstreamRelay
		if err := json.Unmarshal([]byte(val), &list); err == nil {
			return list
		}
	}

	var upstreams []UpstreamRelay
	entries := splitList(val)
	for i, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		port := 587
		tlsType := "AUTO"
		if strings.HasPrefix(entry, "smtps://") {
			port = 465
			tlsType = "TLS"
			entry = strings.TrimPrefix(entry, "smtps://")
		} else if strings.HasPrefix(entry, "smtp://") {
			entry = strings.TrimPrefix(entry, "smtp://")
		}

		username := ""
		password := ""
		if strings.Contains(entry, "@") {
			parts := strings.SplitN(entry, "@", 2)
			userInfo := parts[0]
			entry = parts[1]
			if strings.Contains(userInfo, ":") {
				uParts := strings.SplitN(userInfo, ":", 2)
				username = uParts[0]
				password = uParts[1]
			} else {
				username = userInfo
			}
		}

		host := entry
		if h, pStr, err := net.SplitHostPort(entry); err == nil {
			host = h
			if p, err := strconv.Atoi(pStr); err == nil {
				port = p
			}
		}

		name := host
		if name == "" {
			name = fmt.Sprintf("upstream-%d", i+1)
		}

		upstreams = append(upstreams, UpstreamRelay{
			Name:     name,
			Host:     host,
			Port:     port,
			Username: username,
			Password: password,
			AuthType: "AUTO",
			TLSType:  tlsType,
		})
	}
	return upstreams
}

func parseDomainRoutesEnv(val string) map[string]string {
	routes := make(map[string]string)
	// Check if JSON object
	if strings.HasPrefix(strings.TrimSpace(val), "{") {
		_ = json.Unmarshal([]byte(val), &routes)
		return routes
	}

	// Comma separated list of domain=target or domain:target
	entries := splitList(val)
	for _, entry := range entries {
		var domain, target string
		if strings.Contains(entry, "=") {
			parts := strings.SplitN(entry, "=", 2)
			domain = strings.TrimSpace(parts[0])
			target = strings.TrimSpace(parts[1])
		} else if strings.Contains(entry, "->") {
			parts := strings.SplitN(entry, "->", 2)
			domain = strings.TrimSpace(parts[0])
			target = strings.TrimSpace(parts[1])
		}
		if domain != "" && target != "" {
			routes[strings.ToLower(domain)] = target
		}
	}
	return routes
}

func loadQueueEnv(q *QueueConfig) {
	if val := getEnv("QUEUE_ENABLED", ""); val != "" {
		q.Enabled = parseBool(val, q.Enabled)
	}
	if val := getEnv("QUEUE_SPOOL_DIR", getEnv("SPOOL_DIR", "")); val != "" {
		q.SpoolDir = val
	}
	if val := getEnv("QUEUE_MAX_RETRIES", ""); val != "" {
		if r, err := strconv.Atoi(val); err == nil {
			q.MaxRetries = r
		}
	}
	if val := getEnv("QUEUE_RETRY_INTERVAL", ""); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			q.RetryInterval = Duration{Duration: d}
		}
	}
	if val := getEnv("QUEUE_MAX_CONCURRENCY", ""); val != "" {
		if c, err := strconv.Atoi(val); err == nil {
			q.MaxConcurrency = c
		}
	}
}

func loadHTTPEnv(h *HTTPConfig) {
	if val := getEnv("HTTP_ENABLED", ""); val != "" {
		h.Enabled = parseBool(val, h.Enabled)
	}
	if val := getEnv("HTTP_LISTEN_ADDR", getEnv("HTTP_PORT", "")); val != "" {
		if !strings.Contains(val, ":") {
			h.ListenAddr = ":" + val
		} else {
			h.ListenAddr = val
		}
	}
	if val := getEnv("HTTP_API_KEY", getEnv("API_KEY", "")); val != "" {
		h.APIKey = val
	}
	if val := getEnv("HTTP_DASHBOARD_ENABLED", ""); val != "" {
		h.DashboardEnabled = parseBool(val, h.DashboardEnabled)
	}
}

func loadRateLimitEnv(rl *RateLimitConfig) {
	if val := getEnv("RATE_LIMIT_ENABLED", ""); val != "" {
		rl.Enabled = parseBool(val, rl.Enabled)
	}
	if val := getEnv("RATE_LIMIT_MAX_PER_MINUTE", ""); val != "" {
		if m, err := strconv.Atoi(val); err == nil {
			rl.MaxPerMinute = m
		}
	}
	if val := getEnv("RATE_LIMIT_BURST", ""); val != "" {
		if b, err := strconv.Atoi(val); err == nil {
			rl.Burst = b
		}
	}
}

func loadWebhookEnv(w *WebhookConfig) {
	if val := getEnv("WEBHOOK_ENABLED", ""); val != "" {
		w.Enabled = parseBool(val, w.Enabled)
	}
	if val := getEnv("WEBHOOK_URL", ""); val != "" {
		w.URL = val
	}
	if val := getEnv("WEBHOOK_SECRET", ""); val != "" {
		w.Secret = val
	}
	if val := getEnv("WEBHOOK_TIMEOUT", ""); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			w.Timeout = Duration{Duration: d}
		}
	}
}

func loadLoggingEnv(l *LoggingConfig) {
	if val := getEnv("LOG_LEVEL", ""); val != "" {
		l.Level = strings.ToLower(val)
	}
	if val := getEnv("LOG_FORMAT", ""); val != "" {
		l.Format = strings.ToLower(val)
	}
}

// Compile parses and compiles CIDRs and credentials into memory for fast lookup.
func (c *Config) Compile() error {
	if err := c.compileNetworks(); err != nil {
		return err
	}
	c.compileCredentials()
	c.compileRelay()
	return nil
}

func (c *Config) compileNetworks() error {
	c.Server.ParsedNetworks = make([]*net.IPNet, 0, len(c.Server.AllowedNetworks))
	for _, cidr := range c.Server.AllowedNetworks {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		ipNet, err := parseCIDRString(cidr)
		if err != nil {
			return err
		}
		c.Server.ParsedNetworks = append(c.Server.ParsedNetworks, ipNet)
	}
	return nil
}

func parseCIDRString(cidr string) (*net.IPNet, error) {
	if !strings.Contains(cidr, "/") {
		ip := net.ParseIP(cidr)
		if ip == nil {
			return nil, fmt.Errorf("invalid allowed IP or CIDR: %s", cidr)
		}
		if ip.To4() != nil {
			cidr += "/32"
		} else {
			cidr += "/128"
		}
	}
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid allowed CIDR %q: %w", cidr, err)
	}
	return ipNet, nil
}

func (c *Config) compileCredentials() {
	c.Server.UserCredentials = make(map[string]string)
	for _, userPass := range c.Server.InboundUsers {
		userPass = strings.TrimSpace(userPass)
		if userPass == "" {
			continue
		}
		parts := strings.SplitN(userPass, ":", 2)
		if len(parts) == 2 {
			c.Server.UserCredentials[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
}

func (c *Config) compileRelay() {
	// If Host is set but not in Upstreams list, add it as default upstream
	if c.Relay.Host != "" && len(c.Relay.Upstreams) == 0 {
		c.Relay.Upstreams = append(c.Relay.Upstreams, UpstreamRelay{
			Name:               "default",
			Host:               c.Relay.Host,
			Port:               c.Relay.Port,
			Username:           c.Relay.Username,
			Password:           c.Relay.Password,
			AuthType:           c.Relay.AuthType,
			TLSType:            c.Relay.TLSType,
			InsecureSkipVerify: c.Relay.InsecureSkipVerify,
		})
	}
}

// IsIPAllowed checks whether a remote IP address is in the AllowedNetworks list.
func (c *Config) IsIPAllowed(remoteIP net.IP) bool {
	if remoteIP == nil {
		return false
	}
	for _, ipNet := range c.Server.ParsedNetworks {
		if ipNet.Contains(remoteIP) {
			return true
		}
	}
	return false
}

// AuthenticateInbound validates inbound username and password.
func (c *Config) AuthenticateInbound(username, password string) bool {
	if len(c.Server.UserCredentials) == 0 {
		return false
	}
	expectedPass, ok := c.Server.UserCredentials[username]
	if !ok {
		return false
	}
	return expectedPass == password
}

func loadFromFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, cfg)
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return fallback
}

func parseBool(val string, def bool) bool {
	val = strings.ToLower(strings.TrimSpace(val))
	switch val {
	case "1", "t", "true", "yes", "y", "on":
		return true
	case "0", "f", "false", "no", "n", "off":
		return false
	default:
		return def
	}
}

func splitList(val string) []string {
	var res []string
	fields := strings.FieldsFunc(val, func(r rune) bool {
		return r == ',' || r == ';'
	})
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			res = append(res, f)
		}
	}
	return res
}
