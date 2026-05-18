package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/target"
	jsonv2 "github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

var marshalOpts = jsonv2.JoinOptions(
	jsonv2.DefaultOptionsV2(),
	jsontext.AllowInvalidUTF8(true),
)

type rawSession struct {
	nextID    int64 // Must be first for proper alignment on 32-bit systems
	conn      *rawConn
	sessionID target.SessionID
	targetID  target.ID
	pending   map[int64]chan *cdproto.Message
	listenCtx context.Context
	listenFn  func(ev any)
}

type rawConn struct {
	conn   net.Conn
	reader wsutil.Reader
	writer wsutil.Writer
	mu     sync.Mutex
}

func dialChrome(ctx context.Context, port int) (*rawConn, error) {
	var wsURL string
	var err error
	for i := 0; i < 10; i++ {
		wsURL, err = getBrowserWSURL(port)
		if err == nil && wsURL != "" {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if wsURL == "" {
		return nil, fmt.Errorf("could not get WS URL on port %d: %w", port, err)
	}
	// Use ws.Dial the same way chromedp does
	conn, _, _, err := ws.Dial(ctx, wsURL)
	if err != nil {
		return nil, fmt.Errorf("ws dial %q: %w", wsURL, err)
	}
	return &rawConn{
		conn:   conn,
		writer: *wsutil.NewWriterBufferSize(conn, ws.StateClientSide, ws.OpText, 0),
	}, nil
}

func getBrowserWSURL(port int) (string, error) {
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var info struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	if info.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("empty websocket URL on port %d", port)
	}
	return info.WebSocketDebuggerURL, nil
}

func (c *rawConn) send(ctx context.Context, msg *cdproto.Message) error {
	// respect context cancellation before attempting write
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	buf, err := jsonv2.Marshal(msg, marshalOpts)
	if err != nil {
		return err
	}
	// set write deadline from context if present, otherwise clear deadline
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetWriteDeadline(dl)
	} else {
		_ = c.conn.SetWriteDeadline(time.Time{})
	}
	if err := wsutil.WriteClientText(c.conn, buf); err != nil {
		return err
	}
	return nil
}

func (c *rawConn) read(ctx context.Context) (*cdproto.Message, error) {
	// set read deadline from context if present, otherwise use a sensible default
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetReadDeadline(dl)
	} else {
		_ = c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	}
	c.reader = wsutil.Reader{Source: c.conn, State: ws.StateClientSide}
	h, err := c.reader.NextFrame()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if h.OpCode == ws.OpClose {
		return nil, fmt.Errorf("websocket closed")
	}
	if h.OpCode != ws.OpText {
		return nil, fmt.Errorf("unexpected opcode: %v", h.OpCode)
	}
	var b bytes.Buffer
	if _, err := b.ReadFrom(&c.reader); err != nil {
		return nil, err
	}
	var msg cdproto.Message
	if err := jsonv2.Unmarshal(b.Bytes(), &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (c *rawConn) close() error {
	return c.conn.Close()
}

func newRawSession(ctx context.Context, port int) (*rawSession, error) {
	conn, err := dialChrome(ctx, port)
	if err != nil {
		return nil, fmt.Errorf("dial chrome on port %d: %w", port, err)
	}

	// Create a new target (tab)
	createCmd := &cdproto.Message{
		ID:     1,
		Method: "Target.createTarget",
		Params: mustMarshal(map[string]any{
			"url": "about:blank",
		}),
	}
	if err := conn.send(ctx, createCmd); err != nil {
		conn.close()
		return nil, fmt.Errorf("createTarget send: %w", err)
	}
	resp, err := conn.read(ctx)
	if err != nil {
		conn.close()
		return nil, fmt.Errorf("createTarget read: %w", err)
	}
	if resp.Error != nil {
		conn.close()
		return nil, fmt.Errorf("createTarget error: %s", resp.Error.Message)
	}
	var createResult struct {
		TargetID string `json:"targetId"`
	}
	rawResult := []byte(resp.Result)
	if len(rawResult) == 0 {
		conn.close()
		return nil, fmt.Errorf("createTarget empty result, method=%s id=%d", resp.Method, resp.ID)
	}
	if err := json.Unmarshal(rawResult, &createResult); err != nil {
		conn.close()
		return nil, fmt.Errorf("createTarget unmarshal (%s): %w", string(rawResult), err)
	}

	// Attach to the target
	attachCmd := &cdproto.Message{
		ID:     2,
		Method: "Target.attachToTarget",
		Params: mustMarshal(map[string]any{
			"targetId": createResult.TargetID,
			"flatten":  true,
		}),
	}
	if err := conn.send(ctx, attachCmd); err != nil {
		conn.close()
		return nil, fmt.Errorf("attachTarget send: %w", err)
	}
	attachResp, err := conn.read(ctx)
	if err != nil {
		conn.close()
		return nil, fmt.Errorf("attachTarget read: %w", err)
	}
	if attachResp.Error != nil {
		conn.close()
		return nil, fmt.Errorf("attachTarget error: %s", attachResp.Error.Message)
	}
	// In flattened mode, attachToTarget returns a Target.attachedToTarget event
	// with the sessionId in params, not in result.
	var attachResult struct {
		SessionID string `json:"sessionId"`
	}
	rawAttach := []byte(attachResp.Params)
	if len(rawAttach) == 0 {
		rawAttach = []byte(attachResp.Result)
	}
	if len(rawAttach) == 0 {
		conn.close()
		return nil, fmt.Errorf("attachTarget empty result and params, method=%s id=%d", attachResp.Method, attachResp.ID)
	}
	if err := json.Unmarshal(rawAttach, &attachResult); err != nil {
		conn.close()
		return nil, fmt.Errorf("attachTarget unmarshal (%s): %w", string(rawAttach), err)
	}

	session := &rawSession{
		conn:      conn,
		sessionID: target.SessionID(attachResult.SessionID),
		targetID:  target.ID(createResult.TargetID),
		nextID:    100,
		pending:   make(map[int64]chan *cdproto.Message),
		listenCtx: ctx,
	}

	go session.readLoop()
	return session, nil
}

func (s *rawSession) readLoop() {
	for {
		msg, err := s.conn.read(s.listenCtx)
		if err != nil {
			return
		}
		if msg.ID > 0 {
			// Command response
			if ch, ok := s.pending[msg.ID]; ok {
				select {
				case ch <- msg:
				default:
				}
			}
		} else {
			// Event
			if s.listenFn != nil {
				s.listenFn(msg)
			}
		}
	}
}

func (s *rawSession) Execute(ctx context.Context, method string, params, res any) error {
	id := atomic.AddInt64(&s.nextID, 1)
	var paramsBuf []byte
	if params != nil {
		var err error
		paramsBuf, err = jsonv2.Marshal(params, marshalOpts)
		if err != nil {
			return err
		}
	}
	msg := &cdproto.Message{
		ID:        id,
		SessionID: s.sessionID,
		Method:    cdproto.MethodType(method),
		Params:    paramsBuf,
	}

	ch := make(chan *cdproto.Message, 1)
	s.pending[id] = ch
	defer delete(s.pending, id)

	if err := s.conn.send(ctx, msg); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if res != nil && len(resp.Result) > 0 {
			return json.Unmarshal([]byte(resp.Result), res)
		}
		return nil
	}
}

func (s *rawSession) close() {
	// Try to close the target
	closeCmd := &cdproto.Message{
		ID:     atomic.AddInt64(&s.nextID, 1),
		Method: "Target.closeTarget",
		Params: mustMarshal(map[string]any{
			"targetId": string(s.targetID),
		}),
	}
	_ = s.conn.send(context.Background(), closeCmd)
	s.conn.close()
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
