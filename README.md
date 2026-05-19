# akira-turnstile

> Cloudflare Turnstile solver API, built for the [Akira API](https://github.com/akira-api) ecosystem.

Forked from [siputzx/cf-clearance-scraper](https://github.com/siputzx/cf-clearance-scraper)
and reworked to meet Akira API's architecture and deployment requirements.

> **License** — [MIT](LICENSE)

---

## Features

- Direct Turnstile solving — no relay, no middleman
- API key authentication on solver endpoints
- Health check endpoint for uptime monitoring
- Docker-based runtime with Chromium and Xvfb
- Typical solve time around 10 seconds under normal conditions

---

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/health` | Health check |
| `POST` | `/api/solve/direct` | Solve a Turnstile challenge |

---

## Setup

**1. Copy the environment file.**

```bash
cp .env.example .env
```

**2. Fill in the required values.**

> `API_KEY` — required for protected endpoints  
> `CLOUDFLARED_TOKEN` — only needed if you use the `cloudflared` tunnel service

**3. Start the stack.**

```bash
docker compose up -d --build
```

---

## Usage

```bash
curl -X POST http://localhost:4557/api/solve/direct \
  -H "Content-Type: application/json" \
  -H "apikey: your-secret" \
  -d '{
    "url": "https://target.example/"
  }'
```

---

## Without Cloudflare Tunnel

By default, the compose stack runs a `cloudflared` sidecar alongside the solver.

> **Why `cloudflared`?**  
> NAT VPS — no public IP, no open ports, no fun. `cloudflared` handles the ingress so we do not have to.  
> If your server has a real public IP, skip the tunnel entirely.

To expose the port directly instead, make two changes in `docker-compose.yml`.

**1. Replace `expose` with `ports` on the `solver` service.**

```yaml
# tunnel mode (default) — port stays private, cloudflared handles ingress
expose:
  - "4557"

# direct port mode — bind straight to the host
ports:
  - "4557:4557"
```

**2. Remove or comment out the `cloudflared` service block.**

```yaml
# cloudflared:
#   image: cloudflare/cloudflared:latest
#   container_name: turnstile-cloudflared
#   restart: unless-stopped
#   depends_on:
#     - solver
#   command: tunnel --no-autoupdate run --token ${CLOUDFLARED_TOKEN}
#   networks:
#     - akira-net
```

> You can also drop `CLOUDFLARED_TOKEN` from your `.env` — it will not be referenced.

---

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `4557` | HTTP server port |
| `API_KEY` | — | API key for protected endpoints |
| `DEBUG` | `true` in sample | Enable debug logging |
| `PROXY_SERVER` | — | Optional proxy URL |
| `ALLOW_NO_SANDBOX` | `false` | Run Chrome without sandbox (not recommended) |
| `POOL_SIZE` | `1` | Number of browser workers |
| `TABS_PER_BROWSER` | `1` | Tabs per browser instance |
| `BROWSER_MAX_AGE_MIN` | `30` | Browser recycle interval (minutes) |
| `BROWSER_MAX_SOLVES` | `50` | Max solves before a browser is recycled |
| `SOLVE_TIMEOUT_SEC` | `60` | Per-request timeout (seconds) |
| `CLOUDFLARED_TOKEN` | — | Tunnel token for the `cloudflared` service |