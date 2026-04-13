package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	deepgramBaseURL    = "wss://api.deepgram.com/v1/listen"
	deepgramModel      = "nova-3"
	deepgramSampleRate = 48000
)

// DeepgramSTT implements StreamingSTT using Deepgram's WebSocket streaming API.
// Uses nova-3 (not nova-3-agent) so our own TurnDetector owns endpointing.
type DeepgramSTT struct {
	apiKey string
	logger *slog.Logger

	mu          sync.Mutex
	conn        *websocket.Conn
	transcripts chan Transcript
	started     bool
	closed      bool
	cancel      context.CancelFunc
}

func NewDeepgramSTT(apiKey string, logger *slog.Logger) *DeepgramSTT {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeepgramSTT{
		apiKey:      apiKey,
		logger:      logger,
		transcripts: make(chan Transcript, 64),
	}
}

func (d *DeepgramSTT) Start(ctx context.Context) error {
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return fmt.Errorf("deepgram STT already started")
	}
	d.started = true
	d.mu.Unlock()

	ctx, d.cancel = context.WithCancel(ctx)

	params := url.Values{
		"encoding":        {"linear16"},
		"sample_rate":     {fmt.Sprintf("%d", deepgramSampleRate)},
		"channels":        {"1"},
		"model":           {deepgramModel},
		"punctuate":       {"true"},
		"interim_results": {"true"},
		"endpointing":     {"false"},
		"vad_events":      {"true"},
	}

	wsURL := deepgramBaseURL + "?" + params.Encode()
	header := make(map[string][]string)
	header["Authorization"] = []string{"Token " + d.apiKey}

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return fmt.Errorf("deepgram STT: websocket dial: %w", err)
	}

	d.mu.Lock()
	d.conn = conn
	d.mu.Unlock()

	go d.readLoop(ctx)

	d.logger.Info("deepgram STT started", "model", deepgramModel, "sample_rate", deepgramSampleRate)
	return nil
}

func (d *DeepgramSTT) WriteAudio(pcm []byte) error {
	d.mu.Lock()
	conn := d.conn
	closed := d.closed
	d.mu.Unlock()

	if closed || conn == nil {
		return fmt.Errorf("deepgram STT: not connected")
	}

	return conn.WriteMessage(websocket.BinaryMessage, pcm)
}

func (d *DeepgramSTT) Transcripts() <-chan Transcript {
	return d.transcripts
}

func (d *DeepgramSTT) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	conn := d.conn
	d.mu.Unlock()

	if d.cancel != nil {
		d.cancel()
	}

	if conn != nil {
		closeMsg := []byte(`{"type": "CloseStream"}`)
		_ = conn.WriteMessage(websocket.TextMessage, closeMsg)
		_ = conn.Close()
	}

	close(d.transcripts)
	d.logger.Info("deepgram STT closed")
	return nil
}

type deepgramResponse struct {
	Type    string `json:"type"`
	Channel struct {
		Alternatives []struct {
			Transcript string  `json:"transcript"`
			Confidence float64 `json:"confidence"`
		} `json:"alternatives"`
	} `json:"channel"`
	IsFinal    bool `json:"is_final"`
	SpeechFinal bool `json:"speech_final"`
}

type deepgramVADEvent struct {
	Type string `json:"type"`
}

func (d *DeepgramSTT) readLoop(ctx context.Context) {
	defer func() {
		d.mu.Lock()
		if !d.closed {
			close(d.transcripts)
		}
		d.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		d.mu.Lock()
		conn := d.conn
		d.mu.Unlock()

		if conn == nil {
			return
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			d.mu.Lock()
			closed := d.closed
			d.mu.Unlock()
			if !closed {
				d.logger.Warn("deepgram STT: read error", "error", err)
			}
			return
		}

		var baseMsg struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(msg, &baseMsg); err != nil {
			d.logger.Warn("deepgram STT: unmarshal error", "error", err)
			continue
		}

		switch baseMsg.Type {
		case "Results":
			d.handleResults(msg)
		case "SpeechStarted":
			d.handleVADStart()
		case "Metadata", "UtteranceEnd":
			// informational, ignore
		default:
			d.logger.Debug("deepgram STT: unhandled message type", "type", baseMsg.Type)
		}
	}
}

func (d *DeepgramSTT) handleResults(msg []byte) {
	var resp deepgramResponse
	if err := json.Unmarshal(msg, &resp); err != nil {
		d.logger.Warn("deepgram STT: results unmarshal error", "error", err)
		return
	}

	if len(resp.Channel.Alternatives) == 0 {
		return
	}

	alt := resp.Channel.Alternatives[0]
	if alt.Transcript == "" {
		return
	}

	t := Transcript{
		Text:       alt.Transcript,
		IsFinal:    resp.IsFinal,
		Confidence: alt.Confidence,
	}

	select {
	case d.transcripts <- t:
	default:
		d.logger.Warn("deepgram STT: transcript channel full, dropping")
	}
}

func (d *DeepgramSTT) handleVADStart() {
	d.logger.Debug("deepgram STT: VAD speech started")
}
