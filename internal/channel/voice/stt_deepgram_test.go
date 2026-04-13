package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newDeepgramTestServer(t *testing.T, handler func(conn *websocket.Conn)) *httptest.Server {
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

func deepgramWSURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestDeepgramSTT_ConnectAndTranscript(t *testing.T) {
	var received [][]byte
	var mu sync.Mutex

	srv := newDeepgramTestServer(t, func(conn *websocket.Conn) {
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if msgType == websocket.BinaryMessage {
				mu.Lock()
				cp := make([]byte, len(msg))
				copy(cp, msg)
				received = append(received, cp)
				mu.Unlock()
			}
			if msgType == websocket.TextMessage {
				var m map[string]string
				if json.Unmarshal(msg, &m) == nil && m["type"] == "CloseStream" {
					return
				}
			}

			resp := map[string]any{
				"type": "Results",
				"channel": map[string]any{
					"alternatives": []map[string]any{
						{"transcript": "hello world", "confidence": 0.95},
					},
				},
				"is_final":     true,
				"speech_final": false,
			}
			data, _ := json.Marshal(resp)
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	})

	// Override base URL for testing
	origURL := deepgramBaseURL
	stt := NewDeepgramSTT("test-key", nil)
	// Patch the URL by creating a custom start that uses the test server
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	params := fmt.Sprintf("?encoding=linear16&sample_rate=%d&channels=1&model=%s&punctuate=true&interim_results=true&endpointing=false&vad_events=true",
		deepgramSampleRate, deepgramModel)
	wsURL := deepgramWSURL(srv) + params

	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	stt.mu.Lock()
	stt.conn = conn
	stt.started = true
	stt.mu.Unlock()

	ctx2, cancel2 := context.WithCancel(ctx)
	stt.cancel = cancel2
	stt.ctx = ctx2
	stt.wg.Add(1)
	go stt.readLoop(ctx2)

	_ = origURL

	pcm := make([]byte, 1920)
	if err := stt.WriteAudio(pcm); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	select {
	case tr := <-stt.Transcripts():
		if tr.Text != "hello world" {
			t.Errorf("got text %q, want %q", tr.Text, "hello world")
		}
		if !tr.IsFinal {
			t.Error("expected final transcript")
		}
		if tr.Confidence != 0.95 {
			t.Errorf("got confidence %f, want 0.95", tr.Confidence)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for transcript")
	}

	mu.Lock()
	gotAudio := len(received)
	mu.Unlock()
	if gotAudio == 0 {
		t.Error("no audio received by server")
	}

	cancel2()
	stt.mu.Lock()
	stt.closed = true
	stt.mu.Unlock()
	_ = conn.Close()
}

func TestDeepgramSTT_EmptyTranscriptIgnored(t *testing.T) {
	srv := newDeepgramTestServer(t, func(conn *websocket.Conn) {
		resp := map[string]any{
			"type": "Results",
			"channel": map[string]any{
				"alternatives": []map[string]any{
					{"transcript": "", "confidence": 0.0},
				},
			},
			"is_final": true,
		}
		data, _ := json.Marshal(resp)
		_ = conn.WriteMessage(websocket.TextMessage, data)

		resp2 := map[string]any{
			"type": "Results",
			"channel": map[string]any{
				"alternatives": []map[string]any{
					{"transcript": "real text", "confidence": 0.9},
				},
			},
			"is_final": true,
		}
		data2, _ := json.Marshal(resp2)
		_ = conn.WriteMessage(websocket.TextMessage, data2)

		time.Sleep(100 * time.Millisecond)
	})

	stt := NewDeepgramSTT("test-key", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wsURL := deepgramWSURL(srv)
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	stt.mu.Lock()
	stt.conn = conn
	stt.started = true
	stt.mu.Unlock()
	stt.cancel = cancel
	stt.ctx = ctx
	stt.wg.Add(1)
	go stt.readLoop(ctx)

	select {
	case tr := <-stt.Transcripts():
		if tr.Text != "real text" {
			t.Errorf("got text %q, want %q", tr.Text, "real text")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for transcript")
	}

	stt.mu.Lock()
	stt.closed = true
	stt.mu.Unlock()
	_ = conn.Close()
}

func TestDeepgramSTT_PartialTranscript(t *testing.T) {
	srv := newDeepgramTestServer(t, func(conn *websocket.Conn) {
		resp := map[string]any{
			"type": "Results",
			"channel": map[string]any{
				"alternatives": []map[string]any{
					{"transcript": "partial", "confidence": 0.6},
				},
			},
			"is_final": false,
		}
		data, _ := json.Marshal(resp)
		_ = conn.WriteMessage(websocket.TextMessage, data)
		time.Sleep(100 * time.Millisecond)
	})

	stt := NewDeepgramSTT("test-key", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wsURL := deepgramWSURL(srv)
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	stt.mu.Lock()
	stt.conn = conn
	stt.started = true
	stt.mu.Unlock()
	stt.cancel = cancel
	stt.ctx = ctx
	stt.wg.Add(1)
	go stt.readLoop(ctx)

	select {
	case tr := <-stt.Transcripts():
		if tr.IsFinal {
			t.Error("expected non-final transcript")
		}
		if tr.Text != "partial" {
			t.Errorf("got %q, want %q", tr.Text, "partial")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}

	stt.mu.Lock()
	stt.closed = true
	stt.mu.Unlock()
	_ = conn.Close()
}

func TestDeepgramSTT_CloseIdempotent(t *testing.T) {
	stt := NewDeepgramSTT("test-key", nil)
	if err := stt.Close(); err != nil {
		t.Errorf("first close: %v", err)
	}
	if err := stt.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

func TestDeepgramSTT_VADEventHandled(t *testing.T) {
	srv := newDeepgramTestServer(t, func(conn *websocket.Conn) {
		vad := map[string]any{"type": "SpeechStarted"}
		data, _ := json.Marshal(vad)
		_ = conn.WriteMessage(websocket.TextMessage, data)
		time.Sleep(100 * time.Millisecond)
	})

	stt := NewDeepgramSTT("test-key", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wsURL := deepgramWSURL(srv)
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	stt.mu.Lock()
	stt.conn = conn
	stt.started = true
	stt.mu.Unlock()
	stt.cancel = cancel
	stt.ctx = ctx
	stt.wg.Add(1)
	go stt.readLoop(ctx)

	// VAD events don't produce transcripts, just ensure no panic
	time.Sleep(200 * time.Millisecond)

	stt.mu.Lock()
	stt.closed = true
	stt.mu.Unlock()
	_ = conn.Close()
}
