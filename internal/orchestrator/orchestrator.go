package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/cost"
	"github.com/rauriemo/anthem/internal/rules"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/workspace"
)

type Orchestrator struct {
	cfg             *config.Config
	body            string
	tracker         tracker.IssueTracker
	runner          agent.AgentRunner
	ws              workspace.WorkspaceManager
	events          EventBus
	rules           *rules.Engine
	costTracker     *cost.Tracker
	logger          *slog.Logger
	voiceContent    string
	userConstraints []string
	statePath       string

	wg         sync.WaitGroup
	mu         sync.Mutex
	active     map[string]*ActiveRun
	retryState map[string]*RetryInfo
}

type ActiveRun struct {
	TaskID    string
	Labels    []string
	StartedAt time.Time
}

type Opts struct {
	Config          *config.Config
	TemplateBody    string
	Tracker         tracker.IssueTracker
	Runner          agent.AgentRunner
	Workspace       workspace.WorkspaceManager
	EventBus        EventBus
	CostTracker     *cost.Tracker
	Logger          *slog.Logger
	VoiceContent    string
	UserConstraints []string
	StatePath       string
}

func New(opts Opts) *Orchestrator {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ct := opts.CostTracker
	if ct == nil {
		ct = cost.NewTracker()
	}
	o := &Orchestrator{
		cfg:             opts.Config,
		body:            opts.TemplateBody,
		tracker:         opts.Tracker,
		runner:          opts.Runner,
		ws:              opts.Workspace,
		events:          opts.EventBus,
		rules:           rules.NewEngine(opts.Config.Rules, logger),
		costTracker:     ct,
		logger:          logger,
		voiceContent:    opts.VoiceContent,
		userConstraints: opts.UserConstraints,
		statePath:       opts.StatePath,
		active:          make(map[string]*ActiveRun),
		retryState:      make(map[string]*RetryInfo),
	}

	return o
}

const shutdownDrainTimeout = 10 * time.Second
const shutdownCleanupTimeout = 5 * time.Second

// Run starts the orchestrator polling loop. Blocks until ctx is canceled,
// then performs graceful shutdown (drain active dispatches, release claims).
func (o *Orchestrator) Run(ctx context.Context) error {
	// Load and reconcile persisted state before first tick
	if o.statePath != "" {
		if err := o.LoadAndReconcile(ctx); err != nil {
			o.logger.Warn("failed to load persisted state, starting fresh", "error", err)
		}
	}

	o.logger.Info("orchestrator started",
		"interval_ms", o.cfg.Polling.IntervalMS,
		"max_concurrent", o.cfg.Agent.MaxConcurrent,
	)

	o.publish(types.Event{Type: "orchestrator.started"})

	interval := time.Duration(o.cfg.Polling.IntervalMS) * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run first tick immediately
	o.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			o.logger.Info("orchestrator stopping, starting graceful shutdown")
			o.Shutdown()
			o.publish(types.Event{Type: "orchestrator.stopped"})
			return ctx.Err()
		case <-ticker.C:
			o.tick(ctx)
		}
	}
}

// Shutdown drains active dispatches, releases all claims, and saves state.
// Safe to call after the polling context has been canceled.
func (o *Orchestrator) Shutdown() {
	// Wait for active dispatch goroutines to finish
	done := make(chan struct{})
	go func() {
		o.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		o.logger.Info("all dispatches drained")
	case <-time.After(shutdownDrainTimeout):
		o.logger.Warn("shutdown timeout, force-killing active agents")
	}

	// Release all active claims and remove in-progress labels
	o.releaseClaims()

	// Save state for next startup
	o.saveState()
}

// releaseClaims removes in-progress labels for all active runs and clears the
// active map. Uses a fresh context since the original is already canceled.
func (o *Orchestrator) releaseClaims() {
	o.mu.Lock()
	ids := make([]string, 0, len(o.active))
	for id := range o.active {
		ids = append(ids, id)
	}
	o.mu.Unlock()

	if len(ids) == 0 {
		return
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), shutdownCleanupTimeout)
	defer cancel()

	for _, id := range ids {
		if err := o.tracker.RemoveLabel(cleanupCtx, id, "in-progress"); err != nil {
			o.logger.Warn("shutdown: failed to remove in-progress label",
				"task_id", id,
				"error", err,
			)
		} else {
			o.logger.Info("shutdown: released claim", "task_id", id)
		}
		o.release(id)
	}
}

// LoadAndReconcile loads persisted state from disk, restores retry queue and
// cost sessions, and reconciles retry state against the tracker — dropping
// entries for tasks that have reached a terminal state while Anthem was down.
func (o *Orchestrator) LoadAndReconcile(ctx context.Context) error {
	state, err := LoadState(o.statePath)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	if len(state.RetryState) == 0 && len(state.CostSessions) == 0 {
		o.logger.Debug("no persisted state to restore", "path", o.statePath)
		return nil
	}

	// Restore cost sessions (always safe — no reconciliation needed)
	if len(state.CostSessions) > 0 {
		o.costTracker.LoadSessions(state.CostSessions)
	}

	// Restore retry state, but check each task against the tracker first
	restored := 0
	skipped := 0
	for id, rs := range state.RetryState {
		task, err := o.tracker.GetTask(ctx, id)
		if err != nil {
			o.logger.Warn("reconcile: failed to check task, restoring retry state anyway",
				"task_id", id, "error", err)
		} else if task == nil || task.Status.IsTerminal() {
			o.logger.Info("reconcile: skipping retry for terminal/missing task",
				"task_id", id)
			skipped++
			continue
		}

		o.mu.Lock()
		o.retryState[id] = &RetryInfo{
			TaskID:      rs.TaskID,
			Attempts:    rs.Attempts,
			NextRetryAt: rs.NextRetryAt,
			LastError:   rs.LastError,
		}
		o.mu.Unlock()
		restored++
	}

	o.logger.Info("restored persisted state",
		"retry_restored", restored,
		"retry_skipped_terminal", skipped,
		"cost_sessions", len(state.CostSessions),
		"saved_at", state.SavedAt,
	)

	return nil
}

func (o *Orchestrator) saveState() {
	if o.statePath == "" {
		return
	}

	o.mu.Lock()
	retrySnapshot := make(map[string]*RetryInfo, len(o.retryState))
	for id, ri := range o.retryState {
		dup := *ri
		retrySnapshot[id] = &dup
	}
	o.mu.Unlock()

	if err := SaveState(o.statePath, retrySnapshot, o.costTracker); err != nil {
		o.logger.Error("failed to save state", "path", o.statePath, "error", err)
	} else {
		o.logger.Info("state saved", "path", o.statePath)
	}
}

type cfgSnapshot struct {
	cfg  *config.Config
	body string
}

// configSnapshot returns a consistent snapshot of the current config and body.
func (o *Orchestrator) configSnapshot() cfgSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	return cfgSnapshot{cfg: o.cfg, body: o.body}
}

// ReloadConfig safely swaps the orchestrator's config and template body.
// Goroutine-safe — called from the watcher goroutine while the orchestrator runs.
func (o *Orchestrator) ReloadConfig(cfg *config.Config, body string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cfg = cfg
	o.body = body
	o.rules = rules.NewEngine(cfg.Rules, o.logger)
}

func (o *Orchestrator) tick(ctx context.Context) {
	o.logger.Debug("tick start")

	if throttler, ok := o.tracker.(interface{ ShouldThrottle() (bool, time.Duration) }); ok {
		if throttled, remaining := throttler.ShouldThrottle(); throttled {
			o.logger.Warn("skipping tick: rate limit throttled", "remaining", remaining)
			return
		}
	}

	// 1. Reconcile active runs
	o.reconcile(ctx)

	// 2. Fetch active tasks
	tasks, err := o.tracker.ListActive(ctx)
	if err != nil {
		o.logger.Error("failed to fetch tasks", "error", err)
		return
	}

	// 3. Sort by priority (ascending), then created_at, then ID
	sortTasks(tasks)

	// 4. Check eligibility and dispatch
	for _, task := range tasks {
		if ctx.Err() != nil {
			return
		}

		o.mu.Lock()
		_, running := o.active[task.ID]
		slots := o.availableSlots(task)
		o.mu.Unlock()

		if running || slots <= 0 {
			continue
		}

		// 4b. Check retry eligibility
		if !o.isRetryEligible(task.ID) {
			o.logger.Debug("task in backoff, skipping",
				"task_id", task.ID,
			)
			continue
		}

		// 5. Evaluate rules (snapshot to avoid holding lock)
		o.mu.Lock()
		rulesEngine := o.rules
		o.mu.Unlock()

		results := rulesEngine.Evaluate(task)
		skip := false
		for _, r := range results {
			switch r.Action {
			case rules.ActionRequireApproval:
				if rules.NeedsApproval(task, r.ApprovalLabel) {
					o.logger.Info("task waiting for approval",
						"task_id", task.ID,
						"identifier", task.Identifier,
						"approval_label", r.ApprovalLabel,
					)
					_ = o.tracker.AddLabel(ctx, task.ID, "waiting-for-approval")
					o.publish(types.Event{
						Type:   "task.waiting_approval",
						TaskID: task.ID,
					})
					skip = true
				}
			case rules.ActionAutoAssign:
				if r.AutoAssignee != "" {
					_ = o.tracker.AddComment(ctx, task.ID, fmt.Sprintf("Auto-assigned to @%s", r.AutoAssignee))
					o.logger.Info("auto-assigned task",
						"task_id", task.ID,
						"assignee", r.AutoAssignee,
					)
				}
			case rules.ActionMaxCost:
				if r.MaxCost > 0 {
					currentCost := o.costTracker.TaskCost(task.ID)
					if currentCost >= r.MaxCost {
						o.logger.Warn("task exceeded budget, skipping",
							"task_id", task.ID,
							"max_cost", r.MaxCost,
							"current_cost", currentCost,
						)
						_ = o.tracker.AddLabel(ctx, task.ID, "exceeded-budget")
						o.publish(types.Event{
							Type:   "task.budget_exceeded",
							TaskID: task.ID,
							Data:   map[string]float64{"max_cost": r.MaxCost, "current_cost": currentCost},
						})
						skip = true
					}
				}
			}
			if skip {
				break
			}
		}
		if skip {
			continue
		}

		// 6. Claim and dispatch (capture config snapshot for the goroutine)
		o.claim(task)
		snap := o.configSnapshot()
		o.wg.Add(1)
		go o.dispatch(ctx, task, snap)
	}

	o.logger.Debug("tick complete", "active_count", o.activeCount())
}

func (o *Orchestrator) dispatch(ctx context.Context, task types.Task, snap cfgSnapshot) {
	defer o.wg.Done()
	cfg := snap.cfg

	o.logger.Info("dispatching task",
		"task_id", task.ID,
		"identifier", task.Identifier,
		"title", task.Title,
	)
	o.publish(types.Event{Type: "task.claimed", TaskID: task.ID})

	// Mark as in-progress on the tracker
	if err := o.tracker.AddLabel(ctx, task.ID, "in-progress"); err != nil {
		o.logger.Warn("failed to add in-progress label", "task_id", task.ID, "error", err)
	}
	for _, label := range cfg.Tracker.Labels.Active {
		if label != "in-progress" {
			if err := o.tracker.RemoveLabel(ctx, task.ID, label); err != nil {
				o.logger.Warn("failed to remove active label", "task_id", task.ID, "label", label, "error", err)
			}
		}
	}

	// Prepare workspace
	wsPath, err := o.ws.Prepare(ctx, task)
	if err != nil {
		o.logger.Error("workspace prepare failed",
			"task_id", task.ID,
			"error", err,
		)
		o.release(task.ID)
		o.publish(types.Event{Type: "task.failed", TaskID: task.ID, Data: err.Error()})
		return
	}

	// Run before_run hook
	if cfg.Hooks.BeforeRun != "" {
		if err := o.ws.RunHook(ctx, "before_run", wsPath); err != nil {
			o.logger.Error("before_run hook failed",
				"task_id", task.ID,
				"error", err,
			)
			o.release(task.ID)
			o.publish(types.Event{Type: "task.failed", TaskID: task.ID, Data: err.Error()})
			return
		}
	}

	// Render prompt
	prompt, err := config.RenderBody(snap.body, map[string]any{
		"issue": map[string]any{
			"title":      task.Title,
			"body":       task.Body,
			"identifier": task.Identifier,
			"repo_url":   task.RepoURL,
			"labels":     task.Labels,
		},
	})
	if err != nil {
		o.logger.Error("template render failed",
			"task_id", task.ID,
			"error", err,
		)
		o.release(task.ID)
		return
	}

	// Build full prompt: voice + constraints + task template
	fullPrompt := buildFullPrompt(o.voiceContent, o.userConstraints, cfg.System.Constraints, prompt)

	// Continuation delay for retried tasks
	if ri := o.retryInfo(task.ID); ri != nil {
		select {
		case <-ctx.Done():
			o.release(task.ID)
			return
		case <-time.After(1 * time.Second):
		}
	}

	// Run agent
	o.publish(types.Event{Type: "agent.started", TaskID: task.ID})

	result, err := o.runner.Run(ctx, types.RunOpts{
		WorkspacePath:  wsPath,
		Prompt:         fullPrompt,
		MaxTurns:       cfg.Agent.MaxTurns,
		AllowedTools:   cfg.Agent.AllowedTools,
		Model:          cfg.Agent.Model,
		StallTimeoutMS: cfg.Agent.StallTimeoutMS,
	})

	o.release(task.ID)

	// Record cost regardless of success/failure
	if result != nil {
		o.costTracker.Record(cost.SessionCost{
			TaskID:    task.ID,
			SessionID: result.SessionID,
			TokensIn:  result.TokensIn,
			TokensOut: result.TokensOut,
			CostUSD:   result.CostUSD,
			TurnsUsed: result.TurnsUsed,
		})
	}

	if err != nil {
		o.recordFailure(task.ID, err)
		ri := o.retryInfo(task.ID)
		o.logger.Error("agent run failed",
			"task_id", task.ID,
			"error", err,
			"attempt", ri.Attempts,
			"next_retry_at", ri.NextRetryAt,
		)
		_ = o.tracker.AddComment(ctx, task.ID,
			fmt.Sprintf("Anthem agent failed: %s. Retry attempt %d, next retry at %s.",
				err, ri.Attempts, ri.NextRetryAt.Format(time.RFC3339)))
		o.publish(types.Event{Type: "task.failed", TaskID: task.ID, Data: err.Error()})
		return
	}

	o.clearRetry(task.ID)

	o.logger.Info("task completed",
		"task_id", task.ID,
		"identifier", task.Identifier,
		"exit_code", result.ExitCode,
		"tokens_in", result.TokensIn,
		"tokens_out", result.TokensOut,
		"cost_usd", result.CostUSD,
		"duration", result.Duration,
	)

	// Mark task as done on the tracker
	for _, label := range cfg.Tracker.Labels.Terminal {
		if err := o.tracker.AddLabel(ctx, task.ID, label); err != nil {
			o.logger.Warn("failed to add terminal label", "task_id", task.ID, "label", label, "error", err)
		}
	}
	_ = o.tracker.RemoveLabel(ctx, task.ID, "in-progress")
	_ = o.tracker.UpdateStatus(ctx, task.ID, string(types.StatusCompleted))

	o.publish(types.Event{
		Type:   "task.completed",
		TaskID: task.ID,
		Data:   result,
	})
}

func (o *Orchestrator) reconcile(ctx context.Context) {
	snap := o.configSnapshot()
	stallLimit := 2 * time.Duration(snap.cfg.Agent.StallTimeoutMS) * time.Millisecond

	o.mu.Lock()
	type runSnapshot struct {
		id        string
		startedAt time.Time
	}
	runs := make([]runSnapshot, 0, len(o.active))
	for id, run := range o.active {
		runs = append(runs, runSnapshot{id: id, startedAt: run.StartedAt})
	}
	o.mu.Unlock()

	for _, run := range runs {
		// Stall detection: release claims for runs far beyond timeout
		if stallLimit > 0 && time.Since(run.startedAt) > stallLimit {
			o.logger.Warn("reconcile: task appears stalled beyond timeout, releasing claim",
				"task_id", run.id,
				"running_for", time.Since(run.startedAt),
				"stall_limit", stallLimit,
			)
			o.release(run.id)
			o.publish(types.Event{Type: "task.stalled", TaskID: run.id})
			continue
		}

		task, err := o.tracker.GetTask(ctx, run.id)
		if err != nil {
			o.logger.Warn("reconcile: failed to get task", "task_id", run.id, "error", err)
			continue
		}
		if task == nil || task.Status.IsTerminal() {
			o.logger.Info("reconcile: releasing stale claim", "task_id", run.id)
			o.release(run.id)
		}
	}
}

func (o *Orchestrator) claim(task types.Task) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.active[task.ID] = &ActiveRun{
		TaskID:    task.ID,
		Labels:    task.Labels,
		StartedAt: time.Now(),
	}
}

func (o *Orchestrator) release(taskID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.active, taskID)
}

func (o *Orchestrator) activeCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.active)
}

// availableSlots returns >0 if the task can be dispatched given concurrency limits.
// Must be called with o.mu held.
func (o *Orchestrator) availableSlots(task types.Task) int {
	// Global limit
	if len(o.active) >= o.cfg.Agent.MaxConcurrent {
		return 0
	}

	// Per-label limits
	for label, maxForLabel := range o.cfg.Agent.MaxConcurrentPerLabel {
		if !hasLabel(task.Labels, label) {
			continue
		}
		count := 0
		for _, run := range o.active {
			if hasLabel(run.Labels, label) {
				count++
			}
		}
		if count >= maxForLabel {
			return 0
		}
	}

	return 1
}

func (o *Orchestrator) publish(event types.Event) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	o.events.Publish(event)
}

func sortTasks(tasks []types.Task) {
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Priority != tasks[j].Priority {
			return tasks[i].Priority < tasks[j].Priority
		}
		if !tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
			return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
		}
		return tasks[i].ID < tasks[j].ID
	})
}

func hasLabel(labels []string, label string) bool {
	for _, l := range labels {
		if l == label {
			return true
		}
	}
	return false
}

const metaConstraint = "Do not modify constraint definitions in WORKFLOW.md system.constraints or ~/.anthem/constraints.yaml"

func buildConstraints(userConstraints []string, projectConstraints []string) string {
	if len(userConstraints) == 0 && len(projectConstraints) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, "## Constraints (non-negotiable)")
	for _, c := range userConstraints {
		lines = append(lines, "- "+c)
	}
	for _, c := range projectConstraints {
		lines = append(lines, "- "+c)
	}
	lines = append(lines, "- "+metaConstraint)
	return strings.Join(lines, "\n")
}

func buildFullPrompt(voiceContent string, userConstraints []string, projectConstraints []string, taskPrompt string) string {
	var sections []string
	if voiceContent != "" {
		sections = append(sections, voiceContent)
	}
	if constraintBlock := buildConstraints(userConstraints, projectConstraints); constraintBlock != "" {
		sections = append(sections, constraintBlock)
	}
	sections = append(sections, taskPrompt)
	return strings.Join(sections, "\n\n")
}
