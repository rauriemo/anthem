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
	"github.com/rauriemo/anthem/internal/rules"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/workspace"
)

type Orchestrator struct {
	cfg              *config.Config
	body             string
	tracker          tracker.IssueTracker
	runner           agent.AgentRunner
	ws               workspace.WorkspaceManager
	events           EventBus
	rules            *rules.Engine
	logger           *slog.Logger
	voiceContent     string
	userConstraints  []string

	mu       sync.Mutex
	active   map[string]*ActiveRun // task ID -> active run
	stopping bool
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
	Logger          *slog.Logger
	VoiceContent    string
	UserConstraints []string
}

func New(opts Opts) *Orchestrator {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Orchestrator{
		cfg:             opts.Config,
		body:            opts.TemplateBody,
		tracker:         opts.Tracker,
		runner:          opts.Runner,
		ws:              opts.Workspace,
		events:          opts.EventBus,
		rules:           rules.NewEngine(opts.Config.Rules),
		logger:          logger,
		voiceContent:    opts.VoiceContent,
		userConstraints: opts.UserConstraints,
		active:          make(map[string]*ActiveRun),
	}
}

// Run starts the orchestrator polling loop. Blocks until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context) error {
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
			o.logger.Info("orchestrator stopping")
			o.publish(types.Event{Type: "orchestrator.stopped"})
			return ctx.Err()
		case <-ticker.C:
			o.tick(ctx)
		}
	}
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

		// 5. Evaluate rules
		results := o.rules.Evaluate(task)
		skip := false
		for _, r := range results {
			if r.Action == rules.ActionRequireApproval {
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
					break
				}
			}
		}
		if skip {
			continue
		}

		// 6. Claim and dispatch
		o.claim(task)
		go o.dispatch(ctx, task)
	}

	o.logger.Debug("tick complete", "active_count", o.activeCount())
}

func (o *Orchestrator) dispatch(ctx context.Context, task types.Task) {
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
	for _, label := range o.cfg.Tracker.Labels.Active {
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
	if o.cfg.Hooks.BeforeRun != "" {
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
	prompt, err := config.RenderBody(o.body, map[string]any{
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
	fullPrompt := buildFullPrompt(o.voiceContent, o.userConstraints, o.cfg.System.Constraints, prompt)

	// Run agent
	o.publish(types.Event{Type: "agent.started", TaskID: task.ID})

	result, err := o.runner.Run(ctx, types.RunOpts{
		WorkspacePath:  wsPath,
		Prompt:         fullPrompt,
		MaxTurns:       o.cfg.Agent.MaxTurns,
		AllowedTools:   o.cfg.Agent.AllowedTools,
		Model:          o.cfg.Agent.Model,
		StallTimeoutMS: o.cfg.Agent.StallTimeoutMS,
	})

	o.release(task.ID)

	if err != nil {
		o.logger.Error("agent run failed",
			"task_id", task.ID,
			"error", err,
		)
		_ = o.tracker.AddComment(ctx, task.ID, fmt.Sprintf("Anthem agent failed: %s", err))
		o.publish(types.Event{Type: "task.failed", TaskID: task.ID, Data: err.Error()})
		return
	}

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
	for _, label := range o.cfg.Tracker.Labels.Terminal {
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
	// Check that active runs are still for valid active tasks
	o.mu.Lock()
	ids := make([]string, 0, len(o.active))
	for id := range o.active {
		ids = append(ids, id)
	}
	o.mu.Unlock()

	for _, id := range ids {
		task, err := o.tracker.GetTask(ctx, id)
		if err != nil {
			o.logger.Warn("reconcile: failed to get task", "task_id", id, "error", err)
			continue
		}
		if task == nil || task.Status.IsTerminal() {
			o.logger.Info("reconcile: releasing stale claim", "task_id", id)
			o.release(id)
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
