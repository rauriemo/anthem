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

type OutgoingMessage struct {
	Text          string         `json:"text,omitempty"`
	ThreadID      string         `json:"thread_id,omitempty"`
	Markdown      bool           `json:"markdown,omitempty"`
	EventType     string         `json:"event_type,omitempty"`
	Ack           bool           `json:"ack,omitempty"`
	Display       any            `json:"display,omitempty"`
	DisplayID     string         `json:"display_id,omitempty"`
	DisplayIDs    []string       `json:"display_ids,omitempty"`
	StreamDelta   string         `json:"stream_delta,omitempty"`
	StreamDone    bool           `json:"stream_done,omitempty"`
	GuestID       string         `json:"guest_id,omitempty"`
	SuggestGuest  *SuggestGuest  `json:"suggest_guest,omitempty"`
	ActivateGuest *ActivateGuest `json:"activate_guest,omitempty"`
}

type Channel interface {
	Kind() string
	Start(ctx context.Context) error
	Send(ctx context.Context, msg OutgoingMessage) error
	Incoming() <-chan IncomingMessage
	Close() error
}
