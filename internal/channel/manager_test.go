package channel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type mockChannel struct {
	kind     string
	incoming chan IncomingMessage
	sent     []OutgoingMessage
	started  bool
	closed   bool
	sendErr  error
	closeErr error
	mu       sync.Mutex
}

func newMockChannel(kind string) *mockChannel {
	return &mockChannel{
		kind:     kind,
		incoming: make(chan IncomingMessage, 16),
	}
}

func (m *mockChannel) Kind() string { return m.kind }

func (m *mockChannel) Start(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = true
	return nil
}

func (m *mockChannel) Send(_ context.Context, msg OutgoingMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sent = append(m.sent, msg)
	return nil
}

func (m *mockChannel) Incoming() <-chan IncomingMessage {
	return m.incoming
}

func (m *mockChannel) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	close(m.incoming)
	return m.closeErr
}

func (m *mockChannel) sentMessages() []OutgoingMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]OutgoingMessage, len(m.sent))
	copy(cp, m.sent)
	return cp
}

func TestRegisterAndStart(t *testing.T) {
	mgr := NewManager(nil)
	ch1 := newMockChannel("slack")
	ch2 := newMockChannel("discord")

	mgr.Register(ch1)
	mgr.Register(ch2)

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if !ch1.started {
		t.Error("ch1 should be started")
	}
	if !ch2.started {
		t.Error("ch2 should be started")
	}
}

func TestBroadcast(t *testing.T) {
	mgr := NewManager(nil)
	ch1 := newMockChannel("slack")
	ch2 := newMockChannel("discord")

	mgr.Register(ch1)
	mgr.Register(ch2)

	msg := OutgoingMessage{Text: "hello", Markdown: true}
	if err := mgr.Broadcast(context.Background(), msg); err != nil {
		t.Fatalf("Broadcast() error: %v", err)
	}

	sent1 := ch1.sentMessages()
	sent2 := ch2.sentMessages()
	if len(sent1) != 1 || sent1[0].Text != "hello" {
		t.Errorf("ch1 sent = %v, want [{hello}]", sent1)
	}
	if len(sent2) != 1 || sent2[0].Text != "hello" {
		t.Errorf("ch2 sent = %v, want [{hello}]", sent2)
	}
}

func TestIncomingMerge(t *testing.T) {
	mgr := NewManager(nil)
	ch1 := newMockChannel("slack")
	ch2 := newMockChannel("discord")

	mgr.Register(ch1)
	mgr.Register(ch2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	ch1.incoming <- IncomingMessage{ChannelKind: "slack", Text: "from slack"}
	ch2.incoming <- IncomingMessage{ChannelKind: "discord", Text: "from discord"}

	received := make(map[string]bool)
	timeout := time.After(time.Second)
	for i := 0; i < 2; i++ {
		select {
		case msg := <-mgr.Incoming():
			received[msg.Text] = true
		case <-timeout:
			t.Fatal("timed out waiting for merged messages")
		}
	}

	if !received["from slack"] {
		t.Error("missing message from slack")
	}
	if !received["from discord"] {
		t.Error("missing message from discord")
	}
}

func TestCloseAll(t *testing.T) {
	mgr := NewManager(nil)
	ch1 := newMockChannel("slack")
	ch2 := newMockChannel("discord")
	ch2.closeErr = errors.New("discord close failed")

	mgr.Register(ch1)
	mgr.Register(ch2)

	err := mgr.Close()
	if err == nil {
		t.Fatal("expected error from Close")
	}
	if !ch1.closed {
		t.Error("ch1 should be closed")
	}
	if !ch2.closed {
		t.Error("ch2 should be closed")
	}
}

func TestBroadcastPartialFailure(t *testing.T) {
	mgr := NewManager(nil)
	ch1 := newMockChannel("slack")
	ch2 := newMockChannel("discord")
	ch2.sendErr = errors.New("discord send failed")

	mgr.Register(ch1)
	mgr.Register(ch2)

	msg := OutgoingMessage{Text: "test"}
	err := mgr.Broadcast(context.Background(), msg)

	if err == nil {
		t.Fatal("expected error from Broadcast")
	}

	sent1 := ch1.sentMessages()
	if len(sent1) != 1 {
		t.Errorf("ch1 should still receive message, got %d", len(sent1))
	}
}
