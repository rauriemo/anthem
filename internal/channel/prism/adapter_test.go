package prism

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/guests"
)

const testToken = "test-prism-token"

func startTestAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	a := NewAdapter(testToken, "127.0.0.1:0", nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = a.Close() })

	if err := a.Start(ctx); err != nil {
		t.Fatalf("start adapter: %v", err)
	}

	addr := a.Addr().String()
	return a, "ws://" + addr + "/"
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func authenticate(t *testing.T, conn *websocket.Conn, token string) frame {
	t.Helper()
	authFrame := frame{Type: "auth", Token: token, Client: "test"}
	data, _ := json.Marshal(authFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, resp, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read auth response: %v", err)
	}
	var f frame
	if err := json.Unmarshal(resp, &f); err != nil {
		t.Fatalf("parse auth response: %v", err)
	}
	return f
}

func TestAuthSuccess(t *testing.T) {
	_, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("expected auth_ok, got %s", f.Type)
	}
}

func TestAuthFailure(t *testing.T) {
	_, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, "wrong-token")
	if f.Type != "auth_fail" {
		t.Fatalf("expected auth_fail, got %s", f.Type)
	}
}

func TestKind(t *testing.T) {
	a := NewAdapter("tok", ":0", nil)
	if a.Kind() != "prism" {
		t.Fatalf("expected kind prism, got %s", a.Kind())
	}
}

func TestRequestResponse(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	reqFrame := frame{Type: "req", ID: "req-1", Text: "hello prism"}
	data, _ := json.Marshal(reqFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	err := a.Send(context.Background(), channel.OutgoingMessage{
		ThreadID: "req-1",
		Text:     "hello back",
	})
	if err != nil {
		t.Fatalf("send reply: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, resp, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resFrame frame
	if err := json.Unmarshal(resp, &resFrame); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resFrame.Type != "res" || resFrame.Text != "hello back" || resFrame.ID != "req-1" {
		t.Fatalf("unexpected response: %+v", resFrame)
	}
}

func TestDisplayBroadcast(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	time.Sleep(50 * time.Millisecond)

	component := map[string]any{
		"kind":    "markdown",
		"content": "# Hello",
		"title":   "test.md",
	}
	err := a.Send(context.Background(), channel.OutgoingMessage{
		Display: component,
	})
	if err != nil {
		t.Fatalf("send display: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, resp, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read display: %v", err)
	}
	var displayFrame frame
	if err := json.Unmarshal(resp, &displayFrame); err != nil {
		t.Fatalf("parse display: %v", err)
	}
	if displayFrame.Type != "display" {
		t.Fatalf("expected type display, got %s", displayFrame.Type)
	}
	compMap, ok := displayFrame.Component.(map[string]any)
	if !ok {
		t.Fatalf("component is not a map: %T", displayFrame.Component)
	}
	if compMap["kind"] != "markdown" {
		t.Fatalf("expected kind markdown, got %v", compMap["kind"])
	}
	if compMap["content"] != "# Hello" {
		t.Fatalf("expected content '# Hello', got %v", compMap["content"])
	}
}

func readFrame(t *testing.T, conn *websocket.Conn) frame {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var f frame
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse frame: %v", err)
	}
	return f
}

// TestRoundTripChatAndDisplay simulates a full Prism session:
// connect -> authenticate -> send chat request -> receive text reply + display frame
func TestRoundTripChatAndDisplay(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed: %s", f.Type)
	}

	reqFrame := frame{Type: "req", ID: "chat-42", Text: "Show me the data"}
	data, _ := json.Marshal(reqFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Server sends a text reply
	if err := a.Send(context.Background(), channel.OutgoingMessage{
		ThreadID: "chat-42",
		Text:     "Here is the data overview.",
	}); err != nil {
		t.Fatalf("send reply: %v", err)
	}

	// Server sends a display frame with A2UI component
	component := map[string]any{
		"kind":     "code",
		"content":  "const x = 42;",
		"language": "javascript",
		"title":    "example.js",
	}
	if err := a.Send(context.Background(), channel.OutgoingMessage{
		Display:  component,
		ThreadID: "chat-42",
	}); err != nil {
		t.Fatalf("send display: %v", err)
	}

	// Read the text reply
	resFrame := readFrame(t, conn)
	if resFrame.Type != "res" || resFrame.ID != "chat-42" {
		t.Fatalf("expected res for chat-42, got %+v", resFrame)
	}
	if resFrame.Text != "Here is the data overview." {
		t.Fatalf("unexpected reply text: %s", resFrame.Text)
	}

	// Read the display frame
	dispFrame := readFrame(t, conn)
	if dispFrame.Type != "display" {
		t.Fatalf("expected display frame, got %s", dispFrame.Type)
	}
	compMap, ok := dispFrame.Component.(map[string]any)
	if !ok {
		t.Fatalf("component is not a map: %T", dispFrame.Component)
	}
	if compMap["kind"] != "code" {
		t.Fatalf("expected kind code, got %v", compMap["kind"])
	}
	if compMap["language"] != "javascript" {
		t.Fatalf("expected language javascript, got %v", compMap["language"])
	}
	if compMap["content"] != "const x = 42;" {
		t.Fatalf("expected 'const x = 42;', got %v", compMap["content"])
	}
}

func TestEventBroadcast(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	// Allow server goroutine to register the connection after auth handshake
	time.Sleep(50 * time.Millisecond)

	err := a.Send(context.Background(), channel.OutgoingMessage{
		Text:      "Task completed",
		EventType: "task.completed",
	})
	if err != nil {
		t.Fatalf("send event: %v", err)
	}

	evFrame := readFrame(t, conn)
	if evFrame.Type != "event" {
		t.Fatalf("expected type event, got %s", evFrame.Type)
	}
	if evFrame.Text != "Task completed" {
		t.Fatalf("unexpected text: %s", evFrame.Text)
	}
}

func TestStreamFrames(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	// Send a request to register the thread
	reqFrame := frame{Type: "req", ID: "stream-1", Text: "hello"}
	data, _ := json.Marshal(reqFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Send stream deltas
	if err := a.Send(context.Background(), channel.OutgoingMessage{
		StreamDelta: "Hello ",
		ThreadID:    "stream-1",
	}); err != nil {
		t.Fatalf("send stream delta: %v", err)
	}

	if err := a.Send(context.Background(), channel.OutgoingMessage{
		StreamDelta: "world",
		ThreadID:    "stream-1",
	}); err != nil {
		t.Fatalf("send stream delta 2: %v", err)
	}

	// Send stream done
	if err := a.Send(context.Background(), channel.OutgoingMessage{
		StreamDone: true,
		ThreadID:   "stream-1",
	}); err != nil {
		t.Fatalf("send stream done: %v", err)
	}

	// Read first delta
	f1 := readFrame(t, conn)
	if f1.Type != "stream" {
		t.Fatalf("expected stream frame, got %s", f1.Type)
	}
	if f1.Text != "Hello " {
		t.Fatalf("expected 'Hello ', got %q", f1.Text)
	}
	if f1.Thread != "stream-1" {
		t.Fatalf("expected thread stream-1, got %s", f1.Thread)
	}
	if f1.Done {
		t.Fatal("expected done=false for first delta")
	}

	// Read second delta
	f2 := readFrame(t, conn)
	if f2.Type != "stream" || f2.Text != "world" {
		t.Fatalf("unexpected second delta: %+v", f2)
	}

	// Read done frame
	f3 := readFrame(t, conn)
	if f3.Type != "stream" {
		t.Fatalf("expected stream frame for done, got %s", f3.Type)
	}
	if !f3.Done {
		t.Fatal("expected done=true for final stream frame")
	}
	if f3.Thread != "stream-1" {
		t.Fatalf("expected thread stream-1, got %s", f3.Thread)
	}
}

func TestAuthOkIncludesGuestAgents(t *testing.T) {
	a, url := startTestAdapter(t)

	a.UpdateGuestIndex(guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"game-designer": {
				ID:           "game-designer",
				Name:         "Game Designer",
				Description:  "Designs games",
				Role:         "specialist",
				Capabilities: []string{"story arc"},
				Icon:         "book",
				Scope:        "project",
			},
		},
	})

	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("expected auth_ok, got %s", f.Type)
	}
	if len(f.GuestAgents) != 1 {
		t.Fatalf("expected 1 guest agent, got %d", len(f.GuestAgents))
	}
	if f.GuestAgents[0].ID != "game-designer" {
		t.Errorf("guest ID = %q, want %q", f.GuestAgents[0].ID, "game-designer")
	}
	if f.GuestAgents[0].Name != "Game Designer" {
		t.Errorf("guest name = %q, want %q", f.GuestAgents[0].Name, "Game Designer")
	}
}

func TestAuthOkWithoutGuestAgents(t *testing.T) {
	_, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("expected auth_ok, got %s", f.Type)
	}
	if len(f.GuestAgents) != 0 {
		t.Fatalf("expected 0 guest agents, got %d", len(f.GuestAgents))
	}
}

func TestReqWithActiveGuestsAndMention(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	reqFrame := frame{
		Type:         "req",
		ID:           "req-g1",
		Text:         "help with story",
		ActiveGuests: []string{"game-designer", "code-reviewer"},
		Mention:      "game-designer",
	}
	data, _ := json.Marshal(reqFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	select {
	case msg := <-a.Incoming():
		if len(msg.ActiveGuests) != 2 {
			t.Errorf("active_guests count = %d, want 2", len(msg.ActiveGuests))
		}
		if msg.Mention != "game-designer" {
			t.Errorf("mention = %q, want %q", msg.Mention, "game-designer")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for incoming message")
	}
}

func TestReqWithoutGuestFields(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	reqFrame := frame{Type: "req", ID: "req-old", Text: "plain message"}
	data, _ := json.Marshal(reqFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	select {
	case msg := <-a.Incoming():
		if msg.ActiveGuests != nil {
			t.Errorf("expected nil ActiveGuests, got %v", msg.ActiveGuests)
		}
		if msg.Mention != "" {
			t.Errorf("expected empty Mention, got %q", msg.Mention)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for incoming message")
	}
}

func TestResWithGuestID(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	reqFrame := frame{Type: "req", ID: "req-g2", Text: "test"}
	data, _ := json.Marshal(reqFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	err := a.Send(context.Background(), channel.OutgoingMessage{
		ThreadID: "req-g2",
		Text:     "I am the game designer.",
		GuestID:  "game-designer",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	resFrame := readFrame(t, conn)
	if resFrame.GuestID != "game-designer" {
		t.Errorf("guest_id = %q, want %q", resFrame.GuestID, "game-designer")
	}
}

func TestResWithSuggestGuest(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	reqFrame := frame{Type: "req", ID: "req-g3", Text: "test"}
	data, _ := json.Marshal(reqFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	err := a.Send(context.Background(), channel.OutgoingMessage{
		ThreadID: "req-g3",
		Text:     "Should I bring in the game designer?",
		SuggestGuest: &channel.SuggestGuest{
			ID:     "game-designer",
			Reason: "This topic involves narrative design",
		},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	resFrame := readFrame(t, conn)
	if resFrame.SuggestGuest == nil {
		t.Fatal("expected suggest_guest, got nil")
	}
	if resFrame.SuggestGuest.ID != "game-designer" {
		t.Errorf("suggest_guest.id = %q, want %q", resFrame.SuggestGuest.ID, "game-designer")
	}
	if resFrame.SuggestGuest.Reason != "This topic involves narrative design" {
		t.Errorf("suggest_guest.reason = %q", resFrame.SuggestGuest.Reason)
	}
}

func TestResWithoutGuestFields(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	reqFrame := frame{Type: "req", ID: "req-plain", Text: "test"}
	data, _ := json.Marshal(reqFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	err := a.Send(context.Background(), channel.OutgoingMessage{
		ThreadID: "req-plain",
		Text:     "Just a normal reply.",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	resFrame := readFrame(t, conn)
	if resFrame.GuestID != "" {
		t.Errorf("expected empty guest_id, got %q", resFrame.GuestID)
	}
	if resFrame.SuggestGuest != nil {
		t.Errorf("expected nil suggest_guest, got %+v", resFrame.SuggestGuest)
	}
}

func TestGuestAgentsBroadcastOnUpdate(t *testing.T) {
	a, url := startTestAdapter(t)

	conn1 := dial(t, url)
	f := authenticate(t, conn1, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed for conn1")
	}

	conn2 := dial(t, url)
	f = authenticate(t, conn2, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed for conn2")
	}

	time.Sleep(50 * time.Millisecond)

	a.UpdateGuestIndex(guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"new-agent": {
				ID:          "new-agent",
				Name:        "New Agent",
				Description: "Just added",
				Scope:       "project",
			},
		},
	})

	for i, conn := range []*websocket.Conn{conn1, conn2} {
		f := readFrame(t, conn)
		if f.Type != "guest_agents_updated" {
			t.Errorf("conn%d: expected guest_agents_updated, got %s", i+1, f.Type)
		}
		if len(f.GuestAgents) != 1 {
			t.Errorf("conn%d: expected 1 guest agent, got %d", i+1, len(f.GuestAgents))
		}
	}
}

func TestActivateGuestFrame(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	time.Sleep(50 * time.Millisecond)

	err := a.Send(context.Background(), channel.OutgoingMessage{
		ActivateGuest: &channel.ActivateGuest{
			ID:     "tolkien",
			Reason: "User asked to invite Tolkien",
		},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	resFrame := readFrame(t, conn)
	if resFrame.Type != "activate_guest" {
		t.Errorf("type = %q, want %q", resFrame.Type, "activate_guest")
	}
	if resFrame.GuestID != "tolkien" {
		t.Errorf("guest_id = %q, want %q", resFrame.GuestID, "tolkien")
	}
	if resFrame.ActivateGuest == nil {
		t.Fatal("expected activate_guest payload, got nil")
	}
	if resFrame.ActivateGuest.ID != "tolkien" {
		t.Errorf("activate_guest.id = %q, want %q", resFrame.ActivateGuest.ID, "tolkien")
	}
	if resFrame.ActivateGuest.Reason != "User asked to invite Tolkien" {
		t.Errorf("activate_guest.reason = %q", resFrame.ActivateGuest.Reason)
	}
}

func TestActivateGuestWithTextContinues(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	reqFrame := frame{Type: "req", ID: "req-act1", Text: "invite tolkien"}
	data, _ := json.Marshal(reqFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	err := a.Send(context.Background(), channel.OutgoingMessage{
		ThreadID: "req-act1",
		Text:     "Tolkien is joining the chat.",
		ActivateGuest: &channel.ActivateGuest{
			ID:     "tolkien",
			Reason: "User asked to invite Tolkien",
		},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	activateFrame := readFrame(t, conn)
	if activateFrame.Type != "activate_guest" {
		t.Errorf("first frame type = %q, want activate_guest", activateFrame.Type)
	}

	resFrame := readFrame(t, conn)
	if resFrame.Type != "res" {
		t.Errorf("second frame type = %q, want res", resFrame.Type)
	}
	if resFrame.Text != "Tolkien is joining the chat." {
		t.Errorf("res text = %q", resFrame.Text)
	}
}

func TestSendStream_KindFieldSerializes(t *testing.T) {
	msg := channel.OutgoingMessage{
		StreamDelta: "hello",
		ThreadID:    "t-1",
		StreamKind:  "chat",
	}

	f := frame{
		Type:   "stream",
		Text:   msg.StreamDelta,
		Thread: msg.ThreadID,
		Done:   msg.StreamDone,
		Kind:   msg.StreamKind,
	}

	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("failed to marshal frame: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal frame: %v", err)
	}

	kind, ok := decoded["kind"]
	if !ok {
		t.Fatal("expected 'kind' field in serialized frame")
	}
	if kind != "chat" {
		t.Errorf("expected kind=chat, got %v", kind)
	}
}

func TestSendStream_KindOmittedWhenEmpty(t *testing.T) {
	f := frame{
		Type: "stream",
		Text: "hello",
	}

	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("failed to marshal frame: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal frame: %v", err)
	}

	if _, ok := decoded["kind"]; ok {
		t.Error("kind field should be omitted when empty")
	}
}

func TestAuthOk_CurrentModeOmittedWhenUnset(t *testing.T) {
	_, url := startTestAdapter(t)
	conn := dial(t, url)

	authFrame := frame{Type: "auth", Token: testToken, Client: "test"}
	data, _ := json.Marshal(authFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, resp, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read auth response: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(resp, &raw); err != nil {
		t.Fatalf("parse auth response: %v", err)
	}
	if _, ok := raw["current_mode"]; ok {
		t.Error("current_mode should be omitted from auth_ok when not set")
	}
}

func TestAuthOk_CurrentModeIncludedWhenSet(t *testing.T) {
	a, url := startTestAdapter(t)
	a.SetCurrentMode("loop")

	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("expected auth_ok, got %s", f.Type)
	}
	if f.CurrentMode != "loop" {
		t.Errorf("current_mode = %q, want %q", f.CurrentMode, "loop")
	}
}

func TestSendRes_CurrentModeFromCache(t *testing.T) {
	a, url := startTestAdapter(t)
	a.SetCurrentMode("plan")

	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	reqFrame := frame{Type: "req", ID: "mode-res-1", Text: "hello"}
	data, _ := json.Marshal(reqFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	err := a.Send(context.Background(), channel.OutgoingMessage{
		ThreadID: "mode-res-1",
		Text:     "response",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	resFrame := readFrame(t, conn)
	if resFrame.Type != "res" {
		t.Fatalf("expected res, got %s", resFrame.Type)
	}
	if resFrame.CurrentMode != "plan" {
		t.Errorf("current_mode = %q, want %q", resFrame.CurrentMode, "plan")
	}
}

func TestSendRes_CurrentModeExplicitOverridesCache(t *testing.T) {
	a, url := startTestAdapter(t)
	a.SetCurrentMode("loop")

	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	reqFrame := frame{Type: "req", ID: "mode-res-2", Text: "hello"}
	data, _ := json.Marshal(reqFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	err := a.Send(context.Background(), channel.OutgoingMessage{
		ThreadID:    "mode-res-2",
		Text:        "response",
		CurrentMode: "execute",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	resFrame := readFrame(t, conn)
	if resFrame.CurrentMode != "execute" {
		t.Errorf("current_mode = %q, want %q (explicit should override cache)", resFrame.CurrentMode, "execute")
	}
}

func TestSendStream_CurrentModeFromCache(t *testing.T) {
	a, url := startTestAdapter(t)
	a.SetCurrentMode("execute")

	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	time.Sleep(50 * time.Millisecond)

	if err := a.Send(context.Background(), channel.OutgoingMessage{
		StreamDelta: "chunk",
	}); err != nil {
		t.Fatalf("send stream delta: %v", err)
	}
	if err := a.Send(context.Background(), channel.OutgoingMessage{
		StreamDone: true,
	}); err != nil {
		t.Fatalf("send stream done: %v", err)
	}

	delta := readFrame(t, conn)
	if delta.Type != "stream" {
		t.Fatalf("expected stream, got %s", delta.Type)
	}
	if delta.CurrentMode != "execute" {
		t.Errorf("delta current_mode = %q, want %q", delta.CurrentMode, "execute")
	}

	done := readFrame(t, conn)
	if done.CurrentMode != "execute" {
		t.Errorf("done current_mode = %q, want %q", done.CurrentMode, "execute")
	}
}

func TestStreamFrameKindPropagation(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	time.Sleep(50 * time.Millisecond)

	err := a.Send(context.Background(), channel.OutgoingMessage{
		StreamDelta: "respec data",
		StreamKind:  "chat",
	})
	if err != nil {
		t.Fatalf("send stream: %v", err)
	}

	sf := readFrame(t, conn)
	if sf.Type != "stream" {
		t.Fatalf("expected stream frame, got %s", sf.Type)
	}
	if sf.Kind != "chat" {
		t.Errorf("expected kind=chat, got %q", sf.Kind)
	}
}
