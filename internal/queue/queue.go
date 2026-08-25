package queue

import (
	"bytes"
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

// HookFunc represents a callback triggered on email status changes.
type HookFunc func(event string, item *QueuedEmail, err error)

// QueuedEmail represents an email in the delivery queue or DLQ.
type QueuedEmail struct {
	ID          string    `json:"id"`
	From        string    `json:"from"`
	To          []string  `json:"to"`
	Subject     string    `json:"subject,omitempty"`
	Data        []byte    `json:"data"`
	Retries     int       `json:"retries"`
	Status      string    `json:"status"` // queued, processing, delivered, failed_dlq
	CreatedAt   time.Time `json:"created_at"`
	NextAttempt time.Time `json:"next_attempt"`
	FailedAt    time.Time `json:"failed_at,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

// Queue handles spooling, retry logic, DLQ, and concurrent email dispatch.
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
	mu          sync.RWMutex
	memoryItems map[string]*QueuedEmail
	memoryDLQ   map[string]*QueuedEmail
	hooks       []HookFunc
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
		cfg:         cfg,
		relayer:     relayer,
		metrics:     m,
		logger:      logger,
		itemsChan:   make(chan *QueuedEmail, 1000),
		stopChan:    make(chan struct{}),
		memoryItems: make(map[string]*QueuedEmail),
		memoryDLQ:   make(map[string]*QueuedEmail),
	}
}

// AddHook registers a status change callback hook.
func (q *Queue) AddHook(h HookFunc) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.hooks = append(q.hooks, h)
}

func (q *Queue) notifyHooks(event string, item *QueuedEmail, err error) {
	q.mu.RLock()
	hooks := append([]HookFunc{}, q.hooks...)
	q.mu.RUnlock()

	for _, h := range hooks {
		go h(event, item, err)
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
	subject := extractSubject(data)
	item := &QueuedEmail{
		ID:          id,
		From:        from,
		To:          to,
		Subject:     subject,
		Data:        data,
		Retries:     0,
		Status:      "queued",
		CreatedAt:   time.Now(),
		NextAttempt: time.Now(),
	}

	q.mu.Lock()
	q.memoryItems[id] = item
	q.mu.Unlock()

	if q.cfg.SpoolDir != "" {
		if err := q.saveToDisk(item); err != nil {
			q.logger.Error("failed to write email to disk spool", "id", id, "err", err)
		}
	}

	q.metrics.IncQueued()
	q.metrics.SetQueueLength(q.activeCount.Add(1))
	q.notifyHooks("queued", item, nil)

	select {
	case q.itemsChan <- item:
		return id, nil
	default:
		if q.cfg.SpoolDir != "" {
			return id, nil
		}
		q.mu.Lock()
		delete(q.memoryItems, id)
		q.mu.Unlock()
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
	if delay := time.Until(item.NextAttempt); delay > 0 {
		time.Sleep(delay)
	}

	item.Status = "processing"

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := q.relayer.Send(ctx, item.From, item.To, item.Data)
	if err == nil {
		// Delivery succeeded
		item.Status = "delivered"
		q.mu.Lock()
		delete(q.memoryItems, item.ID)
		q.mu.Unlock()

		q.activeCount.Add(-1)
		q.metrics.SetQueueLength(q.activeCount.Load())
		if q.cfg.SpoolDir != "" {
			q.deleteFromDisk(item.ID)
		}
		q.logger.Info("email delivered successfully from queue", "id", item.ID, "recipients", item.To)
		q.notifyHooks("delivered", item, nil)
		return
	}

	// Delivery failed
	item.Retries++
	item.LastError = err.Error()
	q.logger.Warn("delivery attempt failed", "id", item.ID, "attempt", item.Retries, "max", q.cfg.MaxRetries, "err", err)
	q.notifyHooks("failed", item, err)

	if item.Retries >= q.cfg.MaxRetries {
		q.logger.Error("max delivery retries exceeded, message moved to DLQ", "id", item.ID, "from", item.From, "to", item.To)
		item.Status = "failed_dlq"
		item.FailedAt = time.Now()

		q.mu.Lock()
		delete(q.memoryItems, item.ID)
		q.memoryDLQ[item.ID] = item
		q.mu.Unlock()

		q.activeCount.Add(-1)
		q.metrics.SetQueueLength(q.activeCount.Load())
		if q.cfg.SpoolDir != "" {
			q.moveToFailed(item)
		}
		q.notifyHooks("dlq", item, err)
		return
	}

	// Exponential backoff
	backoff := q.cfg.RetryInterval.Duration * (1 << (item.Retries - 1))
	item.NextAttempt = time.Now().Add(backoff)
	item.Status = "queued"

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
	failedPath := filepath.Join(q.cfg.SpoolDir, "failed", id+".json")
	_ = os.Remove(failedPath)
}

func (q *Queue) moveToFailed(item *QueuedEmail) {
	src := filepath.Join(q.cfg.SpoolDir, "active", item.ID+".json")
	dst := filepath.Join(q.cfg.SpoolDir, "failed", item.ID+".json")
	_ = os.Rename(src, dst)
	// Update file content with new failed metadata
	if data, err := json.Marshal(item); err == nil {
		_ = os.WriteFile(dst, data, 0600)
	}
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
			q.mu.Lock()
			if _, exists := q.memoryItems[item.ID]; !exists {
				q.memoryItems[item.ID] = &item
				q.activeCount.Add(1)
				q.metrics.SetQueueLength(q.activeCount.Load())
				select {
				case q.itemsChan <- &item:
				default:
				}
			}
			q.mu.Unlock()
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

// GetActive returns summary list of currently active queued items.
func (q *Queue) GetActive() []*QueuedEmail {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var result []*QueuedEmail
	for _, item := range q.memoryItems {
		// Copy without large body
		clone := *item
		clone.Data = nil
		result = append(result, &clone)
	}
	return result
}

// GetDLQ returns list of failed messages in Dead Letter Queue.
func (q *Queue) GetDLQ() []*QueuedEmail {
	var result []*QueuedEmail

	if q.cfg.SpoolDir != "" {
		failedDir := filepath.Join(q.cfg.SpoolDir, "failed")
		entries, err := os.ReadDir(failedDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(failedDir, entry.Name()))
				if err != nil {
					continue
				}
				var item QueuedEmail
				if err := json.Unmarshal(data, &item); err == nil {
					item.Data = nil
					result = append(result, &item)
				}
			}
			return result
		}
	}

	q.mu.RLock()
	defer q.mu.RUnlock()
	for _, item := range q.memoryDLQ {
		clone := *item
		clone.Data = nil
		result = append(result, &clone)
	}
	return result
}

// RetryFailed retries an item from the DLQ by moving it back to active queue.
func (q *Queue) RetryFailed(id string) (bool, error) {
	var targetItem *QueuedEmail

	if q.cfg.SpoolDir != "" {
		failedPath := filepath.Join(q.cfg.SpoolDir, "failed", id+".json")
		data, err := os.ReadFile(failedPath)
		if err == nil {
			var item QueuedEmail
			if err := json.Unmarshal(data, &item); err == nil {
				targetItem = &item
				_ = os.Remove(failedPath)
			}
		}
	}

	if targetItem == nil {
		q.mu.Lock()
		if item, ok := q.memoryDLQ[id]; ok {
			targetItem = item
			delete(q.memoryDLQ, id)
		}
		q.mu.Unlock()
	}

	if targetItem == nil {
		return false, fmt.Errorf("item %s not found in DLQ", id)
	}

	targetItem.Retries = 0
	targetItem.Status = "queued"
	targetItem.NextAttempt = time.Now()
	targetItem.LastError = ""

	q.mu.Lock()
	q.memoryItems[targetItem.ID] = targetItem
	q.mu.Unlock()

	if q.cfg.SpoolDir != "" {
		_ = q.saveToDisk(targetItem)
	}

	q.activeCount.Add(1)
	q.metrics.SetQueueLength(q.activeCount.Load())
	q.notifyHooks("retry", targetItem, nil)

	select {
	case q.itemsChan <- targetItem:
		return true, nil
	default:
		return true, nil
	}
}

// DeleteItem removes a message from active or failed queue.
func (q *Queue) DeleteItem(id string) bool {
	q.mu.Lock()
	delete(q.memoryItems, id)
	delete(q.memoryDLQ, id)
	q.mu.Unlock()

	if q.cfg.SpoolDir != "" {
		q.deleteFromDisk(id)
	}
	return true
}

// Flush immediately dispatches all pending queued items without backoff delay.
func (q *Queue) Flush() int {
	q.mu.RLock()
	var toFlush []*QueuedEmail
	for _, item := range q.memoryItems {
		item.NextAttempt = time.Now()
		toFlush = append(toFlush, item)
	}
	q.mu.RUnlock()

	flushed := 0
	for _, item := range toFlush {
		select {
		case q.itemsChan <- item:
			flushed++
		default:
		}
	}
	return flushed
}

func extractSubject(data []byte) string {
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			break
		}
		if bytes.HasPrefix(bytes.ToLower(line), []byte("subject:")) {
			parts := bytes.SplitN(line, []byte(":"), 2)
			if len(parts) == 2 {
				return string(bytes.TrimSpace(parts[1]))
			}
		}
	}
	return "(no subject)"
}

func generateID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
