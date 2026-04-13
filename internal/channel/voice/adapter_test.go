package voice

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/channel"
)

var _ channel.Channel = (*Adapter)(nil)

func newTestAdapter(t *testing.T) (*Adapter, *MockSTT, *MockTTS) {
	t.Helper()
	stt := NewMockSTT()
	tts := NewMockTTS()
	a := NewAdapter(stt, tts, nil, "", slog.Default())
	return a, stt, tts
}

func startTestAdapter(t *testing.T) (*Adapter, *MockSTT, *MockTTS) {
	t.Helper()
	a, stt, tts := newTestAdapter(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return a, stt, tts
}

func TestAdapter_Kind(t *testing.T) {
	t.Parallel()
	a, _, _ := newTestAdapter(t)
	if got := a.Kind(); got != "voice" {
		t.Errorf("Kind() = %q, want voice", got)
	}
}

func TestAdapter_StartAndClose(t *testing.T) {
	t.Parallel()
	a, stt, tts := newTestAdapter(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !stt.Started() {
		t.Error("STT not started")
	}
	if !tts.Started() {
		t.Error("TTS not started")
	}

	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if !stt.Closed() {
		t.Error("STT not closed")
	}
	if !tts.Closed() {
		t.Error("TTS not closed")
	}
}

func TestAdapter_DoubleStart(t *testing.T) {
	t.Parallel()
	a, _, _ := newTestAdapter(t)
	ctx := context.Background()

	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx); err == nil {
		t.Error("expected error on double start")
	}
}

func TestAdapter_DoubleClose(t *testing.T) {
	t.Parallel()
	a, _, _ := newTestAdapter(t)
	ctx := context.Background()

	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal("second close should not error")
	}
}

func TestAdapter_StartSTTFailure(t *testing.T) {
	t.Parallel()
	stt := NewMockSTT()
	stt.StartFunc = func(context.Context) error { return fmt.Errorf("stt down") }
	tts := NewMockTTS()
	a := NewAdapter(stt, tts, nil, "", slog.Default())

	if err := a.Start(context.Background()); err == nil {
		t.Fatal("expected error when STT fails to start")
	}
	if tts.Started() {
		t.Error("TTS should not start when STT fails")
	}
}

func TestAdapter_StartTTSFailure(t *testing.T) {
	t.Parallel()
	stt := NewMockSTT()
	tts := NewMockTTS()
	tts.StartFunc = func(context.Context, string) error { return fmt.Errorf("tts down") }
	a := NewAdapter(stt, tts, nil, "", slog.Default())

	if err := a.Start(context.Background()); err == nil {
		t.Fatal("expected error when TTS fails to start")
	}
	if !stt.Closed() {
		t.Error("STT should be closed when TTS fails to start")
	}
}

func TestAdapter_SendOrchestratorDelta(t *testing.T) {
	t.Parallel()
	a, _, tts := startTestAdapter(t)

	a.floor.forceState(CommitPending)

	msg := channel.OutgoingMessage{
		StreamDelta: "Hello world. ",
		GuestID:     "orchestrator",
	}
	if err := a.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	time.Sleep(10 * time.Millisecond)
	calls := tts.TextCalls()
	if len(calls) != 1 {
		t.Fatalf("TTS got %d calls, want 1: %v", len(calls), calls)
	}
	if calls[0] != "Hello world." {
		t.Errorf("TTS text = %q, want %q", calls[0], "Hello world.")
	}
}

func TestAdapter_SendGuestDeltaSilenced(t *testing.T) {
	t.Parallel()
	a, _, tts := startTestAdapter(t)

	msg := channel.OutgoingMessage{
		StreamDelta: "Guest speech",
		GuestID:     "eiji",
	}
	if err := a.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	time.Sleep(10 * time.Millisecond)
	if len(tts.TextCalls()) != 0 {
		t.Error("guest delta should NOT reach TTS in orchestrator mode")
	}
}

func TestAdapter_SendDeltaDuringBargeIn(t *testing.T) {
	t.Parallel()
	a, _, tts := startTestAdapter(t)

	a.floor.forceState(BargeInAbort)

	msg := channel.OutgoingMessage{
		StreamDelta: "Discarded text",
	}
	if err := a.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	time.Sleep(10 * time.Millisecond)
	if len(tts.TextCalls()) != 0 {
		t.Error("delta during barge-in should be discarded")
	}
}

func TestAdapter_SendStreamDone(t *testing.T) {
	t.Parallel()
	a, _, tts := startTestAdapter(t)

	a.floor.forceState(OrchestratorSpeaking)

	msg := channel.OutgoingMessage{StreamDone: true}
	if err := a.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	if tts.FlushCalls() != 1 {
		t.Errorf("TTS flush calls = %d, want 1", tts.FlushCalls())
	}
	if a.floor.State() != Idle {
		t.Errorf("floor state = %v, want Idle", a.floor.State())
	}
}

func TestAdapter_IncomingFromSTT(t *testing.T) {
	t.Parallel()
	a, stt, _ := startTestAdapter(t)

	stt.InjectTranscript(Transcript{Text: "Hello", IsFinal: true, Confidence: 0.95})

	select {
	case msg := <-a.Incoming():
		if msg.ChannelKind != "voice" {
			t.Errorf("channel kind = %q, want voice", msg.ChannelKind)
		}
		if msg.Text != "Hello" {
			t.Errorf("text = %q, want Hello", msg.Text)
		}
		if msg.SenderID != "user" {
			t.Errorf("sender = %q, want user", msg.SenderID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for incoming message")
	}
}

func TestAdapter_LowConfidenceDiscarded(t *testing.T) {
	t.Parallel()
	a, stt, _ := startTestAdapter(t)

	a.floor.forceState(UserSpeaking)

	stt.InjectTranscript(Transcript{Text: "noise", IsFinal: true, Confidence: 0.1})

	select {
	case <-a.Incoming():
		t.Error("low confidence transcript should not produce incoming message")
	case <-time.After(100 * time.Millisecond):
		// expected
	}
}

func TestAdapter_BargeIn(t *testing.T) {
	t.Parallel()
	a, _, tts := startTestAdapter(t)

	a.floor.forceState(OrchestratorSpeaking)

	if err := a.BargeIn(); err != nil {
		t.Fatal(err)
	}

	if tts.CancelCalls() != 1 {
		t.Errorf("TTS cancel calls = %d, want 1", tts.CancelCalls())
	}
	if a.floor.State() != UserSpeaking {
		t.Errorf("floor state = %v, want UserSpeaking", a.floor.State())
	}

	events := a.EventLog().Events()
	var found bool
	for _, ev := range events {
		if ev.Type == EvBargeIn {
			found = true
			break
		}
	}
	if !found {
		t.Error("no barge-in event in log")
	}
}

func TestAdapter_BargeInFromNonSpeaking(t *testing.T) {
	t.Parallel()
	a, _, tts := startTestAdapter(t)

	if err := a.BargeIn(); err != nil {
		t.Fatal("barge-in from idle should be no-op, not error")
	}
	if tts.CancelCalls() != 0 {
		t.Error("TTS should not be canceled from non-speaking state")
	}
}

func TestAdapter_SendDeltaFromIdle(t *testing.T) {
	t.Parallel()
	a, _, tts := startTestAdapter(t)

	msg := channel.OutgoingMessage{
		StreamDelta: "Early response. ",
	}
	if err := a.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	time.Sleep(10 * time.Millisecond)
	if len(tts.TextCalls()) != 0 {
		t.Error("delta from Idle should be discarded (no valid Idle->OrchestratorSpeaking transition)")
	}
}

func TestAdapter_ConcurrentSendAndBargeIn(t *testing.T) {
	t.Parallel()
	a, _, _ := startTestAdapter(t)

	a.floor.forceState(CommitPending)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = a.Send(context.Background(), channel.OutgoingMessage{
				StreamDelta: fmt.Sprintf("token-%d. ", i),
			})
		}
	}()

	for i := 0; i < 10; i++ {
		_ = a.BargeIn()
		time.Sleep(time.Millisecond)
		a.floor.forceState(CommitPending)
	}

	<-done
}

func TestAdapter_STTLoopClosesCleanly(t *testing.T) {
	t.Parallel()
	stt := NewMockSTT()
	tts := NewMockTTS()
	a := NewAdapter(stt, tts, nil, "", slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}

	close(stt.transcripts)
	time.Sleep(50 * time.Millisecond)

	cancel()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAdapter_BargeInDuringTranscript(t *testing.T) {
	t.Parallel()
	a, stt, tts := startTestAdapter(t)

	a.floor.forceState(CommitPending)
	_ = a.Send(context.Background(), channel.OutgoingMessage{StreamDelta: "Hello. "})
	time.Sleep(10 * time.Millisecond)

	if a.floor.State() != OrchestratorSpeaking {
		t.Fatalf("floor state = %v, want OrchestratorSpeaking", a.floor.State())
	}

	stt.InjectTranscript(Transcript{Text: "Wait stop", IsFinal: true, Confidence: 0.95})

	select {
	case msg := <-a.Incoming():
		if msg.Text != "Wait stop" {
			t.Errorf("text = %q, want 'Wait stop'", msg.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for incoming message after barge-in transcript")
	}

	if tts.CancelCalls() < 1 {
		t.Error("expected TTS cancel from barge-in during transcript")
	}
}

func TestAdapter_EventLogRecordsTransitions(t *testing.T) {
	t.Parallel()
	a, _, _ := startTestAdapter(t)

	a.floor.forceState(CommitPending)

	msg := channel.OutgoingMessage{
		StreamDelta: "Test. ",
	}
	_ = a.Send(context.Background(), msg)

	time.Sleep(50 * time.Millisecond)

	events := a.EventLog().Events()
	if len(events) == 0 {
		t.Fatal("expected events in log")
	}

	var hasTTSStarted bool
	for _, ev := range events {
		if ev.Type == EvTTSStarted {
			hasTTSStarted = true
		}
	}
	if !hasTTSStarted {
		t.Error("missing EvTTSStarted event")
	}
}

// --- FastLLM adapter integration tests ---

func newFastLLMTestAdapter(t *testing.T, sseBody string) (*Adapter, *MockSTT, *MockTTS, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(sseBody))
	}))
	t.Cleanup(srv.Close)

	stt := NewMockSTT()
	tts := NewMockTTS()
	llm := NewFastLLM(FastLLMOpts{
		APIKey:  "test-key",
		BaseURL: srv.URL,
	})
	a := NewAdapterWithOpts(AdapterOpts{
		STT:     stt,
		TTS:     tts,
		FastLLM: llm,
		Logger:  slog.Default(),
	})
	return a, stt, tts, srv
}

func sse(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("data: ")
		b.WriteString(l)
		b.WriteString("\n\n")
	}
	return b.String()
}

func TestAdapter_FastLLM_TextToTTS(t *testing.T) {
	t.Parallel()
	body := sse(
		`{"type":"message_start","message":{"id":"msg_a"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello. "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"World. "}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_stop"}`,
	)

	a, stt, tts, _ := newFastLLMTestAdapter(t, body)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}

	stt.InjectTranscript(Transcript{Text: "Hi there", IsFinal: true, Confidence: 0.95})

	deadline := time.After(3 * time.Second)
	for {
		calls := tts.TextCalls()
		if len(calls) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for TTS calls")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	select {
	case <-a.Incoming():
		t.Error("fastLLM path should NOT push to incoming channel")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestAdapter_FastLLM_ToolUseCallback(t *testing.T) {
	t.Parallel()
	body := sse(
		`{"type":"message_start","message":{"id":"msg_b"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Inviting Noah."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"invite_agent","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"agent_name\": \"noah\", \"task_description\": \"help\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_stop"}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	stt := NewMockSTT()
	tts := NewMockTTS()
	llm := NewFastLLM(FastLLMOpts{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Tools:   VoiceToolDefs(),
	})

	cmdCh := make(chan VoiceCommand, 4)
	a := NewAdapterWithOpts(AdapterOpts{
		STT:       stt,
		TTS:       tts,
		FastLLM:   llm,
		OnCommand: func(cmd VoiceCommand) { cmdCh <- cmd },
		Logger:    slog.Default(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}

	stt.InjectTranscript(Transcript{Text: "Invite Noah", IsFinal: true, Confidence: 0.95})

	select {
	case cmd := <-cmdCh:
		if cmd.Type != "invite_agent" {
			t.Errorf("command type = %q, want invite_agent", cmd.Type)
		}
		if cmd.AgentName != "noah" {
			t.Errorf("agent = %q, want noah", cmd.AgentName)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for command callback")
	}
}

func TestAdapter_FastLLM_BargeInCancels(t *testing.T) {
	t.Parallel()

	var reqCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&reqCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)

		if n == 1 {
			// Slow first response -- will be cancelled by barge-in
			_, _ = w.Write([]byte(sse(
				`{"type":"message_start","message":{"id":"msg_slow"}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Starting a long. "}}`,
			)))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}

		_, _ = w.Write([]byte(sse(
			`{"type":"message_start","message":{"id":"msg_fast"}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Quick reply. "}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_stop"}`,
		)))
	}))
	t.Cleanup(srv.Close)

	stt := NewMockSTT()
	tts := NewMockTTS()
	llm := NewFastLLM(FastLLMOpts{APIKey: "key", BaseURL: srv.URL})
	a := NewAdapterWithOpts(AdapterOpts{
		STT:     stt,
		TTS:     tts,
		FastLLM: llm,
		Logger:  slog.Default(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}

	stt.InjectTranscript(Transcript{Text: "First question", IsFinal: true, Confidence: 0.95})
	time.Sleep(100 * time.Millisecond)

	stt.InjectTranscript(Transcript{Text: "Barge in second", IsFinal: true, Confidence: 0.95})

	deadline := time.After(3 * time.Second)
	for {
		if atomic.LoadInt32(&reqCount) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("second request never arrived (barge-in should cancel first)")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestAdapter_FastLLM_NilFallback(t *testing.T) {
	t.Parallel()

	stt := NewMockSTT()
	tts := NewMockTTS()
	a := NewAdapterWithOpts(AdapterOpts{
		STT:    stt,
		TTS:    tts,
		Logger: slog.Default(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}

	stt.InjectTranscript(Transcript{Text: "Fallback test", IsFinal: true, Confidence: 0.95})

	select {
	case msg := <-a.Incoming():
		if msg.Text != "Fallback test" {
			t.Errorf("text = %q, want %q", msg.Text, "Fallback test")
		}
		if msg.ChannelKind != "voice" {
			t.Errorf("kind = %q, want voice", msg.ChannelKind)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for incoming message (fallback path)")
	}
}

func TestAdapter_FastLLM_ConcurrentTranscriptCancelsPrevious(t *testing.T) {
	t.Parallel()

	var reqCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&reqCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)

		if n == 1 {
			_, _ = w.Write([]byte(sse(
				`{"type":"message_start","message":{"id":"msg_1"}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			)))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}

		_, _ = w.Write([]byte(sse(
			`{"type":"message_start","message":{"id":"msg_2"}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Second reply. "}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_stop"}`,
		)))
	}))
	t.Cleanup(srv.Close)

	stt := NewMockSTT()
	tts := NewMockTTS()
	llm := NewFastLLM(FastLLMOpts{APIKey: "key", BaseURL: srv.URL})
	a := NewAdapterWithOpts(AdapterOpts{
		STT:     stt,
		TTS:     tts,
		FastLLM: llm,
		Logger:  slog.Default(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}

	stt.InjectTranscript(Transcript{Text: "First message", IsFinal: true, Confidence: 0.9})
	time.Sleep(100 * time.Millisecond)

	stt.InjectTranscript(Transcript{Text: "Second message", IsFinal: true, Confidence: 0.9})

	deadline := time.After(3 * time.Second)
	for {
		if atomic.LoadInt32(&reqCount) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("second fastLLM request never started")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestAdapter_FastLLM_ErrorFallsBack(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":{"message":"server error"}}`))
	}))
	t.Cleanup(srv.Close)

	stt := NewMockSTT()
	tts := NewMockTTS()
	llm := NewFastLLM(FastLLMOpts{APIKey: "key", BaseURL: srv.URL})
	a := NewAdapterWithOpts(AdapterOpts{
		STT:     stt,
		TTS:     tts,
		FastLLM: llm,
		Logger:  slog.Default(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}

	stt.InjectTranscript(Transcript{Text: "Error test", IsFinal: true, Confidence: 0.95})

	select {
	case msg := <-a.Incoming():
		if msg.Text != "Error test" {
			t.Errorf("text = %q, want %q", msg.Text, "Error test")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for fallback incoming message")
	}
}
