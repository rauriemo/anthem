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

	incoming     chan channel.IncomingMessage
	sentBuf      *SentenceBuffer
	voiceID      string            // ElevenLabs voice_id for the orchestrator
	voiceConfigs map[string]string // agent slug -> ElevenLabs voice_id

	fastLLM   *FastLLM       // nil -> fallback to orchestrator via incoming channel
	cmdBridge *CommandBridge // nil when fastLLM is nil

	mu        sync.Mutex
	cancel    context.CancelFunc
	started   bool
	closed    bool
	threadID  string             // guarded by mu
	llmCancel context.CancelFunc // cancels in-flight fastLLM request
}

// AdapterOpts configures a new Adapter.
type AdapterOpts struct {
	STT                 StreamingSTT
	TTS                 StreamingTTS
	Transport           *LiveKitTransport // nil for tests / no LiveKit
	OrchestratorVoiceID string
	VoiceConfigs        map[string]string // agent slug -> ElevenLabs voice_id
	FastLLM             *FastLLM          // nil -> fallback to orchestrator path
	OnCommand           CommandCallback   // optional; invoked on tool_use commands
	Logger              *slog.Logger
}

// NewAdapter creates a voice channel adapter. The STT and TTS providers are
// injected to allow testing with mocks and swapping providers at runtime.
// The transport parameter is optional -- pass nil for unit tests or when
// LiveKit is not configured. orchestratorVoiceID is the ElevenLabs voice ID
// for the orchestrator agent (read from agent frontmatter).
func NewAdapter(stt StreamingSTT, tts StreamingTTS, transport *LiveKitTransport, orchestratorVoiceID string, logger *slog.Logger) *Adapter {
	return NewAdapterWithOpts(AdapterOpts{
		STT:                 stt,
		TTS:                 tts,
		Transport:           transport,
		OrchestratorVoiceID: orchestratorVoiceID,
		Logger:              logger,
	})
}

// NewAdapterWithOpts creates a voice channel adapter with full configuration
// including optional FastLLM for direct Anthropic API voice responses.
func NewAdapterWithOpts(opts AdapterOpts) *Adapter {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.OrchestratorVoiceID == "" {
		opts.OrchestratorVoiceID = orchestratorAgentID
	}
	floor := NewFloorController("orchestrator")
	eventLog := NewRoomEventLog(1024, nil)

	vc := opts.VoiceConfigs
	if vc == nil {
		vc = make(map[string]string)
	}
	if opts.OrchestratorVoiceID != "" {
		if _, exists := vc[orchestratorAgentID]; !exists {
			vc[orchestratorAgentID] = opts.OrchestratorVoiceID
		}
	}

	a := &Adapter{
		stt:          opts.STT,
		tts:          opts.TTS,
		transport:    opts.Transport,
		floor:        floor,
		log:          eventLog,
		logger:       opts.Logger,
		incoming:     make(chan channel.IncomingMessage, 64),
		voiceID:      opts.OrchestratorVoiceID,
		voiceConfigs: vc,
		fastLLM:      opts.FastLLM,
	}
	a.sentBuf = NewSentenceBuffer(a.onSentence)
	a.sentBuf.EagerMode = true

	if a.fastLLM != nil {
		a.cmdBridge = NewCommandBridge(opts.OnCommand, a.sentBuf, a.incoming, opts.Logger)
	}
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

// switchVoiceForSpeaker switches the TTS voice to match the agent producing
// the current stream. If the agent has no configured voice_id the current
// voice stays active (acceptable fallback).
func (a *Adapter) switchVoiceForSpeaker(speakerID string) {
	if voiceID, ok := a.voiceConfigs[speakerID]; ok && voiceID != "" {
		if err := a.tts.SwitchVoice(voiceID); err != nil {
			a.logger.Warn("TTS voice switch failed", "agent", speakerID, "error", err)
		}
	}
}

// Send handles outgoing messages. StreamDelta text is buffered into sentences
// and piped to TTS. When a guest has a configured voice_id the TTS voice is
// switched before speaking.
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
	speakerID := msg.GuestID
	if speakerID == "" {
		speakerID = orchestratorAgentID
	}

	state := a.floor.State()
	if state == BargeInAbort {
		return nil
	}

	if state == CommitPending || state == Idle {
		a.switchVoiceForSpeaker(speakerID)
		if err := a.floor.Transition(OrchestratorSpeaking, "tts_start"); err != nil {
			a.logger.Debug("floor transition to speaking failed, queueing delta",
				"state", state.String(), "error", err)
			return nil
		}
		a.log.Emit(EvTTSStarted, speakerID, a.getThreadID(), nil)
	}

	a.sentBuf.Write(msg.StreamDelta)
	return nil
}

func (a *Adapter) handleStreamDone(msg channel.OutgoingMessage) error {
	speakerID := msg.GuestID
	if speakerID == "" {
		speakerID = orchestratorAgentID
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
		a.log.Emit(EvResponseDone, speakerID, a.getThreadID(), nil)
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

	// Cancel any in-flight FastLLM request (barge-in or new turn).
	a.cancelLLM()

	state := a.floor.State()
	if state == OrchestratorSpeaking {
		if err := a.BargeIn(); err != nil {
			a.logger.Warn("barge-in failed during transcript", "error", err)
		}
	}

	state = a.floor.State()
	if state == Idle {
		_ = a.floor.Transition(UserSpeaking, "vad_onset")
		a.log.Emit(EvSpeechStart, "", "", nil)
	}

	_ = a.floor.Transition(CommitPending, "silence_detected")

	threadID := fmt.Sprintf("voice-%d", time.Now().UnixMilli())
	a.setThreadID(threadID)

	if a.fastLLM != nil {
		go a.handleFastLLM(t.Text, threadID)
		return
	}

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

func (a *Adapter) cancelLLM() {
	a.mu.Lock()
	if a.llmCancel != nil {
		a.llmCancel()
		a.llmCancel = nil
	}
	a.mu.Unlock()
}

// handleFastLLM calls the Anthropic API directly and pipes text deltas to TTS.
func (a *Adapter) handleFastLLM(text, threadID string) {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.llmCancel = cancel
	a.mu.Unlock()

	defer cancel()

	events, err := a.fastLLM.StreamChat(ctx, text)
	if err != nil {
		a.logger.Warn("fastllm: stream failed, falling back to orchestrator", "error", err)
		a.fallbackToOrchestrator(text, threadID)
		return
	}

	spoke := false
	for ev := range events {
		if ctx.Err() != nil {
			return
		}

		if ev.TextDelta != "" {
			if !spoke {
				spoke = true
				state := a.floor.State()
				if state == CommitPending || state == Idle {
					if err := a.floor.Transition(OrchestratorSpeaking, "tts_start"); err != nil {
						a.logger.Debug("floor transition to speaking failed", "error", err)
					} else {
						a.log.Emit(EvTTSStarted, orchestratorAgentID, threadID, nil)
					}
				}
			}
			a.sentBuf.Write(ev.TextDelta)
		}

		if ev.ToolUse != nil && a.cmdBridge != nil {
			a.cmdBridge.HandleToolCall(*ev.ToolUse)
		}

		if ev.Done {
			a.sentBuf.Flush()
			if err := a.tts.Flush(); err != nil {
				a.logger.Warn("TTS flush failed", "error", err)
			}
			if spoke {
				state := a.floor.State()
				if state == OrchestratorSpeaking {
					_ = a.floor.Transition(Idle, "response_done")
					a.log.Emit(EvResponseDone, orchestratorAgentID, threadID, nil)
				}
			}
		}
	}
}

func (a *Adapter) fallbackToOrchestrator(text, threadID string) {
	msg := channel.IncomingMessage{
		ChannelKind: "voice",
		SenderID:    "user",
		ThreadID:    threadID,
		Text:        text,
		Timestamp:   time.Now(),
	}
	select {
	case a.incoming <- msg:
	default:
		a.logger.Warn("dropping voice fallback message, buffer full")
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
