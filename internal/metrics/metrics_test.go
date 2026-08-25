package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func TestMetricsCounters(t *testing.T) {
	m := New()
	if m == nil {
		t.Fatal("expected non-nil metrics instance")
	}

	m.IncReceived()
	m.IncRelayed()
	m.IncFailed()
	m.IncQueued()
	m.SetQueueLength(42)

	if m.EmailsReceived.Load() != 1 {
		t.Errorf("expected EmailsReceived 1, got %d", m.EmailsReceived.Load())
	}
	if m.EmailsRelayed.Load() != 1 {
		t.Errorf("expected EmailsRelayed 1, got %d", m.EmailsRelayed.Load())
	}
	if m.EmailsFailed.Load() != 1 {
		t.Errorf("expected EmailsFailed 1, got %d", m.EmailsFailed.Load())
	}
	if m.EmailsQueued.Load() != 1 {
		t.Errorf("expected EmailsQueued 1, got %d", m.EmailsQueued.Load())
	}
	if m.QueueCurrentLength.Load() != 42 {
		t.Errorf("expected QueueCurrentLength 42, got %d", m.QueueCurrentLength.Load())
	}

	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	out := buf.String()

	expectedMetrics := []string{
		"mailer_emails_received_total 1",
		"mailer_emails_relayed_total 1",
		"mailer_emails_failed_total 1",
		"mailer_emails_queued_total 1",
		"mailer_queue_length 42",
		"mailer_uptime_seconds",
	}

	for _, em := range expectedMetrics {
		if !strings.Contains(out, em) {
			t.Errorf("expected prometheus output to contain %q, got:\n%s", em, out)
		}
	}
}
