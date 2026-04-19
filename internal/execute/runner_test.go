package execute

import (
	"context"
	"encoding/json"
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

// TestPlanRunner_GateOpenedCarriesProjectSlug pins the slug-propagation
// contract. Without a slug on gate_opened, Prism's image-gallery /
// video-preview / document review UIs have no anchor for /files/{slug}/{path}
// URLs and render as empty "missing" tiles. Regression for the reviewer
// reporting "I see something behind the icon dashboard but the gallery
// isn't rendered".
func TestPlanRunner_GateOpenedCarriesProjectSlug(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex:  testGuestIndex(),
		Runner:      runner,
		ChannelMgr:  bc,
		ProjectSlug: "rebel-tower",
	})

	plan := twoStepPlan()
	plan.Gates = []ApprovalGate{{ID: "g1", AfterStep: "s1", Prompt: "Approve sprites?"}}

	go func() {
		for {
			for _, et := range bc.eventTypes() {
				if et == EventGateOpened {
					pr.ResolveGate("g1", GateResolution{Action: GateApprove})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	if err := pr.Run(context.Background(), plan, "thread-slug"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()
	var foundSlug bool
	for _, e := range bc.events {
		if e.EventType != EventGateOpened {
			continue
		}
		var got gateEventPayload
		if err := json.Unmarshal([]byte(e.Text), &got); err != nil {
			t.Fatalf("unmarshal gate payload: %v", err)
		}
		if got.Slug != "rebel-tower" {
			t.Fatalf("gate_opened slug = %q, want %q", got.Slug, "rebel-tower")
		}
		foundSlug = true
	}
	if !foundSlug {
		t.Fatal("never saw a gate_opened event to assert slug on")
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

// countGuestFrames counts ActivateGuest / DeactivateGuest frames in the
// broadcaster for a given guest id. Used by the L5b per-step roster
// tests to assert the pairing invariant.
func (m *mockBroadcaster) countGuestFrames(guestID string) (activate, deactivate int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ev := range m.events {
		if ev.ActivateGuest != nil && ev.ActivateGuest.ID == guestID {
			activate++
		}
		if ev.DeactivateGuest != nil && ev.DeactivateGuest.ID == guestID {
			deactivate++
		}
	}
	return
}

// TestPlanRunner_PerStepActivateDeactivate asserts the L5b invariant for
// the no-gate path: every step emits exactly one ActivateGuest at start
// and one DeactivateGuest at end. Over a two-step plan with two different
// agents, this means two activates and two deactivates, one pair per
// agent. If this count ever drifts, the Prism roster will either flicker
// (duplicate activates) or get stuck with a "ghost" chip (missing
// deactivate).
func TestPlanRunner_PerStepActivateDeactivate(t *testing.T) {
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

	if err := pr.Run(context.Background(), twoStepPlan(), "thread-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	artistAct, artistDeact := bc.countGuestFrames("artist")
	coderAct, coderDeact := bc.countGuestFrames("coder")

	if artistAct != 1 || artistDeact != 1 {
		t.Errorf("artist activate/deactivate = %d/%d, want 1/1", artistAct, artistDeact)
	}
	if coderAct != 1 || coderDeact != 1 {
		t.Errorf("coder activate/deactivate = %d/%d, want 1/1", coderAct, coderDeact)
	}

	// Ordering: for each guest, activate must come before deactivate.
	bc.mu.Lock()
	defer bc.mu.Unlock()
	var actIdx, deactIdx int = -1, -1
	for i, ev := range bc.events {
		if ev.ActivateGuest != nil && ev.ActivateGuest.ID == "artist" && actIdx == -1 {
			actIdx = i
		}
		if ev.DeactivateGuest != nil && ev.DeactivateGuest.ID == "artist" && deactIdx == -1 {
			deactIdx = i
		}
	}
	if actIdx == -1 || deactIdx == -1 || actIdx >= deactIdx {
		t.Errorf("artist activate(%d) must precede deactivate(%d)", actIdx, deactIdx)
	}
}

// TestPlanRunner_GateReviseRetainsGuest asserts the L5b nuance: a
// GateRevise resolution intentionally does NOT emit DeactivateGuest,
// because the same agent is about to re-run the step. Dropping and
// re-adding the chip would flicker the sidebar between the Revise click
// and the next StepStarted.
func TestPlanRunner_GateReviseRetainsGuest(t *testing.T) {
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
		Gates: []ApprovalGate{{ID: "g1", AfterStep: "s1", Prompt: "Approve?"}},
	}

	// Resolve first gate with Revise (step will re-queue), then Approve.
	var gateCount int
	var gateMu sync.Mutex
	go func() {
		for {
			types := bc.eventTypes()
			gateMu.Lock()
			current := gateCount
			gateMu.Unlock()
			gateSeen := 0
			for _, et := range types {
				if et == EventGateOpened {
					gateSeen++
				}
			}
			if gateSeen > current {
				gateMu.Lock()
				gateCount = gateSeen
				gateMu.Unlock()
				if gateSeen == 1 {
					pr.ResolveGate("g1", GateResolution{Action: GateRevise, Feedback: "retry"})
				} else if gateSeen == 2 {
					pr.ResolveGate("g1", GateResolution{Action: GateApprove})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	if err := pr.Run(context.Background(), plan, "thread-revise"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	artistAct, artistDeact := bc.countGuestFrames("artist")
	// Two step-starts (one for the revise re-run) => 2 activates.
	// One final approve => 1 deactivate. The revise path must NOT
	// contribute an extra deactivate.
	if artistAct != 2 {
		t.Errorf("artist activates = %d, want 2 (revise re-runs the step)", artistAct)
	}
	if artistDeact != 1 {
		t.Errorf("artist deactivates = %d, want 1 (revise retains the chip)", artistDeact)
	}
}

// TestPlanRunner_GateAbortDeactivates asserts the L5b abort path: when
// the user clicks Abort at a gate, the step owner's chip is removed so
// the Prism roster returns to empty. Contrasts with GateRevise, which
// retains the chip.
func TestPlanRunner_GateAbortDeactivates(t *testing.T) {
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
		Gates: []ApprovalGate{{ID: "g1", AfterStep: "s1", Prompt: "Approve?"}},
	}

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

	err := pr.Run(context.Background(), plan, "thread-abort")
	if err == nil {
		t.Error("expected error on plan abort, got nil")
	}

	artistAct, artistDeact := bc.countGuestFrames("artist")
	if artistAct != 1 || artistDeact != 1 {
		t.Errorf("artist activates/deactivates = %d/%d, want 1/1 on abort", artistAct, artistDeact)
	}
}

// TestPlanRunner_GateAppliesArtifactGlobs pins the end-to-end contract
// for review.artifact_globs enforcement: when a guest's review spec
// declares artifact_globs, the gate_opened event must surface only
// artifacts matching those globs. The motivating case is Miyazaki
// (review.kind: image-gallery) producing both .png sprites AND a .md
// character reference doc in the same step -- ImageGalleryView renders
// every artifact as <img src>, so a .md leaking through becomes a
// broken tile in the review drawer.
//
// The test wires a guest with globs ["**/*.png"], has the artifact
// provider return a mixed slice, and asserts the gate_opened payload
// contains only the .png. GateNotificationEvent also receives the
// filtered count, so the chat notification ("N images ready to review")
// matches what the drawer actually displays.
func TestPlanRunner_GateAppliesArtifactGlobs(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}
	arts := newMockArtifacts()
	arts.toReturn["s1"] = []StepArtifact{
		{StepID: "s1", Path: "Assets/_Project/Art/Generated/hero.png", Kind: "image/png"},
		{StepID: "s1", Path: ".context/features/x/hero-reference.md", Kind: "text/markdown"},
	}

	tru := true
	guestIndex := &guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"artist": {
				ID:    "artist",
				Name:  "Artist",
				Model: "claude-sonnet-4-5",
				Review: &guests.ReviewSpec{
					Kind:          "image-gallery",
					ArtifactGlobs: []string{"**/*.png"},
					ShowPrompt:    &tru,
				},
			},
		},
	}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: guestIndex,
		Runner:     runner,
		ChannelMgr: bc,
		Artifacts:  arts,
	})

	plan := &ExecutionPlan{
		Steps: []PlanStep{
			{ID: "s1", AgentID: "artist", Description: "Draw + doc", Status: StepPending},
		},
		Gates: []ApprovalGate{{ID: "g1", AfterStep: "s1", Prompt: "Approve?"}},
	}

	go func() {
		for {
			for _, et := range bc.eventTypes() {
				if et == EventGateOpened {
					pr.ResolveGate("g1", GateResolution{Action: GateApprove})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	if err := pr.Run(context.Background(), plan, "thread-globs"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()
	var asserted bool
	for _, e := range bc.events {
		if e.EventType != EventGateOpened {
			continue
		}
		var got gateEventPayload
		if err := json.Unmarshal([]byte(e.Text), &got); err != nil {
			t.Fatalf("unmarshal gate_opened: %v", err)
		}
		if len(got.Artifacts) != 1 {
			t.Fatalf("gate_opened artifacts = %d, want 1 (only .png should pass **/*.png)", len(got.Artifacts))
		}
		if got.Artifacts[0].Path != "Assets/_Project/Art/Generated/hero.png" {
			t.Errorf("gate_opened artifact = %q, want the .png", got.Artifacts[0].Path)
		}
		asserted = true
	}
	if !asserted {
		t.Fatal("never saw gate_opened to assert filter")
	}

	// The gate_notification chat chip renders an artifact count into
	// its Text via renderNotificationTemplate; it must agree with the
	// filtered slice so the drawer count and the chat copy don't
	// diverge ("2 images ready to review" -> drawer shows 1). We pin
	// this by looking for the "(1 artifact" fragment the default
	// template produces for count=1.
	for _, e := range bc.events {
		if e.EventType != EventGateNotification {
			continue
		}
		var got gateNotificationPayload
		if err := json.Unmarshal([]byte(e.Text), &got); err != nil {
			t.Fatalf("unmarshal gate_notification: %v", err)
		}
		if !containsSubstring(got.Text, "(1 artifact") {
			t.Errorf("gate_notification.text = %q, want count=1 post-filter", got.Text)
		}
	}
}

// TestPlanRunner_GateEmptyGlobsDoesNotFilter is the backward-compat
// companion: an agent without artifact_globs (today's default for
// every shipped guest except those that opt in) must still see every
// artifact the step produced. Regression guard so future wiring of
// the filter cannot accidentally collapse globs=nil into "drop
// everything".
func TestPlanRunner_GateEmptyGlobsDoesNotFilter(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}
	arts := newMockArtifacts()
	arts.toReturn["s1"] = []StepArtifact{
		{StepID: "s1", Path: "a.png"},
		{StepID: "s1", Path: "b.md"},
	}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(), // "artist" has no ReviewSpec
		Runner:     runner,
		ChannelMgr: bc,
		Artifacts:  arts,
	})

	plan := &ExecutionPlan{
		Steps: []PlanStep{
			{ID: "s1", AgentID: "artist", Description: "mixed", Status: StepPending},
		},
		Gates: []ApprovalGate{{ID: "g1", AfterStep: "s1", Prompt: "Approve?"}},
	}

	go func() {
		for {
			for _, et := range bc.eventTypes() {
				if et == EventGateOpened {
					pr.ResolveGate("g1", GateResolution{Action: GateApprove})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	if err := pr.Run(context.Background(), plan, "thread-noglob"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()
	for _, e := range bc.events {
		if e.EventType != EventGateOpened {
			continue
		}
		var got gateEventPayload
		if err := json.Unmarshal([]byte(e.Text), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(got.Artifacts) != 2 {
			t.Errorf("no-glob gate surfaced %d artifacts, want 2 (unchanged)", len(got.Artifacts))
		}
		return
	}
	t.Fatal("no gate_opened emitted")
}
