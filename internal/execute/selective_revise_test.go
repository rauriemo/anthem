package execute

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/guests"
)

// newGuestIndexWithArtist returns a guest index where `artist` has a
// review spec that supports selective revise. Used by the Stage 4 tests
// that exercise the preserve-and-replace flow.
func newGuestIndexWithArtist() *guests.GuestIndex {
	tru := true
	return &guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"artist": {
				ID:         "artist",
				Name:       "Artist",
				VoiceID:    "voice-artist",
				VoiceModel: "eleven_flash_v2_5",
				Model:      "claude-sonnet-4-5",
				Review: &guests.ReviewSpec{
					Kind:          "image-gallery",
					PartialRevise: &tru,
					CoherenceHint: "Match the preserved sprites' silhouette.",
				},
			},
		},
	}
}

func TestGateNotificationEvent_EmittedBeforeGateOpened(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: newGuestIndexWithArtist(),
		Runner:     runner,
		ChannelMgr: bc,
	})

	plan := &ExecutionPlan{
		Steps:    []PlanStep{{ID: "s1", AgentID: "artist", Description: "Draw hero sprites", Status: StepPending}},
		Gates:    []ApprovalGate{{ID: "g1", AfterStep: "s1", Prompt: "Approve?"}},
		Metadata: PlanMetadata{Title: "Notif Test", CreatedAt: time.Now()},
	}

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

	if err := pr.Run(context.Background(), plan, "thread-42"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events := bc.eventTypes()
	var notifIdx, openIdx int = -1, -1
	for i, e := range events {
		if e == EventGateNotification && notifIdx == -1 {
			notifIdx = i
		}
		if e == EventGateOpened && openIdx == -1 {
			openIdx = i
		}
	}
	if notifIdx == -1 {
		t.Fatal("expected gate_notification event")
	}
	if openIdx == -1 {
		t.Fatal("expected gate_opened event")
	}
	if notifIdx >= openIdx {
		t.Fatalf("gate_notification (%d) must come before gate_opened (%d)", notifIdx, openIdx)
	}

	// Verify notification payload shape.
	var payload gateNotificationPayload
	bc.mu.Lock()
	notifMsg := bc.events[notifIdx]
	bc.mu.Unlock()

	if err := json.Unmarshal([]byte(notifMsg.Text), &payload); err != nil {
		t.Fatalf("notification payload is not JSON: %v", err)
	}
	if payload.AgentID != "artist" {
		t.Errorf("AgentID = %q, want artist", payload.AgentID)
	}
	if payload.AgentName != "Artist" {
		t.Errorf("AgentName = %q, want Artist", payload.AgentName)
	}
	if payload.VoiceID != "voice-artist" {
		t.Errorf("VoiceID = %q, want voice-artist", payload.VoiceID)
	}
	if payload.Cause != "gate_opened" {
		t.Errorf("Cause = %q, want gate_opened", payload.Cause)
	}
	if !payload.VoiceRequest {
		t.Error("VoiceRequest should be true for gate_opened notifications")
	}
	if !strings.Contains(payload.ReviewLink, "gate=g1") {
		t.Errorf("ReviewLink = %q, missing gate id", payload.ReviewLink)
	}
	if notifMsg.GuestID != "artist" {
		t.Errorf("OutgoingMessage.GuestID = %q, want artist", notifMsg.GuestID)
	}
}

func TestRenderNotificationTemplate_SubstitutesKnownVars(t *testing.T) {
	tpl := "{agent_name}: {step_title} ({artifact_count} items)"
	got := renderNotificationTemplate(tpl, "Miyazaki", "sprite pass", 10)
	want := "Miyazaki: sprite pass (10 items)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderNotificationTemplate_EmptyUsesDefault(t *testing.T) {
	got := renderNotificationTemplate("", "M", "hero pass", 3)
	if !strings.Contains(got, "hero pass") {
		t.Errorf("default template should include step title, got %q", got)
	}
	if !strings.Contains(got, "3") {
		t.Errorf("default template should include artifact count, got %q", got)
	}
}

func TestApplyRevise_SelectivePreservesAndRegenerates(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: newGuestIndexWithArtist(),
		Runner:     runner,
		ChannelMgr: bc,
	})

	pr.plan = &ExecutionPlan{
		Steps: []PlanStep{{ID: "s1", AgentID: "artist", Description: "Draw 3 sprites", Status: StepCompleted}},
	}

	artifacts := []StepArtifact{
		{StepID: "s1", Path: "a.png", ArtifactID: "art-a"},
		{StepID: "s1", Path: "b.png", ArtifactID: "art-b"},
		{StepID: "s1", Path: "c.png", ArtifactID: "art-c"},
	}

	res := GateResolution{
		Action:   GateRevise,
		Feedback: "Second one is weak",
		FlaggedArtifacts: []FlaggedArtifact{
			{ArtifactID: "art-b", Note: "More detail on face"},
		},
	}

	pr.applyRevise("s1", res, artifacts, newGuestIndexWithArtist().Agents["artist"].Review)

	// Step must be back to pending
	if pr.plan.Steps[0].Status != StepPending {
		t.Fatalf("expected status pending, got %s", pr.plan.Steps[0].Status)
	}
	desc := pr.plan.Steps[0].Description
	if !strings.Contains(desc, "Preserve (do not regenerate)") {
		t.Errorf("description missing preserve block:\n%s", desc)
	}
	if !strings.Contains(desc, "a.png") || !strings.Contains(desc, "c.png") {
		t.Errorf("preserve block missing expected paths:\n%s", desc)
	}
	if !strings.Contains(desc, "Regenerate") || !strings.Contains(desc, "b.png") {
		t.Errorf("regenerate block missing b.png:\n%s", desc)
	}
	if !strings.Contains(desc, "More detail on face") {
		t.Errorf("regenerate block should include per-item note:\n%s", desc)
	}
	if !strings.Contains(desc, "Coherence hint") {
		t.Errorf("revise block should include coherence hint:\n%s", desc)
	}
	if !strings.Contains(desc, "Match the preserved sprites' silhouette.") {
		t.Errorf("custom coherence hint missing:\n%s", desc)
	}

	// Preserve set should be recorded for the merge step
	preserved := pr.preservedArtifacts["s1"]
	if len(preserved) != 2 {
		t.Errorf("expected 2 preserved artifacts, got %d", len(preserved))
	}

	if pr.ConsecutivePartialRevises("s1") != 1 {
		t.Errorf("expected streak=1, got %d", pr.ConsecutivePartialRevises("s1"))
	}
}

func TestApplyRevise_FullReviseClearsPreserveState(t *testing.T) {
	pr := NewPlanRunner(RunnerOpts{GuestIndex: newGuestIndexWithArtist()})
	pr.plan = &ExecutionPlan{
		Steps: []PlanStep{{ID: "s1", AgentID: "artist", Description: "d", Status: StepCompleted}},
	}
	pr.preservedArtifacts["s1"] = []StepArtifact{{Path: "stale.png"}}
	pr.consecutivePartialRevises["s1"] = 3

	// No FlaggedArtifacts = full revise
	pr.applyRevise("s1", GateResolution{Action: GateRevise, Feedback: "start over"}, nil, nil)

	if _, ok := pr.preservedArtifacts["s1"]; ok {
		t.Error("full revise should clear preserved set")
	}
	if pr.consecutivePartialRevises["s1"] != 0 {
		t.Errorf("full revise should reset streak, got %d", pr.consecutivePartialRevises["s1"])
	}
	if !strings.Contains(pr.plan.Steps[0].Description, "Revision: start over") {
		t.Errorf("full revise should append feedback, got: %s", pr.plan.Steps[0].Description)
	}
}

func TestApplyRevise_PartialReviseFallsBackWhenKindOptsOut(t *testing.T) {
	fls := false
	review := &guests.ReviewSpec{Kind: "image-gallery", PartialRevise: &fls}

	pr := NewPlanRunner(RunnerOpts{})
	pr.plan = &ExecutionPlan{
		Steps: []PlanStep{{ID: "s1", Description: "d", Status: StepCompleted}},
	}

	res := GateResolution{
		Action:   GateRevise,
		Feedback: "tweak",
		FlaggedArtifacts: []FlaggedArtifact{
			{ArtifactID: "art-b", Note: "n"},
		},
	}

	pr.applyRevise("s1", res, []StepArtifact{{StepID: "s1", Path: "b.png", ArtifactID: "art-b"}}, review)

	// Should have fallen back to full revise -- no preserve set recorded.
	if _, ok := pr.preservedArtifacts["s1"]; ok {
		t.Error("partial_revise:false should force full-revise fallback")
	}
	if !strings.Contains(pr.plan.Steps[0].Description, "Revision: tweak") {
		t.Errorf("expected full-revise append, got: %s", pr.plan.Steps[0].Description)
	}
}

func TestMergePreservedWithCollected_MarksNewArtifacts(t *testing.T) {
	pr := NewPlanRunner(RunnerOpts{})
	pr.preservedArtifacts["s1"] = []StepArtifact{
		{StepID: "s1", Path: "a.png", ArtifactID: "art-a"},
		{StepID: "s1", Path: "c.png", ArtifactID: "art-c"},
	}

	regen := []StepArtifact{
		{StepID: "s1", Path: "b.png", ArtifactID: "art-b"},
	}

	out := pr.mergePreservedWithCollected("s1", regen)

	if len(out) != 3 {
		t.Fatalf("expected 3 merged artifacts, got %d", len(out))
	}

	byPath := map[string]StepArtifact{}
	for _, a := range out {
		byPath[a.Path] = a
	}

	if !byPath["b.png"].UpdatedInLastRevise {
		t.Error("b.png should be marked UpdatedInLastRevise")
	}
	if byPath["a.png"].UpdatedInLastRevise {
		t.Error("a.png (preserved) should NOT be marked UpdatedInLastRevise")
	}
	if byPath["c.png"].UpdatedInLastRevise {
		t.Error("c.png (preserved) should NOT be marked UpdatedInLastRevise")
	}

	// Preserve map must be cleared after merge (one-shot)
	if _, ok := pr.preservedArtifacts["s1"]; ok {
		t.Error("preserve map should be cleared after merge")
	}
}

func TestMergePreservedWithCollected_NewWinsOnConflict(t *testing.T) {
	pr := NewPlanRunner(RunnerOpts{})
	pr.preservedArtifacts["s1"] = []StepArtifact{
		{StepID: "s1", Path: "a.png", ArtifactID: "art-a-old", UpdatedInLastRevise: false},
	}

	regen := []StepArtifact{
		{StepID: "s1", Path: "a.png", ArtifactID: "art-a-new"},
	}

	out := pr.mergePreservedWithCollected("s1", regen)

	if len(out) != 1 {
		t.Fatalf("expected 1 artifact after conflict dedup, got %d", len(out))
	}
	if out[0].ArtifactID != "art-a-new" {
		t.Errorf("collected should win on path conflict, got %q", out[0].ArtifactID)
	}
	if !out[0].UpdatedInLastRevise {
		t.Error("regenerated conflict winner should be marked UpdatedInLastRevise")
	}
}

func TestConsecutivePartialRevisesResetOnApprove(t *testing.T) {
	pr := NewPlanRunner(RunnerOpts{})
	pr.consecutivePartialRevises["s1"] = 3
	pr.preservedArtifacts["s1"] = []StepArtifact{{Path: "a"}}

	pr.clearPartialReviseState("s1")

	if pr.ConsecutivePartialRevises("s1") != 0 {
		t.Errorf("streak should reset, got %d", pr.ConsecutivePartialRevises("s1"))
	}
	if _, ok := pr.preservedArtifacts["s1"]; ok {
		t.Error("preserve set should be cleared on approve")
	}
}

func TestReviewSupportsPartial(t *testing.T) {
	t.Parallel()
	tru := true
	fls := false
	cases := []struct {
		name string
		spec *guests.ReviewSpec
		want bool
	}{
		{"nil spec", nil, true},
		{"explicit true", &guests.ReviewSpec{Kind: "image-gallery", PartialRevise: &tru}, true},
		{"explicit false", &guests.ReviewSpec{Kind: "image-gallery", PartialRevise: &fls}, false},
		{"structured-data default false", &guests.ReviewSpec{Kind: "structured-data"}, false},
		{"image-gallery default true", &guests.ReviewSpec{Kind: "image-gallery"}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := reviewSupportsPartial(tc.spec)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// --- end-to-end selective revise: two runs, merged output ------------------

func TestPlanRunner_SelectiveReviseEndToEnd(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}
	arts := &selectiveArtifacts{}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: newGuestIndexWithArtist(),
		Runner:     runner,
		ChannelMgr: bc,
		Artifacts:  arts,
	})

	plan := &ExecutionPlan{
		Steps:    []PlanStep{{ID: "s1", AgentID: "artist", Description: "Draw sprites", Status: StepPending}},
		Gates:    []ApprovalGate{{ID: "g1", AfterStep: "s1", Prompt: "ok?"}},
		Metadata: PlanMetadata{Title: "SelRev", CreatedAt: time.Now()},
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
					pr.ResolveGate("g1", GateResolution{
						Action:   GateRevise,
						Feedback: "fix b",
						FlaggedArtifacts: []FlaggedArtifact{
							{ArtifactID: "art-b", Note: "more detail"},
						},
					})
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

	if err := pr.Run(context.Background(), plan, "thread-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The merged final artifact set should have 3 entries: two preserved
	// (a.png, c.png) and one regenerated (b.png marked as updated).
	final := pr.artifacts["s1"]
	if len(final) != 3 {
		t.Fatalf("expected 3 merged artifacts, got %d: %+v", len(final), final)
	}

	byPath := map[string]StepArtifact{}
	for _, a := range final {
		byPath[a.Path] = a
	}
	if !byPath["b.png"].UpdatedInLastRevise {
		t.Error("b.png should be marked UpdatedInLastRevise after selective revise")
	}
	if byPath["a.png"].UpdatedInLastRevise {
		t.Error("a.png (preserved) should NOT be marked UpdatedInLastRevise")
	}
}

// selectiveArtifacts is a mock provider that emits a different set of
// artifacts on the first and second collect so the selective-revise merge
// can be asserted end-to-end. Call 1 returns {a,b,c}, call 2 returns only
// {b} (the regenerated item).
type selectiveArtifacts struct {
	mu    sync.Mutex
	calls int
}

func (s *selectiveArtifacts) Collect(stepID string) ([]StepArtifact, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	if n == 1 {
		return []StepArtifact{
			{StepID: stepID, Path: "a.png", ArtifactID: "art-a"},
			{StepID: stepID, Path: "b.png", ArtifactID: "art-b"},
			{StepID: stepID, Path: "c.png", ArtifactID: "art-c"},
		}, nil
	}
	return []StepArtifact{
		{StepID: stepID, Path: "b.png", ArtifactID: "art-b"},
	}, nil
}

func (s *selectiveArtifacts) Inject(_ string, _ []StepArtifact) error { return nil }
