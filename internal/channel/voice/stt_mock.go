package voice

import (
	"context"
	"sync"
)

// MockSTT implements StreamingSTT for testing. It records WriteAudio calls
// and emits pre-scripted transcripts.
type MockSTT struct {
	mu          sync.Mutex
	transcripts chan Transcript
	audioCalls  [][]byte
	started     bool
	closed      bool
	StartFunc   func(ctx context.Context) error
}

func NewMockSTT() *MockSTT {
	return &MockSTT{
		transcripts: make(chan Transcript, 64),
	}
}

func (m *MockSTT) Start(ctx context.Context) error {
	if m.StartFunc != nil {
		return m.StartFunc(ctx)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = true
	return nil
}

func (m *MockSTT) WriteAudio(pcm []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(pcm))
	copy(cp, pcm)
	m.audioCalls = append(m.audioCalls, cp)
	return nil
}

func (m *MockSTT) Transcripts() <-chan Transcript {
	return m.transcripts
}

func (m *MockSTT) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// InjectTranscript sends a transcript into the channel for test simulation.
func (m *MockSTT) InjectTranscript(t Transcript) {
	m.transcripts <- t
}

// AudioCalls returns a copy of all pcm byte slices passed to WriteAudio.
func (m *MockSTT) AudioCalls() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]byte, len(m.audioCalls))
	copy(out, m.audioCalls)
	return out
}

// Started reports whether Start was called.
func (m *MockSTT) Started() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started
}

// Closed reports whether Close was called.
func (m *MockSTT) Closed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}
