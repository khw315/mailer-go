package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/khw315/mailer-go/internal/config"
	"github.com/khw315/mailer-go/internal/queue"
)

// WebhookEvent represents the JSON payload sent to configured webhook URL.
type WebhookEvent struct {
	Event     string    `json:"event"` // queued, delivered, failed, dlq, retry
	MessageID string    `json:"message_id"`
	From      string    `json:"from"`
	To        []string  `json:"to"`
	Subject   string    `json:"subject"`
	Attempts  int       `json:"attempts"`
	Timestamp time.Time `json:"timestamp"`
	Error     string    `json:"error,omitempty"`
}

// WebhookDispatcher delivers event notifications asynchronously to external endpoints.
type WebhookDispatcher struct {
	cfg        *config.WebhookConfig
	httpClient *http.Client
	logger     *slog.Logger
}

// NewWebhookDispatcher creates a new WebhookDispatcher instance.
func NewWebhookDispatcher(cfg *config.WebhookConfig, logger *slog.Logger) *WebhookDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	timeout := cfg.Timeout.Duration
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &WebhookDispatcher{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		logger: logger,
	}
}

// Dispatch triggers the webhook notification in the background.
func (w *WebhookDispatcher) Dispatch(event string, item *queue.QueuedEmail, deliveryErr error) {
	if w == nil || !w.cfg.Enabled || w.cfg.URL == "" {
		return
	}

	errStr := ""
	if deliveryErr != nil {
		errStr = deliveryErr.Error()
	}

	payload := WebhookEvent{
		Event:     event,
		MessageID: item.ID,
		From:      item.From,
		To:        item.To,
		Subject:   item.Subject,
		Attempts:  item.Retries,
		Timestamp: time.Now().UTC(),
		Error:     errStr,
	}

	go w.sendPayload(payload)
}

func (w *WebhookDispatcher) sendPayload(payload WebhookEvent) {
	body, err := json.Marshal(payload)
	if err != nil {
		w.logger.Error("failed to marshal webhook payload", "err", err)
		return
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, w.cfg.URL, bytes.NewReader(body))
	if err != nil {
		w.logger.Error("failed to create webhook request", "err", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "mailer-go-webhook/1.0")

	// Add HMAC signature if secret is configured
	if w.cfg.Secret != "" {
		mac := hmac.New(sha256.New, []byte(w.cfg.Secret))
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Mailer-Signature", "sha256="+sig)
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		w.logger.Warn("webhook delivery failed", "url", w.cfg.URL, "event", payload.Event, "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		w.logger.Warn("webhook server returned non-success code", "url", w.cfg.URL, "status", resp.StatusCode)
		return
	}

	w.logger.Debug("webhook dispatched successfully", "url", w.cfg.URL, "event", payload.Event, "id", payload.MessageID)
}
