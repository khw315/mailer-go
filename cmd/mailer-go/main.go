package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/khw315/mailer-go/internal/api"
	"github.com/khw315/mailer-go/internal/config"
	"github.com/khw315/mailer-go/internal/metrics"
	"github.com/khw315/mailer-go/internal/queue"
	"github.com/khw315/mailer-go/internal/relay"
	"github.com/khw315/mailer-go/internal/server"
)

var (
	version   = "1.0.0"
	buildDate = "unknown"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Configuration error: %v\n", err)
		os.Exit(1)
	}

	logger := setupLogger(cfg.Logging)
	slog.SetDefault(logger)

	logger.Info("Starting mailer-go SMTP Relay",
		"version", version,
		"buildDate", buildDate,
		"smtp_addr", cfg.Server.ListenAddr,
		"hostname", cfg.Server.Hostname,
		"relay_host", cfg.Relay.Host,
		"relay_port", cfg.Relay.Port,
	)

	appMetrics := metrics.Default
	relayClient := relay.NewClient(&cfg.Relay, appMetrics, logger)

	deliveryQueue := initQueue(cfg, relayClient, appMetrics, logger)

	smtpServer, err := server.New(cfg, deliveryQueue, relayClient, appMetrics, logger)
	if err != nil {
		logger.Error("failed to initialize SMTP server", "err", err)
		os.Exit(1)
	}

	httpAPIServer := startHTTPServer(cfg, appMetrics, logger)

	go func() {
		if err := smtpServer.Start(); err != nil {
			logger.Error("SMTP server stopped with error", "err", err)
		}
	}()

	waitForShutdown(smtpServer, httpAPIServer, deliveryQueue, logger)
}

func initQueue(cfg *config.Config, relayClient relay.Relayer, m *metrics.Metrics, logger *slog.Logger) *queue.Queue {
	if !cfg.Queue.Enabled {
		return nil
	}
	q := queue.New(&cfg.Queue, relayClient, m, logger)
	if err := q.Start(context.Background()); err != nil {
		logger.Error("failed to start delivery queue", "err", err)
		os.Exit(1)
	}
	return q
}

func startHTTPServer(cfg *config.Config, m *metrics.Metrics, logger *slog.Logger) *api.Server {
	if !cfg.HTTP.Enabled {
		return nil
	}
	srv := api.NewServer(&cfg.HTTP, m, logger)
	go func() {
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP API server encountered an error", "err", err)
		}
	}()
	return srv
}

func waitForShutdown(smtpServer *server.Server, httpAPIServer *api.Server, deliveryQueue *queue.Queue, logger *slog.Logger) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	sig := <-sigChan
	logger.Info("received termination signal, shutting down gracefully...", "signal", sig.String())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := smtpServer.Close(); err != nil {
		logger.Warn("error closing SMTP server", "err", err)
	}

	if httpAPIServer != nil {
		if err := httpAPIServer.Stop(shutdownCtx); err != nil {
			logger.Warn("error stopping HTTP server", "err", err)
		}
	}

	if deliveryQueue != nil {
		deliveryQueue.Stop()
	}

	logger.Info("mailer-go stopped cleanly")
}

func setupLogger(cfg config.LoggingConfig) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}
