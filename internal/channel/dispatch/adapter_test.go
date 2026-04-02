package dispatch

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rauriemo/anthem/internal/channel"
)

const testToken = "test-secret-token"

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
	if f.Error != "invalid token" {
		t.Fatalf("expected 'invalid token' error, got %q", f.Error)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection closed after auth failure")
	}
}

func TestAuthTimeout(t *testing.T) {
	a := NewAdapter(testToken, "127.0.0.1:0", nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = a.Close() })

	if err := a.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	url := "ws://" + a.Addr().String() + "/"

	conn := dial(t, url)

	_ = conn.SetReadDeadline(time.Now().Add(authTimeout + 2*time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection closed due to auth timeout")
	}
}

func TestRequestResponse(t *testing.T) {
	adapter, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed: %s", f.Type)
	}

	reqID := "abcdef01234567890abcdef012345678"
	reqFrame := frame{Type: "req", ID: reqID, Text: "hello anthem"}
	data, _ := json.Marshal(reqFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}

	select {
	case msg := <-adapter.Incoming():
		if msg.ChannelKind != "dispatch" {
			t.Fatalf("expected channel kind dispatch, got %s", msg.ChannelKind)
		}
		if msg.ThreadID != reqID {
			t.Fatalf("expected thread ID %s, got %s", reqID, msg.ThreadID)
		}
		if msg.Text != "hello anthem" {
			t.Fatalf("expected text 'hello anthem', got %q", msg.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for incoming message")
	}

	err := adapter.Send(context.Background(), channel.OutgoingMessage{
		Text:     "hello dispatch",
		ThreadID: reqID,
	})
	if err != nil {
		t.Fatalf("send reply: %v", err)
	}

	resp := readFrame(t, conn)
	if resp.Type != "res" {
		t.Fatalf("expected res, got %s", resp.Type)
	}
	if resp.ID != reqID {
		t.Fatalf("expected ID %s, got %s", reqID, resp.ID)
	}
	if resp.Text != "hello dispatch" {
		t.Fatalf("expected text 'hello dispatch', got %q", resp.Text)
	}
}

func TestEventBroadcast(t *testing.T) {
	adapter, url := startTestAdapter(t)

	conn1 := dial(t, url)
	f := authenticate(t, conn1, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth1 failed: %s", f.Type)
	}

	conn2 := dial(t, url)
	f = authenticate(t, conn2, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth2 failed: %s", f.Type)
	}

	err := adapter.Send(context.Background(), channel.OutgoingMessage{
		Text:      "task completed",
		EventType: "task.completed",
	})
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}

	for i, conn := range []*websocket.Conn{conn1, conn2} {
		f := readFrame(t, conn)
		if f.Type != "event" {
			t.Fatalf("client %d: expected event, got %s", i, f.Type)
		}
		if f.Event != "task.completed" {
			t.Fatalf("client %d: expected event type task.completed, got %s", i, f.Event)
		}
		if f.Text != "task completed" {
			t.Fatalf("client %d: expected text 'task completed', got %q", i, f.Text)
		}
	}
}

func TestConnectionCleanup(t *testing.T) {
	adapter, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed: %s", f.Type)
	}

	reqID := "cleanup-test-id-00000000000000000"
	reqFrame := frame{Type: "req", ID: reqID, Text: "test"}
	data, _ := json.Marshal(reqFrame)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}

	select {
	case <-adapter.Incoming():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for incoming message")
	}

	adapter.mu.RLock()
	_, hasThread := adapter.threads[reqID]
	connCount := len(adapter.conns)
	adapter.mu.RUnlock()
	if !hasThread {
		t.Fatal("thread not registered")
	}
	if connCount != 1 {
		t.Fatalf("expected 1 connection, got %d", connCount)
	}

	conn.Close()
	time.Sleep(200 * time.Millisecond)

	adapter.mu.RLock()
	_, hasThread = adapter.threads[reqID]
	connCount = len(adapter.conns)
	adapter.mu.RUnlock()
	if hasThread {
		t.Fatal("thread not cleaned up after disconnect")
	}
	if connCount != 0 {
		t.Fatalf("expected 0 connections after disconnect, got %d", connCount)
	}
}

func TestConcurrentReplyRouting(t *testing.T) {
	adapter, url := startTestAdapter(t)

	conn1 := dial(t, url)
	authenticate(t, conn1, testToken)
	conn2 := dial(t, url)
	authenticate(t, conn2, testToken)

	id1 := "client1-req-id-0000000000000000"
	id2 := "client2-req-id-0000000000000000"

	data1, _ := json.Marshal(frame{Type: "req", ID: id1, Text: "from client 1"})
	data2, _ := json.Marshal(frame{Type: "req", ID: id2, Text: "from client 2"})
	if err := conn1.WriteMessage(websocket.TextMessage, data1); err != nil {
		t.Fatalf("write req1: %v", err)
	}
	if err := conn2.WriteMessage(websocket.TextMessage, data2); err != nil {
		t.Fatalf("write req2: %v", err)
	}

	for i := 0; i < 2; i++ {
		select {
		case <-adapter.Incoming():
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for incoming")
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = adapter.Send(context.Background(), channel.OutgoingMessage{ThreadID: id2, Text: "reply to 2"})
	}()
	go func() {
		defer wg.Done()
		_ = adapter.Send(context.Background(), channel.OutgoingMessage{ThreadID: id1, Text: "reply to 1"})
	}()
	wg.Wait()

	f1 := readFrame(t, conn1)
	if f1.ID != id1 || f1.Text != "reply to 1" {
		t.Fatalf("client 1 got wrong reply: id=%s text=%q", f1.ID, f1.Text)
	}

	f2 := readFrame(t, conn2)
	if f2.ID != id2 || f2.Text != "reply to 2" {
		t.Fatalf("client 2 got wrong reply: id=%s text=%q", f2.ID, f2.Text)
	}
}

func TestAckFrameSendsAckField(t *testing.T) {
	adapter, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed: %s", f.Type)
	}

	reqID := "ack-test-id-0000000000000000000"
	data, _ := json.Marshal(frame{Type: "req", ID: reqID, Text: "hello"})
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write req: %v", err)
	}
	select {
	case <-adapter.Incoming():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	err := adapter.Send(context.Background(), channel.OutgoingMessage{
		ThreadID: reqID,
		Ack:      true,
	})
	if err != nil {
		t.Fatalf("send ack: %v", err)
	}

	resp := readFrame(t, conn)
	if resp.Type != "res" {
		t.Fatalf("expected res, got %s", resp.Type)
	}
	if !resp.Ack {
		t.Error("expected ack=true in frame")
	}
	if resp.Text != "" {
		t.Errorf("expected empty text for ack, got %q", resp.Text)
	}
	if resp.ID != reqID {
		t.Errorf("expected ID %s, got %s", reqID, resp.ID)
	}
}

func TestEventWithThreadID(t *testing.T) {
	adapter, url := startTestAdapter(t)
	conn := dial(t, url)
	f := authenticate(t, conn, testToken)
	if f.Type != "auth_ok" {
		t.Fatalf("auth failed: %s", f.Type)
	}

	// Allow the server goroutine to register the connection before broadcasting.
	time.Sleep(50 * time.Millisecond)

	err := adapter.Send(context.Background(), channel.OutgoingMessage{
		Text:      "follow-up reply",
		ThreadID:  "original-thread-id",
		EventType: "channel.followup",
	})
	if err != nil {
		t.Fatalf("send event: %v", err)
	}

	resp := readFrame(t, conn)
	if resp.Type != "event" {
		t.Fatalf("expected event frame, got %s", resp.Type)
	}
	if resp.Event != "channel.followup" {
		t.Errorf("expected event type channel.followup, got %s", resp.Event)
	}
	if resp.Thread != "original-thread-id" {
		t.Errorf("expected thread=original-thread-id, got %q", resp.Thread)
	}
	if resp.Text != "follow-up reply" {
		t.Errorf("expected text 'follow-up reply', got %q", resp.Text)
	}
}

var _ channel.Channel = (*Adapter)(nil)
