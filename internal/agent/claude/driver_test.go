package claude

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func makeResultJSON(sessionID string, cost float64, turns int, isError bool) string {
	event := map[string]any{
		"type":           "result",
		"session_id":     sessionID,
		"total_cost_usd": cost,
		"num_turns":      turns,
		"is_error":       isError,
		"usage": map[string]any{
			"input_tokens":  100,
			"output_tokens": 50,
		},
	}
	b, _ := json.Marshal(event)
	return string(b)
}

func TestParseStdoutExtractsResult(t *testing.T) {
	resultLine := makeResultJSON("sess-1", 0.05, 3, false)

	d := NewDriver(nil, nil)
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader(resultLine+"\n"),
		time.Now(),
		5*time.Minute,
		nil, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", result.SessionID)
	}
	if result.CostUSD != 0.05 {
		t.Errorf("CostUSD = %v, want 0.05", result.CostUSD)
	}
	if result.TurnsUsed != 3 {
		t.Errorf("TurnsUsed = %d, want 3", result.TurnsUsed)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.TokensIn != 100 {
		t.Errorf("TokensIn = %d, want 100", result.TokensIn)
	}
}

func TestParseStdoutErrorResult(t *testing.T) {
	resultLine := makeResultJSON("sess-2", 0.01, 1, true)

	d := NewDriver(nil, nil)
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader(resultLine+"\n"),
		time.Now(),
		5*time.Minute,
		nil, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1 for error result", result.ExitCode)
	}
}

func TestParseStdoutNoResult(t *testing.T) {
	d := NewDriver(nil, nil)
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader("{\"type\":\"system\"}\n"),
		time.Now(),
		5*time.Minute,
		nil, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result when no result event in stream")
	}
}

func TestParseStdoutCallsOnResult(t *testing.T) {
	resultLine := makeResultJSON("sess-3", 0.02, 2, false)

	called := false
	d := NewDriver(nil, nil)
	_, err := d.parseStdout(
		context.Background(),
		strings.NewReader(resultLine+"\n"),
		time.Now(),
		5*time.Minute,
		nil,
		func() { called = true },
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("onResult callback was not called")
	}
}

func TestParseStdoutSkipsMalformedLines(t *testing.T) {
	input := "not json at all\n" + makeResultJSON("sess-4", 0.03, 1, false) + "\n"

	d := NewDriver(nil, nil)
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader(input),
		time.Now(),
		5*time.Minute,
		nil, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result despite malformed first line")
	}
	if result.SessionID != "sess-4" {
		t.Errorf("SessionID = %q, want sess-4", result.SessionID)
	}
}
