# mailer-go

[![Go Version](https://img.shields.io/badge/Go-1.24%2B-00ADD8?logo=go&logoColor=white)](https://golang.org)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?logo=docker&logoColor=white)](Dockerfile)
[![Build Status](https://img.shields.io/badge/Build-Passing-brightgreen)](cmd/mailer-go/main.go)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A high-performance, lightweight, and modern **SMTP Smart-Host Relay & Direct MX Mailer** written in Go.

Designed as a drop-in, zero-overhead replacement for containerized **Postfix** relays (such as `bokysan/docker-postfix` or `juanluisbaptiste/docker-postfix`) without complex Linux daemon setups, chroot jails, or heavy configuration files.

---

## Highlights

- **Postfix-Compatible Security (`mynetworks`)**: Native CIDR IP subnet whitelisting (`ALLOWED_NETWORKS`) allowing internal microservices and Docker containers to relay emails without credentials.
- **SASL Authentication**: Built-in `PLAIN` and `LOGIN` SASL support for external authenticated clients and upstream smart-host relays.
- **Dual Delivery Modes & Multi-Relay**:
  - **Smart-Host Outbound Relay**: Forward emails to upstream providers (Gmail, SendGrid, Amazon SES, Mailgun, Office 365, etc.) via opportunistic STARTTLS (port 587/25) or direct TLS/SMTPS (port 465).
  - **Multi-Upstream Failover & Round-Robin**: Automatic fallback across multiple upstream relays (`RELAY_UPSTREAMS`).
  - **Domain-Based Routing**: Route specific recipient domains (`RELAY_DOMAIN_ROUTES`) to dedicated upstreams.
  - **Direct MX Delivery**: Automatic DNS MX resolution and direct opportunistic TLS delivery if upstream relay is omitted.
- **HTTP REST Mail Submission API**: Submit emails via JSON payloads (`POST /v1/send`) with attachments, HTML/plain text, and custom headers.
- **Embedded Web Management Dashboard**: Built-in web UI at `/dashboard` to inspect active queues, view Dead Letter Queue (DLQ), retry failed emails, flush queues, and send test emails.
- **Dead Letter Queue (DLQ) & Spool Management**: Isolated storage for permanently failed emails with REST API endpoints for inspection and manual retry.
- **Rate Limiting & Inbound TLS Enforcement**: Token-bucket rate limiter per IP (`RATE_LIMIT_ENABLED`) and mandatory STARTTLS enforcement (`SERVER_REQUIRE_TLS`).
- **Asynchronous Delivery Webhooks**: Instant HTTP callbacks (`delivered`, `failed`, `dlq`, `queued`) with HMAC SHA-256 signatures.
- **Built-in Observability**: Native HTTP server providing `/healthz`, `/readyz`, JSON `/api/stats`, and Prometheus metrics at `/metrics`.
- **Minimal Footprint**: Multi-stage container build yielding a `< 15MB` image and `< 15MB RAM` runtime consumption running as an unprivileged user (`appuser`).

---

## Quick Start

### Using Docker Compose (Recommended)

Create a `docker-compose.yml` file:

```yaml
services:
  mailer-go:
    image: mailer-go:latest
    build: .
    container_name: mailer-go
    restart: unless-stopped
    ports:
      - "25:2525"      # Map host SMTP port 25 to container 2525
      - "8080:8080"    # Health, REST API & Web Dashboard port
    environment:
      - SERVER_LISTEN_ADDR=:2525
      - SERVER_HOSTNAME=mailer.local
      - ALLOWED_NETWORKS=127.0.0.0/8,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16
      # Outbound Smart-Host Relay (Example: Gmail)
      - RELAY_HOST=smtp.gmail.com
      - RELAY_PORT=587
      - RELAY_USER=your-email@gmail.com
      - RELAY_PASSWORD=your-app-password
      - RELAY_AUTH_TYPE=AUTO
      - RELAY_TLS_TYPE=AUTO
      # Spooling, DLQ & Queue
      - QUEUE_ENABLED=true
      - QUEUE_SPOOL_DIR=/var/spool/mailer-go
      - HTTP_DASHBOARD_ENABLED=true
      - LOG_LEVEL=info
    volumes:
      - mailer-spool:/var/spool/mailer-go

volumes:
  mailer-spool:
```

Start the service:
```bash
docker compose up -d
```

Open Web Dashboard in browser:
```
http://localhost:8080/dashboard
```

---

## HTTP REST Mail Submission API (`/v1/send`)

Submit emails directly over HTTP without needing an SMTP client library:

```bash
curl -X POST http://localhost:8080/v1/send \
  -H "Content-Type: application/json" \
  -d '{
    "from": "notifications@example.com",
    "to": ["user@example.com"],
    "cc": ["team@example.com"],
    "subject": "Order Confirmation #1042",
    "text": "Thank you for your order! Your confirmation number is #1042.",
    "html": "<h1>Thank you for your order!</h1><p>Your confirmation number is <strong>#1042</strong>.</p>",
    "headers": {
      "X-Priority": "1"
    }
  }'
```

### Response
```json
{
  "status": "queued",
  "id": "a1b2c3d4e5f6789012345678",
  "recipients": ["user@example.com", "team@example.com"]
}
```

---

## Queue & Dead Letter Queue (DLQ) Management API

- **List Active & DLQ Messages**:
  `GET /api/queue`
- **Retry Failed Message from DLQ**:
  `POST /api/queue/retry` with payload `{"id": "a1b2c3d4e5f6789012345678"}`
- **Flush Active Queue Immediately**:
  `POST /api/queue/flush`
- **Delete Queued Message**:
  `DELETE /api/queue/{id}`

---

## Multi-Relay Failover & Domain-Based Routing

### Multi-Upstream Failover & Round-Robin
```env
RELAY_STRATEGY=failover # or round-robin
RELAY_UPSTREAMS=smtp://user1:pass1@smtp.primary-provider.com:587,smtps://user2:pass2@smtp.backup-provider.com:465
```

### Domain-Based Routing
Direct emails intended for internal domains or specific partners to dedicated gateways:
```env
RELAY_DOMAIN_ROUTES=corp.internal=10.0.0.50:25,partner.com=smtp.partner-relay.net:587
```

---

## Webhook Notifications

Enable event callbacks for email lifecycle events (`queued`, `delivered`, `failed`, `dlq`, `retry`):

```env
WEBHOOK_ENABLED=true
WEBHOOK_URL=https://my-app.internal/api/email-events
WEBHOOK_SECRET=my_webhook_secret_key
```

Payload delivered to webhook:
```json
{
  "event": "delivered",
  "message_id": "a1b2c3d4e5f6789012345678",
  "from": "notifications@example.com",
  "to": ["user@example.com"],
  "subject": "Order Confirmation #1042",
  "attempts": 1,
  "timestamp": "2026-08-25T09:00:00Z"
}
```

---

## Configuration Reference

### Postfix Environment Mapping

| Postfix Variable | mailer-go Variable | Default | Description |
| :--- | :--- | :--- | :--- |
| `MYNETWORKS` | `ALLOWED_NETWORKS` | `127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16` | CIDR networks allowed to relay without authentication |
| `RELAYHOST` | `RELAY_HOST` | `""` *(Direct MX)* | Upstream SMTP relay host |
| `RELAY_PORT` | `RELAY_PORT` | `587` | Upstream relay port (`587` for STARTTLS, `465` for SMTPS) |
| `SMTP_USERNAME` / `RELAY_USER` | `RELAY_USER` | `""` | Upstream authentication username |
| `SMTP_PASSWORD` / `RELAY_PASSWORD` | `RELAY_PASSWORD` | `""` | Upstream authentication password / API key |
| `MESSAGE_SIZE_LIMIT` | `SERVER_MAX_MESSAGE_SIZE` | `26214400` *(25MB)* | Max message size in bytes |
| `MASQUERADE_DOMAINS` | `SENDER_OVERRIDE` | `""` | Optional envelope `MAIL FROM` override |
| `SMTP_USER_PASS` | `INBOUND_USERS` | `""` | Client auth list in format `user1:pass1,user2:pass2` |
| `SMTP_PORT` | `SERVER_LISTEN_ADDR` | `:2525` | Inbound SMTP listening address |

### Extended Features

| Variable | Default | Description |
| :--- | :--- | :--- |
| `RATE_LIMIT_ENABLED` | `false` | Enable Token Bucket Rate Limiting per IP |
| `RATE_LIMIT_MAX_PER_MINUTE` | `120` | Maximum requests per minute per IP |
| `RATE_LIMIT_BURST` | `30` | Max burst capacity for rate limiter |
| `SERVER_REQUIRE_TLS` | `false` | Enforce STARTTLS for all inbound connections |
| `HTTP_API_KEY` | `""` | Optional API Key protecting REST & Queue endpoints |
| `HTTP_DASHBOARD_ENABLED`| `true` | Enable web dashboard at `/dashboard` |
| `WEBHOOK_ENABLED` | `false` | Enable delivery status webhooks |
| `WEBHOOK_URL` | `""` | Webhook HTTP POST destination endpoint |
| `WEBHOOK_SECRET` | `""` | Optional HMAC SHA-256 signature secret |
| `RELAY_STRATEGY` | `failover` | Strategy for multiple relays (`failover`, `round-robin`) |
| `RELAY_UPSTREAMS` | `""` | List of upstream relays in URL format or JSON |
| `RELAY_DOMAIN_ROUTES` | `""` | Map of domain routes (e.g. `domain.com=host:port`) |

---

## Testing & Verification

### Running Automated Tests
```bash
go test -v ./...
```

### Test via Python SMTP
```python
import smtplib
from email.mime.text import MIMEText

msg = MIMEText("Test message content from mailer-go relay.")
msg["Subject"] = "Test Email"
msg["From"] = "app@internal.local"
msg["To"] = "recipient@example.com"

with smtplib.SMTP("127.0.0.1", 2525) as server:
    server.send_message(msg)
    print("Email successfully accepted by relay!")
```

---

## Monitoring & Health Checks

- **Web Dashboard**: `GET http://localhost:8080/dashboard`
- **Liveness & Readiness**:
  - `GET http://localhost:8080/healthz` -> `{"status":"healthy","uptime":"1h24m10s"}`
  - `GET http://localhost:8080/readyz` -> `ok`
- **Prometheus Metrics**: `GET http://localhost:8080/metrics`
