package execute

import (
	"strings"
	"testing"
	"time"
)

var testGuests = []string{"artist", "coder", "writer"}

func validPlan() ExecutionPlan {
	return ExecutionPlan{
		Steps: []PlanStep{
			{ID: "s1", AgentID: "artist", Description: "Draw sprites", Status: StepPending},
			{ID: "s2", AgentID: "coder", Description: "Implement logic", DependsOn: "s1", Status: StepPending},
		},
		Gates: []ApprovalGate{
			{ID: "g1", AfterStep: "s1", Prompt: "Review sprites?"},
		},
		Metadata: PlanMetadata{Title: "Test Plan", CreatedAt: time.Now()},
	}
}

func TestValidate_Valid(t *testing.T) {
	p := validPlan()
	if err := p.Validate(testGuests); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
}

func TestValidate_Errors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ExecutionPlan)
		wantErr string
	}{
		{"no steps", func(p *ExecutionPlan) { p.Steps = nil }, "no steps"},
		{"empty step ID", func(p *ExecutionPlan) { p.Steps[0].ID = "" }, "empty ID"},
		{"duplicate step ID", func(p *ExecutionPlan) { p.Steps[1].ID = "s1" }, "duplicate step ID"},
		{"unknown agent", func(p *ExecutionPlan) { p.Steps[0].AgentID = "unknown" }, "unknown agent"},
		{"empty agent", func(p *ExecutionPlan) { p.Steps[0].AgentID = "" }, "empty AgentID"},
		{"depends on unknown step", func(p *ExecutionPlan) { p.Steps[1].DependsOn = "nope" }, "unknown step"},
		{"cycle", func(p *ExecutionPlan) {
			p.Steps[0].DependsOn = "s2"
		}, "cycle"},
		{"duplicate gate ID", func(p *ExecutionPlan) {
			p.Gates = append(p.Gates, ApprovalGate{ID: "g1", AfterStep: "s2", Prompt: "ok?"})
		}, "duplicate gate ID"},
		{"gate references unknown step", func(p *ExecutionPlan) {
			p.Gates[0].AfterStep = "nope"
		}, "unknown step"},
		{"empty gate ID", func(p *ExecutionPlan) {
			p.Gates[0].ID = ""
		}, "empty ID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validPlan()
			tt.mutate(&p)
			err := p.Validate(testGuests)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestNextPendingStep(t *testing.T) {
	p := validPlan()

	next := p.NextPendingStep()
	if next == nil || next.ID != "s1" {
		t.Fatalf("expected s1, got %v", next)
	}

	// s2 should NOT be ready yet (s1 still pending)
	p.Steps[0].Status = StepRunning
	if got := p.NextPendingStep(); got != nil {
		t.Fatalf("expected nil while s1 running, got %s", got.ID)
	}

	p.Steps[0].Status = StepCompleted
	next = p.NextPendingStep()
	if next == nil || next.ID != "s2" {
		t.Fatalf("expected s2, got %v", next)
	}

	p.Steps[1].Status = StepCompleted
	if got := p.NextPendingStep(); got != nil {
		t.Fatalf("expected nil when all done, got %s", got.ID)
	}
}

func TestGateAfterStep(t *testing.T) {
	p := validPlan()
	g := p.GateAfterStep("s1")
	if g == nil || g.ID != "g1" {
		t.Fatalf("expected g1, got %v", g)
	}
	if p.GateAfterStep("s2") != nil {
		t.Fatal("expected nil gate for s2")
	}
}

func TestAllCompleted(t *testing.T) {
	p := validPlan()
	if p.AllCompleted() {
		t.Fatal("should not be completed initially")
	}
	for i := range p.Steps {
		p.Steps[i].Status = StepCompleted
	}
	if !p.AllCompleted() {
		t.Fatal("should be completed when all steps done")
	}
}
