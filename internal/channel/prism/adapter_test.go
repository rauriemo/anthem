package prism

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rauriemo/anthem/internal/channel"
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
