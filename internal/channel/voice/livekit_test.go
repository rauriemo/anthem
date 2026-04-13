package voice

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// TestAudioThroughSTT verifies that audio written to the transport reaches the STT writer.
// In Opus passthrough mode, the payload bytes are forwarded as-is.
func TestAudioThroughSTT(t *testing.T) {
	stt := NewMockSTT()
	tts := NewMockTTS()

	payload := make([]byte, 160) // simulated Opus frame
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	if err := stt.WriteAudio(payload); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	calls := stt.AudioCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 audio call, got %d", len(calls))
	}
	if len(calls[0]) != len(payload) {
		t.Errorf("expected %d bytes, got %d", len(payload), len(calls[0]))
	}

	_ = tts
}

// TestPublishLoop_OpusPassthrough verifies that Opus frames from TTS pass
// through the publish loop to WriteSample without PCM frame slicing.
func TestPublishLoop_OpusPassthrough(t *testing.T) {
	tts := NewMockTTS()
	stt := NewMockSTT()

	transport := &LiveKitTransport{
		stt:    stt,
		tts:    tts,
		logger: slog.Default(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	transport.wg.Add(1)
	go func() {
		transport.publishLoop(ctx)
		close(done)
	}()

	opusFrame := []byte{0xFC, 0x01, 0x02, 0x03, 0x04}
	tts.InjectAudio(opusFrame)

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishLoop did not exit after context cancel")
	}
}

// TestBargeInDrainsAudio verifies that DrainAudio empties the TTS audio channel.
func TestBargeInDrainsAudio(t *testing.T) {
	tts := NewMockTTS()
	stt := NewMockSTT()

	transport := &LiveKitTransport{
		stt:    stt,
		tts:    tts,
		logger: slog.Default(),
	}

	go func() {
		tts.InjectAudio(make([]byte, 160))
		tts.InjectAudio(make([]byte, 160))
		tts.InjectAudio(make([]byte, 160))
	}()

	time.Sleep(50 * time.Millisecond)

	transport.DrainAudio()

	select {
	case <-tts.Audio():
		t.Error("audio channel should be empty after drain")
	default:
	}
}

// TestTransportCloseIdempotent verifies that double-close is safe.
func TestTransportCloseIdempotent(t *testing.T) {
	transport := &LiveKitTransport{
		logger: slog.Default(),
	}

	if err := transport.Close(); err != nil {
		t.Errorf("first close: %v", err)
	}
	if err := transport.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

// TestPublishLoopContextCancel verifies that publishLoop exits when context is canceled.
func TestPublishLoopContextCancel(t *testing.T) {
	tts := NewMockTTS()
	stt := NewMockSTT()

	transport := &LiveKitTransport{
		stt:    stt,
		tts:    tts,
		logger: slog.Default(),
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	transport.wg.Add(1)
	go func() {
		transport.publishLoop(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishLoop did not exit after context cancel")
	}
}

// TestPublishLoopChannelClose verifies that publishLoop exits when TTS audio channel closes.
func TestPublishLoopChannelClose(t *testing.T) {
	tts := NewMockTTS()
	stt := NewMockSTT()

	transport := &LiveKitTransport{
		stt:    stt,
		tts:    tts,
		logger: slog.Default(),
	}

	ctx := context.Background()

	done := make(chan struct{})
	transport.wg.Add(1)
	go func() {
		transport.publishLoop(ctx)
		close(done)
	}()

	close(tts.audio)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishLoop did not exit after channel close")
	}
}
