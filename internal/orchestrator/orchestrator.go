package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/audit"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/cost"
	"github.com/rauriemo/anthem/internal/rules"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/voice"
	"github.com/rauriemo/anthem/internal/workspace"
)

type Wave struct {
	ID              string
	FrontierTaskIDs []string
	Status          string // "active" or "exhausted"
	CreatedAt       time.Time
}

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
	orchAgent       *OrchestratorAgent
	auditLogger     audit.AuditLogger
	channelMgr      *channel.Manager
	currentWave     *Wave
	lastSnapHash    string
	homeDir         string

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
	OrchAgent       *OrchestratorAgent
	AuditLogger     audit.AuditLogger
	ChannelManager  *channel.Manager
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
		orchAgent:       opts.OrchAgent,
		auditLogger:     opts.AuditLogger,
		channelMgr:      opts.ChannelManager,
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

	if o.auditLogger != nil {
		if err := o.auditLogger.Close(); err != nil {
			o.logger.Warn("failed to close audit logger", "error", err)
		}
	}
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

	// 4. Build snapshot and check for changes
	snapshot := o.buildStateSnapshot(tasks)
	hash := snapshotHash(snapshot)
	if hash == o.lastSnapHash {
		o.logger.Debug("snapshot unchanged, skipping consult")
		return
	}

	// 5. Orchestrator-driven dispatch or fallback
	if o.orchAgent != nil {
		actions, err := o.orchAgent.ConsultWithRepair(ctx, snapshot)
		if err != nil {
			o.logger.Warn("orchestrator consult failed, falling back to mechanical dispatch", "error", err)
			o.mechanicalDispatch(ctx, tasks)
		} else if actions == nil {
			o.logger.Debug("orchestrator returned nil actions, falling back to mechanical dispatch")
			o.mechanicalDispatch(ctx, tasks)
		} else {
			o.executeActions(ctx, tasks, actions)
		}
	} else {
		o.mechanicalDispatch(ctx, tasks)
	}

	o.lastSnapHash = hash

	// Check wave exhaustion
	if o.isWaveExhausted() {
		o.logger.Info("wave exhausted", "wave_id", o.currentWave.ID)
		o.currentWave.Status = "exhausted"
	}

	o.logger.Debug("tick complete", "active_count", o.activeCount())
}

func (o *Orchestrator) buildStateSnapshot(tasks []types.Task) StateSnapshot {
	snap := StateSnapshot{
		Budget: BudgetSummary{
			TotalSpentUSD: o.costTracker.TotalCost(),
		},
	}

	for _, t := range tasks {
		snap.Tasks = append(snap.Tasks, TaskSummary{
			ID:       t.ID,
			Title:    t.Title,
			Status:   string(t.Status),
			Labels:   t.Labels,
			CostUSD:  o.costTracker.TaskCost(t.ID),
			Priority: t.Priority,
		})
	}

	o.mu.Lock()
	for id, ri := range o.retryState {
		snap.RetryQueue = append(snap.RetryQueue, RetrySummary{
			ID:        id,
			Attempts:  ri.Attempts,
			NextRetry: ri.NextRetryAt.Format(time.RFC3339),
			LastError: ri.LastError,
		})
	}
	o.mu.Unlock()

	if o.currentWave != nil {
		snap.Wave = &WaveSummary{
			ID:              o.currentWave.ID,
			FrontierTaskIDs: o.currentWave.FrontierTaskIDs,
			Status:          o.currentWave.Status,
		}
	}

	return snap
}

func snapshotHash(snap StateSnapshot) string {
	var b strings.Builder
	for _, t := range snap.Tasks {
		b.WriteString(t.ID)
		b.WriteByte(':')
		b.WriteString(t.Status)
		b.WriteByte(',')
	}
	if snap.Wave != nil {
		b.WriteString("wave:")
		b.WriteString(snap.Wave.Status)
	}
	h := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%x", h[:8])
}

func (o *Orchestrator) isWaveExhausted() bool {
	if o.currentWave == nil || o.currentWave.Status == "exhausted" {
		return false
	}
	for _, id := range o.currentWave.FrontierTaskIDs {
		task, err := o.tracker.GetTask(context.Background(), id)
		if err != nil || task == nil {
			continue
		}
		if !task.Status.IsTerminal() && task.Status != types.StatusBlocked && task.Status != types.StatusNeedsApproval {
			return false
		}
	}
	return true
}

func (o *Orchestrator) executeActions(ctx context.Context, tasks []types.Task, actions []Action) {
	taskMap := make(map[string]types.Task, len(tasks))
	validIDs := make([]string, 0, len(tasks))
	for _, t := range tasks {
		taskMap[t.ID] = t
		validIDs = append(validIDs, t.ID)
	}

	var dispatchedIDs []string

	for _, action := range actions {
		if err := ValidateAction(action, validIDs); err != nil {
			o.logger.Warn("invalid orchestrator action, skipping", "action", action.Type, "error", err)
			continue
		}

		if SchemaOnly(action.Type) {
			o.logger.Info("schema-only action, not implemented yet", "action", action.Type)
			o.recordAudit(ctx, "action.not_implemented", action.TaskID, strPtr(string(action.Type)))
			continue
		}

		switch action.Type {
		case ActionDispatch:
			task, ok := taskMap[action.TaskID]
			if !ok {
				continue
			}
			o.mu.Lock()
			_, running := o.active[task.ID]
			slots := o.availableSlots(task)
			o.mu.Unlock()
			if running || slots <= 0 {
				continue
			}
			o.claim(task)
			snap := o.configSnapshot()
			o.wg.Add(1)
			go o.dispatch(ctx, task, snap)
			dispatchedIDs = append(dispatchedIDs, task.ID)
			o.recordAudit(ctx, "task.dispatched", action.TaskID, strPtr("dispatch"))

		case ActionSkip:
			_ = o.tracker.UpdateStatus(ctx, action.TaskID, string(types.StatusSkipped))
			o.recordAudit(ctx, "task.skipped", action.TaskID, strPtr("skip"))

		case ActionComment:
			_ = o.tracker.AddComment(ctx, action.TaskID, action.Body)
			o.recordAudit(ctx, "task.commented", action.TaskID, strPtr("comment"))

		case ActionRequestApproval:
			_ = o.tracker.AddLabel(ctx, action.TaskID, "needs-approval")
			o.recordAudit(ctx, "task.approval_requested", action.TaskID, strPtr("request_approval"))

		case ActionCloseWave:
			if o.currentWave != nil {
				o.currentWave.Status = "exhausted"
			}
			o.recordAudit(ctx, "wave.closed", "", strPtr("close_wave"))

		case ActionUpdateVoice:
			if err := o.executeUpdateVoice(ctx, action); err != nil {
				o.logger.Warn("update_voice failed", "section", action.SectionName, "error", err)
			}

		case ActionCreateSubtasks:
			for _, sub := range action.Subtasks {
				createdID, err := o.tracker.CreateIssue(ctx, sub.Title, sub.Body, sub.Labels)
				if err != nil {
					o.logger.Warn("failed to create subtask", "title", sub.Title, "error", err)
					continue
				}
				o.logger.Info("created subtask", "id", createdID, "title", sub.Title)
			}
			o.recordAudit(ctx, "subtasks.created", "", strPtr("create_subtasks"))

		case ActionReply:
			if o.channelMgr != nil {
				replyMsg := channel.OutgoingMessage{Text: action.Body, Markdown: true}
				if err := o.channelMgr.Broadcast(ctx, replyMsg); err != nil {
					o.logger.Warn("failed to send channel reply", "error", err)
				}
			}
			o.recordAudit(ctx, "channel.reply_sent", "", strPtr("reply"))

		case ActionRequestMaintenance:
			if o.channelMgr != nil {
				notify := channel.OutgoingMessage{
					Text:     fmt.Sprintf("**Maintenance proposal** (%s): %s", action.MaintenanceType, action.Reason),
					Markdown: true,
				}
				if err := o.channelMgr.Broadcast(ctx, notify); err != nil {
					o.logger.Warn("failed to send maintenance notification", "error", err)
				}
			}
			if action.AutoApprovable && o.isMaintenanceAutoApproved(action.MaintenanceType) {
				o.logger.Info("auto-approved maintenance action", "type", action.MaintenanceType)
				o.publish(types.Event{
					Type: "maintenance.approved",
					Data: map[string]string{"type": action.MaintenanceType, "reason": action.Reason},
				})
				o.recordAudit(ctx, "maintenance.auto_approved", "", strPtr("request_maintenance"))
			} else {
				o.logger.Info("maintenance action awaiting user approval", "type", action.MaintenanceType)
				o.recordAudit(ctx, "maintenance.pending_approval", "", strPtr("request_maintenance"))
			}
		}
	}

	// Update or create wave with dispatched frontier
	if len(dispatchedIDs) > 0 {
		if o.currentWave == nil || o.currentWave.Status == "exhausted" {
			o.currentWave = &Wave{
				ID:              fmt.Sprintf("wave-%d", time.Now().UnixMilli()),
				FrontierTaskIDs: dispatchedIDs,
				Status:          "active",
				CreatedAt:       time.Now(),
			}
		} else {
			o.currentWave.FrontierTaskIDs = append(o.currentWave.FrontierTaskIDs, dispatchedIDs...)
		}
	}
}

func (o *Orchestrator) recordAudit(ctx context.Context, eventType string, taskID string, actionName *string) {
	if o.auditLogger == nil {
		return
	}
	ev := audit.AuditEvent{
		Timestamp:  time.Now(),
		EventType:  eventType,
		ActionName: actionName,
	}
	if taskID != "" {
		ev.TaskID = &taskID
	}
	if o.currentWave != nil {
		ev.WaveID = &o.currentWave.ID
	}
	if err := o.auditLogger.Record(ctx, ev); err != nil {
		o.logger.Warn("failed to record audit event", "event_type", eventType, "error", err)
	}
}

func strPtr(s string) *string { return &s }

func (o *Orchestrator) executeUpdateVoice(ctx context.Context, action Action) error {
	home := o.homeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolving home directory: %w", err)
		}
	}

	voicePath := filepath.Join(home, ".anthem", "VOICE.md")
	changelogPath := filepath.Join(home, ".anthem", "voice-changelog.md")

	current, err := voice.LoadFile(voicePath)
	if err != nil {
		current = &voice.VoiceConfig{}
	}

	proposed := &voice.VoiceConfig{
		Sections: []voice.Section{{Name: action.SectionName, Content: action.SectionContent}},
	}

	merged := voice.Merge(current, proposed)

	if err := os.WriteFile(voicePath, []byte(merged.Raw), 0644); err != nil {
		return fmt.Errorf("writing VOICE.md: %w", err)
	}

	diff := fmt.Sprintf("- %s\n+ %s", current.Raw, merged.Raw)

	if err := voice.AppendChangelog(changelogPath, "orchestrator", diff); err != nil {
		o.logger.Warn("failed to append voice changelog", "error", err)
	}

	o.voiceContent = merged.Raw
	if o.orchAgent != nil {
		o.orchAgent.SetVoiceContent(merged.Raw)
	}

	o.logger.Info("voice updated", "section", action.SectionName)

	// Record audit
	inputJSON, _ := json.Marshal(action)
	ev := audit.AuditEvent{
		Timestamp:   time.Now(),
		EventType:   "voice.updated",
		ActionName:  strPtr("update_voice"),
		ActionInput: strPtr(string(inputJSON)),
		Metadata:    strPtr(diff),
	}
	if o.currentWave != nil {
		ev.WaveID = &o.currentWave.ID
	}
	if o.auditLogger != nil {
		if err := o.auditLogger.Record(ctx, ev); err != nil {
			o.logger.Warn("failed to record voice audit event", "error", err)
		}
	}

	return nil
}

func (o *Orchestrator) mechanicalDispatch(ctx context.Context, tasks []types.Task) {
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

		if !o.isRetryEligible(task.ID) {
			o.logger.Debug("task in backoff, skipping", "task_id", task.ID)
			continue
		}

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
					o.publish(types.Event{Type: "task.waiting_approval", TaskID: task.ID})
					skip = true
				}
			case rules.ActionAutoAssign:
				if r.AutoAssignee != "" {
					_ = o.tracker.AddComment(ctx, task.ID, fmt.Sprintf("Auto-assigned to @%s", r.AutoAssignee))
					o.logger.Info("auto-assigned task", "task_id", task.ID, "assignee", r.AutoAssignee)
				}
			case rules.ActionMaxCost:
				if r.MaxCost > 0 {
					currentCost := o.costTracker.TaskCost(task.ID)
					if currentCost >= r.MaxCost {
						o.logger.Warn("task exceeded budget, skipping",
							"task_id", task.ID, "max_cost", r.MaxCost, "current_cost", currentCost)
						_ = o.tracker.AddLabel(ctx, task.ID, "exceeded-budget")
						o.publish(types.Event{
							Type: "task.budget_exceeded", TaskID: task.ID,
							Data: map[string]float64{"max_cost": r.MaxCost, "current_cost": currentCost},
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

		o.claim(task)
		snap := o.configSnapshot()
		o.wg.Add(1)
		go o.dispatch(ctx, task, snap)
		o.recordAudit(ctx, "task.dispatched.fallback", task.ID, nil)
	}
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

	// Build full prompt: constraints + task template (voice is orchestrator-only, not for executors)
	fullPrompt := buildFullPrompt(o.userConstraints, cfg.System.Constraints, prompt)

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

	permMode := cfg.Agent.PermissionMode
	if cfg.Agent.SkipPermissions {
		permMode = "bypassPermissions"
	}

	result, err := o.runner.Run(ctx, types.RunOpts{
		WorkspacePath:  wsPath,
		Prompt:         fullPrompt,
		MaxTurns:       cfg.Agent.MaxTurns,
		AllowedTools:   cfg.Agent.AllowedTools,
		Model:          cfg.Agent.Model,
		StallTimeoutMS: cfg.Agent.StallTimeoutMS,
		PermissionMode: permMode,
		DeniedTools:    cfg.Agent.DeniedTools,
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

func (o *Orchestrator) isMaintenanceAutoApproved(maintenanceType string) bool {
	for _, approved := range o.cfg.Maintenance.AutoApprove {
		if approved == maintenanceType {
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

func buildFullPrompt(userConstraints []string, projectConstraints []string, taskPrompt string) string {
	var sections []string
	if constraintBlock := buildConstraints(userConstraints, projectConstraints); constraintBlock != "" {
		sections = append(sections, constraintBlock)
	}
	sections = append(sections, taskPrompt)
	return strings.Join(sections, "\n\n")
}

const maxFileContentBytes = 50 * 1024

func (o *Orchestrator) HandleUserMessage(ctx context.Context, msg channel.IncomingMessage) {
	o.logger.Info("handling user message",
		"sender", msg.SenderID,
		"text_len", len(msg.Text),
		"file_count", len(msg.Files),
	)

	tasks, err := o.tracker.ListActive(ctx)
	if err != nil {
		o.logger.Error("failed to fetch tasks for user message", "error", err)
		o.sendErrorReply(ctx, msg.ThreadID, "Failed to fetch current tasks.")
		return
	}

	snap := o.buildStateSnapshot(tasks)
	snap.UserMessage = buildUserMessageContext(msg)

	if o.orchAgent == nil {
		o.sendErrorReply(ctx, msg.ThreadID, "Orchestrator agent is not enabled.")
		return
	}

	actions, err := o.orchAgent.ConsultWithRepair(ctx, snap)
	if err != nil {
		o.logger.Warn("orchestrator consultation failed for user message", "error", err)
		o.sendErrorReply(ctx, msg.ThreadID, "I couldn't process your message. The orchestrator encountered an error.")
		return
	}

	if actions == nil {
		o.sendErrorReply(ctx, msg.ThreadID, "I couldn't understand your request. Please try again.")
		return
	}

	// Execute actions, using thread ID for any replies
	for i := range actions {
		if actions[i].Type == ActionReply && o.channelMgr != nil {
			replyMsg := channel.OutgoingMessage{
				Text:     actions[i].Body,
				ThreadID: msg.ThreadID,
				Markdown: true,
			}
			if err := o.channelMgr.Broadcast(ctx, replyMsg); err != nil {
				o.logger.Warn("failed to send channel reply", "error", err)
			}
			o.recordAudit(ctx, "channel.reply_sent", "", strPtr("reply"))
		}
	}

	// Execute non-reply actions via the standard path
	var nonReplyActions []Action
	for _, a := range actions {
		if a.Type != ActionReply {
			nonReplyActions = append(nonReplyActions, a)
		}
	}
	o.executeActions(ctx, tasks, nonReplyActions)

	o.recordAudit(ctx, "channel.user_message", "", strPtr("handle_user_message"))
}

func (o *Orchestrator) sendErrorReply(ctx context.Context, threadID string, text string) {
	if o.channelMgr == nil {
		return
	}
	_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
		Text:     text,
		ThreadID: threadID,
		Markdown: false,
	})
}

func buildUserMessageContext(msg channel.IncomingMessage) *UserMessageContext {
	umc := &UserMessageContext{Text: msg.Text}
	for _, f := range msg.Files {
		if isTextMime(f.MimeType) {
			content := string(f.Content)
			if len(content) > maxFileContentBytes {
				content = content[:maxFileContentBytes] + "\n[truncated]"
			}
			umc.Files = append(umc.Files, content)
		} else if strings.HasPrefix(f.MimeType, "image/") {
			umc.Files = append(umc.Files, fmt.Sprintf("[image: %s]", f.Name))
		} else {
			umc.Files = append(umc.Files, fmt.Sprintf("[file: %s, type: %s]", f.Name, f.MimeType))
		}
	}
	return umc
}

func isTextMime(mime string) bool {
	return strings.HasPrefix(mime, "text/") ||
		mime == "application/json" ||
		mime == "application/yaml" ||
		mime == "application/x-yaml"
}

func (o *Orchestrator) StartChannelListener(ctx context.Context) {
	if o.channelMgr == nil {
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-o.channelMgr.Incoming():
				if !ok {
					return
				}
				o.HandleUserMessage(ctx, msg)
			}
		}
	}()
}
