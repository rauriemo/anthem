package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/types"
)

func mockRunnerWithOutput(output string) *agent.MockRunner {
	r := agent.NewMockRunner()
	r.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "orch-sess-1",
			ExitCode:  0,
			Output:    output,
			TokensIn:  50,
			TokensOut: 30,
			Duration:  time.Millisecond,
		}, nil
	}
	return r
}

func TestStart_ParsesActions(t *testing.T) {
	output := `{"reasoning": "task 1 is ready, task 2 blocked", "actions": [{"type": "dispatch", "task_id": "1"}, {"type": "skip", "task_id": "2", "reason": "blocked by 1"}]}`
	runner := mockRunnerWithOutput(output)

	oa := NewOrchestratorAgent(runner, "", 10000, testLogger())
	actions, err := oa.Start(context.Background(), StateSnapshot{
		Tasks: []TaskSummary{{ID: "1", Title: "T1", Status: "queued"}, {ID: "2", Title: "T2", Status: "queued"}},
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}
	if actions[0].Type != ActionDispatch || actions[0].TaskID != "1" {
		t.Errorf("action[0] = %+v, want dispatch task_id=1", actions[0])
	}
	if actions[1].Type != ActionSkip || actions[1].TaskID != "2" {
		t.Errorf("action[1] = %+v, want skip task_id=2", actions[1])
	}
	if oa.sessionID != "orch-sess-1" {
		t.Errorf("sessionID = %q, want orch-sess-1", oa.sessionID)
	}
	if oa.totalTokens != 80 {
		t.Errorf("totalTokens = %d, want 80", oa.totalTokens)
	}
}

func TestConsult_ContinuesSession(t *testing.T) {
	output := `{"reasoning": "continuing", "actions": [{"type": "dispatch", "task_id": "3"}]}`

	var capturedSessionID string
	runner := agent.NewMockRunner()
	runner.ContinueFunc = func(_ context.Context, sessionID string, _ string, _ types.ContinueOpts) (*types.RunResult, error) {
		capturedSessionID = sessionID
		return &types.RunResult{
			SessionID: "orch-sess-1",
			ExitCode:  0,
			Output:    output,
			TokensIn:  20,
			TokensOut: 10,
			Duration:  time.Millisecond,
		}, nil
	}

	oa := NewOrchestratorAgent(runner, "", 10000, testLogger())
	oa.sessionID = "orch-sess-1"
	oa.totalTokens = 50

	actions, err := oa.Consult(context.Background(), StateSnapshot{
		Tasks: []TaskSummary{{ID: "3", Title: "T3", Status: "queued"}},
	})
	if err != nil {
		t.Fatalf("Consult() error: %v", err)
	}
	if capturedSessionID != "orch-sess-1" {
		t.Errorf("Continue called with sessionID = %q, want orch-sess-1", capturedSessionID)
	}
	if len(actions) != 1 || actions[0].TaskID != "3" {
		t.Errorf("unexpected actions: %+v", actions)
	}
	if oa.totalTokens != 80 {
		t.Errorf("totalTokens = %d, want 80 (50+20+10)", oa.totalTokens)
	}
}

func TestConsult_RefreshesOnTokenLimit(t *testing.T) {
	output := `{"reasoning": "fresh start", "actions": [{"type": "close_wave"}]}`

	runCalls := 0
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		runCalls++
		return &types.RunResult{
			SessionID: "orch-sess-new",
			ExitCode:  0,
			Output:    output,
			TokensIn:  30,
			TokensOut: 20,
			Duration:  time.Millisecond,
		}, nil
	}

	oa := NewOrchestratorAgent(runner, "", 100, testLogger())
	oa.sessionID = "orch-sess-old"
	oa.totalTokens = 200 // over the 100 limit

	actions, err := oa.Consult(context.Background(), StateSnapshot{})
	if err != nil {
		t.Fatalf("Consult() error: %v", err)
	}

	// Should have called Refresh (resetting session) then Start (calling Run)
	if runCalls != 1 {
		t.Errorf("Run called %d times, want 1 (after refresh)", runCalls)
	}
	if oa.sessionID != "orch-sess-new" {
		t.Errorf("sessionID = %q, want orch-sess-new", oa.sessionID)
	}
	if len(actions) != 1 || actions[0].Type != ActionCloseWave {
		t.Errorf("unexpected actions: %+v", actions)
	}
}

func TestParseActions_FencedJSON(t *testing.T) {
	output := "Here is my response:\n```json\n{\"reasoning\": \"ok\", \"actions\": [{\"type\": \"dispatch\", \"task_id\": \"5\"}]}\n```"
	actions, err := parseActions(output)
	if err != nil {
		t.Fatalf("parseActions() error: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	if actions[0].Type != ActionDispatch || actions[0].TaskID != "5" {
		t.Errorf("action = %+v, want dispatch task_id=5", actions[0])
	}
}

func TestParseActions_RawJSON(t *testing.T) {
	output := `Some text before {"reasoning": "plan", "actions": [{"type": "comment", "task_id": "7", "body": "looks good"}]} and after`
	actions, err := parseActions(output)
	if err != nil {
		t.Fatalf("parseActions() error: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	if actions[0].Type != ActionComment || actions[0].Body != "looks good" {
		t.Errorf("action = %+v, want comment with body", actions[0])
	}
}

func TestParseActions_NoJSON(t *testing.T) {
	output := "This response has no JSON at all, just plain text."
	_, err := parseActions(output)
	if err == nil {
		t.Fatal("expected error for output with no JSON")
	}
}

func TestConsultWithRepair_FirstAttemptFails(t *testing.T) {
	callCount := 0
	runner := agent.NewMockRunner()

	// Start returns a valid session
	runner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "sess-repair",
			ExitCode:  0,
			Output:    "not json at all",
			TokensIn:  10,
			TokensOut: 5,
			Duration:  time.Millisecond,
		}, nil
	}

	runner.ContinueFunc = func(_ context.Context, _ string, prompt string, _ types.ContinueOpts) (*types.RunResult, error) {
		callCount++
		if callCount == 1 {
			// First Consult call — invalid output
			return &types.RunResult{
				SessionID: "sess-repair",
				ExitCode:  0,
				Output:    "not json",
				TokensIn:  10,
				TokensOut: 5,
				Duration:  time.Millisecond,
			}, nil
		}
		// Repair call — valid output
		return &types.RunResult{
			SessionID: "sess-repair",
			ExitCode:  0,
			Output:    `{"reasoning": "repaired", "actions": [{"type": "dispatch", "task_id": "9"}]}`,
			TokensIn:  15,
			TokensOut: 10,
			Duration:  time.Millisecond,
		}, nil
	}

	oa := NewOrchestratorAgent(runner, "", 10000, testLogger())
	oa.sessionID = "sess-repair"
	oa.totalTokens = 50

	actions, err := oa.ConsultWithRepair(context.Background(), StateSnapshot{
		Tasks: []TaskSummary{{ID: "9", Title: "T9", Status: "queued"}},
	})
	if err != nil {
		t.Fatalf("ConsultWithRepair() error: %v", err)
	}
	if len(actions) != 1 || actions[0].TaskID != "9" {
		t.Errorf("unexpected actions: %+v", actions)
	}
	if callCount != 2 {
		t.Errorf("Continue called %d times, want 2 (consult + repair)", callCount)
	}
}

func TestBuildSystemPrompt_ContainsNewActions(t *testing.T) {
	prompt := buildSystemPrompt("")

	tests := []struct {
		name    string
		content string
	}{
		{"reply action", "- reply: Send a message back to the user"},
		{"request_maintenance action", "- request_maintenance: Propose a maintenance action"},
		{"channel messages section", "## Channel Messages"},
		{"multi-format section", "## Multi-Format Input"},
		{"create_subtasks not schema-only", "- create_subtasks: Create subtasks as new tracker issues"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(prompt, tt.content) {
				t.Errorf("system prompt missing %q", tt.content)
			}
		})
	}

	if strings.Contains(prompt, "create_subtasks: (schema-only)") {
		t.Error("create_subtasks should no longer be marked as schema-only")
	}
}

func TestConsultWithRepair_BothFail(t *testing.T) {
	runner := agent.NewMockRunner()
	runner.ContinueFunc = func(_ context.Context, _ string, _ string, _ types.ContinueOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "sess-fail",
			ExitCode:  0,
			Output:    "still not json",
			TokensIn:  10,
			TokensOut: 5,
			Duration:  time.Millisecond,
		}, nil
	}

	oa := NewOrchestratorAgent(runner, "", 10000, testLogger())
	oa.sessionID = "sess-fail"
	oa.totalTokens = 50

	actions, err := oa.ConsultWithRepair(context.Background(), StateSnapshot{})
	if err != nil {
		t.Fatalf("ConsultWithRepair() should not return error on fallback, got: %v", err)
	}
	if actions != nil {
		t.Errorf("expected nil actions for fallback, got: %+v", actions)
	}
}
