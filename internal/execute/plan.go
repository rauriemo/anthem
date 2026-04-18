package execute

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepCompleted StepStatus = "completed"
	StepFailed    StepStatus = "failed"
)

type PlanStep struct {
	ID          string     `json:"id"`
	AgentID     string     `json:"agent_id"`
	Description string     `json:"description"`
	DependsOn   string     `json:"depends_on,omitempty"`
	Status      StepStatus `json:"status"`
}

type ApprovalGate struct {
	ID        string `json:"id"`
	AfterStep string `json:"after_step"`
	Prompt    string `json:"prompt"`
}

type StepArtifact struct {
	StepID  string `json:"step_id"`
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	// ArtifactID is a stable, deterministic identifier (sha256 prefix of
	// step_id + path) so deep links and selective-revise can address an
	// individual artifact without depending on its array position. It is
	// populated by NewArtifactID when providers emit artifacts.
	ArtifactID string `json:"artifact_id,omitempty"`
	// UpdatedInLastRevise marks artifacts produced by the most recent
	// partial-revise pass so UIs can visually flag "new" items. Managed
	// by the runner during selective revise (Stage 4).
	UpdatedInLastRevise bool `json:"updated_in_last_revise,omitempty"`
}

// NewArtifactID returns the stable artifact ID for a given (stepID, path).
// It is a truncated sha256 hash so the same (step, path) always maps to the
// same ID across runs and processes, which is the property deep links and
// selective revise rely on.
func NewArtifactID(stepID, path string) string {
	sum := sha256.Sum256([]byte(stepID + "\x00" + path))
	return "art-" + hex.EncodeToString(sum[:8])
}

type PlanMetadata struct {
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	// PlanGeneration is the compile generation recorded by the markdown plan
	// at the moment this ExecutionPlan JSON was produced. It lets callers
	// detect frontmatter-vs-sidecar drift (e.g. a frontmatter bump that
	// succeeded while the subsequent atomic rename of the sidecar failed)
	// without requiring an extra bookkeeping file.
	PlanGeneration int `json:"plan_generation,omitempty"`
}

type ExecutionPlan struct {
	Steps    []PlanStep     `json:"steps"`
	Gates    []ApprovalGate `json:"gates,omitempty"`
	Metadata PlanMetadata   `json:"metadata"`
}

// Validate checks structural integrity of the plan. guestIDs is the set of
// known guest agent slugs; every step's AgentID must appear in this set.
func (p *ExecutionPlan) Validate(guestIDs []string) error {
	if len(p.Steps) == 0 {
		return fmt.Errorf("plan has no steps")
	}

	guestSet := make(map[string]bool, len(guestIDs))
	for _, id := range guestIDs {
		guestSet[id] = true
	}

	stepIDs := make(map[string]bool, len(p.Steps))
	for _, s := range p.Steps {
		if s.ID == "" {
			return fmt.Errorf("step has empty ID")
		}
		if stepIDs[s.ID] {
			return fmt.Errorf("duplicate step ID %q", s.ID)
		}
		stepIDs[s.ID] = true
	}

	for _, s := range p.Steps {
		if s.AgentID == "" {
			return fmt.Errorf("step %q has empty AgentID", s.ID)
		}
		if !guestSet[s.AgentID] {
			return fmt.Errorf("step %q references unknown agent %q", s.ID, s.AgentID)
		}
		if s.DependsOn != "" && !stepIDs[s.DependsOn] {
			return fmt.Errorf("step %q depends on unknown step %q", s.ID, s.DependsOn)
		}
	}

	// Cycle detection via visited set (walk DependsOn chains).
	for _, s := range p.Steps {
		visited := map[string]bool{s.ID: true}
		cur := s.DependsOn
		for cur != "" {
			if visited[cur] {
				return fmt.Errorf("cycle detected involving step %q", cur)
			}
			visited[cur] = true
			cur = stepByID(p.Steps, cur).DependsOn
		}
	}

	gateIDs := make(map[string]bool, len(p.Gates))
	for _, g := range p.Gates {
		if g.ID == "" {
			return fmt.Errorf("gate has empty ID")
		}
		if gateIDs[g.ID] {
			return fmt.Errorf("duplicate gate ID %q", g.ID)
		}
		gateIDs[g.ID] = true
		if !stepIDs[g.AfterStep] {
			return fmt.Errorf("gate %q references unknown step %q", g.ID, g.AfterStep)
		}
	}

	return nil
}

func stepByID(steps []PlanStep, id string) PlanStep {
	for _, s := range steps {
		if s.ID == id {
			return s
		}
	}
	return PlanStep{}
}

// NextPendingStep returns the first step whose status is pending and whose
// dependency (if any) is completed. Returns nil when no step is ready.
func (p *ExecutionPlan) NextPendingStep() *PlanStep {
	for i := range p.Steps {
		s := &p.Steps[i]
		if s.Status != StepPending {
			continue
		}
		if s.DependsOn == "" || stepByID(p.Steps, s.DependsOn).Status == StepCompleted {
			return s
		}
	}
	return nil
}

// GateAfterStep returns the approval gate (if any) configured to fire after
// the given step completes.
func (p *ExecutionPlan) GateAfterStep(stepID string) *ApprovalGate {
	for i := range p.Gates {
		if p.Gates[i].AfterStep == stepID {
			return &p.Gates[i]
		}
	}
	return nil
}

type GateAction string

const (
	GateApprove GateAction = "approve"
	GateRevise  GateAction = "revise"
	GateAbort   GateAction = "abort"
)

type GateResolution struct {
	Action   GateAction
	Feedback string
	// FlaggedArtifacts, when non-empty on a GateRevise, signals a selective
	// revise: only the listed artifacts should be regenerated; all others
	// are preserved in place. Empty (or nil) preserves today's full-revise
	// behavior. See the plan's Phase 5a for the preserve-and-replace flow.
	FlaggedArtifacts []FlaggedArtifact
}

// FlaggedArtifact is one item a user asked to be regenerated during a
// selective revise. ArtifactID matches the stable ID populated on
// StepArtifact.ArtifactID by NewArtifactID. Note is an optional per-item
// revise hint; empty means "produce a different variant, no guidance".
type FlaggedArtifact struct {
	ArtifactID string
	Note       string
}

// AllCompleted returns true when every step has reached StepCompleted.
func (p *ExecutionPlan) AllCompleted() bool {
	for _, s := range p.Steps {
		if s.Status != StepCompleted {
			return false
		}
	}
	return true
}
