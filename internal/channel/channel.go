package channel

import (
	"context"
	"time"
)

type IncomingMessage struct {
	ChannelKind string
	SenderID    string
	ThreadID    string
	Text        string
	Files       []File
	Timestamp   time.Time
	Raw         any
}

type File struct {
	Name     string
	Content  []byte
	MimeType string
}

type OutgoingMessage struct {
	Text     string
	ThreadID string
	Markdown bool
}

type Channel interface {
	Kind() string
	Start(ctx context.Context) error
	Send(ctx context.Context, msg OutgoingMessage) error
	Incoming() <-chan IncomingMessage
	Close() error
}
