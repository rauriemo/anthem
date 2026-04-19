package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
)

// nonLoopModes is the full set of modes in which the tracker facade
// must deny every mutation. ModeLoop is intentionally excluded from
// this list and covered by a separate subtest.
var nonLoopModes = []types.Mode{types.ModeChat, types.ModePlan, types.ModeExecute}

// TestModeGatedTracker_MutationsDeniedOutsideLoop is the contract
// test for Layer 3 of the route-isolation boundary. For every
// non-Loop mode, every mutating tracker method must return
// ErrTrackerMutationDeniedInMode and leave the inner mock
// untouched. If a new mutating method is added to IssueTracker,
// it should be added here explicitly -- the table driver names
// each method so a missed one fails with a clear error.
func TestModeGatedTracker_MutationsDeniedOutsideLoop(t *testing.T) {
	for _, mode := range nonLoopModes {
		mode := mode
		t.Run(string(mode), func(t *testing.T) {
			cases := []struct {
				name string
				call func(t *testing.T, tkr *modeGatedTracker) error
			}{
				{
					name: "CreateIssue",
					call: func(t *testing.T, tkr *modeGatedTracker) error {
						id, err := tkr.CreateIssue(context.Background(), "T", "B", []string{"todo"})
						if id != "" {
							t.Errorf("CreateIssue returned id %q, want empty on deny", id)
						}
						return err
					},
				},
				{
					name: "UpdateStatus",
					call: func(_ *testing.T, tkr *modeGatedTracker) error {
						return tkr.UpdateStatus(context.Background(), "1", "done")
					},
				},
				{
					name: "AddComment",
					call: func(_ *testing.T, tkr *modeGatedTracker) error {
						return tkr.AddComment(context.Background(), "1", "hi")
					},
				},
				{
					name: "AddLabel",
					call: func(_ *testing.T, tkr *modeGatedTracker) error {
						return tkr.AddLabel(context.Background(), "1", "in-progress")
					},
				},
				{
					name: "RemoveLabel",
					call: func(_ *testing.T, tkr *modeGatedTracker) error {
						return tkr.RemoveLabel(context.Background(), "1", "in-progress")
					},
				},
			}

			for _, tc := range cases {
				tc := tc
				t.Run(tc.name, func(t *testing.T) {
					inner := tracker.NewMockTracker([]types.Task{
						{ID: "1", Title: "Parent", Status: types.StatusQueued, Labels: []string{"in-progress"}},
					})
					gated := newModeGatedTracker(inner, func() types.Mode { return mode })

					beforeTasks := len(inner.Tasks)
					beforeLabels := append([]string{}, inner.Tasks[0].Labels...)
					beforeComments := len(inner.Comments["1"])

					err := tc.call(t, gated)
					if !errors.Is(err, ErrTrackerMutationDeniedInMode) {
						t.Errorf("%s err = %v, want ErrTrackerMutationDeniedInMode", tc.name, err)
					}

					if got := len(inner.Tasks); got != beforeTasks {
						t.Errorf("%s: inner.Tasks count = %d, want %d (inner mutated despite deny)", tc.name, got, beforeTasks)
					}
					if got := len(inner.Comments["1"]); got != beforeComments {
						t.Errorf("%s: inner.Comments count = %d, want %d (inner mutated despite deny)", tc.name, got, beforeComments)
					}
					if got := inner.Tasks[0].Labels; !slicesEqual(got, beforeLabels) {
						t.Errorf("%s: inner.Tasks[0].Labels = %v, want %v (inner mutated despite deny)", tc.name, got, beforeLabels)
					}
				})
			}
		})
	}
}

// TestModeGatedTracker_MutationsAllowedInLoop is the dual of the
// deny test: ModeLoop must forward every mutating call to the
// inner tracker exactly once and return its result. If someone
// inverts the mode condition in tracker_gate.go, every subtest
// here fails loudly.
func TestModeGatedTracker_MutationsAllowedInLoop(t *testing.T) {
	t.Run("CreateIssue", func(t *testing.T) {
		inner := tracker.NewMockTracker(nil)
		gated := newModeGatedTracker(inner, func() types.Mode { return types.ModeLoop })

		id, err := gated.CreateIssue(context.Background(), "New", "Body", []string{"todo"})
		if err != nil {
			t.Fatalf("CreateIssue err = %v", err)
		}
		if id == "" {
			t.Error("CreateIssue returned empty id")
		}
		if len(inner.Tasks) != 1 {
			t.Errorf("inner.Tasks = %d, want 1", len(inner.Tasks))
		}
	})

	t.Run("UpdateStatus", func(t *testing.T) {
		inner := tracker.NewMockTracker([]types.Task{{ID: "1", Status: types.StatusQueued}})
		gated := newModeGatedTracker(inner, func() types.Mode { return types.ModeLoop })

		if err := gated.UpdateStatus(context.Background(), "1", string(types.StatusCompleted)); err != nil {
			t.Fatalf("UpdateStatus err = %v", err)
		}
		if inner.Tasks[0].Status != types.StatusCompleted {
			t.Errorf("inner.Tasks[0].Status = %q, want completed", inner.Tasks[0].Status)
		}
	})

	t.Run("AddComment", func(t *testing.T) {
		inner := tracker.NewMockTracker([]types.Task{{ID: "1"}})
		gated := newModeGatedTracker(inner, func() types.Mode { return types.ModeLoop })

		if err := gated.AddComment(context.Background(), "1", "hello"); err != nil {
			t.Fatalf("AddComment err = %v", err)
		}
		if got := inner.Comments["1"]; len(got) != 1 || got[0] != "hello" {
			t.Errorf("inner.Comments[1] = %v, want [hello]", got)
		}
	})

	t.Run("AddLabel", func(t *testing.T) {
		inner := tracker.NewMockTracker([]types.Task{{ID: "1"}})
		gated := newModeGatedTracker(inner, func() types.Mode { return types.ModeLoop })

		if err := gated.AddLabel(context.Background(), "1", "in-progress"); err != nil {
			t.Fatalf("AddLabel err = %v", err)
		}
		if !containsString(inner.Tasks[0].Labels, "in-progress") {
			t.Errorf("inner.Tasks[0].Labels = %v, want to contain 'in-progress'", inner.Tasks[0].Labels)
		}
	})

	t.Run("RemoveLabel", func(t *testing.T) {
		inner := tracker.NewMockTracker([]types.Task{{ID: "1", Labels: []string{"in-progress", "todo"}}})
		gated := newModeGatedTracker(inner, func() types.Mode { return types.ModeLoop })

		if err := gated.RemoveLabel(context.Background(), "1", "in-progress"); err != nil {
			t.Fatalf("RemoveLabel err = %v", err)
		}
		if containsString(inner.Tasks[0].Labels, "in-progress") {
			t.Errorf("inner.Tasks[0].Labels = %v, still contains 'in-progress'", inner.Tasks[0].Labels)
		}
	})
}

// TestModeGatedTracker_ReadsPassThroughInAllModes verifies the
// documented contract that read methods (ListActive, GetTask,
// ShouldThrottle) are never gated -- Chat/Plan/Execute must be
// able to observe tracker state to inject context into prompts,
// they just can't mutate it.
func TestModeGatedTracker_ReadsPassThroughInAllModes(t *testing.T) {
	allModes := append([]types.Mode{types.ModeLoop}, nonLoopModes...)
	for _, mode := range allModes {
		mode := mode
		t.Run(string(mode), func(t *testing.T) {
			inner := tracker.NewMockTracker([]types.Task{
				{ID: "1", Title: "T1", Status: types.StatusQueued},
				{ID: "2", Title: "T2", Status: types.StatusCompleted},
			})
			gated := newModeGatedTracker(inner, func() types.Mode { return mode })

			active, err := gated.ListActive(context.Background())
			if err != nil {
				t.Errorf("ListActive err = %v", err)
			}
			if len(active) != 1 {
				t.Errorf("ListActive returned %d tasks, want 1 (only non-terminal)", len(active))
			}

			task, err := gated.GetTask(context.Background(), "1")
			if err != nil {
				t.Errorf("GetTask err = %v", err)
			}
			if task == nil || task.ID != "1" {
				t.Errorf("GetTask returned %+v, want task ID 1", task)
			}

			throttled, _ := gated.ShouldThrottle()
			if throttled {
				t.Error("ShouldThrottle returned true on fresh mock; want false")
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
