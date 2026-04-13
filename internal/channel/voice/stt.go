package voice

import "context"

// Transcript represents a speech-to-text result from the STT provider.
type Transcript struct {
	Text       string
	IsFinal    bool
	Confidence float64
}

// StreamingSTT abstracts a streaming speech-to-text provider. Implementations
// accept raw PCM audio and emit partial/final transcripts on a channel.
type StreamingSTT interface {
	Start(ctx context.Context) error
	WriteAudio(pcm []byte) error
	Transcripts() <-chan Transcript
	Close() error
}
