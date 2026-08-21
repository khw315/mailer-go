# Changelog

All notable changes to **mailer-go** will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [1.0.0] - 2026-08-21

### Features

- **High-Performance SMTP Inbound Server**: RFC-compliant SMTP server built on Go, handling inbound mail traffic across configurable ports (`:2525`, `:25`, `:587`).
- **Smart-Host Outbound Relay**: Seamless forwarding to external providers including Google Workspace / Gmail, SendGrid, Amazon SES, Mailgun, and Microsoft Office 365.
- **Direct MX Delivery Mode**: Automatic DNS MX resolution and direct delivery to recipient domains when `RELAY_HOST` is left empty.
- **Dual SASL Authentication**: Native SASL `PLAIN` and `LOGIN` authentication mechanisms for both incoming connections and upstream relay servers.
- **Header Injection Filter**: Automatic injection of tracking and routing headers such as `X-Relayed-By`.

### Security

- **Network CIDR Whitelisting**: Granular IP subnet filtering matching Postfix's `MYNETWORKS` behavior (`ALLOWED_NETWORKS`) for credential-less internal microservice relaying.
- **Anti-Spam Relay Enforcement**: Immediate `554 5.7.1 Relay Access Denied` rejection for unauthenticated clients outside permitted subnets.
- **Modern Encryption**: Full support for Opportunistic STARTTLS (Port 587/25) and Direct TLS/SMTPS (Port 465).
- **Unprivileged Container Execution**: Container runs strictly as an unprivileged user (`appuser:appgroup` UID 10001).

### Performance & Reliability

- **Asynchronous Worker Pool**: Concurrent email dispatch with configurable worker concurrency (`QUEUE_MAX_CONCURRENCY`).
- **Resilient Spooling Queue**: In-memory queue with optional persistent disk spooling (`/var/spool/mailer-go`) to prevent email loss during upstream outages.
- **Exponential Backoff Retry**: Automatic delivery retry with exponential backoff on temporary network errors or upstream server rate limits.
- **Graceful Shutdown**: Drains in-flight queue items and terminates active SMTP connections cleanly upon `SIGINT` / `SIGTERM`.

### Observability

- **Prometheus Metrics Exporter**: Built-in `/metrics` endpoint exporting counters for received, relayed, failed, and queued emails.
- **Health Check Endpoints**: Ready-to-use `/healthz` and `/readyz` endpoints for Kubernetes probes and Docker health checks.
- **JSON Statistics API**: Real-time server statistics and uptime reporting via `/api/stats`.
- **Structured Logging**: Zero-dependency structured logging (`log/slog`) supporting both `text` and `json` formats.

### Deployment

- **Ultra-Lightweight Multi-Stage Dockerfile**: Alpine-based final image with a total size of under 15MB.
- **Docker Compose Template**: Out-of-the-box `docker-compose.yml` configuration.
- **Comprehensive Environment Specification**: Complete `.env.example` reference with 12-factor app configuration parameters.
