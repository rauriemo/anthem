package voice

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/rauriemo/anthem/internal/channel"
)

const orchestratorAgentID = "orchestrator"

// Adapter implements channel.Channel as a voice gateway. It bridges LiveKit
// audio transport, streaming STT/TTS providers, and a floor-control state
// machine into Anthem's channel infrastructure.
type Adapter struct {
	stt       StreamingSTT
	tts       StreamingTTS
	transport *LiveKitTransport // nil when running without LiveKit (e.g. tests)
	floor     *FloorController
	log       *RoomEventLog
	logger    *slog.Logger

	incoming chan channel.IncomingMessage
	sentBuf  *SentenceBuffer
	voiceID  string // ElevenLabs voice_id for the orchestrator

	mu       sync.Mutex
	cancel   context.CancelFunc
	started  bool
	closed   bool
	threadID string // guarded by mu
}

// NewAdapter creates a voice channel adapter. The STT and TTS providers are
// injected to allow testing with mocks and swapping providers at runtime.
// The transport parameter is optional -- pass nil for unit tests or when
// LiveKit is not configured. orchestratorVoiceID is the ElevenLabs voice ID
// for the orchestrator agent (read from agent frontmatter).
func NewAdapter(stt StreamingSTT, tts StreamingTTS, transport *LiveKitTransport, orchestratorVoiceID string, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	if orchestratorVoiceID == "" {
		orchestratorVoiceID = orchestratorAgentID
	}
	floor := NewFloorController("orchestrator")
	eventLog := NewRoomEventLog(1024, nil)

	a := &Adapter{
		stt:       stt,
		tts:       tts,
		transport: transport,
		floor:     floor,
		log:       eventLog,
		logger:    logger,
		incoming:  make(chan channel.IncomingMessage, 64),
		voiceID:   orchestratorVoiceID,
	}
	a.sentBuf = NewSentenceBuffer(a.onSentence)
	a.sentBuf.EagerMode = true
	return a
}

// Kind returns "voice" to identify this channel type.
func (a *Adapter) Kind() string { return "voice" }

func (a *Adapter) getThreadID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.threadID
}

func (a *Adapter) setThreadID(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.threadID = id
}

// Floor returns the floor controller for external inspection (e.g. tests, Prism events).
func (a *Adapter) Floor() *FloorController { return a.floor }

// EventLog returns the room event log.
func (a *Adapter) EventLog() *RoomEventLog { return a.log }

// Start initializes the STT and TTS providers and begins the transcription
// listen loop. The provided context controls the adapter's lifetime.
func (a *Adapter) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return fmt.Errorf("voice adapter already started")
	}
	a.started = true
	ctx, a.cancel = context.WithCancel(ctx)
	a.mu.Unlock()

	if err := a.stt.Start(ctx); err != nil {
		return fmt.Errorf("voice adapter: starting STT: %w", err)
	}
	if err := a.tts.Start(ctx, a.voiceID); err != nil {
		_ = a.stt.Close()
		return fmt.Errorf("voice adapter: starting TTS: %w", err)
	}

	if a.transport != nil {
		if err := a.transport.Start(ctx); err != nil {
			_ = a.stt.Close()
			_ = a.tts.Close()
			return fmt.Errorf("voice adapter: starting transport: %w", err)
		}
	}

	go a.sttLoop(ctx)
	go a.floorEventLoop(ctx)

	a.logger.Info("voice adapter started")
	return nil
}

// Send handles outgoing messages from the orchestrator. StreamDelta text from
// the orchestrator is buffered into sentences and piped to TTS. Guest deltas
// are silently ignored in orchestrator mode.
func (a *Adapter) Send(_ context.Context, msg channel.OutgoingMessage) error {
	if msg.StreamDelta != "" {
		return a.handleStreamDelta(msg)
	}
	if msg.StreamDone {
		return a.handleStreamDone(msg)
	}
	return nil
}

func (a *Adapter) handleStreamDelta(msg channel.OutgoingMessage) error {
	if msg.GuestID != "" && msg.GuestID != orchestratorAgentID {
		return nil
	}

	state := a.floor.State()
	if state == BargeInAbort {
		return nil
	}

	if state == CommitPending || state == Idle {
		if err := a.floor.Transition(OrchestratorSpeaking, "tts_start"); err != nil {
			a.logger.Debug("floor transition to speaking failed, queueing delta",
				"state", state.String(), "error", err)
			return nil
		}
		a.log.Emit(EvTTSStarted, orchestratorAgentID, a.getThreadID(), nil)
	}

	a.sentBuf.Write(msg.StreamDelta)
	return nil
}

func (a *Adapter) handleStreamDone(msg channel.OutgoingMessage) error {
	if msg.GuestID != "" && msg.GuestID != orchestratorAgentID {
		return nil
	}

	a.sentBuf.Flush()

	if err := a.tts.Flush(); err != nil {
		a.logger.Warn("TTS flush failed", "error", err)
	}

	state := a.floor.State()
	if state == OrchestratorSpeaking {
		if err := a.floor.Transition(Idle, "response_done"); err != nil {
			a.logger.Warn("floor transition to idle failed", "error", err)
		}
		a.log.Emit(EvResponseDone, orchestratorAgentID, a.getThreadID(), nil)
	}

	return nil
}

// Incoming returns the channel that receives transcribed user messages.
func (a *Adapter) Incoming() <-chan channel.IncomingMessage {
	return a.incoming
}

// Close shuts down the voice adapter, cleaning up STT, TTS, and floor state.
func (a *Adapter) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	a.mu.Unlock()

	if a.cancel != nil {
		a.cancel()
	}

	if a.transport != nil {
		_ = a.transport.Close()
	}

	var sttErr, ttsErr error
	sttErr = a.stt.Close()
	ttsErr = a.tts.Close()
	close(a.incoming)

	a.floor.forceState(Idle)
	a.logger.Info("voice adapter closed")

	if sttErr != nil {
		return fmt.Errorf("voice adapter: closing STT: %w", sttErr)
	}
	if ttsErr != nil {
		return fmt.Errorf("voice adapter: closing TTS: %w", ttsErr)
	}
	return nil
}

// BargeIn triggers an immediate interruption of the orchestrator's speech.
func (a *Adapter) BargeIn() error {
	state := a.floor.State()
	if state != OrchestratorSpeaking {
		return nil
	}

	if err := a.floor.Transition(BargeInAbort, "vad_onset"); err != nil {
		return fmt.Errorf("barge-in transition failed: %w", err)
	}

	a.log.Emit(EvBargeIn, "", a.getThreadID(), map[string]any{
		"partial_response": a.sentBuf.Buffered(),
	})

	if err := a.tts.Cancel(); err != nil {
		a.logger.Warn("TTS cancel on barge-in failed", "error", err)
	}
	a.sentBuf.Reset()

	if err := a.floor.Transition(UserSpeaking, "abort_complete"); err != nil {
		return fmt.Errorf("barge-in recovery transition failed: %w", err)
	}

	return nil
}

func (a *Adapter) onSentence(sentence string) {
	if err := a.tts.WriteText(sentence); err != nil {
		a.logger.Warn("TTS write failed", "error", err, "text_len", len(sentence))
	}
}

func (a *Adapter) sttLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-a.stt.Transcripts():
			if !ok {
				return
			}
			a.handleTranscript(t)
		}
	}
}

func (a *Adapter) handleTranscript(t Transcript) {
	if !t.IsFinal {
		a.log.Emit(EvPartialTranscript, "", a.getThreadID(), map[string]any{
			"text":       t.Text,
			"confidence": t.Confidence,
		})
		return
	}

	if t.Confidence > 0 && t.Confidence < 0.3 {
		a.logger.Warn("low confidence transcript, discarding", "confidence", t.Confidence, "text", t.Text)
		state := a.floor.State()
		if state == UserSpeaking {
			_ = a.floor.Transition(Idle, "vad_false_positive")
		}
		return
	}

	a.log.Emit(EvFinalTranscript, "", a.getThreadID(), map[string]any{
		"text":       t.Text,
		"confidence": t.Confidence,
	})

	state := a.floor.State()
	if state == OrchestratorSpeaking {
		if err := a.BargeIn(); err != nil {
			a.logger.Warn("barge-in failed during transcript", "error", err)
		}
	}

	// Re-read state after potential barge-in which transitions
	// OrchestratorSpeaking -> BargeInAbort -> UserSpeaking.
	state = a.floor.State()
	if state == Idle {
		_ = a.floor.Transition(UserSpeaking, "vad_onset")
		a.log.Emit(EvSpeechStart, "", "", nil)
	}

	_ = a.floor.Transition(CommitPending, "silence_detected")

	threadID := fmt.Sprintf("voice-%d", time.Now().UnixMilli())
	a.setThreadID(threadID)

	msg := channel.IncomingMessage{
		ChannelKind: "voice",
		SenderID:    "user",
		ThreadID:    threadID,
		Text:        t.Text,
		Timestamp:   time.Now(),
	}

	select {
	case a.incoming <- msg:
	default:
		a.logger.Warn("dropping voice incoming message, buffer full")
	}
}

func (a *Adapter) floorEventLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-a.floor.Events():
			if !ok {
				return
			}
			a.log.Emit(EvFloorTransition, ev.AgentID, a.getThreadID(), map[string]any{
				"from_state": ev.FromState.String(),
				"to_state":   ev.ToState.String(),
				"reason":     ev.Reason,
			})
		}
	}
}
