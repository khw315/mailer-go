package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/khw315/mailer-go/internal/config"
	"github.com/khw315/mailer-go/internal/metrics"
	"github.com/khw315/mailer-go/internal/queue"
	"github.com/khw315/mailer-go/internal/relay"
	"github.com/khw315/mailer-go/internal/web"
)

const (
	contentTypeHeader   = "Content-Type"
	contentTypeJSON     = "application/json"
	contentTypeHTML     = "text/html; charset=utf-8"
	contentTypeMetrics  = "text/plain; version=0.0.4"
	msgMethodNotAllowed = "Method Not Allowed"
	msgQueueDisabled    = "Queue is disabled"
)

// Server handles health checks, status, Prometheus metrics, REST send API, and dashboard.
type Server struct {
	cfg     *config.HTTPConfig
	queue   *queue.Queue
	relay   relay.Relayer
	metrics *metrics.Metrics
	logger  *slog.Logger
	httpSrv *http.Server
}

// NewServer creates a new HTTP API and observability server.
func NewServer(cfg *config.HTTPConfig, q *queue.Queue, r relay.Relayer, m *metrics.Metrics, logger *slog.Logger) *Server {
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

	mux := http.NewServeMux()

	// Observability endpoints
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/api/stats", s.handleStats)

	// Web Dashboard
	mux.HandleFunc("/dashboard", s.handleDashboard)
	mux.HandleFunc("/", s.handleRoot)

	// REST Email Submission API
	mux.HandleFunc("/v1/send", s.authMiddleware(s.handleSend))
	mux.HandleFunc("/api/send", s.authMiddleware(s.handleSend))

	// Queue Management REST API
	mux.HandleFunc("/api/queue", s.authMiddleware(s.handleQueue))
	mux.HandleFunc("/api/queue/retry", s.authMiddleware(s.handleQueueRetry))
	mux.HandleFunc("/api/queue/flush", s.authMiddleware(s.handleQueueFlush))

	s.httpSrv = &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	return s
}

// Start begins listening on the HTTP port.
func (s *Server) Start() error {
	s.logger.Info("starting HTTP health, metrics & REST API server", "addr", s.cfg.ListenAddr)
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

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIKey != "" {
			apiKey := r.Header.Get("X-API-Key")
			if apiKey == "" {
				authHeader := r.Header.Get("Authorization")
				if strings.HasPrefix(authHeader, "Bearer ") {
					apiKey = strings.TrimPrefix(authHeader, "Bearer ")
				}
			}

			if subtle.ConstantTimeCompare([]byte(apiKey), []byte(s.cfg.APIKey)) != 1 {
				w.Header().Set(contentTypeHeader, contentTypeJSON)
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "Unauthorized: invalid or missing API key",
				})
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(contentTypeHeader, contentTypeJSON)
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
	w.Header().Set(contentTypeHeader, contentTypeMetrics)
	s.metrics.WritePrometheus(w)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(contentTypeHeader, contentTypeJSON)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"uptime_seconds":  time.Since(s.metrics.StartTime).Seconds(),
		"emails_received": s.metrics.EmailsReceived.Load(),
		"emails_relayed":  s.metrics.EmailsRelayed.Load(),
		"emails_failed":   s.metrics.EmailsFailed.Load(),
		"emails_queued":   s.metrics.EmailsQueued.Load(),
		"queue_length":    s.metrics.QueueCurrentLength.Load(),
	})
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		// If requesting /api/queue/{id} with DELETE or GET
		if strings.HasPrefix(r.URL.Path, "/api/queue/") {
			s.authMiddleware(s.handleQueueItem)(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}

	if s.cfg.DashboardEnabled {
		w.Header().Set(contentTypeHeader, contentTypeHTML)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(web.DashboardHTML)
		return
	}

	s.handleHealthz(w, r)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DashboardEnabled {
		http.NotFound(w, r)
		return
	}
	w.Header().Set(contentTypeHeader, contentTypeHTML)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(web.DashboardHTML)
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
		return
	}

	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set(contentTypeHeader, contentTypeJSON)
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("invalid JSON payload: %v", err),
		})
		return
	}

	mimeBytes, err := BuildMIME(&req)
	if err != nil {
		w.Header().Set(contentTypeHeader, contentTypeJSON)
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": err.Error(),
		})
		return
	}

	s.metrics.IncReceived()

	// All recipients combined for SMTP envelope
	allRecipients := append([]string{}, req.To...)
	allRecipients = append(allRecipients, req.Cc...)
	allRecipients = append(allRecipients, req.Bcc...)

	// If queue is available, enqueue
	if s.queue != nil {
		id, err := s.queue.Enqueue(req.From, allRecipients, mimeBytes)
		if err != nil {
			s.logger.Error("HTTP send enqueue failed", "err", err)
			w.Header().Set(contentTypeHeader, contentTypeJSON)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": fmt.Sprintf("failed to queue email: %v", err),
			})
			return
		}

		w.Header().Set(contentTypeHeader, contentTypeJSON)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":     "queued",
			"id":         id,
			"recipients": allRecipients,
		})
		return
	}

	// Synchronous delivery
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.relay.Send(ctx, req.From, allRecipients, mimeBytes); err != nil {
		w.Header().Set(contentTypeHeader, contentTypeJSON)
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("delivery failed: %v", err),
		})
		return
	}

	w.Header().Set(contentTypeHeader, contentTypeJSON)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":     "delivered",
		"recipients": allRecipients,
	})
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	if s.queue == nil {
		w.Header().Set(contentTypeHeader, contentTypeJSON)
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msgQueueDisabled})
		return
	}

	active := s.queue.GetActive()
	dlq := s.queue.GetDLQ()

	if active == nil {
		active = []*queue.QueuedEmail{}
	}
	if dlq == nil {
		dlq = []*queue.QueuedEmail{}
	}

	w.Header().Set(contentTypeHeader, contentTypeJSON)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"active":       active,
		"active_count": len(active),
		"dlq":          dlq,
		"dlq_count":    len(dlq),
	})
}

func (s *Server) handleQueueRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
		return
	}
	if s.queue == nil {
		http.Error(w, msgQueueDisabled, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.ID == "" {
		req.ID = r.URL.Query().Get("id")
	}

	if req.ID == "" {
		w.Header().Set(contentTypeHeader, contentTypeJSON)
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "missing message id"})
		return
	}

	ok, err := s.queue.RetryFailed(req.ID)
	if err != nil || !ok {
		w.Header().Set(contentTypeHeader, contentTypeJSON)
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("retry failed: %v", err)})
		return
	}

	w.Header().Set(contentTypeHeader, contentTypeJSON)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "retrying",
		"message": fmt.Sprintf("message %s re-queued", req.ID),
	})
}

func (s *Server) handleQueueFlush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
		return
	}
	if s.queue == nil {
		http.Error(w, msgQueueDisabled, http.StatusServiceUnavailable)
		return
	}

	count := s.queue.Flush()
	w.Header().Set(contentTypeHeader, contentTypeJSON)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "flushed",
		"flushed": count,
	})
}

func (s *Server) handleQueueItem(w http.ResponseWriter, r *http.Request) {
	if s.queue == nil {
		http.Error(w, msgQueueDisabled, http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/queue/")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	if r.Method == http.MethodDelete {
		deleted := s.queue.DeleteItem(id)
		w.Header().Set(contentTypeHeader, contentTypeJSON)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"deleted": deleted,
			"id":      id,
		})
		return
	}

	http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
}
