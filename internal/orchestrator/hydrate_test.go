package orchestrator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/execute"
	"github.com/rauriemo/anthem/internal/plans"
	"github.com/rauriemo/anthem/internal/plans/runs"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/workspace"
)

// newHydrateTestOrch builds an Orchestrator whose projectSlug()
// resolves to repoSlug (via cfg.Tracker.Repo = "owner/"+repoSlug)
// and whose planStore is rooted at home (shared across instances).
// The plan store root is ~home/.anthem/plans so multiple orchestrators
// can point at the same on-disk tree to simulate a real multi-agent
// setup where every anthem shares ~/.anthem/plans/.
func newHydrateTestOrch(t *testing.T, home, repoSlug string) *Orchestrator {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "owner/" + repoSlug
	cfg.Tracker.Labels.Active = []string{"todo"}

	store, err := plans.NewStore(home)
	if err != nil {
		t.Fatalf("plans.NewStore: %v", err)
	}

	tasks := []types.Task{{ID: "1", Title: "seed", Status: types.StatusQueued, Labels: []string{"todo"}, CreatedAt: time.Now()}}
	trk := tracker.NewMockTracker(tasks)
	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchAgent := NewOrchestratorAgent(agent.NewMockRunner(), "", "", 100000, 10, 25, 10, 5, testLogger())

	o := New(Opts{
		Config:         &cfg,
		TemplateBody:   "{{.issue.title}}",
		Tracker:        trk,
		Runner:         agent.NewMockRunner(),
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		OrchAgent:      orchAgent,
		ChannelManager: mgr,
	})
	o.planStore = store
	return o
}

// seedRun writes a minimal two-event run log (run_started + a
// step_completed tail) under ~home/.anthem/plans/<projectSlug>/
// <planName>.md.runs/run_<ts>_gen<gen>.jsonl and returns the plan_id
// used in the header. Uses runs.Append so the file path/name matches
// what listPlanRuns will scan for.
func seedRun(t *testing.T, home, projectSlug, planName, planID string, startedAt time.Time) {
	t.Helper()
	plansRoot := filepath.Join(home, ".anthem", "plans", projectSlug)
	planPath := filepath.Join(plansRoot, planName+".md")
	runPath := runs.NewRunLogPath(planPath, 1, startedAt)

	startEvt := runs.RunStartedEvent(planID, 1, &execute.ExecutionPlan{
		Metadata: execute.PlanMetadata{Title: planName, PlanGeneration: 1},
		Steps:    []execute.PlanStep{{ID: "s1", AgentID: "orchestrator", Description: "noop"}},
	}, projectSlug)
	startEvt.Timestamp = startedAt
	if err := runs.Append(runPath, startEvt); err != nil {
		t.Fatalf("append run_started for %s: %v", projectSlug, err)
	}

	doneEvt := runs.StepCompletedEvent("s1", "orchestrator", nil)
	doneEvt.Timestamp = startedAt.Add(5 * time.Second)
	if err := runs.Append(runPath, doneEvt); err != nil {
		t.Fatalf("append step_completed for %s: %v", projectSlug, err)
	}
}

// decodeRunHistory unwraps an execution.run_history OutgoingMessage
// and returns the embedded Snapshot slice. Fails the test if the
// shape doesn't match.
func decodeRunHistory(t *testing.T, msg channel.OutgoingMessage) []runs.Snapshot {
	t.Helper()
	if msg.EventType != execute.EventRunHistory {
		t.Fatalf("wrong event type: %q, want %q", msg.EventType, execute.EventRunHistory)
	}
	var payload struct {
		Runs []runs.Snapshot `json:"runs"`
	}
	if err := json.Unmarshal([]byte(msg.Text), &payload); err != nil {
		t.Fatalf("unmarshal run_history text: %v", err)
	}
	return payload.Runs
}

// TestHistoryEventsForConnect_ScopedToOwnProject verifies that an
// anthem's hydrate frame returns ONLY its own project's runs, even
// when the shared ~/.anthem/plans/ tree holds runs from other
// anthems. Without scoping, a Prism frontend would receive duplicate
// rows in the cross-agent aggregated view (one copy per agent that
// hydrated), because every anthem would re-emit every other anthem's
// runs under its own socket identity.
func TestHistoryEventsForConnect_ScopedToOwnProject(t *testing.T) {
	home := t.TempDir()

	base := time.Date(2026, 4, 19, 22, 0, 0, 0, time.UTC)
	seedRun(t, home, "rebeltower", "20260419-cub-peacock", "plan-rebeltower-001", base)
	seedRun(t, home, "prism", "20260419-ws-upgrade", "plan-prism-001", base.Add(time.Hour))
	seedRun(t, home, "anthem", "20260419-gate-resume", "plan-anthem-001", base.Add(2*time.Hour))

	o := newHydrateTestOrch(t, home, "rebeltower")
	msgs := o.HistoryEventsForConnect(context.Background())
	if len(msgs) != 1 {
		t.Fatalf("want 1 run_history message, got %d", len(msgs))
	}
	snaps := decodeRunHistory(t, msgs[0])
	if len(snaps) != 1 {
		t.Fatalf("want exactly 1 snapshot for rebeltower, got %d", len(snaps))
	}
	if snaps[0].PlanID != "plan-rebeltower-001" {
		t.Errorf("got plan_id %q, want %q", snaps[0].PlanID, "plan-rebeltower-001")
	}

	oPrism := newHydrateTestOrch(t, home, "prism")
	msgsPrism := oPrism.HistoryEventsForConnect(context.Background())
	if len(msgsPrism) != 1 {
		t.Fatalf("prism: want 1 run_history message, got %d", len(msgsPrism))
	}
	prismSnaps := decodeRunHistory(t, msgsPrism[0])
	if len(prismSnaps) != 1 {
		t.Fatalf("prism: want exactly 1 snapshot, got %d", len(prismSnaps))
	}
	if prismSnaps[0].PlanID != "plan-prism-001" {
		t.Errorf("prism: got plan_id %q, want %q", prismSnaps[0].PlanID, "plan-prism-001")
	}

	for _, snap := range prismSnaps {
		if strings.HasPrefix(snap.PlanID, "plan-rebeltower-") || strings.HasPrefix(snap.PlanID, "plan-anthem-") {
			t.Errorf("prism hydrate leaked cross-project run: plan_id=%q", snap.PlanID)
		}
	}
}

// TestHistoryEventsForConnect_EmptyProject confirms that an agent
// whose project directory doesn't exist yet (pristine anthem install
// or first-ever run) returns no messages rather than an error. This
// matches runs.ListAll's os.IsNotExist behavior and is what the
// Prism adapter's on-connect hook expects: nil messages = skip send.
func TestHistoryEventsForConnect_EmptyProject(t *testing.T) {
	home := t.TempDir()
	base := time.Date(2026, 4, 19, 22, 0, 0, 0, time.UTC)
	seedRun(t, home, "rebeltower", "20260419-cub-peacock", "plan-rebeltower-001", base)

	o := newHydrateTestOrch(t, home, "sandbox-project")
	msgs := o.HistoryEventsForConnect(context.Background())
	if len(msgs) != 0 {
		t.Fatalf("want 0 messages for project with no plans, got %d", len(msgs))
	}
}
