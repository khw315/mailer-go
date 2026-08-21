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
	Server  ServerConfig  `json:"server"`
	Relay   RelayConfig   `json:"relay"`
	Queue   QueueConfig   `json:"queue"`
	HTTP    HTTPConfig    `json:"http"`
	Logging LoggingConfig `json:"logging"`
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
	AllowInsecureAuth bool     `json:"allow_insecure_auth"`
	AllowedNetworks   []string `json:"allowed_networks"`
	InboundUsers      []string `json:"inbound_users"` // format "user:pass,user2:pass2"

	// Parsed runtime values
	ParsedNetworks  []*net.IPNet      `json:"-"`
	UserCredentials map[string]string `json:"-"`
}

// RelayConfig defines outbound smart-host relay settings.
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
}

// QueueConfig defines email spooling and retry behavior.
type QueueConfig struct {
	Enabled        bool     `json:"enabled"`
	SpoolDir       string   `json:"spool_dir"`
	MaxRetries     int      `json:"max_retries"`
	RetryInterval  Duration `json:"retry_interval"`
	MaxConcurrency int      `json:"max_concurrency"`
}

// HTTPConfig defines the healthcheck and metrics HTTP server settings.
type HTTPConfig struct {
	Enabled    bool   `json:"enabled"`
	ListenAddr string `json:"listen_addr"`
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
			AllowedNetworks: []string{
				"127.0.0.0/8",
				"10.0.0.0/8",
				"172.16.0.0/12",
				"192.168.0.0/16",
				"::1/128",
				"fc00::/7",
				"fe80::/10",
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
		},
		Queue: QueueConfig{
			Enabled:        true,
			SpoolDir:       "",
			MaxRetries:     5,
			RetryInterval:  Duration{Duration: 1 * time.Minute},
			MaxConcurrency: 10,
		},
		HTTP: HTTPConfig{
			Enabled:    true,
			ListenAddr: ":8080",
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
	// support comma, space, or semicolon separated list
	fields := strings.FieldsFunc(val, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';'
	})
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			res = append(res, f)
		}
	}
	return res
}
