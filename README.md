# CF Clearance Scraper

API for solving Cloudflare Turnstile and UAM challenges.

## What it does

- `POST /api/solve` solves a Turnstile challenge.
- `POST /api/solve/uam` handles UAM pages.
- `GET /api/healthz` returns a simple health check.

## Docker setup

1. Copy the sample environment file.

   ```bash
   cp .env.example .env
   ```

2. Set the values you need in `.env`.
   - `API_KEY` protects the API endpoints.
   - `PROXY_SERVER` is optional.
   - `CLOUDFLARED_TOKEN` is required only if you want the bundled `cloudflared` container to run.

3. Start the stack.

   ```bash
   docker compose up -d --build
   ```

The solver container runs with Chromium and Xvfb inside Docker. You do not need Go or Chromium installed on the host.

## Authentication

Protected endpoints expect the `apikey` header.

```bash
curl -H "apikey: your-secret" http://localhost:4557/api/healthz
```

## API usage

### Solve Turnstile

```bash
curl -X POST http://localhost:4557/api/solve \
  -H "Content-Type: application/json" \
  -H "apikey: your-secret" \
  -d '{
    "url": "https://target.example/",
    "sitekey": "your-turnstile-sitekey"
  }'
```

### Solve UAM

```bash
curl -X POST http://localhost:4557/api/solve/uam \
  -H "Content-Type: application/json" \
  -H "apikey: your-secret" \
  -d '{
    "url": "https://target-protected.example/"
  }'
```

## Environment variables

| Variable              | Default | Description                               |
| --------------------- | ------- | ----------------------------------------- |
| `PORT`                | `4557`  | HTTP server port                          |
| `PROXY_SERVER`        | none    | Optional SOCKS5 proxy URL                 |
| `API_KEY`             | none    | API key required by protected endpoints   |
| `DEBUG`               | `false` | Enable debug logging                      |
| `ALLOW_NO_SANDBOX`    | `false` | Allow Chrome to run without sandbox       |
| `XVFB_DISPLAY_BASE`   | `400`   | Base display number for Xvfb              |
| `POOL_SIZE`           | `1`     | Browser worker count                      |
| `TABS_PER_BROWSER`    | `1`     | Tabs per browser instance                 |
| `BROWSER_MAX_AGE_MIN` | `30`    | Browser restart interval in minutes       |
| `BROWSER_MAX_SOLVES`  | `50`    | Max solves before browser restart         |
| `SOLVE_TIMEOUT_SEC`   | `60`    | Per-request timeout in seconds            |
| `CLOUDFLARED_TOKEN`   | none    | Cloudflare Tunnel token for `cloudflared` |
