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
	"sync/atomic"
	"time"

	"github.com/khw315/mailer-go/internal/config"
	"github.com/khw315/mailer-go/internal/metrics"
)

// Relayer defines the interface for delivering emails.
type Relayer interface {
	Send(ctx context.Context, from string, to []string, msg []byte) error
}

// Client implements the Relayer interface with multi-relay failover and smart routing.
type Client struct {
	cfg       *config.RelayConfig
	metrics   *metrics.Metrics
	logger    *slog.Logger
	rrCounter atomic.Uint64
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

// Send delivers an email either via domain routing, smart-host upstreams, or direct MX lookup.
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

	// Check domain-specific routing rules
	if len(c.cfg.DomainRoutes) > 0 {
		domainGroups, err := groupRecipientsByDomain(to)
		if err == nil {
			// Check if any domain has a specific route
			hasSpecialRoute := false
			for domain := range domainGroups {
				if _, ok := c.cfg.DomainRoutes[domain]; ok {
					hasSpecialRoute = true
					break
				}
			}

			if hasSpecialRoute {
				return c.sendWithDomainRoutes(ctx, actualFrom, domainGroups, finalMsg)
			}
		}
	}

	// Send via upstream relays if configured
	upstreams := c.getUpstreams()
	if len(upstreams) > 0 {
		err := c.sendViaUpstreams(ctx, actualFrom, to, finalMsg, upstreams)
		if err == nil {
			c.metrics.IncRelayed()
			return nil
		}
		c.logger.Warn("all configured upstreams failed, attempting direct MX fallback if applicable", "err", err)
	}

	// Direct MX delivery fallback
	err := c.sendDirectMX(ctx, actualFrom, to, finalMsg)
	if err != nil {
		c.metrics.IncFailed()
		return fmt.Errorf("delivery failed: %w", err)
	}
	c.metrics.IncRelayed()
	return nil
}

func (c *Client) getUpstreams() []config.UpstreamRelay {
	if len(c.cfg.Upstreams) > 0 {
		return c.cfg.Upstreams
	}
	if c.cfg.Host != "" {
		return []config.UpstreamRelay{
			{
				Name:               "primary",
				Host:               c.cfg.Host,
				Port:               c.cfg.Port,
				Username:           c.cfg.Username,
				Password:           c.cfg.Password,
				AuthType:           c.cfg.AuthType,
				TLSType:            c.cfg.TLSType,
				InsecureSkipVerify: c.cfg.InsecureSkipVerify,
			},
		}
	}
	return nil
}

func (c *Client) sendWithDomainRoutes(ctx context.Context, from string, domainGroups map[string][]string, msg []byte) error {
	for domain, recipients := range domainGroups {
		target, hasRoute := c.cfg.DomainRoutes[domain]
		if hasRoute {
			// Find matching named upstream or create ad-hoc upstream from target host:port
			upstream := c.resolveTargetUpstream(target)
			err := c.sendViaSingleUpstream(ctx, from, recipients, msg, upstream)
			if err != nil {
				c.metrics.IncFailed()
				return fmt.Errorf("domain route delivery for %s via %s failed: %w", domain, target, err)
			}
			c.metrics.IncRelayed()
		} else {
			// Send through standard upstream or direct MX
			upstreams := c.getUpstreams()
			if len(upstreams) > 0 {
				if err := c.sendViaUpstreams(ctx, from, recipients, msg, upstreams); err == nil {
					c.metrics.IncRelayed()
					continue
				}
			}
			if err := c.deliverToDomain(ctx, domain, recipients, from, msg); err != nil {
				c.metrics.IncFailed()
				return err
			}
			c.metrics.IncRelayed()
		}
	}
	return nil
}

func (c *Client) resolveTargetUpstream(target string) config.UpstreamRelay {
	for _, u := range c.cfg.Upstreams {
		if strings.EqualFold(u.Name, target) {
			return u
		}
	}
	// Parse as host or host:port
	host := target
	port := 587
	if h, p, err := net.SplitHostPort(target); err == nil {
		host = h
		if portNum, err := fmt.Sscanf(p, "%d", &port); err != nil || portNum == 0 {
			port = 587
		}
	}
	return config.UpstreamRelay{
		Name:     target,
		Host:     host,
		Port:     port,
		AuthType: "NONE",
		TLSType:  "AUTO",
	}
}

func (c *Client) sendViaUpstreams(ctx context.Context, from string, to []string, msg []byte, upstreams []config.UpstreamRelay) error {
	if len(upstreams) == 1 {
		return c.sendViaSingleUpstream(ctx, from, to, msg, upstreams[0])
	}

	strategy := strings.ToLower(c.cfg.Strategy)
	if strategy == "round-robin" {
		idx := int(c.rrCounter.Add(1)-1) % len(upstreams)
		// Reorder upstreams starting with idx
		ordered := append([]config.UpstreamRelay{}, upstreams[idx:]...)
		ordered = append(ordered, upstreams[:idx]...)
		return c.tryUpstreamList(ctx, from, to, msg, ordered)
	}

	// Default: failover
	return c.tryUpstreamList(ctx, from, to, msg, upstreams)
}

func (c *Client) tryUpstreamList(ctx context.Context, from string, to []string, msg []byte, upstreams []config.UpstreamRelay) error {
	var lastErr error
	for _, upstream := range upstreams {
		c.logger.Debug("attempting delivery via upstream", "upstream", upstream.Name, "host", upstream.Host, "port", upstream.Port)
		err := c.sendViaSingleUpstream(ctx, from, to, msg, upstream)
		if err == nil {
			return nil
		}
		c.logger.Warn("upstream relay failed, trying next upstream", "upstream", upstream.Name, "host", upstream.Host, "err", err)
		lastErr = err
	}
	return fmt.Errorf("all upstreams failed, last error: %w", lastErr)
}

func (c *Client) sendViaSingleUpstream(ctx context.Context, from string, to []string, msg []byte, u config.UpstreamRelay) error {
	if u.Port == 0 {
		u.Port = 587
	}
	addr := net.JoinHostPort(u.Host, fmt.Sprintf("%d", u.Port))
	c.logger.Debug("connecting to upstream smart-host", "addr", addr, "from", from, "recipients", len(to))

	tlsConfig := &tls.Config{
		ServerName:         u.Host,
		InsecureSkipVerify: u.InsecureSkipVerify,
		MinVersion:         tls.VersionTLS12,
	}

	var conn net.Conn
	var err error

	dialer := &net.Dialer{Timeout: 30 * time.Second}

	isDirectTLS := u.TLSType == "TLS" || (u.TLSType == "AUTO" && u.Port == 465)
	if isDirectTLS {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("failed to dial %s: %w", addr, err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	client, err := smtp.NewClient(conn, u.Host) // NOSONAR: Socket connection upgraded via STARTTLS or direct SMTPS
	if err != nil {
		return fmt.Errorf("smtp handshake error: %w", err)
	}
	defer func() { _ = client.Quit() }()

	if !isDirectTLS && u.TLSType != "NONE" {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("starttls error: %w", err)
			}
		} else if u.TLSType == "STARTTLS" {
			return errors.New("upstream server does not support mandatory STARTTLS")
		}
	}

	if u.Username != "" && u.Password != "" && u.AuthType != "NONE" {
		if err := c.authenticateUpstream(client, u); err != nil {
			return fmt.Errorf("authentication failed for %s: %w", u.Username, err)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM <%s> rejected: %w", from, err)
	}

	for _, recipient := range to {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("RCPT TO <%s> rejected: %w", recipient, err)
		}
	}

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

	c.logger.Info("email successfully relayed to smart-host", "host", u.Host, "port", u.Port, "from", from, "to", to)
	return nil
}

func (c *Client) authenticateUpstream(client *smtp.Client, u config.UpstreamRelay) error {
	hasAuth, authMechanisms := client.Extension("AUTH")
	c.logger.Debug("upstream auth mechanisms", "hasAuth", hasAuth, "mechanisms", authMechanisms)

	authType := strings.ToUpper(u.AuthType)

	if authType == "LOGIN" || (authType == "AUTO" && strings.Contains(strings.ToUpper(authMechanisms), "LOGIN") && !strings.Contains(strings.ToUpper(authMechanisms), "PLAIN")) {
		auth := &loginAuth{username: u.Username, password: u.Password}
		return client.Auth(auth)
	}

	auth := smtp.PlainAuth("", u.Username, u.Password, u.Host)
	if err := client.Auth(auth); err != nil {
		if authType == "AUTO" && strings.Contains(strings.ToUpper(authMechanisms), "LOGIN") {
			c.logger.Debug("PLAIN auth failed, retrying with LOGIN auth")
			login := &loginAuth{username: u.Username, password: u.Password}
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

	// Direct MX delivery initiates over port 25 and upgrades via opportunistic STARTTLS (RFC 3207)
	client, err := smtp.NewClient(conn, mxHost) // NOSONAR: Opportunistic STARTTLS handshake over port 25 (RFC 3207)
	if err != nil {
		return err
	}
	defer func() { _ = client.Quit() }()

	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsConfig := &tls.Config{
			ServerName:         mxHost,
			InsecureSkipVerify: c.cfg.InsecureSkipVerify,
			MinVersion:         tls.VersionTLS12,
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			c.logger.Debug("direct MX STARTTLS handshake failed, continuing with plain delivery", "mx", mxHost, "err", err)
		}
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
		return []byte(a.username), nil
	}
	return nil, nil
}
