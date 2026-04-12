package prism

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/guests"
)

const (
	authTimeout  = 10 * time.Second
	writeTimeout = 10 * time.Second
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

type guestAgentInfo struct {
	ID                      string   `json:"id"`
	Name                    string   `json:"name"`
	Description             string   `json:"description"`
	Role                    string   `json:"role,omitempty"`
	Capabilities            []string `json:"capabilities,omitempty"`
	Icon                    string   `json:"icon,omitempty"`
	Model                   string   `json:"model,omitempty"`
	Quotes                  []string `json:"quotes,omitempty"`
	RequirementsFingerprint string   `json:"requirementsFingerprint,omitempty"`
	Scope                   string   `json:"scope,omitempty"`
	Source                  string   `json:"source,omitempty"`
}

type suggestGuestFrame struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type activateGuestFrame struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type frame struct {
	Type            string              `json:"type"`
	Token           string              `json:"token,omitempty"`
	Client          string              `json:"client,omitempty"`
	ID              string              `json:"id,omitempty"`
	Text            string              `json:"text,omitempty"`
	Event           string              `json:"event,omitempty"`
	Error           string              `json:"error,omitempty"`
	Ack             bool                `json:"ack,omitempty"`
	Thread          string              `json:"thread,omitempty"`
	Component       any                 `json:"component,omitempty"`
	Done            bool                `json:"done,omitempty"`
	Files           []frameFile         `json:"files,omitempty"`
	GuestAgents     []guestAgentInfo    `json:"guest_agents,omitempty"`
	MaxActiveGuests int                 `json:"max_active_guests,omitempty"`
	ActiveGuests    []string            `json:"active_guests,omitempty"`
	Mention         string              `json:"mention,omitempty"`
	GuestID         string              `json:"guest_id,omitempty"`
	DisplayIDs      []string            `json:"display_ids,omitempty"`
	SuggestGuest    *suggestGuestFrame  `json:"suggest_guest,omitempty"`
	ActivateGuest   *activateGuestFrame `json:"activate_guest,omitempty"`
	Kind            string              `json:"kind,omitempty"`
}

type frameFile struct {
	Name     string `json:"name"`
	Content  string `json:"content"`
	MimeType string `json:"mime_type"`
}

type connEntry struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

func (c *connEntry) writeJSON(f frame) error {
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("prism marshal frame: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// Adapter implements channel.Channel as a WebSocket server for Prism clients.
// Extends the Dispatch adapter pattern with a "display" frame type for A2UI content.
type Adapter struct {
	token      string
	listenAddr string
	logger     *slog.Logger
	incoming   chan channel.IncomingMessage

	server *http.Server
	ln     net.Listener

	mu              sync.RWMutex
	conns           map[*connEntry]bool
	threads         map[string]*connEntry
	guestAgents     []guestAgentInfo
	maxActiveGuests int

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

func (a *Adapter) Kind() string { return "prism" }

// SetMaxActiveGuests sets the soft cap on active guests and includes it in auth_ok.
func (a *Adapter) SetMaxActiveGuests(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.maxActiveGuests = n
}

func (a *Adapter) Start(ctx context.Context) error {
	ctx, a.cancel = context.WithCancel(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleWS)

	ln, err := net.Listen("tcp", a.listenAddr)
	if err != nil {
		return fmt.Errorf("prism adapter listen: %w", err)
	}
	a.ln = ln

	a.server = &http.Server{Handler: mux}

	go func() {
		if err := a.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			a.logger.Warn("prism adapter serve exited", "error", err)
		}
	}()

	go func() {
		<-ctx.Done()
		_ = a.server.Close()
	}()

	a.logger.Info("prism adapter listening", "addr", ln.Addr().String())
	return nil
}

// Addr returns the listener address (useful in tests).
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

	a.logger.Info("prism client connected", "remote", ws.RemoteAddr())
	a.readLoop(entry)
}

// UpdateGuestIndex converts a GuestIndex to the wire format, caches it, and broadcasts to all connected clients.
func (a *Adapter) UpdateGuestIndex(index guests.GuestIndex) {
	infos := make([]guestAgentInfo, 0, len(index.Agents))
	for _, agent := range index.Agents {
		infos = append(infos, guestAgentInfo{
			ID:                      agent.ID,
			Name:                    agent.Name,
			Description:             agent.Description,
			Role:                    agent.Role,
			Capabilities:            agent.Capabilities,
			Icon:                    agent.Icon,
			Model:                   agent.Model,
			Quotes:                  agent.Quotes,
			RequirementsFingerprint: agent.RequirementsFingerprint,
			Scope:                   agent.Scope,
			Source:                  agent.Source,
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	a.setGuestAgents(infos)
}

func (a *Adapter) setGuestAgents(agents []guestAgentInfo) {
	a.mu.Lock()
	a.guestAgents = agents
	entries := make([]*connEntry, 0, len(a.conns))
	for e := range a.conns {
		entries = append(entries, e)
	}
	a.mu.Unlock()

	if len(entries) == 0 {
		return
	}

	f := frame{Type: "guest_agents_updated", GuestAgents: agents}
	for _, e := range entries {
		if err := e.writeJSON(f); err != nil {
			a.logger.Warn("failed to broadcast guest agents update", "error", err)
		}
	}
}

func (a *Adapter) authenticate(conn *websocket.Conn) bool {
	_ = conn.SetReadDeadline(time.Now().Add(authTimeout))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	_, data, err := conn.ReadMessage()
	if err != nil {
		a.logger.Warn("prism auth read failed", "error", err)
		return false
	}

	var f frame
	if err := json.Unmarshal(data, &f); err != nil {
		a.logger.Warn("prism auth parse failed", "error", err)
		_ = a.writeFrameRaw(conn, frame{Type: "auth_fail", Error: "invalid frame"})
		return false
	}

	if f.Type != "auth" || f.Token != a.token {
		_ = a.writeFrameRaw(conn, frame{Type: "auth_fail", Error: "invalid token"})
		return false
	}

	a.mu.RLock()
	agents := a.guestAgents
	maxActive := a.maxActiveGuests
	a.mu.RUnlock()

	_ = a.writeFrameRaw(conn, frame{Type: "auth_ok", GuestAgents: agents, MaxActiveGuests: maxActive})
	return true
}

func (a *Adapter) readLoop(entry *connEntry) {
	defer a.removeConn(entry)
	defer entry.conn.Close()

	for {
		_, data, err := entry.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				a.logger.Warn("prism read error", "error", err)
			}
			return
		}

		var f frame
		if err := json.Unmarshal(data, &f); err != nil {
			a.logger.Warn("prism frame parse error", "error", err)
			continue
		}

		if f.Type != "req" || f.ID == "" {
			continue
		}

		a.mu.Lock()
		a.threads[f.ID] = entry
		a.mu.Unlock()

		msg := channel.IncomingMessage{
			ChannelKind:  "prism",
			SenderID:     "prism",
			ThreadID:     f.ID,
			Text:         f.Text,
			Timestamp:    time.Now(),
			ActiveGuests: f.ActiveGuests,
			Mention:      f.Mention,
		}

		for _, ff := range f.Files {
			raw, err := base64.StdEncoding.DecodeString(ff.Content)
			if err != nil {
				a.logger.Warn("prism file decode error", "name", ff.Name, "error", err)
				continue
			}
			msg.Files = append(msg.Files, channel.File{
				Name:     ff.Name,
				Content:  raw,
				MimeType: ff.MimeType,
			})
		}

		select {
		case a.incoming <- msg:
		default:
			a.logger.Warn("dropping prism incoming message, buffer full")
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
	a.logger.Info("prism client disconnected", "remote", entry.conn.RemoteAddr())
}

func (a *Adapter) Send(_ context.Context, msg channel.OutgoingMessage) error {
	if msg.StreamDelta != "" || msg.StreamDone {
		return a.sendStream(msg)
	}

	if msg.ActivateGuest != nil {
		f := frame{
			Type:    "activate_guest",
			GuestID: msg.ActivateGuest.ID,
			ActivateGuest: &activateGuestFrame{
				ID:     msg.ActivateGuest.ID,
				Reason: msg.ActivateGuest.Reason,
			},
		}
		if err := a.broadcastFrame(f); err != nil {
			a.logger.Warn("prism activate_guest broadcast failed", "error", err)
		}
	}

	if msg.Display != nil {
		if err := a.sendDisplay(msg); err != nil {
			a.logger.Warn("prism display send failed", "error", err)
		}
	}

	if msg.Text == "" {
		return nil
	}

	if msg.ThreadID != "" {
		return a.sendReply(msg)
	}
	if msg.EventType != "" {
		return a.broadcastEvent(msg)
	}
	return a.broadcastEvent(msg)
}

func (a *Adapter) buildResFrame(msg channel.OutgoingMessage) frame {
	f := frame{Type: "res", ID: msg.ThreadID, Text: msg.Text, Ack: msg.Ack}
	if msg.GuestID != "" {
		f.GuestID = msg.GuestID
	}
	if len(msg.DisplayIDs) > 0 {
		f.DisplayIDs = msg.DisplayIDs
	}
	if msg.SuggestGuest != nil {
		f.SuggestGuest = &suggestGuestFrame{
			ID:     msg.SuggestGuest.ID,
			Reason: msg.SuggestGuest.Reason,
		}
	}
	return f
}

func (a *Adapter) sendReply(msg channel.OutgoingMessage) error {
	a.mu.RLock()
	entry, ok := a.threads[msg.ThreadID]
	a.mu.RUnlock()

	if !ok {
		a.logger.Warn("prism sendReply: thread not registered, broadcasting res to all prism clients",
			"thread", msg.ThreadID)
		return a.broadcastRes(msg)
	}

	return entry.writeJSON(a.buildResFrame(msg))
}

func (a *Adapter) broadcastFrame(f frame) error {
	a.mu.RLock()
	entries := make([]*connEntry, 0, len(a.conns))
	for e := range a.conns {
		entries = append(entries, e)
	}
	a.mu.RUnlock()

	var firstErr error
	for _, e := range entries {
		if err := e.writeJSON(f); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("prism broadcast frame: %w", err)
		}
	}
	return firstErr
}

func (a *Adapter) broadcastRes(msg channel.OutgoingMessage) error {
	f := a.buildResFrame(msg)
	a.mu.RLock()
	entries := make([]*connEntry, 0, len(a.conns))
	for e := range a.conns {
		entries = append(entries, e)
	}
	a.mu.RUnlock()

	var firstErr error
	for _, e := range entries {
		if err := e.writeJSON(f); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("prism res broadcast: %w", err)
		}
	}
	return firstErr
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
			firstErr = fmt.Errorf("prism broadcast: %w", err)
		}
	}
	return firstErr
}

// sendDisplay broadcasts an A2UI display frame to all connected Prism clients.
// If the message has a ThreadID, it is sent only to the connection for that thread.
func (a *Adapter) sendDisplay(msg channel.OutgoingMessage) error {
	f := frame{Type: "display", Component: msg.Display, Thread: msg.ThreadID, ID: msg.DisplayID, GuestID: msg.GuestID}

	if msg.ThreadID != "" {
		a.mu.RLock()
		entry, ok := a.threads[msg.ThreadID]
		a.mu.RUnlock()
		if ok {
			return entry.writeJSON(f)
		}
	}

	a.mu.RLock()
	entries := make([]*connEntry, 0, len(a.conns))
	for e := range a.conns {
		entries = append(entries, e)
	}
	a.mu.RUnlock()

	var firstErr error
	for _, e := range entries {
		if err := e.writeJSON(f); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("prism display broadcast: %w", err)
		}
	}
	return firstErr
}

func (a *Adapter) sendStream(msg channel.OutgoingMessage) error {
	f := frame{Type: "stream", Text: msg.StreamDelta, Thread: msg.ThreadID, Done: msg.StreamDone, GuestID: msg.GuestID, Kind: msg.StreamKind}

	if msg.ThreadID != "" {
		a.mu.RLock()
		entry, ok := a.threads[msg.ThreadID]
		a.mu.RUnlock()
		if ok {
			return entry.writeJSON(f)
		}
	}

	a.mu.RLock()
	entries := make([]*connEntry, 0, len(a.conns))
	for e := range a.conns {
		entries = append(entries, e)
	}
	a.mu.RUnlock()

	var firstErr error
	for _, e := range entries {
		if err := e.writeJSON(f); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("prism stream broadcast: %w", err)
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

func (a *Adapter) writeFrameRaw(conn *websocket.Conn, f frame) error {
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("prism marshal frame: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return conn.WriteMessage(websocket.TextMessage, data)
}
