package voice

import "context"

// StreamingTTS abstracts a streaming text-to-speech provider. Text is written
// incrementally and synthesized audio chunks are read from the Audio channel.
type StreamingTTS interface {
	Start(ctx context.Context, voiceID string) error
	WriteText(text string) error
	Flush() error
	Audio() <-chan []byte
	Cancel() error
	Close() error
}
