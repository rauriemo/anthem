package voice

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sseLines(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("data: ")
		b.WriteString(l)
		b.WriteString("\n\n")
	}
	return b.String()
}

func TestFastLLM_StreamingTextResponse(t *testing.T) {
	body := sseLines(
		`{"type":"message_start","message":{"id":"msg_1","role":"assistant","content":[],"model":"claude-3-5-haiku-latest"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"!"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`{"type":"message_stop"}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	f := NewFastLLM(FastLLMOpts{
		APIKey:  "test-key",
		BaseURL: srv.URL,
	})

	ch, err := f.StreamChat(context.Background(), "Hi")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	var texts []string
	var gotDone bool
	for ev := range ch {
		if ev.TextDelta != "" {
			texts = append(texts, ev.TextDelta)
		}
		if ev.Done {
			gotDone = true
		}
	}

	if want := "Hello world!"; strings.Join(texts, "") != want {
		t.Errorf("got %q, want %q", strings.Join(texts, ""), want)
	}
	if !gotDone {
		t.Error("never received Done event")
	}
}

func TestFastLLM_ToolUseParsing(t *testing.T) {
	body := sseLines(
		`{"type":"message_start","message":{"id":"msg_2","role":"assistant","content":[],"model":"claude-3-5-haiku-latest"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Sure, inviting Noah."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"invite_agent","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"agent_name\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":" \"noah\", \"task_description\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":" \"help with code\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
		`{"type":"message_stop"}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	f := NewFastLLM(FastLLMOpts{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Tools:   VoiceToolDefs(),
	})

	ch, err := f.StreamChat(context.Background(), "Invite noah to help")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	var gotText string
	var toolName string
	var toolInput string
	for ev := range ch {
		if ev.TextDelta != "" {
			gotText += ev.TextDelta
		}
		if ev.ToolUse != nil {
			toolName = ev.ToolUse.Name
			toolInput = string(ev.ToolUse.Input)
		}
	}

	if gotText != "Sure, inviting Noah." {
		t.Errorf("text = %q, want %q", gotText, "Sure, inviting Noah.")
	}
	if toolName != "invite_agent" {
		t.Errorf("tool name = %q, want %q", toolName, "invite_agent")
	}
	if !strings.Contains(toolInput, "noah") {
		t.Errorf("tool input missing 'noah': %s", toolInput)
	}
}

func TestFastLLM_Error401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"invalid api key"}}`))
	}))
	defer srv.Close()

	f := NewFastLLM(FastLLMOpts{APIKey: "bad-key", BaseURL: srv.URL})
	_, err := f.StreamChat(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should contain '401': %v", err)
	}
}

func TestFastLLM_Error429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"rate limited"}}`))
	}))
	defer srv.Close()

	f := NewFastLLM(FastLLMOpts{APIKey: "key", BaseURL: srv.URL})
	_, err := f.StreamChat(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should contain '429': %v", err)
	}
}

func TestFastLLM_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	f := NewFastLLM(FastLLMOpts{APIKey: "key", BaseURL: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.StreamChat(ctx, "hi")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestFastLLM_MalformedSSE(t *testing.T) {
	body := sseLines(
		`{"type":"message_start","message":{"id":"msg_3"}}`,
		`THIS IS NOT JSON`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}`,
		`{"type":"message_stop"}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	f := NewFastLLM(FastLLMOpts{APIKey: "key", BaseURL: srv.URL})
	ch, err := f.StreamChat(context.Background(), "hi")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	var gotText string
	for ev := range ch {
		gotText += ev.TextDelta
	}
	if gotText != "OK" {
		t.Errorf("got %q, want %q (should skip malformed line)", gotText, "OK")
	}
}

func TestFastLLM_HistoryEviction(t *testing.T) {
	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		body := sseLines(
			`{"type":"message_start","message":{"id":"msg_h"}}`,
			fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,),
			fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"reply %d"}}`, requestCount.Load()),
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_stop"}`,
		)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	f := NewFastLLM(FastLLMOpts{APIKey: "key", BaseURL: srv.URL})

	for i := 0; i < 12; i++ {
		ch, err := f.StreamChat(context.Background(), fmt.Sprintf("msg %d", i))
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		for range ch {
		}
	}

	if f.HistoryLen() > maxHistoryTurns*2 {
		t.Errorf("history length = %d, want <= %d", f.HistoryLen(), maxHistoryTurns*2)
	}
}

func TestFastLLM_NetworkTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer srv.Close()

	f := NewFastLLM(FastLLMOpts{APIKey: "key", BaseURL: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := f.StreamChat(ctx, "hi")
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
