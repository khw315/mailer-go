package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/khw315/mailer-go/internal/config"
	"github.com/khw315/mailer-go/internal/metrics"
)

// Server handles health checks, status, and Prometheus metrics.
type Server struct {
	cfg     *config.HTTPConfig
	metrics *metrics.Metrics
	logger  *slog.Logger
	httpSrv *http.Server
}

// NewServer creates a new HTTP observability server.
func NewServer(cfg *config.HTTPConfig, m *metrics.Metrics, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if m == nil {
		m = metrics.Default
	}

	s := &Server{
		cfg:     cfg,
		metrics: m,
		logger:  logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/api/stats", s.handleStats)

	s.httpSrv = &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	return s
}

// Start begins listening on the HTTP port.
func (s *Server) Start() error {
	s.logger.Info("starting HTTP health & metrics server", "addr", s.cfg.ListenAddr)
	if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server failed: %w", err)
	}
	return nil
}

// Stop gracefully shuts down the HTTP server.
func (s *Server) Stop(ctx context.Context) error {
	s.logger.Info("shutting down HTTP server")
	return s.httpSrv.Shutdown(ctx)
}

const contentTypeHeader = "Content-Type"

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(contentTypeHeader, "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "healthy",
		"uptime": time.Since(s.metrics.StartTime).String(),
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(contentTypeHeader, "text/plain; version=0.0.4")
	s.metrics.WritePrometheus(w)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(contentTypeHeader, "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"uptime_seconds":  time.Since(s.metrics.StartTime).Seconds(),
		"emails_received": s.metrics.EmailsReceived.Load(),
		"emails_relayed":  s.metrics.EmailsRelayed.Load(),
		"emails_failed":   s.metrics.EmailsFailed.Load(),
		"emails_queued":   s.metrics.EmailsQueued.Load(),
		"queue_length":    s.metrics.QueueCurrentLength.Load(),
	})
}
