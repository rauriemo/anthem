package orchestrator

// OriginKind classifies where an artifact entry came from. It drives the
// surgical scoping filter in HydrateFeatureContext so Execute steps only
// see artifacts from their own plan's upstream steps, while Chat-mode
// registrations and pre-schema entries render unconditionally.
type OriginKind string

const (
	// OriginKindExecute stamps every artifact registered by the Execute
	// runner. PlanID + RunID + StepID are mandatory; empty StepID is a
	// programmer error and panics loud at write time (see runtime
	// invariant below).
	OriginKindExecute OriginKind = "execute"
	// OriginKindChat stamps artifacts registered by direct chat
	// invocations of a guest. Only AgentID is populated.
	OriginKindChat OriginKind = "chat"
	// OriginKindLegacy stamps artifacts discovered on disk that pre-date
	// the origin schema. The backfill pass stamps them once so readers
	// can tell "no origin" from "unknown provenance" and filter them
	// out of upstream-scoped views.
	OriginKindLegacy OriginKind = "legacy"
)

// ArtifactOrigin is the structured origin block persisted inside each
// artifacts.yaml entry. See plan decision D13 for the full rationale.
// The struct deliberately carries the smallest key set that can answer
// "which plan, which run, which step produced this?" -- so a later
// cross-plan or re-run scan never has to fall back to string heuristics.
type ArtifactOrigin struct {
	Kind    OriginKind `yaml:"kind"`
	PlanID  string     `yaml:"plan_id,omitempty"`
	RunID   string     `yaml:"run_id,omitempty"`
	StepID  string     `yaml:"step_id,omitempty"`
	AgentID string     `yaml:"agent_id,omitempty"`
}

// OriginTag is a sealed interface: only the concrete tag types declared
// in this file implement it (via the unexported marker method), so the
// featurewriter cannot be invoked with a hand-rolled "general purpose"
// origin -- every call site is forced through a known variant at
// compile time. This is the guardrail GPT flagged: without it, the
// write path silently misclassifies artifacts and D11's surgical
// scoping filter degrades invisibly.
type OriginTag interface {
	toOrigin() ArtifactOrigin
	isOriginTag()
}

// ExecuteOrigin is the call-site tag used by the PlanRunner when a
// guest's context_report is parsed during an execute run. Every field
// is mandatory; the runtime invariant in AppendArtifactWithOrigin
// panics if StepID is blank so the empty-string failure mode cannot
// slip into production.
type ExecuteOrigin struct {
	PlanID  string
	RunID   string
	StepID  string
	AgentID string
}

func (e ExecuteOrigin) toOrigin() ArtifactOrigin {
	return ArtifactOrigin{
		Kind:    OriginKindExecute,
		PlanID:  e.PlanID,
		RunID:   e.RunID,
		StepID:  e.StepID,
		AgentID: e.AgentID,
	}
}

func (ExecuteOrigin) isOriginTag() {}

// ChatOrigin is the call-site tag used by guestdispatch when a guest
// invoked outside of a plan emits a context_report. Only AgentID is
// populated; the plan/run/step keys are meaningless in chat mode.
type ChatOrigin struct {
	AgentID string
}

func (c ChatOrigin) toOrigin() ArtifactOrigin {
	return ArtifactOrigin{
		Kind:    OriginKindChat,
		AgentID: c.AgentID,
	}
}

func (ChatOrigin) isOriginTag() {}

// legacyOrigin is the stamp applied by the on-read backfill pass in
// readArtifactsRaw. It is NOT exported: no live call site should ever
// construct a legacy origin, the writer hands them out only when a
// pre-schema entry is discovered on disk.
func legacyOrigin() ArtifactOrigin {
	return ArtifactOrigin{Kind: OriginKindLegacy}
}
