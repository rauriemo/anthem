package execute

import (
	"context"
	"fmt"
	"log/slog"
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
	GuestIndex       *guests.GuestIndex
	Runner           agent.AgentRunner
	ChannelMgr       Broadcaster
	Artifacts        ArtifactProvider
	ProjectRoot      string
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
}

func NewPlanRunner(opts RunnerOpts) *PlanRunner {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &PlanRunner{
		opts:      opts,
		logger:    logger,
		gateCh:    make(chan gateMsg, 1),
		artifacts: make(map[string][]StepArtifact),
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
			r.broadcast(ctx, PlanAbortedEvent(r.plan, "step failure aborted by user", threadID))
			return fmt.Errorf("plan aborted at step %q", step.ID)
		}
	}

	return nil
}

func (r *PlanRunner) handleGate(ctx context.Context, gate *ApprovalGate, step *PlanStep, artifacts []StepArtifact, threadID string) error {
	agentID, agentName, review := r.agentReviewContext(step.AgentID)
	r.broadcast(ctx, GateOpenedEvent(
		*gate,
		artifacts,
		step.ID,
		agentID,
		agentName,
		review,
		threadID,
		threadID,
	))

	select {
	case <-ctx.Done():
		return ctx.Err()
	case msg := <-r.gateCh:
		r.broadcast(ctx, GateResolvedEvent(gate.ID, msg.Resolution.Action, msg.Resolution.Feedback, threadID))

		switch msg.Resolution.Action {
		case GateApprove:
			return nil
		case GateRevise:
			// Reset the gate's after-step to pending with revision instructions
			r.mu.Lock()
			for i := range r.plan.Steps {
				if r.plan.Steps[i].ID == gate.AfterStep {
					r.plan.Steps[i].Description += "\n\nRevision: " + msg.Resolution.Feedback
					r.plan.Steps[i].Status = StepPending
					break
				}
			}
			r.mu.Unlock()
			return nil
		case GateAbort:
			r.broadcast(ctx, PlanAbortedEvent(r.plan, "aborted at gate", threadID))
			return fmt.Errorf("plan aborted at gate %q", gate.ID)
		}
	}

	return nil
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

	if r.opts.GuestIndex != nil {
		if ag, ok := r.opts.GuestIndex.Agents[guestID]; ok {
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

	maxTurns := 1
	if mcpActive {
		maxTurns = r.opts.GuestMCPMaxTurns
		if maxTurns <= 0 {
			maxTurns = 16
		}
	}

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
