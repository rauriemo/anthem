package orchestrator

import (
	"context"
	"testing"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/types"
)

// TestModeGatedRunner_AppendsDefaultDeniesOutsideLoop is the Layer
// 2 contract test. For every non-Loop mode, Run must forward the
// caller's RunOpts with DefaultInteractiveDeniedTools appended to
// DeniedTools. Any caller-supplied deny entries must be preserved
// at the front so Claude's deny-log keeps showing the caller's
// entries first.
func TestModeGatedRunner_AppendsDefaultDeniesOutsideLoop(t *testing.T) {
	for _, mode := range nonLoopModes {
		mode := mode
		t.Run(string(mode), func(t *testing.T) {
			t.Run("Run no pre-existing denies", func(t *testing.T) {
				var observed types.RunOpts
				inner := agent.NewMockRunner()
				inner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
					observed = opts
					return &types.RunResult{SessionID: "s"}, nil
				}
				gated := newModeGatedRunner(inner, func() types.Mode { return mode })

				_, err := gated.Run(context.Background(), types.RunOpts{Prompt: "x"})
				if err != nil {
					t.Fatalf("Run err = %v", err)
				}
				assertContainsAllDefaults(t, observed.DeniedTools)
			})

			t.Run("Run preserves caller denies at head", func(t *testing.T) {
				var observed types.RunOpts
				inner := agent.NewMockRunner()
				inner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
					observed = opts
					return &types.RunResult{SessionID: "s"}, nil
				}
				gated := newModeGatedRunner(inner, func() types.Mode { return mode })

				caller := []string{"Bash(rm -rf *)", "Write"}
				_, err := gated.Run(context.Background(), types.RunOpts{DeniedTools: caller})
				if err != nil {
					t.Fatalf("Run err = %v", err)
				}

				if len(observed.DeniedTools) < len(caller) {
					t.Fatalf("observed DeniedTools too short: %v", observed.DeniedTools)
				}
				for i, want := range caller {
					if observed.DeniedTools[i] != want {
						t.Errorf("observed.DeniedTools[%d] = %q, want %q (caller deny moved)",
							i, observed.DeniedTools[i], want)
					}
				}
				assertContainsAllDefaults(t, observed.DeniedTools)
			})

			t.Run("Run does not mutate caller slice", func(t *testing.T) {
				var observed types.RunOpts
				inner := agent.NewMockRunner()
				inner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
					observed = opts
					return &types.RunResult{SessionID: "s"}, nil
				}
				gated := newModeGatedRunner(inner, func() types.Mode { return mode })

				caller := []string{"Bash(rm -rf *)"}
				_, err := gated.Run(context.Background(), types.RunOpts{DeniedTools: caller})
				if err != nil {
					t.Fatalf("Run err = %v", err)
				}
				if len(caller) != 1 || caller[0] != "Bash(rm -rf *)" {
					t.Errorf("caller DeniedTools mutated to %v, want [Bash(rm -rf *)]", caller)
				}
				_ = observed
			})

			t.Run("Continue appends defaults", func(t *testing.T) {
				var observed types.ContinueOpts
				inner := agent.NewMockRunner()
				inner.ContinueFunc = func(_ context.Context, _ string, _ string, opts types.ContinueOpts) (*types.RunResult, error) {
					observed = opts
					return &types.RunResult{SessionID: "s"}, nil
				}
				gated := newModeGatedRunner(inner, func() types.Mode { return mode })

				_, err := gated.Continue(context.Background(), "sess", "hi", types.ContinueOpts{})
				if err != nil {
					t.Fatalf("Continue err = %v", err)
				}
				assertContainsAllDefaults(t, observed.DeniedTools)
			})

			t.Run("Continue preserves caller denies", func(t *testing.T) {
				var observed types.ContinueOpts
				inner := agent.NewMockRunner()
				inner.ContinueFunc = func(_ context.Context, _ string, _ string, opts types.ContinueOpts) (*types.RunResult, error) {
					observed = opts
					return &types.RunResult{SessionID: "s"}, nil
				}
				gated := newModeGatedRunner(inner, func() types.Mode { return mode })

				caller := []string{"Write"}
				_, err := gated.Continue(context.Background(), "sess", "hi", types.ContinueOpts{DeniedTools: caller})
				if err != nil {
					t.Fatalf("Continue err = %v", err)
				}
				if len(observed.DeniedTools) == 0 || observed.DeniedTools[0] != "Write" {
					t.Errorf("observed.DeniedTools[0] = %v, want 'Write' (caller deny preserved at head)",
						observed.DeniedTools)
				}
				assertContainsAllDefaults(t, observed.DeniedTools)
			})
		})
	}
}

// TestModeGatedRunner_LoopForwardsUnchanged pins the Loop-mode
// pass-through contract: no default denies are injected and the
// caller's DeniedTools slice is forwarded byte-for-byte. Tests
// that someone hasn't accidentally inverted the mode check or
// widened the deny list.
func TestModeGatedRunner_LoopForwardsUnchanged(t *testing.T) {
	t.Run("Run", func(t *testing.T) {
		var observed types.RunOpts
		inner := agent.NewMockRunner()
		inner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
			observed = opts
			return &types.RunResult{SessionID: "s"}, nil
		}
		gated := newModeGatedRunner(inner, func() types.Mode { return types.ModeLoop })

		caller := []string{"Bash(rm -rf *)"}
		_, err := gated.Run(context.Background(), types.RunOpts{DeniedTools: caller})
		if err != nil {
			t.Fatalf("Run err = %v", err)
		}
		if !slicesEqual(observed.DeniedTools, caller) {
			t.Errorf("observed.DeniedTools = %v, want %v (Loop must forward unchanged)",
				observed.DeniedTools, caller)
		}
	})

	t.Run("Continue", func(t *testing.T) {
		var observed types.ContinueOpts
		inner := agent.NewMockRunner()
		inner.ContinueFunc = func(_ context.Context, _ string, _ string, opts types.ContinueOpts) (*types.RunResult, error) {
			observed = opts
			return &types.RunResult{SessionID: "s"}, nil
		}
		gated := newModeGatedRunner(inner, func() types.Mode { return types.ModeLoop })

		caller := []string{"WebFetch"} // would normally be denied outside Loop
		_, err := gated.Continue(context.Background(), "sess", "hi", types.ContinueOpts{DeniedTools: caller})
		if err != nil {
			t.Fatalf("Continue err = %v", err)
		}
		if !slicesEqual(observed.DeniedTools, caller) {
			t.Errorf("observed.DeniedTools = %v, want %v (Loop must forward unchanged)",
				observed.DeniedTools, caller)
		}
	})

	t.Run("Run with empty DeniedTools stays empty", func(t *testing.T) {
		var observed types.RunOpts
		inner := agent.NewMockRunner()
		inner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
			observed = opts
			return &types.RunResult{SessionID: "s"}, nil
		}
		gated := newModeGatedRunner(inner, func() types.Mode { return types.ModeLoop })

		_, err := gated.Run(context.Background(), types.RunOpts{})
		if err != nil {
			t.Fatalf("Run err = %v", err)
		}
		if len(observed.DeniedTools) != 0 {
			t.Errorf("observed.DeniedTools = %v, want [] (Loop with no denies stays empty)",
				observed.DeniedTools)
		}
	})
}

// TestModeGatedRunner_KillPassesThrough pins the Kill pass-through
// contract across every mode -- killing a process is not a tool
// call, so there is no mode-gating to do.
func TestModeGatedRunner_KillPassesThrough(t *testing.T) {
	allModes := append([]types.Mode{types.ModeLoop}, nonLoopModes...)
	for _, mode := range allModes {
		mode := mode
		t.Run(string(mode), func(t *testing.T) {
			var observedPID int
			inner := agent.NewMockRunner()
			inner.KillFunc = func(pid int) error {
				observedPID = pid
				return nil
			}
			gated := newModeGatedRunner(inner, func() types.Mode { return mode })

			if err := gated.Kill(1234); err != nil {
				t.Errorf("Kill err = %v", err)
			}
			if observedPID != 1234 {
				t.Errorf("observedPID = %d, want 1234 (Kill did not pass through)", observedPID)
			}
		})
	}
}

// assertContainsAllDefaults fails the test if got is missing any
// entry from DefaultInteractiveDeniedTools. The exact ordering is
// part of the contract for deny-log readability, so it is also
// checked.
func assertContainsAllDefaults(t *testing.T, got []string) {
	t.Helper()
	for _, want := range DefaultInteractiveDeniedTools {
		if !containsString(got, want) {
			t.Errorf("DeniedTools missing %q; got %v", want, got)
		}
	}
}
