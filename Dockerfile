# syntax=docker/dockerfile:1

# Build Stage
FROM golang:alpine AS builder

WORKDIR /build

RUN apk add --no-cache ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w \
    -X main.version=1.0.0 \
    -X main.buildDate=2026-08-21T00:00:00Z" \
    -o mailer-go ./cmd/mailer-go

# Production Minimal Stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata curl && \
    addgroup -g 10001 -S appgroup && \
    adduser -u 10001 -S appuser -G appgroup && \
    mkdir -p /var/spool/mailer-go && \
    chown -R appuser:appgroup /var/spool/mailer-go

WORKDIR /app

COPY --from=builder /build/mailer-go /app/mailer-go

USER appuser:appgroup

# Ports: 2525 (SMTP Inbound), 8080 (Healthz & Metrics)
EXPOSE 2525 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:8080/healthz || exit 1

ENTRYPOINT ["/app/mailer-go"]
