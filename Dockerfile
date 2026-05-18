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

# Install runtime deps: Chromium, Xvfb and fonts
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       ca-certificates \
       wget \
       gnupg \
       xvfb \
       chromium \
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

RUN useradd --create-home --shell /bin/bash solver \
    && chown solver:solver /usr/local/bin/solver

USER solver
WORKDIR /home/solver

# Defaults (can be overridden via docker-compose or env)
ENV PORT=4557 \
    SOLVE_TIMEOUT_SEC=60 \
    ALLOW_NO_SANDBOX=true \
    BROWSER_HEADLESS=true 

EXPOSE 4557

# If headed mode requested, start Xvfb and set DISPLAY. Then exec the binary.
CMD ["sh", "-lc", "if [ \"$BROWSER_HEADLESS\" = \"false\" ]; then Xvfb :99 -screen 0 1920x1080x24 & export DISPLAY=:99; fi; exec /usr/local/bin/solver"]
