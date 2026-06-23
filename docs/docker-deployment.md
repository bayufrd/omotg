# Docker Deployment on VPS

This guide covers running OMOTG via Docker on a VPS with Docker installed.

## Prerequisites

- Docker installed on VPS
- Domain pointed to VPS IP
- Ports 8443 and 9090 available (or map to different host ports)

## Quick Start

### 1. Create directory structure

```bash
mkdir -p ~/.config/omotg
```

### 2. Create environment config

```bash
vim ~/.config/omotg/env
```

See [env.template](../env.template) for all options.

### 3. Generate TLS certificates

```bash
openssl req -x509 -nodes -days 365 -newkey rsa:2048 \
  -keyout ~/.config/omotg/webhook.key \
  -out ~/.config/omotg/webhook.crt \
  -subj "/CN=your-domain.com" \
  -addext "subjectAltName=DNS:your-domain.com"
```

### 4. Create Docker network

```bash
docker network create omotg-net
```

### 5. Create docker-compose.yml

```yaml
version: '3.8'

services:
  omotg:
    image: omotg:latest
    container_name: omotg
    restart: unless-stopped
    ports:
      - "127.0.0.1:8443:8443"   # Webhook (bind to localhost, nginx/caddy handles TLS)
      - "127.0.0.1:9090:9090"   # MCP SSE (bind to localhost only)
    volumes:
      - ~/.config/omotg:/root/.config/omotg:ro
    environment:
      - TZ=Asia/Jakarta
    networks:
      - omotg-net
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8443/health"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 10s

networks:
  omotg-net:
    external: false
```

### 6. Build Docker image locally

Create `Dockerfile` in project root:

```dockerfile
FROM golang:1.26-alpine AS builder

WORKDIR /build
COPY . .
RUN go build -ldflags="-s -w" -o omotg .

FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /build/omotg .

EXPOSE 8443 9090

ENTRYPOINT ["./omotg"]
```

Build:

```bash
docker build -t omotg:latest .
```

### 7. Run container

```bash
docker compose up -d
```

### 8. Verify logs

```bash
docker logs -f omotg
```

## Reverse Proxy Setup

OMOTG webhook expects TLS-terminated traffic. Use nginx or caddy.

### Nginx config

```nginx
server {
    listen 443 ssl;
    server_name omotg.your-domain.com;

    ssl_certificate     /etc/letsencrypt/live/your-domain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/your-domain.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8443;
        proxy_ssl_verify off;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        # Required for Telegram webhook verification
        proxy_read_timeout 86400;
        proxy_send_timeout 86400;
    }
}

server {
    listen 80;
    server_name omotg.your-domain.com;
    return 301 https://$host$request_uri;
}
```

### Caddy config

```json
{
  "apps": {
    "http": {
      "servers": {
        "omotg": {
          "listen": [":443"],
          "tls": {
            "cert_file": "/etc/caddy/certs/your-domain.com.crt",
            "key_file": "/etc/caddy/certs/your-domain.com.key"
          },
          "routes": [
            {
              "match": [{"host": ["omotg.your-domain.com"]}],
              "handle": [{
                "handler": "reverse_proxy",
                "upstreams": [{"dial": "127.0.0.1:8443"}]
              }]
            }
          ]
        }
      }
    }
  }
}
```

## OpenCode Server

If using `OMOTG_PROVIDER=opencode`, you need OpenCode server running:

```yaml
services:
  opencode:
    image: ghcr.io/sst/opencode:latest
    container_name: opencode
    restart: unless-stopped
    command: serve --port 4096
    ports:
      - "127.0.0.1:4096:4096"
    networks:
      - omotg-net
```

## Health Check

```bash
curl http://localhost:8443/health
```

## Common Commands

```bash
# View logs
docker logs -f omotg

# Restart
docker compose restart omotg

# Stop
docker compose down

# Update and rebuild
git pull
docker build -t omotg:latest .
docker compose up -d
```

## Environment Variables

Key vars for Docker (set in `~/.config/omotg/env`):

```bash
# Telegram (required)
TELEGRAM_BOT_TOKEN=your_token
TELEGRAM_WEBHOOK_URL=https://omotg.your-domain.com/webhook
TELEGRAM_SECRET_TOKEN=your_secret

# Provider (default: opencode)
OMOTG_PROVIDER=opencode
OPENCODE_SERVER_URL=http://opencode:4096  # Use container name if using Docker network
OPENCODE_SERVER_PASSWORD=your_password

# Or for OpenAI-compatible (e.g., 9router)
# OMOTG_PROVIDER=openai-compatible
# LLM_BASE_URL=https://your-router.com/v1
# LLM_API_TOKEN=your_token
# LLM_MODEL=openai/gpt-4.1-mini

# WhatsApp (optional)
OMOTG_WA_INBOUND_SECRET=wa_inbound_secret
OMOTG_WA_SERVICE_SECRET=wa_service_secret
WHATSAPP_BASE_URL=http://your-wa-gateway:8090
WHATSAPP_API_TOKEN=wa_token
```

## Troubleshooting

### Webhook not registering

Check Telegram webhook info:

```bash
curl "https://api.telegram.org/bot<TOKEN>/getWebhookInfo"
```

### Connection refused

```bash
# Verify container is running
docker ps | grep omotg

# Check port binding
docker port omotg

# Test local connectivity
curl http://localhost:8443/health
```

### OpenCode not reachable (when using Docker network)

Ensure services are on same Docker network and use container names as hostnames:

```bash
docker network inspect omotg-net
```

### Logs show "x509: certificate signed by unknown authority"

Mount CA certificates or disable TLS verification in your WhatsApp gateway config.
