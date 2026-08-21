package relay

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"sort"
	"strings"
	"time"
	"github.com/khw315/mailer-go/internal/config"
	"github.com/khw315/mailer-go/internal/metrics"
)

// Relayer defines the interface for delivering emails.
type Relayer interface {
	Send(ctx context.Context, from string, to []string, msg []byte) error
}

// Client implements the Relayer interface.
type Client struct {
	cfg     *config.RelayConfig
	metrics *metrics.Metrics
	logger  *slog.Logger
}

// NewClient creates a new Relay client.
func NewClient(cfg *config.RelayConfig, m *metrics.Metrics, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	if m == nil {
		m = metrics.Default
	}
	return &Client{
		cfg:     cfg,
		metrics: m,
		logger:  logger,
	}
}

// Send delivers an email either via configured smart-host or direct MX lookup.
func (c *Client) Send(ctx context.Context, from string, to []string, msg []byte) error {
	if len(to) == 0 {
		return errors.New("relay: no recipient specified")
	}

	// Sender override if configured
	actualFrom := from
	if c.cfg.SenderOverride != "" {
		actualFrom = c.cfg.SenderOverride
	}

	// Prepare message data with custom headers
	finalMsg := c.injectHeaders(msg)

	// If relay host is configured, deliver via smart-host
	if c.cfg.Host != "" {
		err := c.sendViaSmartHost(ctx, actualFrom, to, finalMsg)
		if err != nil {
			c.metrics.IncFailed()
			return fmt.Errorf("smart-host delivery to %s:%d failed: %w", c.cfg.Host, c.cfg.Port, err)
		}
		c.metrics.IncRelayed()
		return nil
	}

	// Otherwise, deliver directly via DNS MX lookup grouped by recipient domain
	err := c.sendDirectMX(ctx, actualFrom, to, finalMsg)
	if err != nil {
		c.metrics.IncFailed()
		return fmt.Errorf("direct MX delivery failed: %w", err)
	}
	c.metrics.IncRelayed()
	return nil
}

// sendViaSmartHost connects and relays the email to the smart-host upstream.
func (c *Client) sendViaSmartHost(ctx context.Context, from string, to []string, msg []byte) error {
	addr := net.JoinHostPort(c.cfg.Host, fmt.Sprintf("%d", c.cfg.Port))
	c.logger.Debug("connecting to upstream smart-host", "addr", addr, "from", from, "recipients", len(to))

	tlsConfig := &tls.Config{
		ServerName:         c.cfg.Host,
		InsecureSkipVerify: c.cfg.InsecureSkipVerify,
	}

	var conn net.Conn
	var err error

	dialer := &net.Dialer{Timeout: 30 * time.Second}

	// Handle direct SMTPS (port 465 or TLSType TLS)
	isDirectTLS := c.cfg.TLSType == "TLS" || (c.cfg.TLSType == "AUTO" && c.cfg.Port == 465)
	if isDirectTLS {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("failed to dial %s: %w", addr, err)
	}
	defer conn.Close()

	// Wrap in deadline if context has deadline
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	// Create SMTP client
	client, err := smtp.NewClient(conn, c.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp handshake error: %w", err)
	}
	defer client.Quit()

	// Handle STARTTLS if not already direct TLS
	if !isDirectTLS && c.cfg.TLSType != "NONE" {
		if ok, _ := client.Extension("STARTTLS"); ok {
			c.logger.Debug("starting STARTTLS handshake with upstream", "host", c.cfg.Host)
			if err := client.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("starttls error: %w", err)
			}
		} else if c.cfg.TLSType == "STARTTLS" {
			return errors.New("upstream server does not support mandatory STARTTLS")
		}
	}

	// Authenticate if credentials are provided
	if c.cfg.Username != "" && c.cfg.Password != "" && c.cfg.AuthType != "NONE" {
		if err := c.authenticate(client); err != nil {
			return fmt.Errorf("authentication failed for %s: %w", c.cfg.Username, err)
		}
	}

	// MAIL FROM
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM <%s> rejected: %w", from, err)
	}

	// RCPT TO
	for _, recipient := range to {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("RCPT TO <%s> rejected: %w", recipient, err)
		}
	}

	// DATA
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA command failed: %w", err)
	}

	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("writing message body failed: %w", err)
	}

	if err := w.Close(); err != nil {
		return fmt.Errorf("closing data stream failed: %w", err)
	}

	c.logger.Info("email successfully relayed to smart-host", "host", c.cfg.Host, "from", from, "to", to)
	return nil
}

// authenticate attempts SASL authentication (PLAIN, LOGIN, or AUTO).
func (c *Client) authenticate(client *smtp.Client) error {
	hasAuth, authMechanisms := client.Extension("AUTH")
	c.logger.Debug("upstream auth mechanisms", "hasAuth", hasAuth, "mechanisms", authMechanisms)

	authType := strings.ToUpper(c.cfg.AuthType)

	// Custom SASL login auth helper for standard net/smtp
	if authType == "LOGIN" || (authType == "AUTO" && strings.Contains(strings.ToUpper(authMechanisms), "LOGIN") && !strings.Contains(strings.ToUpper(authMechanisms), "PLAIN")) {
		auth := &loginAuth{username: c.cfg.Username, password: c.cfg.Password}
		return client.Auth(auth)
	}

	// Standard PLAIN Auth
	auth := smtp.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.Host)
	if err := client.Auth(auth); err != nil {
		// Fallback to LOGIN if PLAIN failed and LOGIN mechanism is available
		if authType == "AUTO" && strings.Contains(strings.ToUpper(authMechanisms), "LOGIN") {
			c.logger.Debug("PLAIN auth failed, retrying with LOGIN auth")
			login := &loginAuth{username: c.cfg.Username, password: c.cfg.Password}
			return client.Auth(login)
		}
		return err
	}
	return nil
}

// sendDirectMX resolves MX records and delivers directly to recipient domains.
func (c *Client) sendDirectMX(ctx context.Context, from string, to []string, msg []byte) error {
	domainGroups, err := groupRecipientsByDomain(to)
	if err != nil {
		return err
	}

	for domain, recipients := range domainGroups {
		if err := c.deliverToDomain(ctx, domain, recipients, from, msg); err != nil {
			return err
		}
	}
	return nil
}

func groupRecipientsByDomain(to []string) (map[string][]string, error) {
	groups := make(map[string][]string)
	for _, recipient := range to {
		parts := strings.Split(recipient, "@")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid recipient address: %s", recipient)
		}
		domain := strings.ToLower(parts[1])
		groups[domain] = append(groups[domain], recipient)
	}
	return groups, nil
}

func (c *Client) deliverToDomain(ctx context.Context, domain string, recipients []string, from string, msg []byte) error {
	mxs, err := net.LookupMX(domain)
	if err != nil || len(mxs) == 0 {
		mxs = []*net.MX{{Host: domain, Pref: 10}}
	}

	sort.Slice(mxs, func(i, j int) bool {
		return mxs[i].Pref < mxs[j].Pref
	})

	var lastErr error
	for _, mx := range mxs {
		mxHost := strings.TrimSuffix(mx.Host, ".")
		if err := c.tryDeliverMX(ctx, mxHost, domain, recipients, from, msg); err == nil {
			c.logger.Info("direct MX email delivered", "domain", domain, "mx", mxHost, "recipients", recipients)
			return nil
		} else {
			lastErr = err
		}
	}

	return fmt.Errorf("failed delivering to domain %s: %w", domain, lastErr)
}

func (c *Client) tryDeliverMX(ctx context.Context, mxHost string, domain string, recipients []string, from string, msg []byte) error {
	c.logger.Debug("attempting direct MX delivery", "domain", domain, "mx", mxHost)
	addr := net.JoinHostPort(mxHost, "25")

	dialer := &net.Dialer{Timeout: 20 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, mxHost)
	if err != nil {
		return err
	}
	defer client.Quit()

	if ok, _ := client.Extension("STARTTLS"); ok {
		_ = client.StartTLS(&tls.Config{ServerName: mxHost, InsecureSkipVerify: true})
	}

	if err := client.Mail(from); err != nil {
		return err
	}

	var rcptCount int
	for _, rcpt := range recipients {
		if err := client.Rcpt(rcpt); err == nil {
			rcptCount++
		}
	}

	if rcptCount == 0 {
		return errors.New("all recipients rejected by MX server")
	}

	w, err := client.Data()
	if err != nil {
		return err
	}

	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return err
	}

	return w.Close()
}

// injectHeaders adds custom headers (like X-Relayed-By) if not already present.
func (c *Client) injectHeaders(msg []byte) []byte {
	if len(c.cfg.AddHeaders) == 0 {
		return msg
	}

	var headerBuf bytes.Buffer
	for k, v := range c.cfg.AddHeaders {
		// Avoid duplicate header insertion
		headerPrefix := []byte(strings.ToLower(k) + ":")
		if !bytes.Contains(bytes.ToLower(msg), headerPrefix) {
			headerBuf.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
		}
	}

	if headerBuf.Len() == 0 {
		return msg
	}

	return append(headerBuf.Bytes(), msg...)
}

// loginAuth implements smtp.Auth for the SASL LOGIN mechanism.
type loginAuth struct {
	username, password string
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", []byte{}, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		prompt := strings.ToLower(string(fromServer))
		if strings.Contains(prompt, "username") || strings.Contains(prompt, "user") || bytes.Equal(fromServer, []byte("VXNlcm5hbWU6")) {
			return []byte(a.username), nil
		}
		if strings.Contains(prompt, "password") || strings.Contains(prompt, "pass") || bytes.Equal(fromServer, []byte("UGFzc3dvcmQ6")) {
			return []byte(a.password), nil
		}
		// If prompt is empty, send username on first step
		return []byte(a.username), nil
	}
	return nil, nil
}


