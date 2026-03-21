package claude

import (
	"testing"
)

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
			input:    `{"type":"result","result":{"session_id":"sess-123","exitCode":0,"input_tokens":500,"output_tokens":200,"cost_usd":0.05,"num_turns":3,"duration_seconds":12.5}}`,
			wantType: "result",
			checkFunc: func(t *testing.T, e *StreamEvent) {
				if e.Result == nil {
					t.Fatal("result should not be nil")
				}
				if e.Result.SessionID != "sess-123" {
					t.Errorf("session_id = %q", e.Result.SessionID)
				}
				if e.Result.ExitCode != 0 {
					t.Errorf("exit_code = %d", e.Result.ExitCode)
				}
				if e.Result.TokensIn != 500 {
					t.Errorf("tokens_in = %d", e.Result.TokensIn)
				}
				if e.Result.TokensOut != 200 {
					t.Errorf("tokens_out = %d", e.Result.TokensOut)
				}
				if e.Result.CostUSD != 0.05 {
					t.Errorf("cost_usd = %f", e.Result.CostUSD)
				}
				if e.Result.TurnsUsed != 3 {
					t.Errorf("num_turns = %d", e.Result.TurnsUsed)
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
