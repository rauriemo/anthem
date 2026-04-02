package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rauriemo/anthem/internal/channel"
)

const (
	authTimeout  = 10 * time.Second
	writeTimeout = 10 * time.Second
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

type frame struct {
	Type   string `json:"type"`
	Token  string `json:"token,omitempty"`
	Client string `json:"client,omitempty"`
	ID     string `json:"id,omitempty"`
	Text   string `json:"text,omitempty"`
	Event  string `json:"event,omitempty"`
	Error  string `json:"error,omitempty"`
	Ack    bool   `json:"ack,omitempty"`
	Thread string `json:"thread,omitempty"`
}

// connEntry wraps a WebSocket connection with a per-connection write mutex.
// gorilla/websocket supports one concurrent reader and one concurrent writer,
// so all writes to a connection must be serialized.
type connEntry struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

func (c *connEntry) writeJSON(f frame) error {
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("dispatch marshal frame: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// Adapter implements channel.Channel as a WebSocket server.
// Dispatch clients connect in and authenticate with a shared token.
type Adapter struct {
	token      string
	listenAddr string
	logger     *slog.Logger
	incoming   chan channel.IncomingMessage

	server *http.Server
	ln     net.Listener

	mu      sync.RWMutex
	conns   map[*connEntry]bool
	threads map[string]*connEntry

	cancel context.CancelFunc
}

func NewAdapter(token, listenAddr string, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{
		token:      token,
		listenAddr: listenAddr,
		logger:     logger,
		incoming:   make(chan channel.IncomingMessage, 64),
		conns:      make(map[*connEntry]bool),
		threads:    make(map[string]*connEntry),
	}
}

func (a *Adapter) Kind() string { return "dispatch" }

func (a *Adapter) Start(ctx context.Context) error {
	ctx, a.cancel = context.WithCancel(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleWS)

	ln, err := net.Listen("tcp", a.listenAddr)
	if err != nil {
		return fmt.Errorf("dispatch adapter listen: %w", err)
	}
	a.ln = ln

	a.server = &http.Server{Handler: mux}

	go func() {
		if err := a.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			a.logger.Warn("dispatch adapter serve exited", "error", err)
		}
	}()

	go func() {
		<-ctx.Done()
		_ = a.server.Close()
	}()

	a.logger.Info("dispatch adapter listening", "addr", ln.Addr().String())
	return nil
}

// Addr returns the listener address. Useful in tests to discover the OS-assigned port.
func (a *Adapter) Addr() net.Addr {
	if a.ln != nil {
		return a.ln.Addr()
	}
	return nil
}

func (a *Adapter) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		a.logger.Warn("websocket upgrade failed", "error", err)
		return
	}

	if !a.authenticate(ws) {
		ws.Close()
		return
	}

	entry := &connEntry{conn: ws}
	a.mu.Lock()
	a.conns[entry] = true
	a.mu.Unlock()

	a.logger.Info("dispatch client connected", "remote", ws.RemoteAddr())
	a.readLoop(entry)
}

func (a *Adapter) authenticate(conn *websocket.Conn) bool {
	_ = conn.SetReadDeadline(time.Now().Add(authTimeout))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	_, data, err := conn.ReadMessage()
	if err != nil {
		a.logger.Warn("dispatch auth read failed", "error", err)
		return false
	}

	var f frame
	if err := json.Unmarshal(data, &f); err != nil {
		a.logger.Warn("dispatch auth parse failed", "error", err)
		_ = a.writeFrameRaw(conn, frame{Type: "auth_fail", Error: "invalid frame"})
		return false
	}

	if f.Type != "auth" || f.Token != a.token {
		_ = a.writeFrameRaw(conn, frame{Type: "auth_fail", Error: "invalid token"})
		return false
	}

	_ = a.writeFrameRaw(conn, frame{Type: "auth_ok"})
	return true
}

func (a *Adapter) readLoop(entry *connEntry) {
	defer a.removeConn(entry)
	defer entry.conn.Close()

	for {
		_, data, err := entry.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				a.logger.Warn("dispatch read error", "error", err)
			}
			return
		}

		var f frame
		if err := json.Unmarshal(data, &f); err != nil {
			a.logger.Warn("dispatch frame parse error", "error", err)
			continue
		}

		if f.Type != "req" || f.ID == "" {
			continue
		}

		a.mu.Lock()
		a.threads[f.ID] = entry
		a.mu.Unlock()

		msg := channel.IncomingMessage{
			ChannelKind: "dispatch",
			SenderID:    "dispatch",
			ThreadID:    f.ID,
			Text:        f.Text,
			Timestamp:   time.Now(),
		}

		select {
		case a.incoming <- msg:
		default:
			a.logger.Warn("dropping dispatch incoming message, buffer full")
		}
	}
}

func (a *Adapter) removeConn(entry *connEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.conns, entry)
	for id, c := range a.threads {
		if c == entry {
			delete(a.threads, id)
		}
	}
	a.logger.Info("dispatch client disconnected", "remote", entry.conn.RemoteAddr())
}

func (a *Adapter) Send(_ context.Context, msg channel.OutgoingMessage) error {
	if msg.StreamDelta != "" || msg.StreamDone {
		return nil
	}
	if msg.EventType != "" {
		return a.broadcastEvent(msg)
	}
	if msg.ThreadID != "" {
		return a.sendReply(msg)
	}
	return a.broadcastEvent(msg)
}

func (a *Adapter) sendReply(msg channel.OutgoingMessage) error {
	a.mu.RLock()
	entry, ok := a.threads[msg.ThreadID]
	a.mu.RUnlock()

	if !ok {
		return fmt.Errorf("dispatch: no connection for thread %s", msg.ThreadID)
	}

	return entry.writeJSON(frame{Type: "res", ID: msg.ThreadID, Text: msg.Text, Ack: msg.Ack})
}

func (a *Adapter) broadcastEvent(msg channel.OutgoingMessage) error {
	f := frame{Type: "event", Event: msg.EventType, Text: msg.Text, Thread: msg.ThreadID}

	a.mu.RLock()
	entries := make([]*connEntry, 0, len(a.conns))
	for e := range a.conns {
		entries = append(entries, e)
	}
	a.mu.RUnlock()

	var firstErr error
	for _, e := range entries {
		if err := e.writeJSON(f); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("dispatch broadcast: %w", err)
		}
	}
	return firstErr
}

func (a *Adapter) Incoming() <-chan channel.IncomingMessage {
	return a.incoming
}

func (a *Adapter) Close() error {
	if a.cancel != nil {
		a.cancel()
	}

	a.mu.RLock()
	entries := make([]*connEntry, 0, len(a.conns))
	for e := range a.conns {
		entries = append(entries, e)
	}
	a.mu.RUnlock()

	for _, e := range entries {
		e.conn.Close()
	}

	if a.server != nil {
		return a.server.Close()
	}
	return nil
}

// writeFrameRaw writes a frame directly to a raw websocket.Conn.
// Only safe when no other goroutine can write to the same connection
// (e.g. during the authentication handshake before the conn is shared).
func (a *Adapter) writeFrameRaw(conn *websocket.Conn, f frame) error {
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("dispatch marshal frame: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return conn.WriteMessage(websocket.TextMessage, data)
}
