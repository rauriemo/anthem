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

	msg := GateOpenedEvent(gate, arts, "s1", "miyazaki", "Miyazaki", review, "plan-42", "thread-42")

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
}

func TestGateOpenedEvent_NilReviewAndEmptyIdentityAreTolerated(t *testing.T) {
	gate := ApprovalGate{ID: "g2", AfterStep: "s2"}

	msg := GateOpenedEvent(gate, nil, "s2", "", "", nil, "", "thread-x")

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
	msg := GateOpenedEvent(gate, nil, "s3", "a", "A", nil, "", "thread")

	var got gateEventPayload
	_ = json.Unmarshal([]byte(msg.Text), &got)
	if got.ReviewLink != "" {
		t.Errorf("expected empty review_link without plan or gate ids, got %q", got.ReviewLink)
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
