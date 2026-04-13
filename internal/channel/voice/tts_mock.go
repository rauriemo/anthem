package voice

import (
	"context"
	"sync"
)

// MockTTS implements StreamingTTS for testing. It records all method calls
// and emits pre-scripted audio chunks.
type MockTTS struct {
	mu               sync.Mutex
	audio            chan []byte
	textCalls        []string
	flushCalls       int
	cancelCalls      int
	switchVoiceCalls []string
	started          bool
	closed           bool
	voiceID          string
	StartFunc        func(ctx context.Context, voiceID string) error
}

func NewMockTTS() *MockTTS {
	return &MockTTS{
		audio: make(chan []byte, 64),
	}
}

func (m *MockTTS) Start(ctx context.Context, voiceID string) error {
	if m.StartFunc != nil {
		return m.StartFunc(ctx, voiceID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = true
	m.voiceID = voiceID
	return nil
}

func (m *MockTTS) SwitchVoice(voiceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.switchVoiceCalls = append(m.switchVoiceCalls, voiceID)
	m.voiceID = voiceID
	return nil
}

func (m *MockTTS) WriteText(text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.textCalls = append(m.textCalls, text)
	return nil
}

func (m *MockTTS) Flush() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushCalls++
	return nil
}

func (m *MockTTS) Audio() <-chan []byte {
	return m.audio
}

func (m *MockTTS) Cancel() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelCalls++
	return nil
}

func (m *MockTTS) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// InjectAudio sends audio data into the channel for test simulation.
func (m *MockTTS) InjectAudio(data []byte) {
	m.audio <- data
}

// TextCalls returns all text passed to WriteText.
func (m *MockTTS) TextCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.textCalls))
	copy(out, m.textCalls)
	return out
}

// FlushCalls returns how many times Flush was called.
func (m *MockTTS) FlushCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.flushCalls
}

// CancelCalls returns how many times Cancel was called.
func (m *MockTTS) CancelCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cancelCalls
}

// Started reports whether Start was called.
func (m *MockTTS) Started() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started
}

// Closed reports whether Close was called.
func (m *MockTTS) Closed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// VoiceID returns the voice ID passed to Start or SwitchVoice.
func (m *MockTTS) VoiceID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.voiceID
}

// SwitchVoiceCalls returns voice IDs passed to SwitchVoice.
func (m *MockTTS) SwitchVoiceCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.switchVoiceCalls))
	copy(out, m.switchVoiceCalls)
	return out
}
