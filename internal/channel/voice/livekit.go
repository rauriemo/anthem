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

// Audio format contract: Opus end-to-end passthrough (no PCM conversion).
//
//	Browser -> [Opus RTP] -> LiveKit -> readTrack -> [Opus payload] -> Deepgram (encoding=opus)
//	ElevenLabs (opus_48000) -> [Opus frames] -> publishLoop -> WriteSample -> LiveKit -> [Opus RTP] -> Browser
//
// Opus RTP always uses a 48 kHz clock rate per RFC 7587.
const (
	OpusClockRate = 48000
	FrameDuration = 20 * time.Millisecond
)

// LiveKitTransport bridges a LiveKit room to STT/TTS providers using Opus
// passthrough. Inbound Opus payloads go directly to Deepgram (configured for
// encoding=opus), and outbound Opus frames from ElevenLabs go directly to
// LiveKit via WriteSample.
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
		ClockRate: OpusClockRate,
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

// readTrack reads RTP packets from a remote participant's audio track,
// extracts the Opus payload, and forwards it to Deepgram (encoding=opus).
func (t *LiveKitTransport) readTrack(ctx context.Context, track *webrtc.TrackRemote, participant string) {
	defer t.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		pkt, _, err := track.ReadRTP()
		if err != nil {
			t.logger.Debug("livekit: track read ended", "participant", participant, "error", err)
			return
		}

		if len(pkt.Payload) == 0 {
			continue
		}

		if err := t.stt.WriteAudio(pkt.Payload); err != nil {
			t.logger.Warn("livekit: stt write failed", "error", err)
		}
	}
}

// publishLoop reads Opus frames from TTS and publishes them to the LiveKit
// room. Each chunk from ElevenLabs (opus_48000) is a complete Opus packet
// that WriteSample packetizes into RTP.
func (t *LiveKitTransport) publishLoop(ctx context.Context) {
	defer t.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-t.tts.Audio():
			if !ok {
				return
			}

			if len(chunk) == 0 {
				continue
			}

			t.mu.Lock()
			track := t.localTrack
			t.mu.Unlock()

			if track == nil {
				return
			}

			if err := track.WriteSample(media.Sample{
				Data:     chunk,
				Duration: FrameDuration,
			}, nil); err != nil {
				t.logger.Warn("livekit: write sample failed", "error", err)
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
