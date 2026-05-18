# CF Clearance Scraper

High-performance API to bypass Cloudflare Turnstile, UAM, and JS challenges using Chrome DevTools Protocol (CDP). Built with Go and chromedp.

## Features

- **Turnstile Solver** — Automatically solves Cloudflare Turnstile challenges
- **UAM Bypass** — Handles "Under Attack Mode" (JS challenge) pages
- **Browser Pool** — Persistent Chrome instances with automatic recycling
- **Rate Limited** — GCRA algorithm to prevent abuse
- **Live Monitoring** — WebSocket endpoint for real-time stats
- **Xvfb Support** — Virtual framebuffer for headless environments

## Tech Stack

| Component          | Technology        |
| ------------------ | ----------------- |
| Language           | Go 1.26           |
| Browser Automation | chromedp (CDP)    |
| Web Framework      | Gin               |
| WebSocket          | gorilla/websocket |
| Display            | Xvfb              |
| Rate Limiting      | GCRA              |

## API Endpoints

### `POST /api/solve`

Solve a Turnstile challenge.

```json
{
  "url": "https://target.example/",
  "sitekey": "your-turnstile-sitekey"
}
```

### `POST /api/solve/uam`

Bypass Cloudflare "Under Attack Mode" (JS challenge).

```json
{
  "url": "https://target-protected.example/"
}
```

### `GET /ws`

WebSocket endpoint for live monitoring stats.

## Configuration

Copy `.env.example` to `.env` and adjust:

```bash
cp .env.example .env
```

| Variable              | Default | Description                                   |
| --------------------- | ------- | --------------------------------------------- |
| `PORT`                | `4557`  | HTTP server port                              |
| `PROXY_SERVER`        | —       | SOCKS5 proxy (e.g. `socks5://127.0.0.1:1080`) |
| `POOL_SIZE`           | auto    | Number of browser workers                     |
| `TABS_PER_BROWSER`    | auto    | Tabs per browser instance                     |
| `BROWSER_MAX_AGE_MIN` | `30`    | Browser restart interval (minutes)            |
| `BROWSER_MAX_SOLVES`  | `50`    | Max solves before browser restart             |
| `SOLVE_TIMEOUT_SEC`   | `60`    | Per-request timeout                           |
| `GCRA_LIMIT`          | `10`    | Rate limit (requests per period)              |
| `GCRA_PERIOD_MS`      | `3000`  | Rate limit period (ms)                        |
| `DEBUG`               | `0`     | Enable debug logging                          |
| `ALLOW_NO_SANDBOX`    | `false` | Allow Chrome without sandbox (root)           |
| `XVFB_DISPLAY_BASE`   | `400`   | Base display number for Xvfb                  |
| `CLOUDFLARED_TOKEN`   | —       | Cloudflare Tunnel token for `cloudflared`     |

## Docker Compose

The bundled compose file starts the solver in headed mode with Xvfb and adds a `cloudflared` container that tunnels to `solver:4557`.

```bash
docker compose up -d --build
```

Set `CLOUDFLARED_TOKEN` in your `.env` before starting the stack.

## Build & Run

```bash
go build -o solver ./main.go
./solver
```

Or with live reload during development:

```bash
go run ./main.go
```

## Requirements

- Go 1.26+
- Chrome/Chromium installed
- Xvfb (for headless servers)
