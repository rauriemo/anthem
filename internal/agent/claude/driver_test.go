package claude

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/types"
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

func makeResultJSONWithText(sessionID string, resultText any) string {
	event := map[string]any{
		"type":           "result",
		"session_id":     sessionID,
		"total_cost_usd": 0.01,
		"num_turns":      1,
		"is_error":       false,
		"result":         resultText,
		"usage": map[string]any{
			"input_tokens":  10,
			"output_tokens": 5,
		},
	}
	b, _ := json.Marshal(event)
	return string(b)
}

func TestParseStdoutExtractsResult(t *testing.T) {
	resultLine := makeResultJSON("sess-1", 0.05, 3, false)

	d := NewDriver(nil, nil, "")
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader(resultLine+"\n"),
		time.Now(),
		5*time.Minute,
		nil, nil, nil,
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

	d := NewDriver(nil, nil, "")
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader(resultLine+"\n"),
		time.Now(),
		5*time.Minute,
		nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1 for error result", result.ExitCode)
	}
}

func TestParseStdoutNoResult(t *testing.T) {
	d := NewDriver(nil, nil, "")
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader("{\"type\":\"system\"}\n"),
		time.Now(),
		5*time.Minute,
		nil, nil, nil,
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
	d := NewDriver(nil, nil, "")
	_, err := d.parseStdout(
		context.Background(),
		strings.NewReader(resultLine+"\n"),
		time.Now(),
		5*time.Minute,
		nil, nil,
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

	d := NewDriver(nil, nil, "")
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader(input),
		time.Now(),
		5*time.Minute,
		nil, nil, nil,
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

// captureArgs uses a mock ProcessManager to capture the args passed to exec.Cmd.
func captureArgs(t *testing.T, fn func(d *Driver)) []string {
	t.Helper()
	var captured []string
	pm := &MockProcessManager{
		StartFunc: func(cmd *exec.Cmd) error {
			captured = cmd.Args[1:] // skip "claude" binary name
			return errCapture
		},
		TerminateFunc: func(_ *exec.Cmd) error { return nil },
		KillFunc:      func(_ *exec.Cmd) error { return nil },
	}
	d := NewDriver(pm, nil, "")
	fn(d)
	return captured
}

// sentinel error to abort execute after capturing args
var errCapture = &captureError{}

type captureError struct{}

func (e *captureError) Error() string { return "capture" }

func containsArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func containsArgPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestRunDefaultPermissionMode(t *testing.T) {
	args := captureArgs(t, func(d *Driver) {
		_, _ = d.Run(context.Background(), types.RunOpts{
			Prompt: "test",
		})
	})

	if containsArg(args, "--dangerously-skip-permissions") {
		t.Error("should not use --dangerously-skip-permissions by default")
	}
	if !containsArgPair(args, "--permission-mode", "dontAsk") {
		t.Error("should use --permission-mode dontAsk by default")
	}
}

func TestRunBypassPermissions(t *testing.T) {
	args := captureArgs(t, func(d *Driver) {
		_, _ = d.Run(context.Background(), types.RunOpts{
			Prompt:         "test",
			PermissionMode: "bypassPermissions",
		})
	})

	if !containsArg(args, "--dangerously-skip-permissions") {
		t.Error("should use --dangerously-skip-permissions when bypassPermissions")
	}
	if containsArgPair(args, "--permission-mode", "dontAsk") {
		t.Error("should not use --permission-mode when bypassPermissions")
	}
}

func TestRunDeniedTools(t *testing.T) {
	args := captureArgs(t, func(d *Driver) {
		_, _ = d.Run(context.Background(), types.RunOpts{
			Prompt:      "test",
			DeniedTools: []string{"Bash", "Write"},
		})
	})

	if !containsArgPair(args, "--disallowedTools", "Bash") {
		t.Error("should include --disallowedTools Bash")
	}
	if !containsArgPair(args, "--disallowedTools", "Write") {
		t.Error("should include --disallowedTools Write")
	}
}

func TestContinuePassesOpts(t *testing.T) {
	var capturedDir string
	var capturedArgs []string
	pm := &MockProcessManager{
		StartFunc: func(cmd *exec.Cmd) error {
			capturedArgs = cmd.Args[1:]
			capturedDir = cmd.Dir
			return errCapture
		},
		TerminateFunc: func(_ *exec.Cmd) error { return nil },
		KillFunc:      func(_ *exec.Cmd) error { return nil },
	}
	d := NewDriver(pm, nil, "")

	_, _ = d.Continue(context.Background(), "sess-123", "continue prompt", types.ContinueOpts{
		WorkspacePath:  "/tmp/ws",
		PermissionMode: "bypassPermissions",
		AllowedTools:   []string{"Read", "Grep"},
	})

	if capturedDir != "/tmp/ws" {
		t.Errorf("Dir = %q, want /tmp/ws", capturedDir)
	}
	if !containsArg(capturedArgs, "--dangerously-skip-permissions") {
		t.Error("should use --dangerously-skip-permissions")
	}
	if !containsArgPair(capturedArgs, "--resume", "sess-123") {
		t.Error("should include --resume sess-123")
	}
	if !containsArgPair(capturedArgs, "--allowedTools", "Read") {
		t.Error("should include --allowedTools Read")
	}
	if !containsArgPair(capturedArgs, "--allowedTools", "Grep") {
		t.Error("should include --allowedTools Grep")
	}
}

func TestContinueDefaultPermissionMode(t *testing.T) {
	args := captureArgs(t, func(d *Driver) {
		_, _ = d.Continue(context.Background(), "sess-1", "prompt", types.ContinueOpts{})
	})

	if containsArg(args, "--dangerously-skip-permissions") {
		t.Error("should not use --dangerously-skip-permissions by default")
	}
	if !containsArgPair(args, "--permission-mode", "dontAsk") {
		t.Error("should use --permission-mode dontAsk by default")
	}
}

func TestExtractResultTextString(t *testing.T) {
	resultLine := makeResultJSONWithText("sess-str", "hello world")

	d := NewDriver(nil, nil, "")
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader(resultLine+"\n"),
		time.Now(),
		5*time.Minute,
		nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "hello world" {
		t.Errorf("Output = %q, want %q", result.Output, "hello world")
	}
}

func TestExtractResultTextContentBlocks(t *testing.T) {
	blocks := []map[string]string{
		{"type": "text", "text": "first "},
		{"type": "text", "text": "second"},
	}
	resultLine := makeResultJSONWithText("sess-blocks", blocks)

	d := NewDriver(nil, nil, "")
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader(resultLine+"\n"),
		time.Now(),
		5*time.Minute,
		nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "first second" {
		t.Errorf("Output = %q, want %q", result.Output, "first second")
	}
}

func TestExtractResultTextEmpty(t *testing.T) {
	// Result event with no "result" field
	resultLine := makeResultJSON("sess-empty", 0.01, 1, false)

	d := NewDriver(nil, nil, "")
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader(resultLine+"\n"),
		time.Now(),
		5*time.Minute,
		nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "" {
		t.Errorf("Output = %q, want empty", result.Output)
	}
}

func TestMergeOutputFromStreamBuffer(t *testing.T) {
	t.Parallel()
	r := &types.RunResult{Output: "keep"}
	mergeOutputFromStreamBuffer(r, `{"x":1}`)
	if r.Output != "keep" {
		t.Errorf("expected non-empty Output unchanged, got %q", r.Output)
	}
	r2 := &types.RunResult{Output: "  \n"}
	mergeOutputFromStreamBuffer(r2, `{"reasoning":"ok","actions":[]}`)
	if r2.Output != `{"reasoning":"ok","actions":[]}` {
		t.Errorf("expected stream fallback, got %q", r2.Output)
	}
	mergeOutputFromStreamBuffer(nil, "x")
}

func TestParseStdoutEmptyResultUsesStreamMerge(t *testing.T) {
	jsonOut := `{"reasoning":"ok","actions":[]}`
	ev, _ := json.Marshal(map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"delta": map[string]any{"type": "text_delta", "text": jsonOut},
		},
	})
	resultLine := makeResultJSON("sess-fb", 0.01, 1, false)
	input := string(ev) + "\n" + resultLine + "\n"

	var acc strings.Builder
	d := NewDriver(nil, nil, "")
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader(input),
		time.Now(),
		5*time.Minute,
		nil,
		func(s string) { acc.WriteString(s) },
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected result")
	}
	if result.Output != "" {
		t.Fatalf("expected empty Output before merge, got %q", result.Output)
	}
	mergeOutputFromStreamBuffer(result, acc.String())
	if result.Output != jsonOut {
		t.Fatalf("after merge Output = %q, want %q", result.Output, jsonOut)
	}
}

func TestParseStdoutCallsOnStream(t *testing.T) {
	assistant1, _ := json.Marshal(map[string]any{"type": "assistant", "content": "Hello "})
	assistant2, _ := json.Marshal(map[string]any{"type": "assistant", "content": "world"})
	assistantEmpty, _ := json.Marshal(map[string]any{"type": "assistant", "content": ""})
	resultLine := makeResultJSON("sess-stream", 0.01, 1, false)

	input := string(assistant1) + "\n" + string(assistantEmpty) + "\n" + string(assistant2) + "\n" + resultLine + "\n"

	var deltas []string
	onStream := func(delta string) {
		deltas = append(deltas, delta)
	}

	d := NewDriver(nil, nil, "")
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader(input),
		time.Now(),
		5*time.Minute,
		nil, onStream, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(deltas) != 2 {
		t.Fatalf("expected 2 stream deltas, got %d: %v", len(deltas), deltas)
	}
	if deltas[0] != "Hello " {
		t.Errorf("delta[0] = %q, want %q", deltas[0], "Hello ")
	}
	if deltas[1] != "world" {
		t.Errorf("delta[1] = %q, want %q", deltas[1], "world")
	}
}

func TestParseStdoutCallsOnStreamForStreamEventTextDelta(t *testing.T) {
	ev1, _ := json.Marshal(map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"delta": map[string]any{"type": "text_delta", "text": "Hi"},
		},
	})
	ev2, _ := json.Marshal(map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"delta": map[string]any{"type": "text_delta", "text": " there"},
		},
	})
	resultLine := makeResultJSON("sess-stream2", 0.01, 1, false)
	input := string(ev1) + "\n" + string(ev2) + "\n" + resultLine + "\n"

	var deltas []string
	onStream := func(delta string) {
		deltas = append(deltas, delta)
	}

	d := NewDriver(nil, nil, "")
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader(input),
		time.Now(),
		5*time.Minute,
		nil, onStream, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(deltas) != 2 || deltas[0] != "Hi" || deltas[1] != " there" {
		t.Fatalf("deltas = %v, want [Hi  there]", deltas)
	}
}

func TestNewDriverCustomBinary(t *testing.T) {
	tests := []struct {
		name   string
		binary string
		want   string
	}{
		{"default", "", "claude"},
		{"custom", "my-llm", "my-llm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDriver(nil, nil, tt.binary)
			if d.binary != tt.want {
				t.Errorf("binary = %q, want %q", d.binary, tt.want)
			}
		})
	}
}

func TestRunUsesCustomBinary(t *testing.T) {
	var capturedBinary string
	pm := &MockProcessManager{
		StartFunc: func(cmd *exec.Cmd) error {
			capturedBinary = cmd.Args[0]
			return errCapture
		},
		TerminateFunc: func(_ *exec.Cmd) error { return nil },
		KillFunc:      func(_ *exec.Cmd) error { return nil },
	}
	d := NewDriver(pm, nil, "my-custom-llm")
	_, _ = d.Run(context.Background(), types.RunOpts{Prompt: "test"})

	if capturedBinary != "my-custom-llm" {
		t.Errorf("binary = %q, want my-custom-llm", capturedBinary)
	}
}

func TestParseStdoutOnStreamNilSafe(t *testing.T) {
	assistant1, _ := json.Marshal(map[string]any{"type": "assistant", "content": "Hello"})
	resultLine := makeResultJSON("sess-nil", 0.01, 1, false)
	input := string(assistant1) + "\n" + resultLine + "\n"

	d := NewDriver(nil, nil, "")
	result, err := d.parseStdout(
		context.Background(),
		strings.NewReader(input),
		time.Now(),
		5*time.Minute,
		nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}
