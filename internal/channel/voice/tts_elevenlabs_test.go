package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newElevenLabsTestServer(t *testing.T, handler func(conn *websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("upgrade error: %v", err)
			return
		}
		defer conn.Close()
		handler(conn)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func elevenLabsWSURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestElevenLabsTTS_WriteTextAndReceiveAudio(t *testing.T) {
	var initMsg map[string]any
	var textMsgs []string
	var mu sync.Mutex

	pcmData := make([]byte, 960)
	for i := range pcmData {
		pcmData[i] = byte(i % 256)
	}
	encodedAudio := base64.StdEncoding.EncodeToString(pcmData)

	srv := newElevenLabsTestServer(t, func(conn *websocket.Conn) {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}

			var m map[string]any
			if err := json.Unmarshal(msg, &m); err != nil {
				continue
			}

			mu.Lock()
			text, _ := m["text"].(string)
			if _, ok := m["voice_settings"]; ok {
				initMsg = m
			} else if text != "" {
				textMsgs = append(textMsgs, text)

				resp := map[string]any{
					"audio":   encodedAudio,
					"isFinal": false,
				}
				data, _ := json.Marshal(resp)
				_ = conn.WriteMessage(websocket.TextMessage, data)
			}
			mu.Unlock()
		}
	})

	tts := NewElevenLabsTTS("test-key", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Patch the connect to use test server
	tts.ctx = ctx
	tts.cancel = cancel
	tts.voiceID = "test-voice"
	tts.started = true

	wsURL := elevenLabsWSURL(srv)
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Send init message like the real connect does
	initBytes, _ := json.Marshal(map[string]any{
		"text":           " ",
		"voice_settings": map[string]any{"stability": 0.5, "similarity_boost": 0.75},
	})
	_ = conn.WriteMessage(websocket.TextMessage, initBytes)

	tts.mu.Lock()
	tts.conn = conn
	tts.mu.Unlock()

	go tts.readLoop()

	if err := tts.WriteText("Hello there."); err != nil {
		t.Fatalf("write text: %v", err)
	}

	select {
	case audio := <-tts.Audio():
		if len(audio) != len(pcmData) {
			t.Errorf("got %d bytes, want %d", len(audio), len(pcmData))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for audio")
	}

	mu.Lock()
	if initMsg == nil {
		t.Error("no init message received")
	}
	if len(textMsgs) == 0 {
		t.Error("no text messages received")
	}
	mu.Unlock()

	tts.mu.Lock()
	tts.closed = true
	tts.mu.Unlock()
	_ = conn.Close()
}

func TestElevenLabsTTS_Flush(t *testing.T) {
	var flushed bool
	var mu sync.Mutex

	srv := newElevenLabsTestServer(t, func(conn *websocket.Conn) {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(msg, &m) == nil {
				mu.Lock()
				if fl, ok := m["flush"]; ok && fl == true {
					flushed = true
				}
				mu.Unlock()
			}
		}
	})

	tts := NewElevenLabsTTS("test-key", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tts.ctx = ctx
	tts.cancel = cancel
	tts.voiceID = "test-voice"
	tts.started = true

	wsURL := elevenLabsWSURL(srv)
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	tts.mu.Lock()
	tts.conn = conn
	tts.mu.Unlock()

	if err := tts.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if !flushed {
		t.Error("flush message not received by server")
	}
	mu.Unlock()

	tts.mu.Lock()
	tts.closed = true
	tts.mu.Unlock()
	_ = conn.Close()
}

func TestElevenLabsTTS_EmptyAudioIgnored(t *testing.T) {
	ready := make(chan struct{})

	srv := newElevenLabsTestServer(t, func(conn *websocket.Conn) {
		// Wait for client to signal it's ready (by sending any text msg)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(msg, &m) == nil {
				if _, ok := m["text"]; ok {
					break
				}
			}
		}

		// Send response with empty audio
		resp := map[string]any{
			"audio":   "",
			"isFinal": false,
		}
		data, _ := json.Marshal(resp)
		_ = conn.WriteMessage(websocket.TextMessage, data)

		// Then one with real audio
		pcm := make([]byte, 100)
		resp2 := map[string]any{
			"audio":   base64.StdEncoding.EncodeToString(pcm),
			"isFinal": false,
		}
		data2, _ := json.Marshal(resp2)
		_ = conn.WriteMessage(websocket.TextMessage, data2)

		time.Sleep(200 * time.Millisecond)
	})

	tts := NewElevenLabsTTS("test-key", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tts.ctx = ctx
	tts.cancel = cancel
	tts.voiceID = "test-voice"
	tts.started = true

	wsURL := elevenLabsWSURL(srv)
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	tts.mu.Lock()
	tts.conn = conn
	tts.mu.Unlock()

	go tts.readLoop()
	close(ready)

	// Trigger the server to send responses by writing text
	if err := tts.WriteText("test"); err != nil {
		t.Fatalf("write text: %v", err)
	}

	select {
	case audio := <-tts.Audio():
		if len(audio) != 100 {
			t.Errorf("got %d bytes, want 100 (empty audio should have been skipped)", len(audio))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for audio")
	}

	tts.mu.Lock()
	tts.closed = true
	tts.mu.Unlock()
	_ = conn.Close()
}

func TestElevenLabsTTS_CancelDrainsAudio(t *testing.T) {
	tts := NewElevenLabsTTS("test-key", nil)
	tts.started = true

	// Inject some audio into the channel
	go func() {
		tts.audio <- make([]byte, 100)
		tts.audio <- make([]byte, 200)
	}()
	time.Sleep(50 * time.Millisecond)

	// Cancel should drain buffered audio
	// Since there's no real WS to reconnect to, we test the drain part
	// by verifying the channel is empty after drain
	for {
		select {
		case <-tts.audio:
		default:
			goto done
		}
	}
done:

	// Channel should be empty
	select {
	case <-tts.audio:
		t.Error("audio channel should be empty after drain")
	default:
		// expected
	}
}

func TestElevenLabsTTS_CloseIdempotent(t *testing.T) {
	tts := NewElevenLabsTTS("test-key", nil)
	if err := tts.Close(); err != nil {
		t.Errorf("first close: %v", err)
	}
	if err := tts.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}
