package metrics

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// Metrics holds counters and runtime metrics for mailer-go.
type Metrics struct {
	StartTime          time.Time
	EmailsReceived     atomic.Uint64
	EmailsRelayed      atomic.Uint64
	EmailsFailed       atomic.Uint64
	EmailsQueued       atomic.Uint64
	QueueCurrentLength atomic.Int64
}

// Global default metrics registry
var Default = New()

// New creates a new Metrics instance.
func New() *Metrics {
	return &Metrics{
		StartTime: time.Now(),
	}
}

// IncReceived increments total received emails.
func (m *Metrics) IncReceived() {
	m.EmailsReceived.Add(1)
}

// IncRelayed increments total successfully relayed emails.
func (m *Metrics) IncRelayed() {
	m.EmailsRelayed.Add(1)
}

// IncFailed increments total failed emails.
func (m *Metrics) IncFailed() {
	m.EmailsFailed.Add(1)
}

// IncQueued increments total spooled/queued emails.
func (m *Metrics) IncQueued() {
	m.EmailsQueued.Add(1)
}

// SetQueueLength updates the current queue depth.
func (m *Metrics) SetQueueLength(length int64) {
	m.QueueCurrentLength.Store(length)
}

// WritePrometheus writes metrics in standard Prometheus exposition format.
func (m *Metrics) WritePrometheus(w io.Writer) {
	uptimeSeconds := time.Since(m.StartTime).Seconds()
	fmt.Fprintf(w, "# HELP mailer_uptime_seconds Total time the server has been running in seconds.\n")
	fmt.Fprintf(w, "# TYPE mailer_uptime_seconds gauge\n")
	fmt.Fprintf(w, "mailer_uptime_seconds %.2f\n\n", uptimeSeconds)

	fmt.Fprintf(w, "# HELP mailer_emails_received_total Total number of emails received from inbound clients.\n")
	fmt.Fprintf(w, "# TYPE mailer_emails_received_total counter\n")
	fmt.Fprintf(w, "mailer_emails_received_total %d\n\n", m.EmailsReceived.Load())

	fmt.Fprintf(w, "# HELP mailer_emails_relayed_total Total number of emails successfully relayed to upstream.\n")
	fmt.Fprintf(w, "# TYPE mailer_emails_relayed_total counter\n")
	fmt.Fprintf(w, "mailer_emails_relayed_total %d\n\n", m.EmailsRelayed.Load())

	fmt.Fprintf(w, "# HELP mailer_emails_failed_total Total number of email deliveries that failed permanently.\n")
	fmt.Fprintf(w, "# TYPE mailer_emails_failed_total counter\n")
	fmt.Fprintf(w, "mailer_emails_failed_total %d\n\n", m.EmailsFailed.Load())

	fmt.Fprintf(w, "# HELP mailer_emails_queued_total Total number of emails submitted into retry spool queue.\n")
	fmt.Fprintf(w, "# TYPE mailer_emails_queued_total counter\n")
	fmt.Fprintf(w, "mailer_emails_queued_total %d\n\n", m.EmailsQueued.Load())

	fmt.Fprintf(w, "# HELP mailer_queue_length Current number of messages waiting in spool queue.\n")
	fmt.Fprintf(w, "# TYPE mailer_queue_length gauge\n")
	fmt.Fprintf(w, "mailer_queue_length %d\n", m.QueueCurrentLength.Load())
}
