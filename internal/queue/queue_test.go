package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khw315/mailer-go/internal/config"
	"github.com/khw315/mailer-go/internal/metrics"
)

type mockRelayer struct {
	sendFunc func(ctx context.Context, from string, to []string, msg []byte) error
}

func (m *mockRelayer) Send(ctx context.Context, from string, to []string, msg []byte) error {
	if m.sendFunc != nil {
		return m.sendFunc(ctx, from, to, msg)
	}
	return nil
}

func TestQueueSuccessfulDelivery(t *testing.T) {
	var delivered atomic.Int32
	mock := &mockRelayer{
		sendFunc: func(ctx context.Context, from string, to []string, msg []byte) error {
			delivered.Add(1)
			return nil
		},
	}

	cfg := &config.QueueConfig{
		Enabled:        true,
		MaxRetries:     3,
		RetryInterval:  config.Duration{Duration: 10 * time.Millisecond},
		MaxConcurrency: 2,
	}

	q := New(cfg, mock, metrics.New(), nil)
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer q.Stop()

	id, err := q.Enqueue("sender@example.com", []string{"recipient@example.com"}, []byte("Subject: Test\r\n\r\nHello World"))
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty message ID")
	}

	// Wait briefly for delivery
	time.Sleep(100 * time.Millisecond)

	if delivered.Load() != 1 {
		t.Errorf("expected 1 delivery, got %d", delivered.Load())
	}
}

func TestQueueRetryAndSpool(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mailer-spool-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	var attempts atomic.Int32
	mock := &mockRelayer{
		sendFunc: func(ctx context.Context, from string, to []string, msg []byte) error {
			n := attempts.Add(1)
			if n < 3 {
				return errors.New("temporary upstream network failure")
			}
			return nil
		},
	}

	cfg := &config.QueueConfig{
		Enabled:        true,
		SpoolDir:       tempDir,
		MaxRetries:     4,
		RetryInterval:  config.Duration{Duration: 20 * time.Millisecond},
		MaxConcurrency: 1,
	}

	q := New(cfg, mock, metrics.New(), nil)
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer q.Stop()

	_, err = q.Enqueue("retry@example.com", []string{"target@example.com"}, []byte("Subject: Retry Test\r\n\r\nRetrying"))
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Wait for retries
	time.Sleep(400 * time.Millisecond)

	if attempts.Load() != 3 {
		t.Errorf("expected 3 delivery attempts before success, got %d", attempts.Load())
	}

	// Verify disk active file is removed after success
	entries, _ := os.ReadDir(filepath.Join(tempDir, "active"))
	if len(entries) != 0 {
		t.Errorf("expected active spool dir to be empty, found %d files", len(entries))
	}
}

func TestQueueDLQAndRetry(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mailer-dlq-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	var shouldFail atomic.Bool
	shouldFail.Store(true)

	mock := &mockRelayer{
		sendFunc: func(ctx context.Context, from string, to []string, msg []byte) error {
			if shouldFail.Load() {
				return errors.New("fatal delivery failure")
			}
			return nil
		},
	}

	cfg := &config.QueueConfig{
		Enabled:        true,
		SpoolDir:       tempDir,
		MaxRetries:     2,
		RetryInterval:  config.Duration{Duration: 10 * time.Millisecond},
		MaxConcurrency: 1,
	}

	q := New(cfg, mock, metrics.New(), nil)
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer q.Stop()

	id, err := q.Enqueue("dlq@example.com", []string{"target@example.com"}, []byte("Subject: DLQ Test\r\n\r\nFailing"))
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Wait for retries to exhaust and move to DLQ
	time.Sleep(150 * time.Millisecond)

	dlqItems := q.GetDLQ()
	if len(dlqItems) == 0 {
		t.Fatalf("expected item to be in DLQ, but DLQ is empty")
	}
	if dlqItems[0].ID != id {
		t.Errorf("expected DLQ item ID %s, got %s", id, dlqItems[0].ID)
	}

	// Now fix the relayer and trigger RetryFailed
	shouldFail.Store(false)
	ok, err := q.RetryFailed(id)
	if err != nil || !ok {
		t.Fatalf("RetryFailed failed: %v", err)
	}

	// Wait for delivery after retry
	time.Sleep(100 * time.Millisecond)

	dlqAfter := q.GetDLQ()
	if len(dlqAfter) != 0 {
		t.Errorf("expected DLQ to be empty after successful retry, got %d items", len(dlqAfter))
	}
}

