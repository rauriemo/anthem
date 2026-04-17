package execute

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/guests"
	"github.com/rauriemo/anthem/internal/types"
)

// --- Mocks ---

type mockRunner struct {
	mu      sync.Mutex
	calls   []string // prompt snippets seen
	results map[string]*types.RunResult
	errors  map[string]error
}

func newMockRunner() *mockRunner {
	return &mockRunner{
		results: make(map[string]*types.RunResult),
		errors:  make(map[string]error),
	}
}

func (m *mockRunner) Run(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
	m.mu.Lock()
	m.calls = append(m.calls, opts.Prompt)
	m.mu.Unlock()

	for key, err := range m.errors {
		if containsSubstring(opts.Prompt, key) {
			return nil, err
		}
	}
	for key, res := range m.results {
		if containsSubstring(opts.Prompt, key) {
			return res, nil
		}
	}
	return &types.RunResult{ExitCode: 0, Output: "ok"}, nil
}

func (m *mockRunner) Continue(_ context.Context, _ string, _ string, _ types.ContinueOpts) (*types.RunResult, error) {
	return nil, nil
}

func (m *mockRunner) Kill(_ int) error { return nil }

func containsSubstring(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub || findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

type mockBroadcaster struct {
	mu     sync.Mutex
	events []channel.OutgoingMessage
}

func (m *mockBroadcaster) Broadcast(_ context.Context, msg channel.OutgoingMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, msg)
	return nil
}

func (m *mockBroadcaster) eventTypes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, e := range m.events {
		if e.EventType != "" {
			out = append(out, e.EventType)
		}
	}
	return out
}

type mockArtifacts struct {
	injected map[string][]StepArtifact
	toReturn map[string][]StepArtifact
}

func newMockArtifacts() *mockArtifacts {
	return &mockArtifacts{
		injected: make(map[string][]StepArtifact),
		toReturn: make(map[string][]StepArtifact),
	}
}

func (m *mockArtifacts) Collect(stepID string) ([]StepArtifact, error) {
	return m.toReturn[stepID], nil
}

func (m *mockArtifacts) Inject(stepID string, upstream []StepArtifact) error {
	m.injected[stepID] = upstream
	return nil
}

// --- Helpers ---

func testGuestIndex() *guests.GuestIndex {
	return &guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"artist": {ID: "artist", Name: "Artist", Model: "claude-sonnet-4-5"},
			"coder":  {ID: "coder", Name: "Coder", Model: "claude-sonnet-4-5"},
		},
	}
}

func twoStepPlan() *ExecutionPlan {
	return &ExecutionPlan{
		Steps: []PlanStep{
			{ID: "s1", AgentID: "artist", Description: "Draw sprites", Status: StepPending},
			{ID: "s2", AgentID: "coder", Description: "Implement logic", DependsOn: "s1", Status: StepPending},
		},
		Metadata: PlanMetadata{Title: "Test", CreatedAt: time.Now()},
	}
}

// --- Tests ---

func TestPlanRunner_HappyPath(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}
	arts := newMockArtifacts()
	arts.toReturn["s1"] = []StepArtifact{{StepID: "s1", Path: "hero.png", Kind: "image"}}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
		Runner:     runner,
		ChannelMgr: bc,
		Artifacts:  arts,
	})

	plan := twoStepPlan()
	err := pr.Run(context.Background(), plan, "thread-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	runner.mu.Lock()
	callCount := len(runner.calls)
	runner.mu.Unlock()
	if callCount != 2 {
		t.Fatalf("expected 2 agent calls, got %d", callCount)
	}

	events := bc.eventTypes()
	want := []string{
		EventPlanLoaded,
		EventStepStarted,
		EventStepCompleted,
		EventStepStarted,
		EventStepCompleted,
		EventPlanCompleted,
	}
	if len(events) != len(want) {
		t.Fatalf("events mismatch:\ngot:  %v\nwant: %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("event[%d] = %q, want %q", i, events[i], want[i])
		}
	}
}

func TestPlanRunner_GateApprove(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
		Runner:     runner,
		ChannelMgr: bc,
	})

	plan := twoStepPlan()
	plan.Gates = []ApprovalGate{{ID: "g1", AfterStep: "s1", Prompt: "Approve sprites?"}}

	// Auto-approve the gate from another goroutine
	go func() {
		for {
			types := bc.eventTypes()
			for _, et := range types {
				if et == EventGateOpened {
					pr.ResolveGate("g1", GateResolution{Action: GateApprove})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	err := pr.Run(context.Background(), plan, "thread-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events := bc.eventTypes()
	hasGateOpened := false
	hasGateResolved := false
	for _, e := range events {
		if e == EventGateOpened {
			hasGateOpened = true
		}
		if e == EventGateResolved {
			hasGateResolved = true
		}
	}
	if !hasGateOpened {
		t.Error("missing gate_opened event")
	}
	if !hasGateResolved {
		t.Error("missing gate_resolved event")
	}
}

func TestPlanRunner_GateAbort(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
		Runner:     runner,
		ChannelMgr: bc,
	})

	plan := twoStepPlan()
	plan.Gates = []ApprovalGate{{ID: "g1", AfterStep: "s1", Prompt: "Approve sprites?"}}

	go func() {
		for {
			types := bc.eventTypes()
			for _, et := range types {
				if et == EventGateOpened {
					pr.ResolveGate("g1", GateResolution{Action: GateAbort})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	err := pr.Run(context.Background(), plan, "thread-1")
	if err == nil {
		t.Fatal("expected abort error")
	}

	hasAbort := false
	for _, e := range bc.eventTypes() {
		if e == EventPlanAborted {
			hasAbort = true
		}
	}
	if !hasAbort {
		t.Error("missing plan_aborted event")
	}
}

func TestPlanRunner_GateRevise(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
		Runner:     runner,
		ChannelMgr: bc,
	})

	plan := &ExecutionPlan{
		Steps: []PlanStep{
			{ID: "s1", AgentID: "artist", Description: "Draw sprites", Status: StepPending},
		},
		Gates:    []ApprovalGate{{ID: "g1", AfterStep: "s1", Prompt: "Good enough?"}},
		Metadata: PlanMetadata{Title: "Revise Test", CreatedAt: time.Now()},
	}

	reviseCount := 0
	go func() {
		for {
			types := bc.eventTypes()
			gateCount := 0
			for _, et := range types {
				if et == EventGateOpened {
					gateCount++
				}
			}
			if gateCount > reviseCount {
				reviseCount++
				if reviseCount == 1 {
					pr.ResolveGate("g1", GateResolution{Action: GateRevise, Feedback: "add shadow"})
				} else {
					pr.ResolveGate("g1", GateResolution{Action: GateApprove})
				}
			}
			time.Sleep(5 * time.Millisecond)
			if reviseCount >= 2 {
				return
			}
		}
	}()

	err := pr.Run(context.Background(), plan, "thread-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	runner.mu.Lock()
	callCount := len(runner.calls)
	runner.mu.Unlock()

	if callCount != 2 {
		t.Fatalf("expected 2 agent calls (original + revised), got %d", callCount)
	}
}

func TestPlanRunner_StepFailureRetry(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}

	callCount := 0
	origRun := runner.Run
	_ = origRun
	failOnce := &mockRunnerFailOnce{succeedAfter: 1}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
		Runner:     failOnce,
		ChannelMgr: bc,
	})

	plan := &ExecutionPlan{
		Steps: []PlanStep{
			{ID: "s1", AgentID: "artist", Description: "Draw sprites", Status: StepPending},
		},
		Metadata: PlanMetadata{Title: "Retry Test", CreatedAt: time.Now()},
	}

	go func() {
		for {
			types := bc.eventTypes()
			for _, et := range types {
				if et == EventStepFailed {
					pr.ResolveFailure("s1", GateResolution{Action: GateApprove})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	err := pr.Run(context.Background(), plan, "thread-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_ = callCount
	failOnce.mu.Lock()
	if failOnce.calls != 2 {
		t.Fatalf("expected 2 calls (fail + retry), got %d", failOnce.calls)
	}
	failOnce.mu.Unlock()
}

type mockRunnerFailOnce struct {
	mu           sync.Mutex
	calls        int
	succeedAfter int
}

func (m *mockRunnerFailOnce) Run(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.mu.Unlock()

	if n <= m.succeedAfter {
		return nil, fmt.Errorf("simulated failure")
	}
	return &types.RunResult{ExitCode: 0, Output: "ok"}, nil
}

func (m *mockRunnerFailOnce) Continue(_ context.Context, _ string, _ string, _ types.ContinueOpts) (*types.RunResult, error) {
	return nil, nil
}

func (m *mockRunnerFailOnce) Kill(_ int) error { return nil }

func TestPlanRunner_StepFailureAbort(t *testing.T) {
	bc := &mockBroadcaster{}

	failAlways := &mockRunnerFailOnce{succeedAfter: 999}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
		Runner:     failAlways,
		ChannelMgr: bc,
	})

	plan := &ExecutionPlan{
		Steps: []PlanStep{
			{ID: "s1", AgentID: "artist", Description: "Draw sprites", Status: StepPending},
		},
		Metadata: PlanMetadata{Title: "Abort Test", CreatedAt: time.Now()},
	}

	go func() {
		for {
			types := bc.eventTypes()
			for _, et := range types {
				if et == EventStepFailed {
					pr.ResolveFailure("s1", GateResolution{Action: GateAbort})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	err := pr.Run(context.Background(), plan, "thread-1")
	if err == nil {
		t.Fatal("expected abort error")
	}
}

func TestPlanRunner_ContextCancel(t *testing.T) {
	bc := &mockBroadcaster{}
	failAlways := &mockRunnerFailOnce{succeedAfter: 999}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
		Runner:     failAlways,
		ChannelMgr: bc,
	})

	plan := &ExecutionPlan{
		Steps: []PlanStep{
			{ID: "s1", AgentID: "artist", Description: "Draw sprites", Status: StepPending},
		},
		Metadata: PlanMetadata{Title: "Cancel Test", CreatedAt: time.Now()},
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		for {
			types := bc.eventTypes()
			for _, et := range types {
				if et == EventStepFailed {
					cancel()
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	err := pr.Run(ctx, plan, "thread-1")
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestPlanRunner_ArtifactInjection(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}
	arts := newMockArtifacts()
	arts.toReturn["s1"] = []StepArtifact{
		{StepID: "s1", Path: "hero.png", Kind: "image", Summary: "Hero sprite"},
	}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
		Runner:     runner,
		ChannelMgr: bc,
		Artifacts:  arts,
	})

	plan := twoStepPlan()
	err := pr.Run(context.Background(), plan, "thread-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// s2 should have received s1's artifacts as upstream
	injected, ok := arts.injected["s2"]
	if !ok {
		t.Fatal("s2 did not receive upstream injection")
	}
	if len(injected) != 1 || injected[0].Path != "hero.png" {
		t.Errorf("unexpected injected artifacts: %v", injected)
	}
}

func TestPlanRunner_PlanSnapshot(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
		Runner:     runner,
		ChannelMgr: bc,
	})

	plan := twoStepPlan()
	_ = pr.Run(context.Background(), plan, "thread-1")

	snap := pr.Plan()
	for _, s := range snap.Steps {
		if s.Status != StepCompleted {
			t.Errorf("step %s should be completed, got %s", s.ID, s.Status)
		}
	}

	// Mutating the snapshot should not affect internal state
	snap.Steps[0].Status = StepPending
	internal := pr.Plan()
	if internal.Steps[0].Status != StepCompleted {
		t.Error("snapshot mutation leaked to internal plan")
	}
}
