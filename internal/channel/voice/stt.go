package voice

import "context"

// Transcript represents a speech-to-text result from the STT provider.
type Transcript struct {
	Text       string
	IsFinal    bool
	Confidence float64
}

// StreamingSTT abstracts a streaming speech-to-text provider. Implementations
// accept raw audio bytes (Opus or PCM depending on configuration) and emit
// partial/final transcripts on a channel.
type StreamingSTT interface {
	Start(ctx context.Context) error
	WriteAudio(data []byte) error
	Transcripts() <-chan Transcript
	Close() error
}
