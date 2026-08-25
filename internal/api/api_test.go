package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/khw315/mailer-go/internal/config"
	"github.com/khw315/mailer-go/internal/metrics"
	"github.com/khw315/mailer-go/internal/queue"
)

type mockRelayer struct {
	sentCount int
	shouldErr bool
}

func (m *mockRelayer) Send(ctx context.Context, from string, to []string, msg []byte) error {
	if m.shouldErr {
		return errors.New("upstream relay failed")
	}
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

	// Test / (root dashboard enabled)
	req = httptest.NewRequest("GET", "/", nil)
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("root expected 200 OK, got %d", w.Code)
	}

	// Test / with dashboard disabled -> returns healthz
	srvNoDash := NewServer(&config.HTTPConfig{Enabled: true, DashboardEnabled: false}, q, mockRelay, m, nil)
	req = httptest.NewRequest("GET", "/", nil)
	w = httptest.NewRecorder()
	srvNoDash.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("root expected 200 OK healthz, got %d", w.Code)
	}

	// Test /dashboard with dashboard disabled -> 404
	req = httptest.NewRequest("GET", "/dashboard", nil)
	w = httptest.NewRecorder()
	srvNoDash.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("dashboard disabled expected 404, got %d", w.Code)
	}
}

func TestHTTPSendEndpoint(t *testing.T) {
	mockRelay := &mockRelayer{}
	m := metrics.New()
	q := queue.New(&config.QueueConfig{Enabled: true}, mockRelay, m, nil)
	_ = q.Start(context.Background())
	defer q.Stop()

	srv := NewServer(&config.HTTPConfig{Enabled: true, ListenAddr: ":8080"}, q, mockRelay, m, nil)

	// Valid POST
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

	// Invalid HTTP Method
	req = httptest.NewRequest("GET", "/v1/send", nil)
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed, got %d", w.Code)
	}

	// Invalid JSON body
	req = httptest.NewRequest("POST", "/v1/send", strings.NewReader("invalid-json{"))
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request on invalid json, got %d", w.Code)
	}

	// Validation failure (missing From)
	invalidPayload, _ := json.Marshal(SendRequest{To: []string{"test@example.com"}})
	req = httptest.NewRequest("POST", "/v1/send", bytes.NewReader(invalidPayload))
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request on missing From, got %d", w.Code)
	}
}

func TestHTTPSendSynchronous(t *testing.T) {
	mockRelay := &mockRelayer{}
	m := metrics.New()

	// Synchronous delivery (queue == nil)
	srv := NewServer(&config.HTTPConfig{Enabled: true, ListenAddr: ":8080"}, nil, mockRelay, m, nil)

	payload := SendRequest{
		From:    "direct@example.com",
		To:      []string{"receiver@example.com"},
		Subject: "Sync Delivery",
		Text:    "Sync text",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/api/send", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for sync delivery, got %d", w.Code)
	}

	// Synchronous delivery failure
	mockRelay.shouldErr = true
	req = httptest.NewRequest("POST", "/api/send", bytes.NewReader(body))
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 Bad Gateway for relay failure, got %d", w.Code)
	}
}

func TestQueueAPIRoutes(t *testing.T) {
	mockRelay := &mockRelayer{}
	m := metrics.New()
	q := queue.New(&config.QueueConfig{Enabled: true}, mockRelay, m, nil)

	srv := NewServer(&config.HTTPConfig{Enabled: true}, q, mockRelay, m, nil)

	// GET /api/queue
	req := httptest.NewRequest("GET", "/api/queue", nil)
	w := httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 OK on GET /api/queue, got %d", w.Code)
	}

	// POST /api/queue/flush
	req = httptest.NewRequest("POST", "/api/queue/flush", nil)
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 OK on /api/queue/flush, got %d", w.Code)
	}

	// POST /api/queue/retry (missing id)
	req = httptest.NewRequest("POST", "/api/queue/retry", strings.NewReader(`{}`))
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for retry with missing id, got %d", w.Code)
	}

	// POST /api/queue/retry (not found id)
	req = httptest.NewRequest("POST", "/api/queue/retry?id=non-existent", nil)
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 Not Found for non-existent id, got %d", w.Code)
	}

	// DELETE /api/queue/{id} via root routing
	req = httptest.NewRequest("DELETE", "/api/queue/item123", nil)
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 OK on DELETE /api/queue/{id}, got %d", w.Code)
	}

	// Invalid method on /api/queue/{id}
	req = httptest.NewRequest("POST", "/api/queue/item123", nil)
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed, got %d", w.Code)
	}

	// When queue is disabled
	srvNoQueue := NewServer(&config.HTTPConfig{Enabled: true}, nil, mockRelay, m, nil)

	req = httptest.NewRequest("GET", "/api/queue", nil)
	w = httptest.NewRecorder()
	srvNoQueue.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 Service Unavailable when queue disabled, got %d", w.Code)
	}

	req = httptest.NewRequest("POST", "/api/queue/flush", nil)
	w = httptest.NewRecorder()
	srvNoQueue.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 on flush when queue disabled, got %d", w.Code)
	}

	req = httptest.NewRequest("POST", "/api/queue/retry", nil)
	w = httptest.NewRecorder()
	srvNoQueue.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 on retry when queue disabled, got %d", w.Code)
	}
}

func TestMIMEBuilder(t *testing.T) {
	req := &SendRequest{
		From:     "alice@example.com",
		To:       []string{"bob@example.com"},
		Cc:       []string{"carol@example.com"},
		Bcc:      []string{"dave@example.com"},
		ReplyTo:  "support@example.com",
		Subject:  "Meeting Notes",
		Text:     "Plain text notes",
		HTML:     "<p>HTML notes</p>",
		Headers:  map[string]string{"X-Custom-App": "Mailer"},
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
	if !strings.Contains(str, "Cc: carol@example.com") {
		t.Errorf("missing Cc header in MIME output")
	}
	if !strings.Contains(str, "Reply-To: support@example.com") {
		t.Errorf("missing Reply-To header in MIME output")
	}
	if !strings.Contains(str, "X-Custom-App: Mailer") {
		t.Errorf("missing custom header in MIME output")
	}
	if !strings.Contains(str, "multipart/mixed") {
		t.Errorf("expected multipart/mixed for attachment")
	}
	if !strings.Contains(str, "filename=\"test.txt\"") {
		t.Errorf("expected attachment filename in MIME output")
	}

	// Missing From validation
	_, err = BuildMIME(&SendRequest{To: []string{"a@b.c"}})
	if err == nil {
		t.Errorf("expected error on missing from")
	}

	// Missing To validation
	_, err = BuildMIME(&SendRequest{From: "a@b.c"})
	if err == nil {
		t.Errorf("expected error on missing to")
	}

	// Text only (no HTML, no attachments)
	textOnly, err := BuildMIME(&SendRequest{
		From: "a@b.c",
		To:   []string{"d@e.f"},
		Text: "simple text",
	})
	if err != nil || !strings.Contains(string(textOnly), "Content-Type: text/plain") {
		t.Errorf("expected text/plain for text only email")
	}

	// HTML only (no text, no attachments)
	htmlOnly, err := BuildMIME(&SendRequest{
		From: "a@b.c",
		To:   []string{"d@e.f"},
		HTML: "<b>hello</b>",
	})
	if err != nil || !strings.Contains(string(htmlOnly), "Content-Type: text/html") {
		t.Errorf("expected text/html for HTML only email")
	}

	// Invalid Base64 Attachment
	_, err = BuildMIME(&SendRequest{
		From: "a@b.c",
		To:   []string{"d@e.f"},
		Attachments: []Attachment{
			{Filename: "bad.bin", Base64Data: "invalid-base64-!@#$"},
		},
	})
	if err == nil {
		t.Errorf("expected error on invalid base64 attachment")
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

	// Authorized with Authorization: Bearer <key> header
	req = httptest.NewRequest("GET", "/api/queue", nil)
	req.Header.Set("Authorization", "Bearer secret-key-123") // NOSONAR
	w = httptest.NewRecorder()
	srv.httpSrv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 OK with Bearer token, got %d", w.Code)
	}
}

func TestWebhookDispatcher(t *testing.T) {
	var receivedPayload WebhookEvent
	var receivedSignature string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSignature = r.Header.Get("X-Mailer-Signature")
		_ = json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	secret := "test-secret"
	cfg := &config.WebhookConfig{
		Enabled: true,
		URL:     mockServer.URL,
		Secret:  secret,
		Timeout: config.Duration{Duration: 2 * time.Second},
	}

	dispatcher := NewWebhookDispatcher(cfg, nil)
	if dispatcher == nil {
		t.Fatal("expected non-nil webhook dispatcher")
	}

	item := &queue.QueuedEmail{
		ID:      "msg-123",
		From:    "from@example.com",
		To:      []string{"to@example.com"},
		Subject: "Test Webhook",
		Retries: 1,
	}

	dispatcher.Dispatch("delivered", item, nil)

	// Wait briefly for asynchronous dispatch goroutine
	time.Sleep(100 * time.Millisecond)

	if receivedPayload.Event != "delivered" || receivedPayload.MessageID != "msg-123" {
		t.Errorf("unexpected payload received: %+v", receivedPayload)
	}

	// Verify HMAC SHA-256 signature
	rawPayload, _ := json.Marshal(receivedPayload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(rawPayload)
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if receivedSignature != "sha256="+expectedSig {
		t.Errorf("signature mismatch: got %s, expected %s", receivedSignature, "sha256="+expectedSig)
	}
}

func TestAPIServerLifecycle(t *testing.T) {
	srv := NewServer(&config.HTTPConfig{
		Enabled:    true,
		ListenAddr: "127.0.0.1:0",
	}, nil, nil, nil, nil)

	go func() {
		_ = srv.Start()
	}()

	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Stop(ctx); err != nil {
		t.Errorf("srv.Stop failed: %v", err)
	}
}

