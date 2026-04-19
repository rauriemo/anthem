package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/types"
)

// Compile-time assertion: MockRunner must satisfy AgentRunner.
// This is the core guarantee of the agent facade — downstream packages
// (orchestrator, tests) consume AgentRunner via interface-injection and
// rely on MockRunner being a drop-in for the real claude.Runner.
var _ AgentRunner = (*MockRunner)(nil)

func TestNewMockRunner_DefaultsReturnOKResult(t *testing.T) {
	r := NewMockRunner()
	ctx := context.Background()

	runRes, err := r.Run(ctx, types.RunOpts{Prompt: "hello"})
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
	if runRes == nil {
		t.Fatal("Run returned nil result")
	}
	if runRes.SessionID != "mock-session" {
		t.Errorf("Run SessionID = %q, want %q", runRes.SessionID, "mock-session")
	}
	if runRes.ExitCode != 0 {
		t.Errorf("Run ExitCode = %d, want 0", runRes.ExitCode)
	}
	if runRes.Output == "" {
		t.Error("Run returned empty Output; default mock should produce non-empty output so downstream code exercises parsing paths")
	}
	if runRes.Duration <= 0 {
		t.Errorf("Run Duration = %v, want > 0", runRes.Duration)
	}

	contRes, err := r.Continue(ctx, "prev-session", "keep going", types.ContinueOpts{})
	if err != nil {
		t.Fatalf("Continue returned unexpected error: %v", err)
	}
	if contRes == nil || contRes.SessionID != "mock-session" {
		t.Errorf("Continue SessionID = %+v, want mock-session", contRes)
	}
	if contRes.ExitCode != 0 {
		t.Errorf("Continue ExitCode = %d, want 0", contRes.ExitCode)
	}

	if err := r.Kill(1234); err != nil {
		t.Errorf("Kill returned unexpected error: %v", err)
	}
}

func TestMockRunner_DelegatesToOverridingFuncs(t *testing.T) {
	sentinelRun := errors.New("run error")
	sentinelContinue := errors.New("continue error")
	sentinelKill := errors.New("kill error")

	var seenRunOpts types.RunOpts
	var seenSessionID, seenPrompt string
	var seenContinueOpts types.ContinueOpts
	var seenKillPID int

	r := &MockRunner{
		RunFunc: func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
			seenRunOpts = opts
			return nil, sentinelRun
		},
		ContinueFunc: func(_ context.Context, sid string, prompt string, opts types.ContinueOpts) (*types.RunResult, error) {
			seenSessionID = sid
			seenPrompt = prompt
			seenContinueOpts = opts
			return nil, sentinelContinue
		},
		KillFunc: func(pid int) error {
			seenKillPID = pid
			return sentinelKill
		},
	}

	wantRunOpts := types.RunOpts{WorkspacePath: "/tmp/ws", Prompt: "hi", MaxTurns: 5}
	if _, err := r.Run(context.Background(), wantRunOpts); !errors.Is(err, sentinelRun) {
		t.Fatalf("Run error = %v, want sentinelRun", err)
	}
	if seenRunOpts.WorkspacePath != wantRunOpts.WorkspacePath || seenRunOpts.Prompt != wantRunOpts.Prompt || seenRunOpts.MaxTurns != wantRunOpts.MaxTurns {
		t.Errorf("Run opts were not forwarded verbatim: got %+v want %+v", seenRunOpts, wantRunOpts)
	}

	wantContinueOpts := types.ContinueOpts{WorkspacePath: "/tmp/ws", StallTimeoutMS: 30000}
	if _, err := r.Continue(context.Background(), "sess-1", "keep going", wantContinueOpts); !errors.Is(err, sentinelContinue) {
		t.Fatalf("Continue error = %v, want sentinelContinue", err)
	}
	if seenSessionID != "sess-1" || seenPrompt != "keep going" {
		t.Errorf("Continue sessionID/prompt not forwarded: sid=%q prompt=%q", seenSessionID, seenPrompt)
	}
	if seenContinueOpts.WorkspacePath != wantContinueOpts.WorkspacePath || seenContinueOpts.StallTimeoutMS != wantContinueOpts.StallTimeoutMS {
		t.Errorf("Continue opts not forwarded: got %+v want %+v", seenContinueOpts, wantContinueOpts)
	}

	if err := r.Kill(9876); !errors.Is(err, sentinelKill) {
		t.Fatalf("Kill error = %v, want sentinelKill", err)
	}
	if seenKillPID != 9876 {
		t.Errorf("Kill PID = %d, want 9876", seenKillPID)
	}
}

func TestMockRunner_CustomResultShape(t *testing.T) {
	// Regression guard: downstream code reads TokensIn/TokensOut/TurnsUsed/CostUSD
	// fields from RunResult. A test that stubs those fields must observe them
	// end-to-end through the facade unchanged.
	r := &MockRunner{
		RunFunc: func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
			return &types.RunResult{
				SessionID: "s-42",
				ExitCode:  1,
				Output:    "custom",
				TokensIn:  123,
				TokensOut: 456,
				TurnsUsed: 7,
				CostUSD:   0.25,
				Duration:  2 * time.Second,
			}, nil
		},
	}

	res, err := r.Run(context.Background(), types.RunOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SessionID != "s-42" || res.ExitCode != 1 || res.Output != "custom" {
		t.Errorf("result identity fields mutated: %+v", res)
	}
	if res.TokensIn != 123 || res.TokensOut != 456 || res.TurnsUsed != 7 {
		t.Errorf("token/turn fields mutated: in=%d out=%d turns=%d", res.TokensIn, res.TokensOut, res.TurnsUsed)
	}
	if res.CostUSD != 0.25 {
		t.Errorf("CostUSD = %v, want 0.25", res.CostUSD)
	}
	if res.Duration != 2*time.Second {
		t.Errorf("Duration = %v, want 2s", res.Duration)
	}
}

func TestMockRunner_ContextPropagation(t *testing.T) {
	// The facade must forward ctx to the underlying func so cancellation /
	// deadlines work in integration tests that construct a canceled context.
	type ctxKey string
	const key ctxKey = "facade-test"
	parent := context.WithValue(context.Background(), key, "seen")

	var runCtx context.Context
	var contCtx context.Context

	r := &MockRunner{
		RunFunc: func(ctx context.Context, _ types.RunOpts) (*types.RunResult, error) {
			runCtx = ctx
			return &types.RunResult{}, nil
		},
		ContinueFunc: func(ctx context.Context, _ string, _ string, _ types.ContinueOpts) (*types.RunResult, error) {
			contCtx = ctx
			return &types.RunResult{}, nil
		},
		KillFunc: func(_ int) error { return nil },
	}

	if _, err := r.Run(parent, types.RunOpts{}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if _, err := r.Continue(parent, "s", "p", types.ContinueOpts{}); err != nil {
		t.Fatalf("Continue error: %v", err)
	}
	if runCtx.Value(key) != "seen" {
		t.Errorf("Run did not forward parent ctx")
	}
	if contCtx.Value(key) != "seen" {
		t.Errorf("Continue did not forward parent ctx")
	}
}
