package voice

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestAdapter_MissingAnthropicKeyFallback(t *testing.T) {
	t.Parallel()

	stt := NewMockSTT()
	tts := NewMockTTS()

	a := NewAdapterWithOpts(AdapterOpts{
		STT:    stt,
		TTS:    tts,
		Logger: slog.Default(),
	})

	if a.fastLLM != nil {
		t.Fatal("fastLLM should be nil when no API key is provided")
	}
	if a.cmdBridge != nil {
		t.Fatal("cmdBridge should be nil when fastLLM is nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}

	stt.InjectTranscript(Transcript{Text: "Should reach orchestrator", IsFinal: true, Confidence: 0.9})

	select {
	case msg := <-a.Incoming():
		if msg.Text != "Should reach orchestrator" {
			t.Errorf("text = %q", msg.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("transcript should reach incoming channel when fastLLM is nil")
	}
}
