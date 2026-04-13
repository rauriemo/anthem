package voice

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// RoomEventType identifies the kind of voice room event.
type RoomEventType string

const (
	// Phase 1 events (orchestrator mode)
	EvSpeechStart      RoomEventType = "speech_start"
	EvPartialTranscript RoomEventType = "partial_transcript"
	EvFinalTranscript  RoomEventType = "final_transcript"
	EvTTSStarted       RoomEventType = "tts_started"
	EvTTSInterrupted   RoomEventType = "tts_interrupted"
	EvFloorTransition  RoomEventType = "floor_transition"
	EvBargeIn          RoomEventType = "barge_in"
	EvResponseDone     RoomEventType = "response_done"

	// Phase 2 events (tool observability)
	EvToolCall   RoomEventType = "tool_call"
	EvToolResult RoomEventType = "tool_result"

	// Phase 3 events (multi-agent mode)
	EvSpeakerSelected  RoomEventType = "speaker_selected"
	EvAckEmitted       RoomEventType = "ack_emitted"
	EvHandoffRequested RoomEventType = "handoff_requested"
	EvHandoffCommitted RoomEventType = "handoff_committed"
)

// RoomEvent is a structured record of a voice room interaction.
type RoomEvent struct {
	ID        string         `json:"id"`
	Timestamp time.Time      `json:"timestamp"`
	Type      RoomEventType  `json:"type"`
	AgentID   string         `json:"agent_id,omitempty"`
	ThreadID  string         `json:"thread_id,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// MarshalJSON implements json.Marshaler.
func (e RoomEvent) MarshalJSON() ([]byte, error) {
	type Alias RoomEvent
	return json.Marshal((*Alias)(&e))
}

// UnmarshalJSON implements json.Unmarshaler.
func (e *RoomEvent) UnmarshalJSON(data []byte) error {
	type Alias RoomEvent
	return json.Unmarshal(data, (*Alias)(e))
}

// EventSink receives room events for persistent storage (e.g. Anthem audit DB).
type EventSink interface {
	WriteEvent(event RoomEvent) error
}

// RoomEventLog is an in-memory ring buffer of voice room events with optional
// persistence to an EventSink.
type RoomEventLog struct {
	mu       sync.Mutex
	events   []RoomEvent
	capacity int
	counter  int
	sink     EventSink
}

// NewRoomEventLog creates an event log with the given hot-buffer capacity.
// If sink is non-nil, every event is also written to persistent storage.
func NewRoomEventLog(capacity int, sink EventSink) *RoomEventLog {
	if capacity <= 0 {
		capacity = 1024
	}
	return &RoomEventLog{
		events:   make([]RoomEvent, 0, capacity),
		capacity: capacity,
		sink:     sink,
	}
}

// Emit records an event in the log.
func (l *RoomEventLog) Emit(evType RoomEventType, agentID, threadID string, data map[string]any) RoomEvent {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.counter++
	ev := RoomEvent{
		ID:        fmt.Sprintf("ev-%d", l.counter),
		Timestamp: time.Now(),
		Type:      evType,
		AgentID:   agentID,
		ThreadID:  threadID,
		Data:      data,
	}

	if len(l.events) >= l.capacity {
		l.events = l.events[1:]
	}
	l.events = append(l.events, ev)

	if l.sink != nil {
		_ = l.sink.WriteEvent(ev)
	}

	return ev
}

// Events returns a copy of all events currently in the buffer.
func (l *RoomEventLog) Events() []RoomEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]RoomEvent, len(l.events))
	copy(out, l.events)
	return out
}

// Len returns the number of events in the buffer.
func (l *RoomEventLog) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.events)
}

// Clear removes all events from the buffer.
func (l *RoomEventLog) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = l.events[:0]
	l.counter = 0
}
