package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	elevenLabsBaseURL = "wss://api.elevenlabs.io/v1/text-to-speech"
	elevenLabsModel   = "eleven_flash_v2_5"
	elevenLabsFormat  = "opus_48000_64"
)

// ElevenLabsTTS implements StreamingTTS using ElevenLabs' WebSocket stream-input API.
// Outputs Opus at 48kHz/64kbps for direct LiveKit publishing without transcoding.
type ElevenLabsTTS struct {
	apiKey string
	logger *slog.Logger

	mu      sync.Mutex
	conn    *websocket.Conn
	audio   chan []byte
	voiceID string
	started bool
	closed  bool
	cancel  context.CancelFunc
	ctx     context.Context
}

func NewElevenLabsTTS(apiKey string, logger *slog.Logger) *ElevenLabsTTS {
	if logger == nil {
		logger = slog.Default()
	}
	return &ElevenLabsTTS{
		apiKey: apiKey,
		logger: logger,
		audio:  make(chan []byte, 64),
	}
}

func (e *ElevenLabsTTS) Start(ctx context.Context, voiceID string) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return fmt.Errorf("elevenlabs TTS already started")
	}
	e.started = true
	e.voiceID = voiceID
	e.mu.Unlock()

	e.ctx, e.cancel = context.WithCancel(ctx)

	if err := e.connect(); err != nil {
		return err
	}

	e.logger.Info("elevenlabs TTS started", "voice_id", voiceID, "model", elevenLabsModel)
	return nil
}

func (e *ElevenLabsTTS) connect() error {
	wsURL := fmt.Sprintf("%s/%s/stream-input?model_id=%s&output_format=%s",
		elevenLabsBaseURL, e.voiceID, elevenLabsModel, elevenLabsFormat)

	header := make(map[string][]string)
	header["xi-api-key"] = []string{e.apiKey}

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(e.ctx, wsURL, header)
	if err != nil {
		return fmt.Errorf("elevenlabs TTS: websocket dial: %w", err)
	}

	initMsg := map[string]any{
		"text": " ",
		"voice_settings": map[string]any{
			"stability":        0.5,
			"similarity_boost": 0.75,
		},
	}
	initBytes, _ := json.Marshal(initMsg)
	if err := conn.WriteMessage(websocket.TextMessage, initBytes); err != nil {
		conn.Close()
		return fmt.Errorf("elevenlabs TTS: init message: %w", err)
	}

	e.mu.Lock()
	e.conn = conn
	e.mu.Unlock()

	go e.readLoop()

	return nil
}

// SwitchVoice changes the active ElevenLabs voice by reconnecting the
// WebSocket with the new voice ID. No-op if the voice is already active.
func (e *ElevenLabsTTS) SwitchVoice(voiceID string) error {
	e.mu.Lock()
	if e.closed || !e.started {
		e.mu.Unlock()
		return nil
	}
	if e.voiceID == voiceID {
		e.mu.Unlock()
		return nil
	}
	conn := e.conn
	e.voiceID = voiceID
	e.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}

	// Drain buffered audio from the previous voice
	for {
		select {
		case <-e.audio:
		default:
			goto drained
		}
	}
drained:

	e.logger.Info("switching TTS voice", "voice_id", voiceID)
	return e.connect()
}

func (e *ElevenLabsTTS) WriteText(text string) error {
	e.mu.Lock()
	conn := e.conn
	closed := e.closed
	e.mu.Unlock()

	if closed || conn == nil {
		return fmt.Errorf("elevenlabs TTS: not connected")
	}

	msg := map[string]any{
		"text":                   text + " ",
		"try_trigger_generation": true,
	}
	data, _ := json.Marshal(msg)
	return conn.WriteMessage(websocket.TextMessage, data)
}

func (e *ElevenLabsTTS) Flush() error {
	e.mu.Lock()
	conn := e.conn
	closed := e.closed
	e.mu.Unlock()

	if closed || conn == nil {
		return nil
	}

	msg := map[string]any{
		"text":  "",
		"flush": true,
	}
	data, _ := json.Marshal(msg)
	return conn.WriteMessage(websocket.TextMessage, data)
}

func (e *ElevenLabsTTS) Audio() <-chan []byte {
	return e.audio
}

// Cancel aborts current generation by closing the WebSocket and reconnecting.
// The audio channel is drained of pending data.
func (e *ElevenLabsTTS) Cancel() error {
	e.mu.Lock()
	conn := e.conn
	closed := e.closed
	e.mu.Unlock()

	if closed {
		return nil
	}

	if conn != nil {
		_ = conn.Close()
	}

	// Drain any buffered audio
	for {
		select {
		case <-e.audio:
		default:
			goto drained
		}
	}
drained:

	return e.connect()
}

func (e *ElevenLabsTTS) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	conn := e.conn
	e.mu.Unlock()

	if e.cancel != nil {
		e.cancel()
	}

	if conn != nil {
		closeMsg := map[string]any{"text": ""}
		data, _ := json.Marshal(closeMsg)
		_ = conn.WriteMessage(websocket.TextMessage, data)
		_ = conn.Close()
	}

	close(e.audio)
	e.logger.Info("elevenlabs TTS closed")
	return nil
}

type elevenLabsResponse struct {
	Audio               string           `json:"audio"`
	IsFinal             bool             `json:"isFinal"`
	NormalizedAlignment *json.RawMessage `json:"normalizedAlignment,omitempty"`
}

func (e *ElevenLabsTTS) readLoop() {
	for {
		e.mu.Lock()
		conn := e.conn
		closed := e.closed
		e.mu.Unlock()

		if closed || conn == nil {
			return
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			e.mu.Lock()
			wasClosed := e.closed
			e.mu.Unlock()
			if !wasClosed {
				e.logger.Debug("elevenlabs TTS: read loop ended", "error", err)
			}
			return
		}

		var resp elevenLabsResponse
		if err := json.Unmarshal(msg, &resp); err != nil {
			e.logger.Warn("elevenlabs TTS: unmarshal error", "error", err)
			continue
		}

		if resp.Audio == "" {
			continue
		}

		opusData, err := base64.StdEncoding.DecodeString(resp.Audio)
		if err != nil {
			e.logger.Warn("elevenlabs TTS: base64 decode error", "error", err)
			continue
		}

		select {
		case e.audio <- opusData:
		default:
			e.logger.Warn("elevenlabs TTS: audio channel full, dropping chunk")
		}
	}
}
