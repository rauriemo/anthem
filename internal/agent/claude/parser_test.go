package claude

import (
	"testing"
)

func TestStreamEventStreamingPayload(t *testing.T) {
	textEv, err := ParseStreamEvent([]byte(
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if got := StreamEventStreamingPayload(textEv); got != "hi" {
		t.Errorf("text_delta: got %q", got)
	}

	jsonEv, err := ParseStreamEvent([]byte(
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"a\""}}}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if got := StreamEventStreamingPayload(jsonEv); got != `{"a"` {
		t.Errorf("partial_json: got %q", got)
	}
}

func TestParseStreamEvent(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantType  string
		wantErr   bool
		wantNil   bool
		checkFunc func(*testing.T, *StreamEvent)
	}{
		{
			name:     "result event",
			input:    `{"type":"result","subtype":"success","is_error":false,"duration_ms":3043,"num_turns":3,"result":"Hello.","session_id":"sess-123","total_cost_usd":0.05,"usage":{"input_tokens":500,"output_tokens":200,"cache_creation_input_tokens":100,"cache_read_input_tokens":50}}`,
			wantType: "result",
			checkFunc: func(t *testing.T, e *StreamEvent) {
				if e.SessionID != "sess-123" {
					t.Errorf("session_id = %q", e.SessionID)
				}
				if e.IsError {
					t.Error("expected is_error=false")
				}
				if e.TotalCost != 0.05 {
					t.Errorf("total_cost_usd = %f", e.TotalCost)
				}
				if e.NumTurns != 3 {
					t.Errorf("num_turns = %d", e.NumTurns)
				}
				if e.DurationMS != 3043 {
					t.Errorf("duration_ms = %d", e.DurationMS)
				}
				if e.Usage == nil {
					t.Fatal("usage should not be nil")
				}
				if e.Usage.InputTokens != 500 {
					t.Errorf("input_tokens = %d", e.Usage.InputTokens)
				}
				if e.Usage.OutputTokens != 200 {
					t.Errorf("output_tokens = %d", e.Usage.OutputTokens)
				}
				if e.Usage.CacheCreationInputTokens != 100 {
					t.Errorf("cache_creation_input_tokens = %d", e.Usage.CacheCreationInputTokens)
				}
				if e.Usage.CacheReadInputTokens != 50 {
					t.Errorf("cache_read_input_tokens = %d", e.Usage.CacheReadInputTokens)
				}
			},
		},
		{
			name:     "content event",
			input:    `{"type":"assistant","subtype":"text","content":"hello world"}`,
			wantType: "assistant",
			checkFunc: func(t *testing.T, e *StreamEvent) {
				if e.Content != "hello world" {
					t.Errorf("content = %q", e.Content)
				}
			},
		},
		{
			name:    "empty line",
			input:   "",
			wantNil: true,
		},
		{
			name:    "invalid json",
			input:   "not json",
			wantErr: true,
		},
		{
			name:     "bom prefix stripped",
			input:    "\xEF\xBB\xBF" + `{"type":"system"}`,
			wantType: "system",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := ParseStreamEvent([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if event != nil {
					t.Fatal("expected nil event")
				}
				return
			}
			if event.Type != tt.wantType {
				t.Errorf("type = %q, want %q", event.Type, tt.wantType)
			}
			if tt.checkFunc != nil {
				tt.checkFunc(t, event)
			}
		})
	}
}
