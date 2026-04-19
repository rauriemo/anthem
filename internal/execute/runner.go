package execute

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/guests"
	"github.com/rauriemo/anthem/internal/harness"
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
}

func NewPlanRunner(opts RunnerOpts) *PlanRunner {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &PlanRunner{
		opts:                      opts,
		logger:                    logger,
		gateCh:                    make(chan gateMsg, 1),
		artifacts:                 make(map[string][]StepArtifact),
		preservedArtifacts:        make(map[string][]StepArtifact),
		consecutivePartialRevises: make(map[string]int),
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
	r.mu.Lock()
	r.plan = plan
	r.mu.Unlock()

	r.broadcast(ctx, PlanLoadedEvent(plan, threadID))

	for {
		r.mu.Lock()
		step := r.plan.NextPendingStep()
		r.mu.Unlock()

		if step == nil {
			r.mu.Lock()
			done := r.plan.AllCompleted()
			r.mu.Unlock()
			if done {
				r.broadcast(ctx, PlanCompletedEvent(plan, threadID))
				return nil
			}
			// No step is ready but plan isn't done -- blocked by deps or all failed
			return fmt.Errorf("plan stuck: no pending step is ready")
		}

		if err := r.runStep(ctx, step, threadID); err != nil {
			return err
		}
	}
}

func (r *PlanRunner) runStep(ctx context.Context, step *PlanStep, threadID string) error {
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

	prompt := r.buildStepPrompt(step, upstream)
	runOpts := r.buildRunOpts(step, prompt, threadID)

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

	r.setStepStatus(step.ID, StepFailed)
	r.broadcast(ctx, StepFailedEvent(step.ID, step.AgentID, errMsg, threadID))

	// Pause and wait for human resolution
	select {
	case <-ctx.Done():
		return ctx.Err()
	case msg := <-r.gateCh:
		switch msg.Resolution.Action {
		case GateApprove:
			// "approve" on a failure means "retry as-is"
			r.setStepStatus(step.ID, StepPending)
			return nil
		case GateRevise:
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
			r.deactivateStepAgent(ctx, step, fmt.Sprintf("step %s aborted", step.ID), threadID)
			r.broadcast(ctx, PlanAbortedEvent(r.plan, "step failure aborted by user", threadID))
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
			r.applyRevise(gate.AfterStep, msg.Resolution, artifacts, review)
			return nil
		case GateAbort:
			r.clearPartialReviseState(gate.AfterStep)
			r.deactivateStepAgent(ctx, step, fmt.Sprintf("step %s aborted", step.ID), threadID)
			r.broadcast(ctx, PlanAbortedEvent(r.plan, "aborted at gate", threadID))
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

func (r *PlanRunner) buildStepPrompt(step *PlanStep, upstream []StepArtifact) string {
	prompt := step.Description

	if len(upstream) > 0 {
		prompt += "\n\n## Upstream Artifacts\n"
		for _, a := range upstream {
			prompt += fmt.Sprintf("- **%s** (%s): %s\n", a.Path, a.Kind, a.Summary)
		}
	}

	return prompt
}

func (r *PlanRunner) buildRunOpts(step *PlanStep, prompt string, threadID string) types.RunOpts {
	guestID := step.AgentID
	model := "claude-sonnet-4-5"
	var allowedTools []string
	var guestMCPServers map[string]mcpconfig.MCPServerRef
	var httpTools map[string]guests.HTTPToolConfig
	var agent guests.GuestAgent

	if r.opts.GuestIndex != nil {
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

	// Load persona and prepend to prompt
	if r.opts.AgentsDir != "" {
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
