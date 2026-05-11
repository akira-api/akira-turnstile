package monitor

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"projek/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Hub provides real-time monitoring over WebSocket.
type Hub struct {
	mu             sync.Mutex
	clients        map[*wsClient]bool
	totalSolved    int64
	totalFailed    int64
	activeSolves   int64
	totalSolveMS   int64
	browserWorkers int
	lastEvents     []map[string]any
}

type wsClient struct {
	conn *websocket.Conn
	mu   sync.Mutex
	once sync.Once
	done chan struct{}
}

var wsUpgrader = websocket.Upgrader{CheckOrigin: isAllowedOrigin}

// New creates a new monitor hub.
func New() *Hub {
	return &Hub{clients: make(map[*wsClient]bool), lastEvents: make([]map[string]any, 0, 20)}
}

// SetBrowserWorkers updates the advertised worker count.
func (h *Hub) SetBrowserWorkers(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.browserWorkers = n
}

// RecordActiveDelta tracks concurrent solve count.
func (h *Hub) RecordActiveDelta(delta int64) {
	h.mu.Lock()
	h.activeSolves += delta
	h.mu.Unlock()
	h.broadcastStats()
}

// RecordSuccess records a successful solve.
func (h *Hub) RecordSuccess(solveMS int64, payload gin.H) {
	h.mu.Lock()
	h.totalSolved++
	h.totalSolveMS += solveMS
	h.mu.Unlock()
	h.Publish("request_succeeded", payload)
}

// RecordFailure records a failed solve.
func (h *Hub) RecordFailure(payload gin.H) {
	h.mu.Lock()
	h.totalFailed++
	h.mu.Unlock()
	h.Publish("request_failed", payload)
}

// Publish sends an event to all connected WS clients.
func (h *Hub) Publish(kind string, payload gin.H) {
	event := map[string]any{"type": kind, "ts": time.Now().Format(time.RFC3339Nano), "payload": payload}
	h.mu.Lock()
	h.lastEvents = append(h.lastEvents, event)
	if len(h.lastEvents) > 20 {
		h.lastEvents = h.lastEvents[len(h.lastEvents)-20:]
	}
	h.mu.Unlock()
	conns := h.snapshotClients()
	h.broadcastStats()
	for _, c := range conns {
		h.writeJSON(c, event)
	}
}

// HandleWS is the Gin handler for WebSocket upgrade.
func (h *Hub) HandleWS(c *gin.Context) {
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	client := &wsClient{conn: conn, done: make(chan struct{})}
	conn.SetReadLimit(config.WsReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(config.WsReadTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(config.WsReadTimeout))
	})
	h.mu.Lock()
	h.clients[client] = true
	h.mu.Unlock()
	go h.keepaliveWS(client)
	h.writeJSON(client, h.snapshot())
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			h.removeClient(client)
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(config.WsReadTimeout))
	}
}

// ---------------------------------------------------------------------------
// internal helpers
// ---------------------------------------------------------------------------

func (h *Hub) snapshot() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	avg := 0.0
	if h.totalSolved > 0 {
		avg = float64(h.totalSolveMS) / float64(h.totalSolved)
	}
	total := h.totalSolved + h.totalFailed
	sr := 100.0
	if total > 0 {
		sr = float64(h.totalSolved) * 100 / float64(total)
	}
	events := make([]map[string]any, len(h.lastEvents))
	copy(events, h.lastEvents)
	return map[string]any{
		"type":            "stats",
		"total_solved":    h.totalSolved,
		"total_failed":    h.totalFailed,
		"active_solves":   h.activeSolves,
		"avg_runtime_ms":  avg,
		"success_rate":    sr,
		"browser_workers": h.browserWorkers,
		"events":          events,
	}
}

func (h *Hub) broadcastStats() {
	stats := h.snapshot()
	conns := h.snapshotClients()
	for _, c := range conns {
		h.writeJSON(c, stats)
	}
}

func (h *Hub) snapshotClients() []*wsClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	conns := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		conns = append(conns, c)
	}
	return conns
}

func (h *Hub) writeJSON(client *wsClient, payload any) {
	client.mu.Lock()
	defer client.mu.Unlock()
	_ = client.conn.SetWriteDeadline(time.Now().Add(config.WsWriteTimeout))
	if err := client.conn.WriteJSON(payload); err != nil {
		h.removeClient(client)
	}
}

func (h *Hub) writeControl(client *wsClient, messageType int, data []byte) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if err := client.conn.WriteControl(messageType, data, time.Now().Add(config.WsWriteTimeout)); err != nil {
		h.removeClient(client)
	}
}

func (h *Hub) keepaliveWS(client *wsClient) {
	ticker := time.NewTicker(config.WsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-client.done:
			return
		case <-ticker.C:
			h.writeControl(client, websocket.PingMessage, []byte("ping"))
		}
	}
}

func (h *Hub) removeClient(client *wsClient) {
	client.once.Do(func() {
		if client.done != nil {
			close(client.done)
		}
		_ = client.conn.Close()
	})
	h.mu.Lock()
	delete(h.clients, client)
	h.mu.Unlock()
}

func isAllowedOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	originURL, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if originURL.Scheme != "http" && originURL.Scheme != "https" {
		return false
	}
	requestScheme := "http"
	if r.TLS != nil {
		requestScheme = "https"
	}
	return sameHostPort(originURL, requestScheme, r.Host)
}

func sameHostPort(u *url.URL, scheme, hostport string) bool {
	if u == nil {
		return false
	}
	originHost, originPort := canonicalHostPort(strings.ToLower(strings.TrimSpace(u.Scheme)), u.Host)
	requestHost, requestPort := canonicalHostPort(strings.ToLower(strings.TrimSpace(scheme)), hostport)
	return originHost == requestHost && originPort == requestPort
}

func canonicalHostPort(scheme, hostport string) (string, string) {
	host := strings.ToLower(strings.TrimSpace(hostport))
	port := defaultPortForScheme(scheme)
	if parsedHost, parsedPort, err := splitHostPort(host); err == nil {
		host = strings.ToLower(strings.TrimSpace(parsedHost))
		if strings.TrimSpace(parsedPort) != "" {
			port = parsedPort
		}
	} else {
		host = strings.Trim(host, "[]")
	}
	return host, port
}

func defaultPortForScheme(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

func splitHostPort(hostport string) (string, string, error) {
	host := strings.ToLower(strings.TrimSpace(hostport))
	colon := strings.LastIndexByte(host, ':')
	if colon == -1 {
		return host, "", nil
	}
	return host[:colon], host[colon+1:], nil
}
