package execute

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/guests"
	"github.com/rauriemo/anthem/internal/harness"
	"github.com/rauriemo/anthem/internal/prompt"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/conduit/pkg/mcpconfig"
)

// Broadcaster abstracts event/message broadcasting so the runner can be tested
// without a real channel.Manager.
type Broadcaster interface {
	Broadcast(ctx context.Context, msg channel.OutgoingMessage) error
}

// RunnerOpts holds dependencies injected into the PlanRunner at construction time.
type RunnerOpts struct {
	GuestIndex  *guests.GuestIndex
	Runner      agent.AgentRunner
	ChannelMgr  Broadcaster
	Artifacts   ArtifactProvider
	ProjectRoot string
	// ProjectSlug identifies the project Prism should resolve for
	// /files/{slug}/{path} static serving. Emitted on gate_opened events so
	// image-gallery / video-preview / document review UIs can fetch artifact
	// bytes. When empty, callers should fall back to the orchestrator agent
	// id (Prism's ProjectResolver registers projects by agent id).
	ProjectSlug      string
	AgentsDir        string
	GlobalMCPServers map[string]mcpconfig.MCPServerRef
	GuestMCPMaxTurns int
	Logger           *slog.Logger
	// PlanID + CompileGeneration identify the run log entry this
	// invocation belongs to. Recorded in the run_started event so
	// ReplayToSnapshot can emit the same plan identity Prism sees on
	// the execution.plan_snapshot wire. Zero values are tolerated for
	// legacy dispatch paths (inline [execute:plan] tags in tests) that
	// don't carry a compiled plan identity.
	PlanID            string
	CompileGeneration int
	// RunLog receives every state transition before the corresponding
	// channel broadcast, making the run log the authoritative source
	// for restart and reconnect recovery. When nil, NoopAppender is
	// used so the runner remains drop-in compatible with tests and
	// legacy paths that have no plan identity.
	RunLog RunEventAppender
	// RunLogPath is the filesystem path of the run log .jsonl file
	// (see plans/runs.NewRunLogPath). When set, PlanRunner dumps the
	// final assembled prompt of every step to a sibling file named
	// step-<id>.prompt.txt so operators can diff exactly what each
	// agent saw (plan decision D6). Empty disables the dump; legacy
	// inline-dispatch paths pass "" and remain silent.
	RunLogPath string
	// OrchestratorAgent carries the parsed frontmatter of
	// agents/orchestrator.md so PlanRunner can dispatch steps whose
	// AgentID is guests.OrchestratorGuestID through the orchestrator's
	// own persona, model, allowed tools, and MCP config. The guest
	// scan intentionally excludes orchestrator.md from GuestIndex, so
	// without this field an orchestrator step would fall through the
	// map lookup in buildRunOpts and run with bare defaults. May be
	// nil for dispatch paths that never produce orchestrator steps
	// (legacy inline [execute:plan] fixtures, tests that don't seed
	// an agents directory).
	OrchestratorAgent *guests.GuestAgent
	// Context is the live read-side handle for the five LiveContext
	// slices (user context, project summary, feature context, shared
	// context, conversation history). The runner re-queries it at the
	// start of every step rather than snapshotting at run-start, so
	// downstream steps see the outputs of upstream steps the moment
	// they land (plan decision D1). May be nil for legacy test paths
	// that don't exercise prompt-building.
	Context prompt.ContextSources
	// ActiveFeature is the feature slug (e.g. "character-sprites")
	// that scopes HydrateFeatureContext lookups. When empty the
	// feature context is suppressed, which matches the legacy behavior
	// for runs launched outside a feature.
	ActiveFeature string
	// ChannelKey is the convo-buffer / shared-context key for the
	// channel this run was dispatched from (e.g. "prism", "cli").
	// Required for HistoryText/SharedContextText to resolve the right
	// per-channel state; empty disables both slices.
	ChannelKey string
	// RunStartedAt is the plan-start timestamp used by HistoryText to
	// filter out user messages that landed after dispatch (D12). Zero
	// value disables filtering, which is the correct behavior for
	// non-execute dispatch paths that reuse RunnerOpts in tests.
	RunStartedAt time.Time
}

type gateMsg struct {
	TargetGateID string
	TargetStepID string
	Resolution   GateResolution
}

// PlanRunner drives an ExecutionPlan through sequential step execution with
// approval gates and failure pauses.
type PlanRunner struct {
	opts   RunnerOpts
	plan   *ExecutionPlan
	logger *slog.Logger

	mu     sync.Mutex
	gateCh chan gateMsg

	// collected artifacts indexed by step ID
	artifacts map[string][]StepArtifact

	// preservedArtifacts holds artifacts kept in place during a selective
	// revise. When a step is re-queued after a partial revise, the runner
	// consults this map to merge the preserved set with whatever the agent
	// produces on the next run (see Phase 5a preserve-and-replace).
	preservedArtifacts map[string][]StepArtifact

	// consecutivePartialRevises tracks how many partial-revise rounds a
	// step has been through in a row so callers (tests, UX nudges) can
	// detect coherence-decay risk. Reset on Approve/Abort/full revise.
	consecutivePartialRevises map[string]int

	// revisionState stores structured revision feedback keyed by step
	// ID so the next run of that step can surface it as a dedicated
	// "Prior Revision Feedback" section via prompt.BuildGuestPrompt
	// (plan decision D4). Cleared on successful completion of the
	// step or on gate approve/abort.
	revisionState map[string]*prompt.RevisionFeedback
}

func NewPlanRunner(opts RunnerOpts) *PlanRunner {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.RunLog == nil {
		opts.RunLog = NoopAppender{}
	}
	return &PlanRunner{
		opts:                      opts,
		logger:                    logger,
		gateCh:                    make(chan gateMsg, 1),
		artifacts:                 make(map[string][]StepArtifact),
		preservedArtifacts:        make(map[string][]StepArtifact),
		consecutivePartialRevises: make(map[string]int),
		revisionState:             make(map[string]*prompt.RevisionFeedback),
	}
}

// Plan returns the current execution plan state (thread-safe snapshot).
func (r *PlanRunner) Plan() *ExecutionPlan {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *r.plan
	cp.Steps = make([]PlanStep, len(r.plan.Steps))
	copy(cp.Steps, r.plan.Steps)
	cp.Gates = make([]ApprovalGate, len(r.plan.Gates))
	copy(cp.Gates, r.plan.Gates)
	return &cp
}

// ResolveGate unblocks the runner when paused at an approval gate.
func (r *PlanRunner) ResolveGate(gateID string, resolution GateResolution) {
	r.gateCh <- gateMsg{TargetGateID: gateID, Resolution: resolution}
}

// ResolveFailure unblocks the runner when paused at a failed step.
func (r *PlanRunner) ResolveFailure(stepID string, resolution GateResolution) {
	r.gateCh <- gateMsg{TargetStepID: stepID, Resolution: resolution}
}

// Run drives the plan to completion (or abort). It blocks until the plan
// finishes, is aborted, or the context is canceled. Intended to be called
// in its own goroutine.
func (r *PlanRunner) Run(ctx context.Context, plan *ExecutionPlan, threadID string) error {
	// Log-first: the run_started event carries the compiled plan
	// snapshot so a replay-only consumer (startup triage, on-connect
	// plan_snapshot emit) can reconstruct the step/gate identities
	// without a second file read.
	r.opts.RunLog.AppendRunStarted(r.opts.PlanID, r.opts.CompileGeneration, plan, r.opts.ProjectSlug)

	r.mu.Lock()
	r.plan = plan
	r.mu.Unlock()

	r.broadcast(ctx, PlanLoadedEvent(plan, threadID))

	return r.driveSteps(ctx, plan, threadID)
}

// driveSteps is the main step-dispatch loop shared by Run (fresh
// dispatch) and ResumeAtGate (orchestrator startup replay). It keeps
// pulling NextPendingStep until the plan is complete, stuck, or a
// step returns an error (abort).
func (r *PlanRunner) driveSteps(ctx context.Context, plan *ExecutionPlan, threadID string) error {
	for {
		r.mu.Lock()
		step := r.plan.NextPendingStep()
		r.mu.Unlock()

		if step == nil {
			r.mu.Lock()
			done := r.plan.AllCompleted()
			r.mu.Unlock()
			if done {
				r.opts.RunLog.AppendRunCompleted()
				r.broadcast(ctx, PlanCompletedEvent(plan, threadID))
				r.broadcast(ctx, RunArchivedEvent(r.opts.PlanID, r.opts.CompileGeneration, "", threadID))
				return nil
			}
			// No step is ready but plan isn't done -- blocked by deps or all failed
			r.opts.RunLog.AppendRunAborted("plan stuck: no pending step is ready")
			return fmt.Errorf("plan stuck: no pending step is ready")
		}

		if err := r.runStep(ctx, step, threadID); err != nil {
			return err
		}
	}
}

// ResumeAtGate reattaches the runner to a run log whose tail event is
// gate_opened. Called by the orchestrator's startup replay once it has
// reconstructed the step statuses and per-step artifacts from the log.
// The gate_opened broadcast is intentionally NOT re-emitted: Prism
// receives the full drawer state via the execution.plan_snapshot event
// emitted on WebSocket connect.
//
// After the user resolves the gate via ResolveGate, ResumeAtGate
// applies the resolution (approve/revise/abort) and continues the
// plan via driveSteps exactly as Run would have.
func (r *PlanRunner) ResumeAtGate(
	ctx context.Context,
	plan *ExecutionPlan,
	perStepArtifacts map[string][]StepArtifact,
	openGate ApprovalGate,
	threadID string,
) error {
	r.mu.Lock()
	r.plan = plan
	for sid, arts := range perStepArtifacts {
		r.artifacts[sid] = append([]StepArtifact(nil), arts...)
	}
	r.mu.Unlock()

	step := findStepInPlan(plan, openGate.AfterStep)
	if step == nil {
		return fmt.Errorf("resume: gate %q refers to unknown step %q", openGate.ID, openGate.AfterStep)
	}
	collected := perStepArtifacts[openGate.AfterStep]
	_, _, review := r.agentReviewContext(step.AgentID)

	// Wait for the resolution the user will send via ResolveGate. The
	// body matches the gate-resolved branch of handleGate but without
	// the gate_opened broadcast (Prism already has the drawer state).
	select {
	case <-ctx.Done():
		return ctx.Err()
	case msg := <-r.gateCh:
		r.opts.RunLog.AppendGateResolved(openGate.ID, msg.Resolution.Action, msg.Resolution.Feedback)
		r.broadcast(ctx, GateResolvedEvent(openGate.ID, msg.Resolution.Action, msg.Resolution.Feedback, threadID))

		switch msg.Resolution.Action {
		case GateApprove:
			r.clearPartialReviseState(openGate.AfterStep)
			r.deactivateStepAgent(ctx, step, fmt.Sprintf("step %s complete", step.ID), threadID)
		case GateRevise:
			r.opts.RunLog.AppendStepQueued(openGate.AfterStep, step.AgentID)
			r.applyRevise(openGate.AfterStep, msg.Resolution, collected, review)
		case GateAbort:
			r.clearPartialReviseState(openGate.AfterStep)
			r.opts.RunLog.AppendRunAborted("aborted at gate")
			r.deactivateStepAgent(ctx, step, fmt.Sprintf("step %s aborted", step.ID), threadID)
			r.broadcast(ctx, PlanAbortedEvent(r.plan, "aborted at gate", threadID))
			r.broadcast(ctx, RunArchivedEvent(r.opts.PlanID, r.opts.CompileGeneration, "aborted at gate", threadID))
			return fmt.Errorf("plan aborted at gate %q", openGate.ID)
		}
	}

	return r.driveSteps(ctx, plan, threadID)
}

// findStepInPlan locates a step by ID inside the plan and returns a
// pointer into the plan's Steps slice so callers can mutate status
// directly (matching the rest of the runner's in-memory discipline).
func findStepInPlan(plan *ExecutionPlan, stepID string) *PlanStep {
	for i := range plan.Steps {
		if plan.Steps[i].ID == stepID {
			return &plan.Steps[i]
		}
	}
	return nil
}

func (r *PlanRunner) runStep(ctx context.Context, step *PlanStep, threadID string) error {
	r.opts.RunLog.AppendStepStarted(step.ID, step.AgentID)
	r.setStepStatus(step.ID, StepRunning)
	r.broadcast(ctx, StepStartedEvent(*step, threadID))

	// Ephemeral per-step roster: PlanRunner is the sole activator during
	// Execute. Broadcasting ActivateGuest here makes the Prism sidebar
	// show exactly "this step's owner" for the duration of the step, then
	// handleGate broadcasts DeactivateGuest so the chip disappears before
	// the next step activates. See orchestrator.clearPlanTimeRosterForExecute
	// for the handoff that guarantees the roster is empty when we start.
	if step.AgentID != "" {
		r.broadcast(ctx, channel.OutgoingMessage{
			ActivateGuest: &channel.ActivateGuest{
				ID:     step.AgentID,
				Reason: fmt.Sprintf("Starting step %s: %s", step.ID, step.Description),
			},
			ThreadID: threadID,
		})
	}

	// Inject upstream artifacts
	upstream := r.upstreamArtifacts(step.DependsOn)
	if r.opts.Artifacts != nil {
		if err := r.opts.Artifacts.Inject(step.ID, upstream); err != nil {
			r.logger.Warn("artifact injection failed", "step", step.ID, "error", err)
		}
		if marker, ok := r.opts.Artifacts.(interface{ MarkStepStart(string) }); ok {
			marker.MarkStepStart(step.ID)
		}
	}

	promptText := r.buildStepPrompt(step, upstream)
	r.dumpStepPrompt(step.ID, promptText)
	runOpts := r.buildRunOpts(step, promptText, threadID)

	result, runErr := r.opts.Runner.Run(ctx, runOpts)

	if runErr != nil || (result != nil && result.ExitCode != 0) {
		return r.handleStepFailure(ctx, step, threadID, runErr, result)
	}

	// Success path
	var collected []StepArtifact
	if r.opts.Artifacts != nil {
		var err error
		collected, err = r.opts.Artifacts.Collect(step.ID)
		if err != nil {
			r.logger.Warn("artifact collection failed", "step", step.ID, "error", err)
		}
	}

	// Selective-revise merge: if a prior gate left a preserved set for this
	// step, the artifacts just collected are treated as "regenerated" items
	// and merged with the preserved set (dedup on path, new wins). Newly-
	// regenerated artifacts are flagged with UpdatedInLastRevise so the UI
	// can badge them as fresh and the user can compare to what was preserved.
	collected = r.mergePreservedWithCollected(step.ID, collected)

	r.mu.Lock()
	r.artifacts[step.ID] = collected
	r.mu.Unlock()

	r.opts.RunLog.AppendStepCompleted(step.ID, step.AgentID, collected)
	r.setStepStatus(step.ID, StepCompleted)
	r.broadcast(ctx, StepCompletedEvent(step.ID, step.AgentID, collected, threadID))

	// Check for approval gate after this step
	r.mu.Lock()
	gate := r.plan.GateAfterStep(step.ID)
	r.mu.Unlock()

	if gate != nil {
		return r.handleGate(ctx, gate, step, collected, threadID)
	}

	// No gate: the step is done and there's nothing for the user to
	// approve, so the step owner's chip should disappear before the next
	// step activates. Gated steps emit DeactivateGuest from handleGate
	// after the resolution (approve or abort) so the chip lingers through
	// review.
	r.deactivateStepAgent(ctx, step, fmt.Sprintf("step %s complete", step.ID), threadID)

	return nil
}

func (r *PlanRunner) handleStepFailure(ctx context.Context, step *PlanStep, threadID string, runErr error, result *types.RunResult) error {
	errMsg := "agent run failed"
	if runErr != nil {
		errMsg = runErr.Error()
	} else if result != nil {
		errMsg = fmt.Sprintf("exit code %d", result.ExitCode)
	}

	// Plan decision D8: append the driver's stderr tail so operators
	// can triage "exit code 1" failures without digging into the run
	// log's raw bytes. The driver caps stderr to 16 KiB, and we
	// further trim to a human-friendly size so step_failed events
	// stay wire-cheap.
	if result != nil && strings.TrimSpace(result.Stderr) != "" {
		errMsg = errMsg + " | stderr: " + trimStderr(result.Stderr)
	}

	r.opts.RunLog.AppendStepFailed(step.ID, step.AgentID, errMsg)
	r.setStepStatus(step.ID, StepFailed)
	r.broadcast(ctx, StepFailedEvent(step.ID, step.AgentID, errMsg, threadID))

	// Pause and wait for human resolution
	select {
	case <-ctx.Done():
		return ctx.Err()
	case msg := <-r.gateCh:
		switch msg.Resolution.Action {
		case GateApprove:
			// "approve" on a failure means "retry as-is". Emitted as a
			// step_queued event so the run log records the user's
			// intent to retry rather than silently resetting status.
			r.opts.RunLog.AppendStepQueued(step.ID, step.AgentID)
			r.setStepStatus(step.ID, StepPending)
			return nil
		case GateRevise:
			r.opts.RunLog.AppendStepQueued(step.ID, step.AgentID)
			r.mu.Lock()
			for i := range r.plan.Steps {
				if r.plan.Steps[i].ID == step.ID {
					r.plan.Steps[i].Description += "\n\nRevision: " + msg.Resolution.Feedback
					r.plan.Steps[i].Status = StepPending
					break
				}
			}
			r.mu.Unlock()
			return nil
		case GateAbort:
			r.opts.RunLog.AppendRunAborted("step failure aborted by user")
			r.deactivateStepAgent(ctx, step, fmt.Sprintf("step %s aborted", step.ID), threadID)
			r.broadcast(ctx, PlanAbortedEvent(r.plan, "step failure aborted by user", threadID))
			r.broadcast(ctx, RunArchivedEvent(r.opts.PlanID, r.opts.CompileGeneration, "step failure aborted by user", threadID))
			return fmt.Errorf("plan aborted at step %q", step.ID)
		}
	}

	return nil
}

func (r *PlanRunner) handleGate(ctx context.Context, gate *ApprovalGate, step *PlanStep, artifacts []StepArtifact, threadID string) error {
	agentID, agentName, review := r.agentReviewContext(step.AgentID)

	// Enforce the review spec's artifact_globs at the gate boundary.
	// ImageGalleryView / VideoPreviewView / DocumentView in Prism each
	// assume every artifact they receive is appropriate for their
	// renderer (an image tag, a video tag, a markdown pane). Without
	// this filter, a step that produces mixed outputs (e.g. Miyazaki
	// generating both a .md character reference doc AND .png sprites)
	// would surface the .md to ImageGalleryView where it renders as a
	// broken <img src> tile -- the filename loads but the browser
	// can't decode markdown as an image.
	//
	// Globs are declared in agent frontmatter (review.artifact_globs)
	// and validated at guest-load time. FilterArtifactsByGlobs
	// silently returns the input unchanged when globs is empty, so
	// legacy agents without a glob config see no behavior change.
	if review != nil {
		artifacts = FilterArtifactsByGlobs(artifacts, review.ArtifactGlobs)
	}

	// Emit the guest-authored notification *before* gate_opened so the chat
	// panel picks it up in the same tick as the drawer. If we don't have the
	// agent in the guest index (legacy seed plans, tests) we fall back to a
	// minimal guest record built from the step metadata.
	agent := r.guestForNotification(step.AgentID, agentID, agentName)
	r.broadcast(ctx, GateNotificationEvent(
		*gate,
		step.ID,
		step.Description,
		len(artifacts),
		agent,
		review,
		threadID,
		threadID,
	))

	allowedActions := []string{"approve", "revise", "abort"}
	reviewLink := buildReviewLink(r.opts.PlanID, gate.ID)
	r.opts.RunLog.AppendGateOpened(
		gate.ID,
		step.ID,
		agentID,
		agentName,
		gate.Prompt,
		artifacts,
		allowedActions,
		review,
		reviewLink,
	)
	r.broadcast(ctx, GateOpenedEvent(
		*gate,
		artifacts,
		step.ID,
		agentID,
		agentName,
		review,
		threadID,
		r.opts.ProjectSlug,
		threadID,
	))

	select {
	case <-ctx.Done():
		return ctx.Err()
	case msg := <-r.gateCh:
		r.opts.RunLog.AppendGateResolved(gate.ID, msg.Resolution.Action, msg.Resolution.Feedback)
		r.broadcast(ctx, GateResolvedEvent(gate.ID, msg.Resolution.Action, msg.Resolution.Feedback, threadID))

		switch msg.Resolution.Action {
		case GateApprove:
			r.clearPartialReviseState(gate.AfterStep)
			// Approve means the step's owner is done; drop the chip
			// so the next step's ActivateGuest is the only pill
			// visible in the Prism roster.
			r.deactivateStepAgent(ctx, step, fmt.Sprintf("step %s complete", step.ID), threadID)
			return nil
		case GateRevise:
			// Revise: the same guest is about to re-run the step, so
			// we intentionally keep them active. Emitting
			// DeactivateGuest here would cause the chip to flicker
			// between the revise click and the next StepStarted.
			r.opts.RunLog.AppendStepQueued(gate.AfterStep, step.AgentID)
			r.applyRevise(gate.AfterStep, msg.Resolution, artifacts, review)
			return nil
		case GateAbort:
			r.clearPartialReviseState(gate.AfterStep)
			r.opts.RunLog.AppendRunAborted("aborted at gate")
			r.deactivateStepAgent(ctx, step, fmt.Sprintf("step %s aborted", step.ID), threadID)
			r.broadcast(ctx, PlanAbortedEvent(r.plan, "aborted at gate", threadID))
			r.broadcast(ctx, RunArchivedEvent(r.opts.PlanID, r.opts.CompileGeneration, "aborted at gate", threadID))
			return fmt.Errorf("plan aborted at gate %q", gate.ID)
		}
	}

	return nil
}

// deactivateStepAgent emits a DeactivateGuest frame for the step's owner
// on the channel. Centralized so every PlanRunner code path that concludes
// a step (gate approve, gate abort, no-gate completion, step failure
// abort) follows the same ordering: StepCompleted/StepFailed frames,
// optional GateResolved, then DeactivateGuest. A missing AgentID (legacy
// seed plans, test fixtures) is treated as a no-op.
func (r *PlanRunner) deactivateStepAgent(ctx context.Context, step *PlanStep, reason, threadID string) {
	if step == nil || step.AgentID == "" {
		return
	}
	r.broadcast(ctx, channel.OutgoingMessage{
		DeactivateGuest: &channel.DeactivateGuest{
			ID:     step.AgentID,
			Reason: reason,
		},
		ThreadID: threadID,
	})
}

// applyRevise rewrites the step description with revise context and re-queues
// it. On a selective revise (FlaggedArtifacts non-empty) the runner records
// the preserved artifacts and builds a structured block that tells the agent
// exactly which items to preserve, which to regenerate, and any per-item
// notes. On a full revise it falls back to today's simple "Revision: <text>"
// append. In both cases the step returns to StepPending so the next
// NextPendingStep() iteration picks it up.
func (r *PlanRunner) applyRevise(stepID string, res GateResolution, artifacts []StepArtifact, review *guests.ReviewSpec) {
	selective := len(res.FlaggedArtifacts) > 0 && reviewSupportsPartial(review)

	if !selective {
		// Full revise: clear any preserved set + streak so a later
		// selective revise starts from a clean slate.
		r.mu.Lock()
		delete(r.preservedArtifacts, stepID)
		r.consecutivePartialRevises[stepID] = 0
		prev := r.revisionState[stepID]
		count := 1
		if prev != nil {
			count = prev.RevisionCount + 1
		}
		r.revisionState[stepID] = &prompt.RevisionFeedback{
			RevisionCount: count,
			UserNote:      res.Feedback,
		}
		for i := range r.plan.Steps {
			if r.plan.Steps[i].ID == stepID {
				r.plan.Steps[i].Description += "\n\nRevision: " + res.Feedback
				r.plan.Steps[i].Status = StepPending
				break
			}
		}
		r.mu.Unlock()
		return
	}

	flagged := make(map[string]string, len(res.FlaggedArtifacts))
	for _, f := range res.FlaggedArtifacts {
		flagged[f.ArtifactID] = f.Note
	}

	var preserve, regenerate []StepArtifact
	for _, a := range artifacts {
		if _, hit := flagged[a.ArtifactID]; hit {
			regenerate = append(regenerate, a)
		} else {
			preserve = append(preserve, a)
		}
	}

	block := buildReviseBlock(res.Feedback, preserve, regenerate, flagged, review)

	r.mu.Lock()
	r.preservedArtifacts[stepID] = preserve
	r.consecutivePartialRevises[stepID]++
	prev := r.revisionState[stepID]
	count := 1
	if prev != nil {
		count = prev.RevisionCount + 1
	}
	r.revisionState[stepID] = &prompt.RevisionFeedback{
		RevisionCount: count,
		UserNote:      res.Feedback,
	}
	for i := range r.plan.Steps {
		if r.plan.Steps[i].ID == stepID {
			r.plan.Steps[i].Description += "\n\n" + block
			r.plan.Steps[i].Status = StepPending
			break
		}
	}
	r.mu.Unlock()
}

// clearPartialReviseState wipes the preserve map + streak counter for a step
// once the gate is definitively approved or aborted. Called from the happy
// path so a later re-opened gate on the same step does not inherit stale
// preserve data.
func (r *PlanRunner) clearPartialReviseState(stepID string) {
	r.mu.Lock()
	delete(r.preservedArtifacts, stepID)
	r.consecutivePartialRevises[stepID] = 0
	delete(r.revisionState, stepID)
	r.mu.Unlock()
}

// ConsecutivePartialRevises exposes the streak counter so callers (tests,
// future UX nudges) can observe coherence-decay risk. Reads are locked; safe
// to call concurrently with Run.
func (r *PlanRunner) ConsecutivePartialRevises(stepID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.consecutivePartialRevises[stepID]
}

// guestForNotification builds the minimum GuestAgent needed for
// GateNotificationEvent. Falls back gracefully when the guest index is nil
// (tests) or missing the agent (legacy seed plans).
func (r *PlanRunner) guestForNotification(agentID, fallbackID, fallbackName string) guests.GuestAgent {
	if r.opts.GuestIndex != nil {
		if ag, ok := r.opts.GuestIndex.Agents[agentID]; ok {
			return ag
		}
	}
	id := agentID
	if id == "" {
		id = fallbackID
	}
	return guests.GuestAgent{ID: id, Name: fallbackName}
}

// mergePreservedWithCollected merges the preserved set (if any) with the
// artifacts just collected from the agent rerun. Artifacts from the
// collected slice win on conflicting paths (the agent genuinely regenerated
// them). The returned slice marks newly-regenerated artifacts with
// UpdatedInLastRevise=true and preserved items with false.
func (r *PlanRunner) mergePreservedWithCollected(stepID string, collected []StepArtifact) []StepArtifact {
	r.mu.Lock()
	preserve := r.preservedArtifacts[stepID]
	// One-shot: we only want this merge on the iteration right after the
	// revise; subsequent normal runs should not pick up stale preserves.
	delete(r.preservedArtifacts, stepID)
	r.mu.Unlock()

	if len(preserve) == 0 {
		return collected
	}

	newPaths := make(map[string]bool, len(collected))
	for _, a := range collected {
		newPaths[a.Path] = true
	}

	out := make([]StepArtifact, 0, len(collected)+len(preserve))
	for _, a := range collected {
		a.UpdatedInLastRevise = true
		out = append(out, a)
	}
	for _, a := range preserve {
		if newPaths[a.Path] {
			continue
		}
		a.UpdatedInLastRevise = false
		out = append(out, a)
	}
	return out
}

// reviewSupportsPartial returns true when the guest's declared review spec
// allows partial revise. A missing spec defaults to true (the kind will be
// checked by Prism); an explicit `partial_revise: false` disables it.
func reviewSupportsPartial(review *guests.ReviewSpec) bool {
	if review == nil {
		return true
	}
	if review.PartialRevise != nil && !*review.PartialRevise {
		return false
	}
	if review.Kind != "" && !guests.KindSupportsPartialRevise(review.Kind) {
		return false
	}
	return true
}

// buildReviseBlock renders the structured revise instructions appended to a
// step's description when the user asked for a selective revise. Format is
// markdown-style so guest agents (which read markdown prompts) can parse it
// naturally. The block is deterministic for stable test assertions.
func buildReviseBlock(feedback string, preserve, regenerate []StepArtifact, notes map[string]string, review *guests.ReviewSpec) string {
	var b strings.Builder
	b.WriteString("## Selective revise\n")
	if strings.TrimSpace(feedback) != "" {
		b.WriteString("Overall guidance: ")
		b.WriteString(feedback)
		b.WriteString("\n\n")
	}
	if len(preserve) > 0 {
		b.WriteString("### Preserve (do not regenerate)\n")
		for _, a := range preserve {
			b.WriteString("- ")
			b.WriteString(a.Path)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(regenerate) > 0 {
		b.WriteString("### Regenerate\n")
		for _, a := range regenerate {
			b.WriteString("- ")
			b.WriteString(a.Path)
			if note := strings.TrimSpace(notes[a.ArtifactID]); note != "" {
				b.WriteString(" -- ")
				b.WriteString(note)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	hint := coherenceHint(review)
	if hint != "" {
		b.WriteString("### Coherence hint\n")
		b.WriteString(hint)
		b.WriteString("\n")
	}
	return b.String()
}

func coherenceHint(review *guests.ReviewSpec) string {
	if review == nil {
		return ""
	}
	if h := strings.TrimSpace(review.CoherenceHint); h != "" {
		return h
	}
	return "Keep the preserved items' style, palette, and tone consistent with the regenerated ones. The user approved the preserved items; do not contradict them."
}

// buildStepPrompt assembles the full guest-facing prompt for a chained
// step. The output matches the shape produced by prompt.BuildGuestPrompt
// for chat dispatch so agents see the same persona + context envelope
// on every turn regardless of dispatch path (plan decision D10).
//
// When the optional prompt.ContextSources handle is not wired (legacy
// tests that construct a bare RunnerOpts), the builder falls back to
// the minimal Upstream Artifacts block so existing unit tests keep
// their deterministic expectations.
func (r *PlanRunner) buildStepPrompt(step *PlanStep, upstream []StepArtifact) string {
	if r.opts.Context == nil {
		return legacyStepPrompt(step, upstream)
	}

	upstreamIDs := make([]string, 0, len(upstream))
	for _, a := range upstream {
		if a.ArtifactID != "" {
			upstreamIDs = append(upstreamIDs, a.ArtifactID)
		}
	}

	live := prompt.LoadLiveContext(
		r.opts.Context,
		r.opts.ActiveFeature,
		r.opts.ChannelKey,
		r.opts.RunStartedAt,
		upstreamIDs,
	)

	persona := ""
	if r.opts.AgentsDir != "" {
		if body, err := guests.LoadPersona(r.opts.AgentsDir, step.AgentID); err == nil {
			persona = body
		}
	}

	planCtx := r.buildPlanContext(step, upstream)

	upstreamSummaries := make([]prompt.ArtifactSummary, 0, len(upstream))
	for _, a := range upstream {
		upstreamSummaries = append(upstreamSummaries, prompt.ArtifactSummary{
			ID:      a.ArtifactID,
			Kind:    a.Kind,
			Path:    a.Path,
			Summary: a.Summary,
		})
	}

	r.mu.Lock()
	revState := r.revisionState[step.ID]
	r.mu.Unlock()

	opts := prompt.GuestPromptOpts{
		Mode:                       types.ModeExecute,
		ChannelKind:                r.opts.ChannelKey,
		IncludeCharacterCommitment: true,
		PlanContext:                planCtx,
		PriorRevisionFeedback:      revState,
		UpstreamArtifactsText:      prompt.RenderUpstreamArtifacts(upstreamSummaries),
	}

	return prompt.BuildGuestPrompt(persona, live, step.Description, opts)
}

// legacyStepPrompt preserves the pre-D10 minimal prompt shape for
// test harnesses that build a RunnerOpts without a ContextSources
// handle. New production paths always supply Context and route through
// prompt.BuildGuestPrompt.
func legacyStepPrompt(step *PlanStep, upstream []StepArtifact) string {
	body := step.Description
	if len(upstream) > 0 {
		body += "\n\n## Upstream Artifacts\n"
		for _, a := range upstream {
			body += fmt.Sprintf("- **%s** (%s): %s\n", a.Path, a.Kind, a.Summary)
		}
	}
	return body
}

// buildPlanContext assembles the surgical plan-position section
// described in plan decision D5. Prior steps render as one-liners; the
// step identified by DependsOn expands into a CompletedStepDetail so
// the agent sees exactly what its upstream produced. The next-step
// hint (if any) is derived by scanning plan.Steps for a step whose
// DependsOn points at the current step.
func (r *PlanRunner) buildPlanContext(step *PlanStep, upstream []StepArtifact) *prompt.PlanContextOpts {
	r.mu.Lock()
	plan := r.plan
	r.mu.Unlock()
	if plan == nil {
		return nil
	}

	pc := &prompt.PlanContextOpts{
		PlanTitle:       plan.Metadata.Title,
		ThisStepID:      step.ID,
		ThisStepTitle:   firstLine(step.Description),
		ThisStepAgentID: step.AgentID,
	}

	for i := range plan.Steps {
		s := plan.Steps[i]
		if s.ID == step.ID {
			continue
		}
		if s.Status != StepCompleted {
			continue
		}
		pc.PriorStepSummaries = append(pc.PriorStepSummaries, prompt.StepOneLiner{
			ID:      s.ID,
			AgentID: s.AgentID,
			Status:  string(s.Status),
			Title:   firstLine(s.Description),
		})
	}

	if step.DependsOn != "" {
		if upstreamStep := findStepInPlan(plan, step.DependsOn); upstreamStep != nil {
			detail := prompt.CompletedStepDetail{
				ID:          upstreamStep.ID,
				AgentID:     upstreamStep.AgentID,
				Description: upstreamStep.Description,
			}
			for _, a := range upstream {
				detail.Artifacts = append(detail.Artifacts, prompt.ArtifactSummary{
					ID:      a.ArtifactID,
					Kind:    a.Kind,
					Path:    a.Path,
					Summary: a.Summary,
				})
			}
			pc.UpstreamDetail = append(pc.UpstreamDetail, detail)
		}
	}

	for i := range plan.Steps {
		s := plan.Steps[i]
		if s.DependsOn == step.ID {
			pc.NextStep = &prompt.NextStepHint{
				ID:      s.ID,
				AgentID: s.AgentID,
				Title:   firstLine(s.Description),
			}
			break
		}
	}

	return pc
}

// dumpStepPrompt writes the final assembled prompt for step to a
// sibling file of the run log. Silently no-ops when RunLogPath is
// empty or any filesystem op fails -- the dump is a diagnostic aid,
// not a correctness requirement. File name is deterministic per step
// so re-runs of the same step overwrite rather than accumulate.
func (r *PlanRunner) dumpStepPrompt(stepID, promptText string) {
	if r.opts.RunLogPath == "" {
		return
	}
	dir := r.opts.RunLogPath + ".prompts"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.logger.Debug("prompt dump: mkdir failed", "step", stepID, "error", err)
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("step-%s.prompt.txt", stepID))
	if err := os.WriteFile(path, []byte(promptText), 0o644); err != nil {
		r.logger.Debug("prompt dump: write failed", "step", stepID, "path", path, "error", err)
	}
}

// trimStderr reduces a potentially-large stderr blob down to the
// tail bytes that are typically actionable (last 1 KiB). Called only
// on failure paths so hot runs pay no cost.
func trimStderr(s string) string {
	s = strings.TrimSpace(s)
	const limit = 1024
	if len(s) <= limit {
		return s
	}
	return "..." + s[len(s)-limit:]
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return s
}

func (r *PlanRunner) buildRunOpts(step *PlanStep, prompt string, threadID string) types.RunOpts {
	guestID := step.AgentID
	model := "claude-sonnet-4-5"
	var allowedTools []string
	var guestMCPServers map[string]mcpconfig.MCPServerRef
	var httpTools map[string]guests.HTTPToolConfig
	var agent guests.GuestAgent

	// Orchestrator branch: the guest scan excludes orchestrator.md from
	// GuestIndex, so a step routed to guests.OrchestratorGuestID can't be
	// resolved through the normal map lookup. Route through the injected
	// OrchestratorAgent instead, which carries the orchestrator's model,
	// max_turns, allowed tools, and MCP/HTTP tool config from
	// agents/orchestrator.md frontmatter. Persona loading below
	// (via LoadPersona keyed on guestID) already resolves
	// agents/orchestrator.md by slug, so no special case needed there.
	if guestID == guests.OrchestratorGuestID && r.opts.OrchestratorAgent != nil {
		agent = *r.opts.OrchestratorAgent
		if agent.Model != "" {
			model = agent.Model
		}
		allowedTools = agent.AllowedTools
		guestMCPServers = agent.MCPServers
		httpTools = agent.HTTPTools
	} else if r.opts.GuestIndex != nil {
		if ag, ok := r.opts.GuestIndex.Agents[guestID]; ok {
			agent = ag
			if ag.Model != "" {
				model = ag.Model
			}
			allowedTools = ag.AllowedTools
			guestMCPServers = ag.MCPServers
			httpTools = ag.HTTPTools
		}
	}

	// Load persona and prepend to prompt when buildStepPrompt did not
	// already inline it via prompt.BuildGuestPrompt (legacy test path
	// with no ContextSources handle). The prefix check is cheap and
	// keeps the harness contract backwards compatible.
	if r.opts.AgentsDir != "" && r.opts.Context == nil {
		if persona, err := guests.LoadPersona(r.opts.AgentsDir, guestID); err == nil && persona != "" {
			prompt = persona + "\n\n---\n\n" + prompt
		}
	}

	// Merge MCP servers: global + guest + HTTP tools
	merged := harness.MergeGuestServers(r.opts.GlobalMCPServers, guestMCPServers)
	if httpTools != nil {
		httpServers := harness.HTTPToolsToMCPServers(httpTools, r.opts.ProjectRoot)
		for k, v := range httpServers {
			if merged == nil {
				merged = make(map[string]mcpconfig.MCPServerRef)
			}
			merged[k] = v
		}
	}

	mcpActive := len(merged) > 0
	if mcpActive && r.opts.ProjectRoot != "" {
		if err := harness.WriteMCPConfig(r.opts.ProjectRoot, merged); err != nil {
			r.logger.Warn("failed to write MCP config for step", "step", step.ID, "error", err)
		}
	}

	onStream := func(delta string) {
		if r.opts.ChannelMgr != nil {
			_ = r.opts.ChannelMgr.Broadcast(context.Background(), channel.OutgoingMessage{
				StreamDelta: delta,
				GuestID:     guestID,
				ThreadID:    threadID,
			})
		}
	}

	maxTurns := guests.ResolveMaxTurns(agent, mcpActive, r.opts.GuestMCPMaxTurns)

	opts := types.RunOpts{
		Prompt:         prompt,
		Model:          model,
		MaxTurns:       maxTurns,
		PermissionMode: "bypassPermissions",
		OnStream:       onStream,
	}
	if len(allowedTools) > 0 {
		opts.AllowedTools = allowedTools
	}

	return opts
}

func (r *PlanRunner) setStepStatus(stepID string, status StepStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.plan.Steps {
		if r.plan.Steps[i].ID == stepID {
			r.plan.Steps[i].Status = status
			return
		}
	}
}

// agentReviewContext looks up the guest persona's identity and review spec so
// gate events can surface the right drawer title, notification text, and
// panel layout. If the guest index is missing the agent, we return just the
// agent ID so the rest of the payload still renders sensibly.
func (r *PlanRunner) agentReviewContext(agentID string) (string, string, *guests.ReviewSpec) {
	if r.opts.GuestIndex == nil {
		return agentID, "", nil
	}
	ag, ok := r.opts.GuestIndex.Agents[agentID]
	if !ok {
		return agentID, "", nil
	}
	return ag.ID, ag.Name, ag.Review
}

func (r *PlanRunner) upstreamArtifacts(dependsOn string) []StepArtifact {
	if dependsOn == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.artifacts[dependsOn]
}

func (r *PlanRunner) broadcast(ctx context.Context, msg channel.OutgoingMessage) {
	if r.opts.ChannelMgr == nil {
		return
	}
	if err := r.opts.ChannelMgr.Broadcast(ctx, msg); err != nil {
		r.logger.Warn("broadcast failed", "event", msg.EventType, "error", err)
	}
}
