package channel

import (
	"context"
	"time"
)

type IncomingMessage struct {
	ChannelKind  string
	SenderID     string
	ThreadID     string
	Text         string
	Files        []File
	Timestamp    time.Time
	Raw          any
	ActiveGuests []string
	Mention      string
}

type File struct {
	Name     string
	Content  []byte
	MimeType string
}

type SuggestGuest struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type ActivateGuest struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// DeactivateGuest removes a guest from the active roster on the receiving
// channel. Emitted by PlanRunner when a step finishes (on GateApprove or
// GateAbort; retained on GateRevise because the guest is about to re-run)
// and by the orchestrator on the Plan->Execute handoff to clear any
// plan-time participants before PlanRunner takes over.
//
// Reason is a short operator-readable string ("step s1 complete",
// "plan approved, entering execute", "aborted at gate") surfaced by the
// client for logging or tooltips. The receiving client is expected to
// remove the guest chip from its active roster immediately; the guest
// remains discoverable in the guest index and can be re-invited later.
type DeactivateGuest struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type OutgoingMessage struct {
	Text            string           `json:"text,omitempty"`
	ThreadID        string           `json:"thread_id,omitempty"`
	Markdown        bool             `json:"markdown,omitempty"`
	EventType       string           `json:"event_type,omitempty"`
	Ack             bool             `json:"ack,omitempty"`
	Display         any              `json:"display,omitempty"`
	DisplayID       string           `json:"display_id,omitempty"`
	DisplayIDs      []string         `json:"display_ids,omitempty"`
	StreamDelta     string           `json:"stream_delta,omitempty"`
	StreamDone      bool             `json:"stream_done,omitempty"`
	StreamKind      string           `json:"stream_kind,omitempty"`
	GuestID         string           `json:"guest_id,omitempty"`
	SuggestGuest    *SuggestGuest    `json:"suggest_guest,omitempty"`
	ActivateGuest   *ActivateGuest   `json:"activate_guest,omitempty"`
	DeactivateGuest *DeactivateGuest `json:"deactivate_guest,omitempty"`
	CurrentMode     string           `json:"current_mode,omitempty"`
}

type Channel interface {
	Kind() string
	Start(ctx context.Context) error
	Send(ctx context.Context, msg OutgoingMessage) error
	Incoming() <-chan IncomingMessage
	Close() error
}
