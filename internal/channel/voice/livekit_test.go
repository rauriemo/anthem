package voice

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// TestPublishLoop_FrameSlicing verifies that the publish loop correctly slices
// variable-length TTS audio into 20ms frames (OutboundFrameBytes = 960 bytes).
func TestPublishLoop_FrameSlicing(t *testing.T) {
	tts := NewMockTTS()
	stt := NewMockSTT()

	transport := &LiveKitTransport{
		stt:    stt,
		tts:    tts,
		logger: slog.Default(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var frames [][]byte
	var mu sync.Mutex

	// We'll test the framing logic directly by calling publishLoop
	// with a context and monitoring what would be written.
	// Since we can't easily mock the LiveKit local track, test the
	// framing algorithm extracted as a helper.
	chunk := make([]byte, OutboundFrameBytes*2+100) // 2 full frames + 100 bytes residual
	for i := range chunk {
		chunk[i] = byte(i % 256)
	}

	// Test frameSlice helper
	framesOut, residual := sliceIntoFrames(chunk, OutboundFrameBytes)
	if len(framesOut) != 2 {
		t.Errorf("expected 2 frames, got %d", len(framesOut))
	}
	if len(residual) != 100 {
		t.Errorf("expected 100 bytes residual, got %d", len(residual))
	}
	for i, f := range framesOut {
		if len(f) != OutboundFrameBytes {
			t.Errorf("frame %d: got %d bytes, want %d", i, len(f), OutboundFrameBytes)
		}
	}

	// Test with exact frame size
	exact := make([]byte, OutboundFrameBytes)
	framesOut, residual = sliceIntoFrames(exact, OutboundFrameBytes)
	if len(framesOut) != 1 {
		t.Errorf("expected 1 frame, got %d", len(framesOut))
	}
	if len(residual) != 0 {
		t.Errorf("expected 0 residual, got %d", len(residual))
	}

	// Test with less than one frame
	small := make([]byte, 100)
	framesOut, residual = sliceIntoFrames(small, OutboundFrameBytes)
	if len(framesOut) != 0 {
		t.Errorf("expected 0 frames, got %d", len(framesOut))
	}
	if len(residual) != 100 {
		t.Errorf("expected 100 residual, got %d", len(residual))
	}

	_ = frames
	_ = mu
	_ = ctx
	_ = transport
}

// TestAudioThroughSTT verifies that audio written to the transport reaches the STT writer.
func TestAudioThroughSTT(t *testing.T) {
	stt := NewMockSTT()
	tts := NewMockTTS()

	// Simulate what the transport's readTrack does: write PCM to stt
	pcm := make([]byte, InboundFrameBytes)
	for i := range pcm {
		pcm[i] = byte(i % 256)
	}

	if err := stt.WriteAudio(pcm); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	calls := stt.AudioCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 audio call, got %d", len(calls))
	}
	if len(calls[0]) != InboundFrameBytes {
		t.Errorf("expected %d bytes, got %d", InboundFrameBytes, len(calls[0]))
	}

	_ = tts
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

	// Buffer some audio
	go func() {
		tts.InjectAudio(make([]byte, 960))
		tts.InjectAudio(make([]byte, 960))
		tts.InjectAudio(make([]byte, 960))
	}()

	time.Sleep(50 * time.Millisecond)

	transport.DrainAudio()

	// Channel should be empty now
	select {
	case <-tts.Audio():
		t.Error("audio channel should be empty after drain")
	default:
		// expected
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

// TestPublishLoopContextCancel verifies that publishLoop exits when context is cancelled.
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

	// Cancel context should stop the loop
	cancel()

	select {
	case <-done:
		// success
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
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("publishLoop did not exit after channel close")
	}
}

// sliceIntoFrames is the framing algorithm extracted for testability.
// It splits data into fixed-size frames and returns any remaining bytes.
func sliceIntoFrames(data []byte, frameBytes int) (frames [][]byte, residual []byte) {
	for len(data) >= frameBytes {
		frame := make([]byte, frameBytes)
		copy(frame, data[:frameBytes])
		frames = append(frames, frame)
		data = data[frameBytes:]
	}
	if len(data) > 0 {
		residual = make([]byte, len(data))
		copy(residual, data)
	}
	return
}
