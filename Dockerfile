# Multi-stage Dockerfile
# Builder: compile Go binary
FROM golang:1.26.3-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o /app/solver ./main.go

# Runtime image: Debian with Chromium and Xvfb
FROM debian:bookworm-slim
ENV DEBIAN_FRONTEND=noninteractive

# Install runtime deps: Chromium, Xvfb dan fonts
RUN apt-get update \
     && apt-get install -y --no-install-recommends \
         ca-certificates \
         wget \
         gnupg \
         xvfb \
         chromium \
         chromium-sandbox \
         fonts-liberation \
         libnss3 \
         libxss1 \
         libasound2 \
         libatk1.0-0 \
         libatk-bridge2.0-0 \
         libgtk-3-0 \
         procps \
     && rm -rf /var/lib/apt/lists/*

# Copy binary
COPY --from=builder /app/solver /usr/local/bin/solver

# Create non-root user
RUN useradd --create-home --shell /bin/bash solver \
    && chown solver:solver /usr/local/bin/solver

# Ensure chrome-sandbox helper is owned by root and setuid
RUN for p in \
        /usr/lib/chromium/chrome-sandbox \
        /usr/lib/chromium-browser/chrome-sandbox \
        /opt/google/chrome/chrome-sandbox; do \
      if [ -f "$p" ]; then \
        chown root:root "$p" && chmod 4755 "$p"; \
        echo "Sandbox configured: $p"; \
      fi; \
    done

ENV CHROME_DEVEL_SANDBOX=/usr/lib/chromium/chrome-sandbox

USER solver
WORKDIR /home/solver

ENV PORT=4557 \
    SOLVE_TIMEOUT_SEC=60 \
    ALLOW_NO_SANDBOX=false \
    BROWSER_HEADLESS=false

EXPOSE 4557

CMD ["sh", "-lc", "if [ \"$BROWSER_HEADLESS\" = \"false\" ]; then Xvfb :99 -screen 0 1920x1080x24 & export DISPLAY=:99; fi; exec /usr/local/bin/solver"]