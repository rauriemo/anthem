package prism

import (
	"context"
	"encoding/json"
	"sync/atomic"
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

// TestEventBroadcast_ThreadIDDoesNotDowngradeToRes is a regression test for a
// routing bug where execution.* events emitted by PlanRunner with both
// EventType and ThreadID set were being misrouted to sendReply (producing a
// `res` chat frame with the serialized payload as text) instead of
// broadcastEvent (producing a typed `event` frame). Symptom in Prism: every
// gate_notification rendered as a guest chat bubble containing raw JSON, and
// the ReviewDrawer never opened.
func TestEventBroadcast_ThreadIDDoesNotDowngradeToRes(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	time.Sleep(50 * time.Millisecond)

	err := a.Send(context.Background(), channel.OutgoingMessage{
		Text:      `{"gate_id":"g1","text":"Miyazaki finished step s1"}`,
		EventType: "execution.gate_notification",
		ThreadID:  "plan-thread-42",
		GuestID:   "miyazaki",
	})
	if err != nil {
		t.Fatalf("send event: %v", err)
	}

	evFrame := readFrame(t, conn)
	if evFrame.Type != "event" {
		t.Fatalf("expected type=event for EventType+ThreadID send, got %s (would render as chat bubble in Prism)", evFrame.Type)
	}
	if evFrame.Event != "execution.gate_notification" {
		t.Fatalf("expected event=execution.gate_notification, got %s", evFrame.Event)
	}
	if evFrame.Thread != "plan-thread-42" {
		t.Fatalf("expected thread preserved on event frame, got %q", evFrame.Thread)
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

// TestDeactivateGuestFrame covers the L5a wire-format contract: when the
// orchestrator (Plan->Execute handoff) or PlanRunner (step complete/abort)
// sends a DeactivateGuest OutgoingMessage, the Prism adapter must emit a
// "deactivate_guest" frame carrying both the flat guest_id (used by the
// frontend to locate the chip in the roster) and the typed payload with
// a human-readable reason (used for tooltips / transcript traces).
func TestDeactivateGuestFrame(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	time.Sleep(50 * time.Millisecond)

	err := a.Send(context.Background(), channel.OutgoingMessage{
		DeactivateGuest: &channel.DeactivateGuest{
			ID:     "tolkien",
			Reason: "step s1 complete",
		},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	resFrame := readFrame(t, conn)
	if resFrame.Type != "deactivate_guest" {
		t.Errorf("type = %q, want %q", resFrame.Type, "deactivate_guest")
	}
	if resFrame.GuestID != "tolkien" {
		t.Errorf("guest_id = %q, want %q", resFrame.GuestID, "tolkien")
	}
	if resFrame.DeactivateGuest == nil {
		t.Fatal("expected deactivate_guest payload, got nil")
	}
	if resFrame.DeactivateGuest.ID != "tolkien" {
		t.Errorf("deactivate_guest.id = %q, want %q", resFrame.DeactivateGuest.ID, "tolkien")
	}
	if resFrame.DeactivateGuest.Reason != "step s1 complete" {
		t.Errorf("deactivate_guest.reason = %q", resFrame.DeactivateGuest.Reason)
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

// Stage 2 coverage: gate_action intake.
//
// The review drawer sends {type: gate_action, gate_id, action, ...} instead
// of the legacy chat-style [gate:*] command. The adapter must translate
// each valid action into a channel.IncomingMessage the orchestrator's
// existing gate router can consume (i.e. with a [gate:<action>] text tag),
// preserve the raw frame on IncomingMessage.Raw so downstream code can
// read gate_id and flagged_artifacts without re-parsing, and reject
// frames whose action isn't approve/revise/abort.

func TestGateAction_Approve_EmitsIncomingMessage(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	gateFrame := frame{
		Type:   "gate_action",
		GateID: "gate-miyazaki-1",
		Action: "approve",
		Thread: "thread-42",
	}
	data, _ := json.Marshal(gateFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write gate_action: %v", err)
	}

	select {
	case msg := <-a.Incoming():
		if msg.Text != "[gate:approve]" {
			t.Errorf("text = %q, want %q", msg.Text, "[gate:approve]")
		}
		if msg.ThreadID != "thread-42" {
			t.Errorf("thread_id = %q, want %q", msg.ThreadID, "thread-42")
		}
		gateID, action, rev, flagged, ok := GateActionRaw(msg)
		if !ok {
			t.Fatalf("GateActionRaw returned ok=false")
		}
		if gateID != "gate-miyazaki-1" {
			t.Errorf("gate_id = %q", gateID)
		}
		if action != "approve" {
			t.Errorf("action = %q", action)
		}
		if rev != "" {
			t.Errorf("expected empty revision_text, got %q", rev)
		}
		if len(flagged) != 0 {
			t.Errorf("expected no flagged artifacts, got %d", len(flagged))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for gate_action incoming message")
	}
}

func TestGateAction_Revise_CarriesTextAndFlaggedArtifacts(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	gateFrame := frame{
		Type:         "gate_action",
		GateID:       "gate-walt-3",
		Action:       "revise",
		RevisionText: "make the intro shorter",
		Thread:       "thread-77",
		FlaggedArtifacts: []flaggedArtifactRef{
			{ArtifactID: "art-aaaa0001", Note: "looks blurry"},
			{ArtifactID: "art-aaaa0002"},
		},
	}
	data, _ := json.Marshal(gateFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write gate_action: %v", err)
	}

	select {
	case msg := <-a.Incoming():
		if msg.Text != "[gate:revise] make the intro shorter" {
			t.Errorf("text = %q", msg.Text)
		}
		gateID, action, rev, flagged, ok := GateActionRaw(msg)
		if !ok {
			t.Fatalf("GateActionRaw returned ok=false")
		}
		if gateID != "gate-walt-3" || action != "revise" {
			t.Errorf("gate_id=%q action=%q", gateID, action)
		}
		if rev != "make the intro shorter" {
			t.Errorf("revision_text = %q", rev)
		}
		if len(flagged) != 2 {
			t.Fatalf("expected 2 flagged artifacts, got %d", len(flagged))
		}
		if flagged[0].ArtifactID != "art-aaaa0001" || flagged[0].Note != "looks blurry" {
			t.Errorf("flagged[0] = %+v", flagged[0])
		}
		if flagged[1].ArtifactID != "art-aaaa0002" || flagged[1].Note != "" {
			t.Errorf("flagged[1] = %+v", flagged[1])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for gate_action incoming message")
	}
}

func TestGateAction_InvalidAction_EmitsErrorAndNoMessage(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}

	bad := frame{Type: "gate_action", GateID: "gate-bad", Action: "nuke", Thread: "t1"}
	data, _ := json.Marshal(bad)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write gate_action: %v", err)
	}

	errFrame := readFrame(t, conn)
	if errFrame.Type != "error" {
		t.Fatalf("expected error frame, got %s", errFrame.Type)
	}

	select {
	case msg := <-a.Incoming():
		t.Fatalf("unexpected incoming message for invalid action: %+v", msg)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestGateActionRaw_NonGateMessageReturnsFalse(t *testing.T) {
	msg := channel.IncomingMessage{ChannelKind: "prism", Text: "hello"}
	if _, _, _, _, ok := GateActionRaw(msg); ok {
		t.Fatal("expected ok=false for non-gate message")
	}
}

// TestSetOnConnect_DeliversEventsOnlyToNewClient pins the contract
// the plan_snapshot hydration depends on: the on-connect callback's
// returned messages must reach the freshly authenticated client as
// event frames, and must NOT be fanned out to other already-connected
// clients. If the adapter ever regressed to broadcasting on-connect
// events, a user with two tabs would see hydration events arrive on
// the passive tab too (duplicated store writes, confusing Active
// Chains churn).
func TestSetOnConnect_DeliversEventsOnlyToNewClient(t *testing.T) {
	a, url := startTestAdapter(t)

	// First client authenticates before the hook is installed so the
	// test can assert the passive tab receives nothing during a
	// subsequent connect. This mirrors real-world reconnect timing:
	// one tab is already steady when another tab reloads.
	passiveConn := dial(t, url)
	if f := authenticate(t, passiveConn, testToken); f.Type != "auth_ok" {
		t.Fatalf("passive auth failed: %s", f.Type)
	}

	var called int32
	a.SetOnConnect(func(ctx context.Context) []channel.OutgoingMessage {
		atomic.AddInt32(&called, 1)
		return []channel.OutgoingMessage{
			{EventType: "execution.plan_snapshot", Text: `{"plan_id":"p1"}`, ThreadID: "resume:p1"},
		}
	})

	freshConn := dial(t, url)
	if f := authenticate(t, freshConn, testToken); f.Type != "auth_ok" {
		t.Fatalf("fresh auth failed: %s", f.Type)
	}

	// The fresh client should receive the hydration event next.
	got := readFrame(t, freshConn)
	if got.Type != "event" || got.Event != "execution.plan_snapshot" {
		t.Fatalf("fresh client frame = %+v, want type=event event=execution.plan_snapshot", got)
	}
	if got.Thread != "resume:p1" {
		t.Errorf("fresh client thread = %q, want resume:p1", got.Thread)
	}
	if got.Text != `{"plan_id":"p1"}` {
		t.Errorf("fresh client text = %q", got.Text)
	}

	// The passive client must stay quiet -- a short deadline is fine
	// because any broadcast from SetOnConnect would be synchronous.
	_ = passiveConn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, _, err := passiveConn.ReadMessage(); err == nil {
		t.Fatal("passive client received a frame; expected timeout")
	}

	if got := atomic.LoadInt32(&called); got != 1 {
		t.Fatalf("on-connect invoked %d times, want 1", got)
	}
}

// TestHydrateFrame_RepliesOnlyToRequester covers the client-initiated
// hydrate path: an authenticated client sends {"type":"hydrate"} and
// the adapter invokes the on-connect hook again, but only writes the
// returned messages to the requesting connection. A second client on
// the same adapter must receive nothing -- otherwise a browser tab
// that hydrates on reload would also trigger hydration events on
// peer tabs, duplicating store writes.
func TestHydrateFrame_RepliesOnlyToRequester(t *testing.T) {
	a, url := startTestAdapter(t)

	// Install the hook BEFORE either connection so the initial auth
	// handshake fires it once per connection -- the test reads those
	// warm-up frames off the wire before sending the hydrate, so the
	// assertions below only see the hydrate-triggered reply.
	var called int32
	a.SetOnConnect(func(ctx context.Context) []channel.OutgoingMessage {
		atomic.AddInt32(&called, 1)
		return []channel.OutgoingMessage{
			{EventType: "execution.run_history", Text: `{"runs":[]}`, ThreadID: "resume"},
		}
	})

	passiveConn := dial(t, url)
	if f := authenticate(t, passiveConn, testToken); f.Type != "auth_ok" {
		t.Fatalf("passive auth failed: %s", f.Type)
	}
	// Drain the auth-time on-connect message.
	_ = readFrame(t, passiveConn)

	freshConn := dial(t, url)
	if f := authenticate(t, freshConn, testToken); f.Type != "auth_ok" {
		t.Fatalf("fresh auth failed: %s", f.Type)
	}
	_ = readFrame(t, freshConn)

	// Reset the counter so we only assert the hydrate-triggered call.
	atomic.StoreInt32(&called, 0)

	hydrate := frame{Type: "hydrate"}
	data, _ := json.Marshal(hydrate)
	if err := freshConn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write hydrate: %v", err)
	}

	got := readFrame(t, freshConn)
	if got.Type != "event" || got.Event != "execution.run_history" {
		t.Fatalf("requester frame = %+v, want type=event event=execution.run_history", got)
	}

	_ = passiveConn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, _, err := passiveConn.ReadMessage(); err == nil {
		t.Fatal("passive client received a frame; hydrate should not broadcast")
	}

	if got := atomic.LoadInt32(&called); got != 1 {
		t.Fatalf("on-connect invoked %d times after hydrate, want 1", got)
	}
}

// TestHydrateFrame_NoHookInstalledIsNoop verifies that a hydrate
// frame arriving before any hook is installed (startup race) does
// not crash the read loop or write an error frame to the client.
// The client should simply observe silence; subsequent frames
// continue to work.
func TestHydrateFrame_NoHookInstalledIsNoop(t *testing.T) {
	a, url := startTestAdapter(t)
	conn := dial(t, url)
	if f := authenticate(t, conn, testToken); f.Type != "auth_ok" {
		t.Fatalf("auth failed")
	}
	time.Sleep(50 * time.Millisecond)

	hydrate := frame{Type: "hydrate"}
	data, _ := json.Marshal(hydrate)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write hydrate: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected no reply when no on-connect hook is installed")
	}

	// Verify the read loop is still alive: follow up with a chat req
	// and expect the adapter to surface it on Incoming() as usual.
	reqFrame := frame{Type: "req", ID: "post-hydrate", Text: "hello"}
	data, _ = json.Marshal(reqFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}
	select {
	case <-a.Incoming():
	case <-time.After(2 * time.Second):
		t.Fatal("read loop stalled after no-op hydrate")
	}
}
