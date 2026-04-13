package voice

import (
	"encoding/json"
	"sync"
	"testing"
)

type mockSink struct {
	mu     sync.Mutex
	events []RoomEvent
}

func (s *mockSink) WriteEvent(ev RoomEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return nil
}

func TestRoomEventLog_Emit(t *testing.T) {
	t.Parallel()
	log := NewRoomEventLog(100, nil)

	ev := log.Emit(EvSpeechStart, "", "t1", nil)
	if ev.ID != "ev-1" {
		t.Errorf("id = %q, want ev-1", ev.ID)
	}
	if ev.Type != EvSpeechStart {
		t.Errorf("type = %q, want %q", ev.Type, EvSpeechStart)
	}
	if ev.Timestamp.IsZero() {
		t.Error("timestamp is zero")
	}
	if log.Len() != 1 {
		t.Errorf("len = %d, want 1", log.Len())
	}
}

func TestRoomEventLog_MonotonicIDs(t *testing.T) {
	t.Parallel()
	log := NewRoomEventLog(100, nil)

	ev1 := log.Emit(EvSpeechStart, "", "", nil)
	ev2 := log.Emit(EvFinalTranscript, "", "", nil)
	ev3 := log.Emit(EvResponseDone, "", "", nil)

	if ev1.ID >= ev2.ID || ev2.ID >= ev3.ID {
		t.Errorf("IDs not monotonic: %q, %q, %q", ev1.ID, ev2.ID, ev3.ID)
	}
}

func TestRoomEventLog_MonotonicTimestamps(t *testing.T) {
	t.Parallel()
	log := NewRoomEventLog(100, nil)

	ev1 := log.Emit(EvSpeechStart, "", "", nil)
	ev2 := log.Emit(EvFinalTranscript, "", "", nil)

	if ev2.Timestamp.Before(ev1.Timestamp) {
		t.Errorf("timestamps not monotonic: %v before %v", ev2.Timestamp, ev1.Timestamp)
	}
}

func TestRoomEventLog_Eviction(t *testing.T) {
	t.Parallel()
	log := NewRoomEventLog(3, nil)

	log.Emit(EvSpeechStart, "", "", nil)
	log.Emit(EvFinalTranscript, "", "", nil)
	log.Emit(EvTTSStarted, "", "", nil)
	log.Emit(EvResponseDone, "", "", nil) // should evict first

	if log.Len() != 3 {
		t.Errorf("len = %d, want 3", log.Len())
	}

	events := log.Events()
	if events[0].Type != EvFinalTranscript {
		t.Errorf("first event type = %q, want %q (earliest should have been evicted)",
			events[0].Type, EvFinalTranscript)
	}
}

func TestRoomEventLog_Clear(t *testing.T) {
	t.Parallel()
	log := NewRoomEventLog(100, nil)
	log.Emit(EvSpeechStart, "", "", nil)
	log.Clear()

	if log.Len() != 0 {
		t.Errorf("len after clear = %d, want 0", log.Len())
	}
}

func TestRoomEventLog_Sink(t *testing.T) {
	t.Parallel()
	sink := &mockSink{}
	log := NewRoomEventLog(100, sink)

	log.Emit(EvSpeechStart, "orch", "t1", map[string]any{"key": "val"})

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 1 {
		t.Fatalf("sink got %d events, want 1", len(sink.events))
	}
	if sink.events[0].AgentID != "orch" {
		t.Errorf("sink agent = %q, want orch", sink.events[0].AgentID)
	}
}

func TestRoomEventLog_EventsCopy(t *testing.T) {
	t.Parallel()
	log := NewRoomEventLog(100, nil)
	log.Emit(EvSpeechStart, "", "", nil)

	events := log.Events()
	events[0].AgentID = "mutated"

	original := log.Events()
	if original[0].AgentID == "mutated" {
		t.Error("Events() did not return a copy")
	}
}

func TestRoomEvent_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	log := NewRoomEventLog(100, nil)
	ev := log.Emit(EvFinalTranscript, "orch", "t1", map[string]any{
		"text":       "hello",
		"confidence": 0.95,
	})

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}

	var got RoomEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if got.ID != ev.ID {
		t.Errorf("id = %q, want %q", got.ID, ev.ID)
	}
	if got.Type != ev.Type {
		t.Errorf("type = %q, want %q", got.Type, ev.Type)
	}
	if got.AgentID != ev.AgentID {
		t.Errorf("agent = %q, want %q", got.AgentID, ev.AgentID)
	}
}
