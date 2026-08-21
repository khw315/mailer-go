package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/khw315/mailer-go/internal/config"
	"github.com/khw315/mailer-go/internal/metrics"
)

func TestAPIEndpoints(t *testing.T) {
	m := metrics.New()
	m.IncReceived()
	m.IncRelayed()
	m.IncQueued()
	m.SetQueueLength(3)

	srv := NewServer(&config.HTTPConfig{Enabled: true, ListenAddr: ":8080"}, m, nil)

	// Test /healthz
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("healthz expected 200 OK, got %d", w.Code)
	}
	var healthResp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&healthResp); err != nil {
		t.Fatalf("failed to decode healthz response: %v", err)
	}
	if healthResp["status"] != "healthy" {
		t.Errorf("expected status healthy, got %v", healthResp["status"])
	}

	// Test /readyz
	req = httptest.NewRequest("GET", "/readyz", nil)
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "ok" {
		t.Errorf("readyz expected 'ok', got %s", w.Body.String())
	}

	// Test /metrics
	req = httptest.NewRequest("GET", "/metrics", nil)
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "mailer_emails_received_total 1") {
		t.Errorf("metrics output missing mailer_emails_received_total: %s", body)
	}
	if !strings.Contains(body, "mailer_emails_relayed_total 1") {
		t.Errorf("metrics output missing mailer_emails_relayed_total: %s", body)
	}
	if !strings.Contains(body, "mailer_queue_length 3") {
		t.Errorf("metrics output missing mailer_queue_length: %s", body)
	}

	// Test /api/stats
	req = httptest.NewRequest("GET", "/api/stats", nil)
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)

	var stats map[string]any
	if err := json.NewDecoder(w.Body).Decode(&stats); err != nil {
		t.Fatalf("failed to decode stats response: %v", err)
	}
	if stats["emails_received"] != float64(1) {
		t.Errorf("expected emails_received 1, got %v", stats["emails_received"])
	}
}
