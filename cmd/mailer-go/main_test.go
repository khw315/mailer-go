package main

import (
	"testing"

	"github.com/khw315/mailer-go/internal/config"
)

func TestSetupLogger(t *testing.T) {
	configs := []config.LoggingConfig{
		{Level: "debug", Format: "json"},
		{Level: "warn", Format: "text"},
		{Level: "error", Format: "json"},
		{Level: "info", Format: "text"},
	}

	for _, cfg := range configs {
		logger := setupLogger(cfg)
		if logger == nil {
			t.Errorf("expected non-nil logger for config: %+v", cfg)
		}
	}
}
