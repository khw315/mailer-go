package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/khw315/mailer-go/internal/config"
	"github.com/khw315/mailer-go/internal/metrics"
	"github.com/khw315/mailer-go/internal/queue"
	"github.com/khw315/mailer-go/internal/relay"
)

// Server is the inbound SMTP server.
type Server struct {
	cfg        *config.Config
	smtpServer *smtp.Server
	queue      *queue.Queue
	relay      relay.Relayer
	metrics    *metrics.Metrics
	logger     *slog.Logger
}

// New creates a new SMTP inbound Server.
func New(cfg *config.Config, q *queue.Queue, r relay.Relayer, m *metrics.Metrics, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if m == nil {
		m = metrics.Default
	}

	s := &Server{
		cfg:     cfg,
		queue:   q,
		relay:   r,
		metrics: m,
		logger:  logger,
	}

	backend := &Backend{
		server: s,
	}

	smtpSrv := smtp.NewServer(backend)
	smtpSrv.Addr = cfg.Server.ListenAddr
	smtpSrv.Domain = cfg.Server.Hostname
	smtpSrv.ReadTimeout = cfg.Server.ReadTimeout.Duration
	smtpSrv.WriteTimeout = cfg.Server.WriteTimeout.Duration
	smtpSrv.MaxMessageBytes = cfg.Server.MaxMessageSize
	smtpSrv.MaxRecipients = cfg.Server.MaxRecipients
	smtpSrv.AllowInsecureAuth = cfg.Server.AllowInsecureAuth

	// Load TLS Certificate if provided
	if cfg.Server.TLSCertFile != "" && cfg.Server.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS key pair: %w", err)
		}
		smtpSrv.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
		}
	}

	s.smtpServer = smtpSrv
	return s, nil
}

// Start begins accepting inbound SMTP connections.
func (s *Server) Start() error {
	s.logger.Info("starting SMTP inbound server",
		"listen_addr", s.cfg.Server.ListenAddr,
		"hostname", s.cfg.Server.Hostname,
		"max_message_size", s.cfg.Server.MaxMessageSize,
	)
	return s.smtpServer.ListenAndServe()
}

// Serve accepts connections on an existing listener (useful for tests).
func (s *Server) Serve(l net.Listener) error {
	return s.smtpServer.Serve(l)
}

// Close gracefully stops the inbound SMTP server.
func (s *Server) Close() error {
	s.logger.Info("shutting down SMTP inbound server")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.smtpServer.Shutdown(ctx)
}

// Backend implements smtp.Backend.
type Backend struct {
	server *Server
}

// NewSession creates a new SMTP Session per client connection.
func (b *Backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	remoteIP := extractIP(c.Conn().RemoteAddr())
	isAllowedNet := b.server.cfg.IsIPAllowed(remoteIP)

	b.server.logger.Debug("new inbound SMTP connection",
		"remote_addr", c.Conn().RemoteAddr().String(),
		"ip", remoteIP.String(),
		"allowed_by_network", isAllowedNet,
	)

	return &Session{
		server:          b.server,
		conn:            c,
		remoteIP:        remoteIP,
		isAllowedByNet:  isAllowedNet,
		isAuthenticated: false,
		recipients:      make([]string, 0),
	}, nil
}

// Session handles the lifecycle of an individual inbound SMTP connection.
type Session struct {
	server          *Server
	conn            *smtp.Conn
	remoteIP        net.IP
	isAllowedByNet  bool
	isAuthenticated bool
	username        string
	from            string
	recipients      []string
}

// Ensure AuthSession interface is implemented
var _ smtp.AuthSession = (*Session)(nil)

// AuthMechanisms returns the supported SASL mechanisms.
func (s *Session) AuthMechanisms() []string {
	return []string{sasl.Plain, sasl.Login}
}

// Auth handles SASL authentication (PLAIN / LOGIN).
func (s *Session) Auth(mech string) (sasl.Server, error) {
	switch strings.ToUpper(mech) {
	case sasl.Plain:
		return sasl.NewPlainServer(func(identity, username, password string) error {
			if s.server.cfg.AuthenticateInbound(username, password) {
				s.isAuthenticated = true
				s.username = username
				s.server.logger.Info("inbound client authenticated via PLAIN", "user", username, "ip", s.remoteIP.String())
				return nil
			}
			s.server.logger.Warn("inbound PLAIN authentication failed", "user", username, "ip", s.remoteIP.String())
			return smtp.ErrAuthFailed
		}), nil
	case sasl.Login:
		return NewLoginServer(func(username, password string) error {
			if s.server.cfg.AuthenticateInbound(username, password) {
				s.isAuthenticated = true
				s.username = username
				s.server.logger.Info("inbound client authenticated via LOGIN", "user", username, "ip", s.remoteIP.String())
				return nil
			}
			s.server.logger.Warn("inbound LOGIN authentication failed", "user", username, "ip", s.remoteIP.String())
			return smtp.ErrAuthFailed
		}), nil
	default:
		return nil, smtp.ErrAuthUnknownMechanism
	}
}

// Mail handles MAIL FROM command.
func (s *Session) Mail(from string, opts *smtp.MailOptions) error {
	// Verify relay permissions (either allowed by IP whitelist or authenticated)
	if !s.isAllowedByNet && !s.isAuthenticated {
		s.server.logger.Warn("relay access denied for unauthenticated client not in allowed networks",
			"ip", s.remoteIP.String(), "from", from)
		return &smtp.SMTPError{
			Code:         554,
			EnhancedCode: smtp.EnhancedCode{5, 7, 1},
			Message:      "Relay access denied - client IP not whitelisted and not authenticated",
		}
	}

	s.from = from
	s.recipients = s.recipients[:0]
	return nil
}

// Rcpt handles RCPT TO command.
func (s *Session) Rcpt(to string, opts *smtp.RcptOptions) error {
	if !s.isAllowedByNet && !s.isAuthenticated {
		return &smtp.SMTPError{
			Code:         554,
			EnhancedCode: smtp.EnhancedCode{5, 7, 1},
			Message:      "Relay access denied",
		}
	}

	s.recipients = append(s.recipients, to)
	return nil
}

// Data receives and processes the email message body.
func (s *Session) Data(r io.Reader) error {
	s.server.metrics.IncReceived()

	msgBytes, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading message data failed: %w", err)
	}

	s.server.logger.Info("received email",
		"from", s.from,
		"recipients", s.recipients,
		"size_bytes", len(msgBytes),
		"authenticated", s.isAuthenticated,
		"allowed_by_net", s.isAllowedByNet,
	)

	// If queue is enabled, enqueue for async delivery / retry
	if s.server.queue != nil && s.server.cfg.Queue.Enabled {
		id, err := s.server.queue.Enqueue(s.from, s.recipients, msgBytes)
		if err != nil {
			s.server.logger.Error("failed to enqueue email", "err", err)
			return &smtp.SMTPError{
				Code:         451,
				EnhancedCode: smtp.EnhancedCode{4, 3, 0},
				Message:      "Mail server temporary queue error",
			}
		}
		s.server.logger.Debug("email enqueued with id", "id", id)
		return nil
	}

	// Direct synchronous relay
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err = s.server.relay.Send(ctx, s.from, s.recipients, msgBytes)
	if err != nil {
		s.server.logger.Error("synchronous relay failed", "err", err)
		return &smtp.SMTPError{
			Code:         451,
			EnhancedCode: smtp.EnhancedCode{4, 4, 0},
			Message:      fmt.Sprintf("Relay delivery failed: %v", err),
		}
	}

	return nil
}

// Reset resets the current session state.
func (s *Session) Reset() {
	s.from = ""
	s.recipients = s.recipients[:0]
}

// Logout terminates the session.
func (s *Session) Logout() error {
	return nil
}

func extractIP(addr net.Addr) net.IP {
	if addr == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	return net.ParseIP(host)
}
