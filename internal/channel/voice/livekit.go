package voice

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// Audio format contract constants.
// Inbound (user -> STT):  48kHz, mono, PCM16 LE, 20ms frames = 960 samples = 1920 bytes
// Outbound (TTS -> user): 24kHz, mono, PCM16 LE, 20ms frames = 480 samples =  960 bytes
const (
	InboundSampleRate  = 48000
	OutboundSampleRate = 24000
	FrameDuration      = 20 * time.Millisecond
	InboundFrameSize   = InboundSampleRate * 20 / 1000  // 960 samples
	OutboundFrameSize  = OutboundSampleRate * 20 / 1000 // 480 samples
	InboundFrameBytes  = InboundFrameSize * 2           // 1920 bytes (16-bit)
	OutboundFrameBytes = OutboundFrameSize * 2          // 960 bytes (16-bit)
)

// LiveKitTransport bridges a LiveKit room to STT/TTS providers.
// It subscribes to user audio, decodes to PCM, and pipes to STT.
// It reads TTS audio, frames it into 20ms chunks, and publishes to the room.
type LiveKitTransport struct {
	url       string
	apiKey    string
	apiSecret string
	roomName  string
	identity  string
	stt       StreamingSTT
	tts       StreamingTTS
	logger    *slog.Logger

	mu         sync.Mutex
	room       *lksdk.Room
	localTrack *lksdk.LocalTrack
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	started    bool
	closed     bool
}

func NewLiveKitTransport(url, apiKey, apiSecret, roomName, identity string, stt StreamingSTT, tts StreamingTTS, logger *slog.Logger) *LiveKitTransport {
	if logger == nil {
		logger = slog.Default()
	}
	return &LiveKitTransport{
		url:       url,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		roomName:  roomName,
		identity:  identity,
		stt:       stt,
		tts:       tts,
		logger:    logger,
	}
}

func (t *LiveKitTransport) Start(ctx context.Context) error {
	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return fmt.Errorf("livekit transport already started")
	}
	t.started = true
	t.mu.Unlock()

	ctx, t.cancel = context.WithCancel(ctx)

	callback := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				if track.Kind() == webrtc.RTPCodecTypeAudio {
					t.logger.Info("livekit: subscribed to audio track",
						"participant", rp.Identity(),
						"track_id", track.ID())
					t.wg.Add(1)
					go t.readTrack(ctx, track, rp.Identity())
				}
			},
		},
		OnDisconnected: func() {
			t.logger.Warn("livekit: disconnected from room")
		},
	}

	room, err := lksdk.ConnectToRoom(t.url, lksdk.ConnectInfo{
		APIKey:              t.apiKey,
		APISecret:           t.apiSecret,
		RoomName:            t.roomName,
		ParticipantIdentity: t.identity,
		ParticipantName:     "Anthem Voice Gateway",
	}, callback, lksdk.WithAutoSubscribe(true))
	if err != nil {
		return fmt.Errorf("livekit transport: connect: %w", err)
	}

	t.mu.Lock()
	t.room = room
	t.mu.Unlock()

	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: uint32(OutboundSampleRate),
		Channels:  1,
	})
	if err != nil {
		room.Disconnect()
		return fmt.Errorf("livekit transport: create local track: %w", err)
	}

	t.mu.Lock()
	t.localTrack = track
	t.mu.Unlock()

	_, err = room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name: "anthem-tts",
	})
	if err != nil {
		room.Disconnect()
		return fmt.Errorf("livekit transport: publish track: %w", err)
	}

	t.wg.Add(1)
	go t.publishLoop(ctx)

	t.logger.Info("livekit transport started", "room", t.roomName, "identity", t.identity)
	return nil
}

// readTrack reads PCM audio from a remote participant's track and pipes it to STT.
func (t *LiveKitTransport) readTrack(ctx context.Context, track *webrtc.TrackRemote, participant string) {
	defer t.wg.Done()

	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, _, err := track.Read(buf)
		if err != nil {
			t.logger.Debug("livekit: track read ended", "participant", participant, "error", err)
			return
		}

		if n == 0 {
			continue
		}

		pcm := make([]byte, n)
		copy(pcm, buf[:n])

		if err := t.stt.WriteAudio(pcm); err != nil {
			t.logger.Warn("livekit: stt write failed", "error", err)
		}
	}
}

// publishLoop reads TTS audio, slices it into 20ms frames, and publishes to the room.
func (t *LiveKitTransport) publishLoop(ctx context.Context) {
	defer t.wg.Done()

	var residual []byte

	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-t.tts.Audio():
			if !ok {
				return
			}

			data := append(residual, chunk...)
			residual = nil

			for len(data) >= OutboundFrameBytes {
				frame := data[:OutboundFrameBytes]
				data = data[OutboundFrameBytes:]

				t.mu.Lock()
				track := t.localTrack
				t.mu.Unlock()

				if track == nil {
					return
				}

				if err := track.WriteSample(media.Sample{
					Data:     frame,
					Duration: FrameDuration,
				}, nil); err != nil {
					t.logger.Warn("livekit: write sample failed", "error", err)
				}
			}

			if len(data) > 0 {
				residual = data
			}
		}
	}
}

// DrainAudio discards any pending audio in the TTS channel without publishing it.
// Called during barge-in to stop stale audio from playing.
func (t *LiveKitTransport) DrainAudio() {
	for {
		select {
		case <-t.tts.Audio():
		default:
			return
		}
	}
}

func (t *LiveKitTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	room := t.room
	track := t.localTrack
	t.mu.Unlock()

	if t.cancel != nil {
		t.cancel()
	}

	t.wg.Wait()

	if track != nil {
		_ = track.Close()
	}
	if room != nil {
		room.Disconnect()
	}

	t.logger.Info("livekit transport closed")
	return nil
}
