package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/khw315/mailer-go/internal/config"
	"github.com/khw315/mailer-go/internal/metrics"
	"github.com/khw315/mailer-go/internal/queue"
)

type mockRelayer struct {
	sentCount int
}

func (m *mockRelayer) Send(ctx context.Context, from string, to []string, msg []byte) error {
	m.sentCount++
	return nil
}

func TestAPIEndpoints(t *testing.T) {
	m := metrics.New()
	m.IncReceived()
	m.IncRelayed()
	m.IncQueued()
	m.SetQueueLength(3)

	mockRelay := &mockRelayer{}
	q := queue.New(&config.QueueConfig{Enabled: true}, mockRelay, m, nil)

	srv := NewServer(&config.HTTPConfig{Enabled: true, ListenAddr: ":8080", DashboardEnabled: true}, q, mockRelay, m, nil)

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

	// Test /dashboard
	req = httptest.NewRequest("GET", "/dashboard", nil)
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "mailer-go") {
		t.Errorf("expected 200 OK dashboard, got %d", w.Code)
	}
}

func TestHTTPSendEndpoint(t *testing.T) {
	mockRelay := &mockRelayer{}
	m := metrics.New()
	q := queue.New(&config.QueueConfig{Enabled: true}, mockRelay, m, nil)
	_ = q.Start(context.Background())
	defer q.Stop()

	srv := NewServer(&config.HTTPConfig{Enabled: true, ListenAddr: ":8080"}, q, mockRelay, m, nil)

	payload := SendRequest{
		From:    "sender@example.com",
		To:      []string{"receiver@example.com"},
		Subject: "Test Email from API",
		Text:    "Hello World Text",
		HTML:    "<h1>Hello World</h1>",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/v1/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d (body: %s)", w.Code, w.Body.String())
	}

	var res map[string]any
	_ = json.NewDecoder(w.Body).Decode(&res)
	if res["status"] != "queued" || res["id"] == "" {
		t.Errorf("unexpected send response: %+v", res)
	}
}

func TestMIMEBuilder(t *testing.T) {
	req := &SendRequest{
		From:    "alice@example.com",
		To:      []string{"bob@example.com"},
		Subject: "Meeting Notes",
		Text:    "Plain text notes",
		HTML:    "<p>HTML notes</p>",
		Attachments: []Attachment{
			{
				Filename:    "test.txt",
				ContentType: "text/plain",
				Base64Data:  "SGVsbG8gQXR0YWNobWVudA==", // "Hello Attachment"
			},
		},
	}

	mimeBytes, err := BuildMIME(req)
	if err != nil {
		t.Fatalf("BuildMIME failed: %v", err)
	}

	str := string(mimeBytes)
	if !strings.Contains(str, "From: alice@example.com") {
		t.Errorf("missing From header in MIME output")
	}
	if !strings.Contains(str, "Subject: Meeting Notes") {
		t.Errorf("missing Subject header in MIME output")
	}
	if !strings.Contains(str, "multipart/mixed") {
		t.Errorf("expected multipart/mixed for attachment")
	}
	if !strings.Contains(str, "filename=\"test.txt\"") {
		t.Errorf("expected attachment filename in MIME output")
	}
}

func TestAPIKeyAuth(t *testing.T) {
	mockRelay := &mockRelayer{}
	m := metrics.New()
	q := queue.New(&config.QueueConfig{Enabled: true}, mockRelay, m, nil)

	srv := NewServer(&config.HTTPConfig{Enabled: true, APIKey: "secret-key-123"}, q, mockRelay, m, nil)

	// Unauthorized without key
	req := httptest.NewRequest("GET", "/api/queue", nil)
	w := httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", w.Code)
	}

	// Authorized with X-API-Key header
	req = httptest.NewRequest("GET", "/api/queue", nil)
	req.Header.Set("X-API-Key", "secret-key-123")
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 OK with valid API key, got %d", w.Code)
	}
}
