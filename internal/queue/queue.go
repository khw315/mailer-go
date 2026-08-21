package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/khw315/mailer-go/internal/config"
	"github.com/khw315/mailer-go/internal/metrics"
	"github.com/khw315/mailer-go/internal/relay"
)

// QueuedEmail represents an email in the delivery queue.
type QueuedEmail struct {
	ID          string    `json:"id"`
	From        string    `json:"from"`
	To          []string  `json:"to"`
	Data        []byte    `json:"data"`
	Retries     int       `json:"retries"`
	CreatedAt   time.Time `json:"created_at"`
	NextAttempt time.Time `json:"next_attempt"`
	LastError   string    `json:"last_error,omitempty"`
}

// Queue handles spooling, retry logic, and concurrent email dispatch.
type Queue struct {
	cfg     *config.QueueConfig
	relayer relay.Relayer
	metrics *metrics.Metrics
	logger  *slog.Logger

	itemsChan chan *QueuedEmail
	stopChan  chan struct{}
	wg        sync.WaitGroup
	running   atomic.Bool

	activeCount atomic.Int64
}

// New creates a new Queue instance.
func New(cfg *config.QueueConfig, relayer relay.Relayer, m *metrics.Metrics, logger *slog.Logger) *Queue {
	if logger == nil {
		logger = slog.Default()
	}
	if m == nil {
		m = metrics.Default
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 5
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 5
	}
	if cfg.RetryInterval.Duration <= 0 {
		cfg.RetryInterval.Duration = 1 * time.Minute
	}

	return &Queue{
		cfg:       cfg,
		relayer:   relayer,
		metrics:   m,
		logger:    logger,
		itemsChan: make(chan *QueuedEmail, 1000),
		stopChan:  make(chan struct{}),
	}
}

// Start begins the worker pool and background disk scanner.
func (q *Queue) Start(ctx context.Context) error {
	if q.running.Swap(true) {
		return nil
	}

	if q.cfg.SpoolDir != "" {
		if err := os.MkdirAll(filepath.Join(q.cfg.SpoolDir, "active"), 0750); err != nil {
			return fmt.Errorf("failed to create spool active directory: %w", err)
		}
		if err := os.MkdirAll(filepath.Join(q.cfg.SpoolDir, "failed"), 0750); err != nil {
			return fmt.Errorf("failed to create spool failed directory: %w", err)
		}
		// Load existing spooled items
		q.loadDiskSpool()
	}

	// Start worker pool
	for i := 0; i < q.cfg.MaxConcurrency; i++ {
		q.wg.Add(1)
		go q.worker(i)
	}

	// Start periodic disk scanner if spooling is enabled
	if q.cfg.SpoolDir != "" {
		q.wg.Add(1)
		go q.spoolScanner()
	}

	q.logger.Info("delivery queue started", "concurrency", q.cfg.MaxConcurrency, "spoolDir", q.cfg.SpoolDir)
	return nil
}

// Stop gracefully stops all queue workers.
func (q *Queue) Stop() {
	if !q.running.Swap(false) {
		return
	}
	close(q.stopChan)
	q.wg.Wait()
	q.logger.Info("delivery queue stopped")
}

// Enqueue adds an email to the queue for delivery.
func (q *Queue) Enqueue(from string, to []string, data []byte) (string, error) {
	id := generateID()
	item := &QueuedEmail{
		ID:          id,
		From:        from,
		To:          to,
		Data:        data,
		Retries:     0,
		CreatedAt:   time.Now(),
		NextAttempt: time.Now(),
	}

	if q.cfg.SpoolDir != "" {
		if err := q.saveToDisk(item); err != nil {
			q.logger.Error("failed to write email to disk spool", "id", id, "err", err)
		}
	}

	q.metrics.IncQueued()
	q.metrics.SetQueueLength(q.activeCount.Add(1))

	select {
	case q.itemsChan <- item:
		return id, nil
	default:
		// Channel full, will be picked up by disk scanner if persistent spooling is active
		if q.cfg.SpoolDir != "" {
			return id, nil
		}
		q.activeCount.Add(-1)
		q.metrics.SetQueueLength(q.activeCount.Load())
		return "", fmt.Errorf("in-memory queue is full")
	}
}

// worker processes items from the channel.
func (q *Queue) worker(_ int) {
	defer q.wg.Done()

	for {
		select {
		case <-q.stopChan:
			return
		case item, ok := <-q.itemsChan:
			if !ok {
				return
			}
			q.processItem(item)
		}
	}
}

func (q *Queue) processItem(item *QueuedEmail) {
	// If it's not time yet for retry, sleep or reschedule
	if delay := time.Until(item.NextAttempt); delay > 0 {
		time.Sleep(delay)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := q.relayer.Send(ctx, item.From, item.To, item.Data)
	if err == nil {
		// Delivery succeeded
		q.activeCount.Add(-1)
		q.metrics.SetQueueLength(q.activeCount.Load())
		if q.cfg.SpoolDir != "" {
			q.deleteFromDisk(item.ID)
		}
		q.logger.Info("email delivered successfully from queue", "id", item.ID, "recipients", item.To)
		return
	}

	// Delivery failed
	item.Retries++
	item.LastError = err.Error()
	q.logger.Warn("delivery attempt failed", "id", item.ID, "attempt", item.Retries, "max", q.cfg.MaxRetries, "err", err)

	if item.Retries >= q.cfg.MaxRetries {
		q.logger.Error("max delivery retries exceeded, message discarded", "id", item.ID, "from", item.From, "to", item.To)
		q.activeCount.Add(-1)
		q.metrics.SetQueueLength(q.activeCount.Load())
		if q.cfg.SpoolDir != "" {
			q.moveToFailed(item)
		}
		return
	}

	// Exponential backoff
	backoff := q.cfg.RetryInterval.Duration * (1 << (item.Retries - 1))
	item.NextAttempt = time.Now().Add(backoff)

	if q.cfg.SpoolDir != "" {
		_ = q.saveToDisk(item)
	}

	// Requeue after backoff
	go func(retryItem *QueuedEmail, waitDuration time.Duration) {
		select {
		case <-q.stopChan:
			return
		case <-time.After(waitDuration):
			select {
			case q.itemsChan <- retryItem:
			case <-q.stopChan:
			}
		}
	}(item, backoff)
}

func (q *Queue) saveToDisk(item *QueuedEmail) error {
	filePath := filepath.Join(q.cfg.SpoolDir, "active", item.ID+".json")
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, data, 0600)
}

func (q *Queue) deleteFromDisk(id string) {
	filePath := filepath.Join(q.cfg.SpoolDir, "active", id+".json")
	_ = os.Remove(filePath)
}

func (q *Queue) moveToFailed(item *QueuedEmail) {
	src := filepath.Join(q.cfg.SpoolDir, "active", item.ID+".json")
	dst := filepath.Join(q.cfg.SpoolDir, "failed", item.ID+".json")
	_ = os.Rename(src, dst)
}

func (q *Queue) loadDiskSpool() {
	activeDir := filepath.Join(q.cfg.SpoolDir, "active")
	entries, err := os.ReadDir(activeDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(activeDir, entry.Name()))
		if err != nil {
			continue
		}
		var item QueuedEmail
		if err := json.Unmarshal(data, &item); err == nil {
			q.activeCount.Add(1)
			q.metrics.SetQueueLength(q.activeCount.Load())
			select {
			case q.itemsChan <- &item:
			default:
			}
		}
	}
}

func (q *Queue) spoolScanner() {
	defer q.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-q.stopChan:
			return
		case <-ticker.C:
			q.loadDiskSpool()
		}
	}
}

func generateID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
