package execute

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rauriemo/anthem/internal/guests"
)

// Stage 2 coverage: gate_opened event payload enrichment.
//
// gate_opened must carry enough context for Prism's review drawer to render
// without having to cross-reference anything else:
//   - agent_id / agent_name / step_id so the drawer titles itself correctly
//   - plan_id and review_link so deep links work externally
//   - the agent's review spec so the right panels + viewer are chosen
//   - artifact_ids that are stable across runs (same step/path -> same id)

func TestGateOpenedEvent_EnrichesAgentAndPlanCoordinates(t *testing.T) {
	gate := ApprovalGate{ID: "gate-1", AfterStep: "s1", Prompt: "review sprites"}
	review := &guests.ReviewSpec{Kind: "image-gallery", Title: "Goblin sprites"}
	arts := []StepArtifact{
		{StepID: "s1", Path: "a/b.png", Kind: "image/png", ArtifactID: NewArtifactID("s1", "a/b.png")},
	}

	msg := GateOpenedEvent(gate, arts, "s1", "miyazaki", "Miyazaki", review, "plan-42", "rebel-tower", "thread-42")

	if msg.EventType != EventGateOpened {
		t.Fatalf("event_type = %q", msg.EventType)
	}
	if msg.ThreadID != "thread-42" {
		t.Fatalf("thread_id = %q", msg.ThreadID)
	}

	var got gateEventPayload
	if err := json.Unmarshal([]byte(msg.Text), &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if got.GateID != "gate-1" {
		t.Errorf("gate_id = %q", got.GateID)
	}
	if got.AgentID != "miyazaki" || got.AgentName != "Miyazaki" {
		t.Errorf("agent identity wrong: %+v", got)
	}
	if got.StepID != "s1" {
		t.Errorf("step_id = %q", got.StepID)
	}
	if got.PlanID != "plan-42" {
		t.Errorf("plan_id = %q", got.PlanID)
	}
	if got.ReviewLink != "/chain?gate=gate-1&plan=plan-42" {
		t.Errorf("review_link = %q", got.ReviewLink)
	}
	if got.Review == nil || got.Review.Kind != "image-gallery" {
		t.Errorf("review spec missing or wrong kind: %+v", got.Review)
	}
	if len(got.AllowedActions) != 3 {
		t.Errorf("allowed_actions = %v", got.AllowedActions)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].ArtifactID == "" {
		t.Errorf("artifacts missing stable id: %+v", got.Artifacts)
	}
	if got.Slug != "rebel-tower" {
		t.Errorf("slug = %q, want %q (Prism needs this to resolve /files/{slug}/{path})", got.Slug, "rebel-tower")
	}
}

// TestGateOpenedEvent_SlugPropagated pins the project slug contract. Without
// the slug on the wire, Prism's review drawer (image-gallery, video-preview,
// document) cannot build <img src>, <video src>, or fetch() URLs -- tiles
// render as empty "missing" placeholders and the reviewer sees no content.
func TestGateOpenedEvent_SlugPropagated(t *testing.T) {
	gate := ApprovalGate{ID: "g1", AfterStep: "s1"}
	msg := GateOpenedEvent(gate, nil, "s1", "miyazaki", "Miyazaki", nil, "plan-1", "rebel-tower", "thread")

	var got gateEventPayload
	if err := json.Unmarshal([]byte(msg.Text), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Slug != "rebel-tower" {
		t.Errorf("slug = %q, want %q", got.Slug, "rebel-tower")
	}

	// Also verify the raw JSON uses snake_case `slug` so Prism's payload
	// decoder (payload.slug) finds it without a field rename.
	if !strings.Contains(msg.Text, `"slug":"rebel-tower"`) {
		t.Errorf("wire JSON missing snake_case slug: %s", msg.Text)
	}
}

func TestGateOpenedEvent_NilReviewAndEmptyIdentityAreTolerated(t *testing.T) {
	gate := ApprovalGate{ID: "g2", AfterStep: "s2"}

	msg := GateOpenedEvent(gate, nil, "s2", "", "", nil, "", "", "thread-x")

	var got gateEventPayload
	if err := json.Unmarshal([]byte(msg.Text), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Review != nil {
		t.Errorf("expected nil review, got %+v", got.Review)
	}
	if got.AgentID != "" || got.AgentName != "" {
		t.Errorf("expected empty agent identity, got %+v", got)
	}
	if got.ReviewLink != "/chain?gate=g2" {
		t.Errorf("review_link = %q (should still carry gate id)", got.ReviewLink)
	}
}

func TestGateOpenedEvent_ReviewLinkEmptyWhenNoIDs(t *testing.T) {
	gate := ApprovalGate{ID: "", AfterStep: "s3"}
	msg := GateOpenedEvent(gate, nil, "s3", "a", "A", nil, "", "", "thread")

	var got gateEventPayload
	_ = json.Unmarshal([]byte(msg.Text), &got)
	if got.ReviewLink != "" {
		t.Errorf("expected empty review_link without plan or gate ids, got %q", got.ReviewLink)
	}
}

// TestPlanLoadedEvent_CarriesStepsAndGates pins the plan_loaded wire
// contract. Prism's Execute pipeline view (ExecuteProgressView) renders
// the step list using exec.loadPlan(steps, gates); if plan_loaded ships
// only metadata (title + counts) the pipeline shows "0/0 steps" and the
// user has no visibility into what the plan will run. The payload must
// expose each step's id/agent_id/label/depends_on and each gate's
// id/after_step/prompt using the snake_case JSON names Prism decodes.
func TestPlanLoadedEvent_CarriesStepsAndGates(t *testing.T) {
	plan := &ExecutionPlan{
		Steps: []PlanStep{
			{ID: "s1", AgentID: "miyazaki", Description: "Draft hero sprite"},
			{ID: "s2", AgentID: "kaplan", Description: "Balance hero stats", DependsOn: "s1"},
		},
		Gates: []ApprovalGate{
			{ID: "g1", AfterStep: "s1", Prompt: "Approve the hero sprite?"},
		},
		Metadata: PlanMetadata{Title: "Hero bring-up"},
	}

	msg := PlanLoadedEvent(plan, "thread-plan")
	if msg.EventType != EventPlanLoaded {
		t.Fatalf("event_type = %q", msg.EventType)
	}

	var got planLoadedPayload
	if err := json.Unmarshal([]byte(msg.Text), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Title != "Hero bring-up" {
		t.Errorf("title = %q", got.Title)
	}
	if got.StepCount != 2 || got.GateCount != 1 {
		t.Errorf("counts wrong: steps=%d gates=%d", got.StepCount, got.GateCount)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("steps len = %d, want 2", len(got.Steps))
	}
	// Description must be exposed as `label` so ExecuteProgressView's
	// `step.label ?? step.id` renders the human-authored title.
	if got.Steps[0].ID != "s1" || got.Steps[0].AgentID != "miyazaki" || got.Steps[0].Label != "Draft hero sprite" {
		t.Errorf("step[0] = %+v", got.Steps[0])
	}
	if got.Steps[1].DependsOn != "s1" {
		t.Errorf("step[1].depends_on = %q, want s1", got.Steps[1].DependsOn)
	}
	if len(got.Gates) != 1 {
		t.Fatalf("gates len = %d, want 1", len(got.Gates))
	}
	if got.Gates[0].ID != "g1" || got.Gates[0].AfterStep != "s1" || got.Gates[0].Prompt == "" {
		t.Errorf("gate[0] = %+v", got.Gates[0])
	}

	// Guard the exact snake_case keys Prism's useWebSocket.ts decoder looks
	// for. A casing regression here would silently leave the pipeline empty.
	raw := msg.Text
	for _, want := range []string{
		`"steps":`,
		`"gates":`,
		`"agent_id":"miyazaki"`,
		`"label":"Draft hero sprite"`,
		`"after_step":"s1"`,
		`"depends_on":"s1"`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("wire JSON missing %q: %s", want, raw)
		}
	}
}

// TestPlanLoadedEvent_EmptyPlan guards against nil-slice serialization
// quirks. An empty plan must still ship `steps: []` and `gates: []` (not
// omitted or `null`) so Prism's `payload.steps ?? []` fallback sees a real
// array and the pipeline renders as "empty" rather than crashing on a null
// iterator.
func TestPlanLoadedEvent_EmptyPlan(t *testing.T) {
	plan := &ExecutionPlan{Metadata: PlanMetadata{Title: "empty"}}
	msg := PlanLoadedEvent(plan, "thread-empty")

	if !strings.Contains(msg.Text, `"steps":[]`) {
		t.Errorf("empty plan should serialize steps as [], got %s", msg.Text)
	}
	if !strings.Contains(msg.Text, `"gates":[]`) {
		t.Errorf("empty plan should serialize gates as [], got %s", msg.Text)
	}
}

func TestNewArtifactID_StableAndDistinct(t *testing.T) {
	a := NewArtifactID("s1", "a/b.png")
	b := NewArtifactID("s1", "a/b.png")
	if a != b {
		t.Fatalf("NewArtifactID not stable: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "art-") {
		t.Errorf("ID should have art- prefix: %q", a)
	}
	c := NewArtifactID("s1", "a/c.png")
	if a == c {
		t.Errorf("different paths should yield different IDs: %q == %q", a, c)
	}
	d := NewArtifactID("s2", "a/b.png")
	if a == d {
		t.Errorf("different step ids should yield different IDs: %q == %q", a, d)
	}
}
