package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/audit"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/cost"
	"github.com/rauriemo/anthem/internal/harness"
	"github.com/rauriemo/anthem/internal/plans"
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
	projectCtx      *ProjectContext
	planStore       *plans.Store

	wg            sync.WaitGroup
	mu            sync.Mutex
	active        map[string]*ActiveRun
	retryState    map[string]*RetryInfo
	taskDeps      map[string][]string
	reviewRetries map[string]int
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
		taskDeps:        make(map[string][]string),
		reviewRetries:   make(map[string]int),
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

	o.loadProjectContext()
	o.initPlanStore()

	o.logger.Info("orchestrator started",
		"interval_ms", o.cfg.Polling.IntervalMS,
		"max_concurrent", o.cfg.Agent.MaxConcurrent,
	)

	o.publish(types.Event{Type: "orchestrator.started"})

	if o.tracker != nil {
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
	} else {
		o.logger.Info("no tracker configured, running in channel-only mode")
		<-ctx.Done()
		o.logger.Info("orchestrator stopping, starting graceful shutdown")
		o.Shutdown()
		o.publish(types.Event{Type: "orchestrator.stopped"})
		return ctx.Err()
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

	if len(state.RetryState) == 0 && len(state.CostSessions) == 0 && len(state.TaskDeps) == 0 {
		o.logger.Debug("no persisted state to restore", "path", o.statePath)
		return nil
	}

	// Restore cost sessions (always safe — no reconciliation needed)
	if len(state.CostSessions) > 0 {
		o.costTracker.LoadSessions(state.CostSessions)
	}

	// Restore task dependencies
	if len(state.TaskDeps) > 0 {
		o.mu.Lock()
		for id, deps := range state.TaskDeps {
			o.taskDeps[id] = deps
		}
		o.mu.Unlock()
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
		"task_deps", len(state.TaskDeps),
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
	depsSnapshot := make(map[string][]string, len(o.taskDeps))
	for id, deps := range o.taskDeps {
		depsSnapshot[id] = deps
	}
	o.mu.Unlock()

	if err := SaveState(o.statePath, retrySnapshot, o.costTracker, depsSnapshot); err != nil {
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
	o.loadProjectContext()
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
		tokIn, tokOut, orchCostUSD := o.orchAgent.DrainCost()
		if orchCostUSD > 0 {
			o.costTracker.Record(cost.SessionCost{
				TaskID:    orchCostTaskID,
				SessionID: fmt.Sprintf("orch-%d", time.Now().UnixMilli()),
				CostUSD:   orchCostUSD,
			})
		}
		o.recordTrace(ctx, "orchestrator", "", &types.RunResult{
			TokensIn:  tokIn,
			TokensOut: tokOut,
			CostUSD:   orchCostUSD,
		}, truncateStr(snapshot.Serialize(), 500))
		if err != nil {
			o.logger.Warn("orchestrator consult failed, falling back to mechanical dispatch", "error", err)
			o.mechanicalDispatch(ctx, tasks)
		} else if actions == nil {
			o.logger.Debug("orchestrator returned nil actions, falling back to mechanical dispatch")
			o.mechanicalDispatch(ctx, tasks)
		} else {
			// Filter out reply/display actions from the polling cycle --
			// these are only meaningful for user-initiated interactions
			// and would duplicate frames already sent by HandleUserMessage.
			var cycleActions []Action
			for _, a := range actions {
				if a.Type != ActionReply && a.Type != ActionDisplay {
					cycleActions = append(cycleActions, a)
				}
			}
			o.executeActions(ctx, tasks, cycleActions)
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

	o.mu.Lock()
	depsSnapshot := make(map[string][]string, len(o.taskDeps))
	for k, v := range o.taskDeps {
		depsSnapshot[k] = v
	}
	o.mu.Unlock()

	for _, t := range tasks {
		ts := TaskSummary{
			ID:       t.ID,
			Title:    t.Title,
			Status:   string(t.Status),
			Labels:   t.Labels,
			CostUSD:  o.costTracker.TaskCost(t.ID),
			Priority: t.Priority,
		}
		if deps, ok := depsSnapshot[t.ID]; ok {
			ts.DependsOn = deps
		}
		snap.Tasks = append(snap.Tasks, ts)
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
		var waveSpent float64
		for _, fid := range o.currentWave.FrontierTaskIDs {
			waveSpent += o.costTracker.TaskCost(fid)
		}
		snap.Budget.WaveSpentUSD = waveSpent
	}

	if o.auditLogger != nil {
		events, err := o.auditLogger.Query(context.Background(), audit.QueryFilter{Limit: 10})
		if err == nil {
			for _, ev := range events {
				es := EventSummary{
					Type:      ev.EventType,
					Timestamp: ev.Timestamp.Format(time.RFC3339),
				}
				if ev.TaskID != nil {
					es.TaskID = *ev.TaskID
				}
				snap.RecentEvents = append(snap.RecentEvents, es)
			}
		}
	}

	snap.Project = o.projectCtx

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

// depsAreMet returns true if all dependency task IDs for the given task have
// reached a terminal status. If the task has no deps, returns true.
func (o *Orchestrator) depsAreMet(taskID string) bool {
	o.mu.Lock()
	deps := o.taskDeps[taskID]
	o.mu.Unlock()
	if len(deps) == 0 {
		return true
	}
	for _, depID := range deps {
		task, err := o.tracker.GetTask(context.Background(), depID)
		if err != nil || task == nil {
			return false
		}
		if !task.Status.IsTerminal() {
			return false
		}
	}
	return true
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
		if task.Status.IsTerminal() || task.Status == types.StatusBlocked || task.Status == types.StatusNeedsApproval {
			continue
		}
		// Tasks with unmet deps that are waiting on non-terminal deps
		// should not prevent wave exhaustion
		if !o.depsAreMet(id) {
			continue
		}
		return false
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

	snap := o.configSnapshot()
	requireApproval := snap.cfg.System.RequireApprovalForRiskyActions

	for _, action := range actions {
		if err := ValidateAction(action, validIDs); err != nil {
			o.logger.Warn("invalid orchestrator action, skipping", "action", action.Type, "error", err)
			continue
		}

		risk := RiskForAction(action.Type)
		if risk == RiskHigh {
			o.logger.Warn("high-risk action proposed", "action", action.Type, "task_id", action.TaskID)
		}
		if requireApproval && risk != RiskLow {
			o.logger.Warn("skipping risky action (require_approval_for_risky_actions is set)",
				"action", action.Type, "risk", risk)
			o.recordAudit(ctx, "action.blocked_by_risk", action.TaskID, strPtr(string(action.Type)))
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
			if !o.depsAreMet(task.ID) {
				o.logger.Debug("dispatch: task has unmet dependencies, skipping", "task_id", task.ID)
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
			dsnap := o.configSnapshot()
			o.wg.Add(1)
			go o.dispatch(ctx, task, dsnap, action.Profile)
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
			activeLabel := ""
			if len(o.cfg.Tracker.Labels.Active) > 0 {
				activeLabel = o.cfg.Tracker.Labels.Active[0]
			}

			// Pass 1: create all issues and build ordinal-to-ID mapping
			ordinalToID := make(map[string]string, len(action.Subtasks))
			type created struct {
				idx int
				id  string
			}
			var createdList []created
			for i, sub := range action.Subtasks {
				createdID, err := o.tracker.CreateIssue(ctx, sub.Title, sub.Body, sub.Labels)
				if err != nil {
					o.logger.Warn("failed to create subtask", "title", sub.Title, "error", err)
					continue
				}
				o.logger.Info("created subtask", "id", createdID, "title", sub.Title)
				ordinalToID[strconv.Itoa(i+1)] = createdID
				createdList = append(createdList, created{idx: i, id: createdID})

				if activeLabel != "" && !slices.Contains(sub.Labels, activeLabel) {
					if err := o.tracker.AddLabel(ctx, createdID, activeLabel); err != nil {
						o.logger.Warn("failed to add active label", "id", createdID, "error", err)
					}
				}
			}

			// Pass 2: register dependencies with remapped IDs
			for _, c := range createdList {
				sub := action.Subtasks[c.idx]
				if len(sub.DependsOn) == 0 {
					continue
				}
				remapped := make([]string, 0, len(sub.DependsOn))
				for _, dep := range sub.DependsOn {
					if realID, ok := ordinalToID[dep]; ok {
						remapped = append(remapped, realID)
					} else {
						remapped = append(remapped, dep)
					}
				}
				o.mu.Lock()
				o.taskDeps[c.id] = remapped
				o.mu.Unlock()
				o.logger.Info("registered task dependencies", "id", c.id, "depends_on", remapped)
			}
			o.recordAudit(ctx, "subtasks.created", "", strPtr("create_subtasks"))

		case ActionPromoteKnowledge:
			if err := o.executePromoteKnowledge(action); err != nil {
				o.logger.Warn("promote_knowledge failed", "error", err)
			} else {
				o.recordAudit(ctx, "knowledge.promoted", "", strPtr("promote_knowledge"))
			}

		case ActionReply:
			if o.channelMgr != nil {
				replyMsg := channel.OutgoingMessage{Text: action.Body, Markdown: true}
				if err := o.channelMgr.Broadcast(ctx, replyMsg); err != nil {
					o.logger.Warn("failed to send channel reply", "error", err)
				}
			}
			o.recordAudit(ctx, "channel.reply_sent", "", strPtr("reply"))

		case ActionDisplay:
			if o.channelMgr != nil {
				component := map[string]any{
					"kind": action.DisplayKind,
				}
				if action.DisplayContent != "" {
					component["content"] = action.DisplayContent
				}
				if action.DisplayTitle != "" {
					component["title"] = action.DisplayTitle
				}
				if action.DisplayLanguage != "" {
					component["language"] = action.DisplayLanguage
				}
				if action.DisplayData != nil {
					if m, ok := action.DisplayData.(map[string]any); ok {
						for k, v := range m {
							component[k] = v
						}
					} else {
						component["data"] = action.DisplayData
					}
				}
				displayMsg := channel.OutgoingMessage{
					Display:   component,
					DisplayID: newDisplayID(),
				}
				if err := o.channelMgr.Broadcast(ctx, displayMsg); err != nil {
					o.logger.Warn("failed to send display payload", "error", err)
				}
			}
			o.recordAudit(ctx, "channel.display_sent", "", strPtr("display"))

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

const orchCostTaskID = "__orchestrator__"

func (o *Orchestrator) recordOrchCost() {
	if o.orchAgent == nil {
		return
	}
	_, _, costUSD := o.orchAgent.DrainCost()
	if costUSD <= 0 {
		return
	}
	o.costTracker.Record(cost.SessionCost{
		TaskID:    orchCostTaskID,
		SessionID: fmt.Sprintf("orch-%d", time.Now().UnixMilli()),
		CostUSD:   costUSD,
	})
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func (o *Orchestrator) recordTrace(ctx context.Context, traceType string, taskID string, result *types.RunResult, prompt string) {
	if o.auditLogger == nil || result == nil {
		return
	}
	promptPreview := truncateStr(prompt, 500)
	responsePreview := truncateStr(result.Output, 500)

	trace := audit.TraceRecord{
		Timestamp:       time.Now(),
		TraceType:       traceType,
		PromptPreview:   &promptPreview,
		ResponsePreview: &responsePreview,
		TokensIn:        result.TokensIn,
		TokensOut:       result.TokensOut,
		CostUSD:         result.CostUSD,
		DurationMS:      result.Duration.Milliseconds(),
	}
	if taskID != "" {
		trace.TaskID = &taskID
	}
	if result.SessionID != "" {
		trace.SessionID = &result.SessionID
	}
	if o.currentWave != nil {
		trace.WaveID = &o.currentWave.ID
	}
	if err := o.auditLogger.RecordTrace(ctx, trace); err != nil {
		o.logger.Warn("failed to record trace", "trace_type", traceType, "error", err)
	}
}

func (o *Orchestrator) recordAudit(ctx context.Context, eventType string, taskID string, actionName *string) {
	o.recordAuditWithCost(ctx, eventType, taskID, actionName, nil, nil)
}

func (o *Orchestrator) recordAuditWithCost(ctx context.Context, eventType string, taskID string, actionName *string, costUSD *float64, errMsg *string) {
	if o.auditLogger == nil {
		return
	}
	ev := audit.AuditEvent{
		Timestamp:  time.Now(),
		EventType:  eventType,
		ActionName: actionName,
		CostUSD:    costUSD,
		Error:      errMsg,
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

func newDisplayID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "dsp-" + hex.EncodeToString(b)
}

type reviewResult struct {
	Passed   bool   `json:"passed"`
	Feedback string `json:"feedback"`
}

func (o *Orchestrator) runReview(ctx context.Context, task types.Task, execResult *types.RunResult) (passed bool, feedback string) {
	snap := o.configSnapshot()
	cfg := snap.cfg

	outputPreview := ""
	if execResult != nil && execResult.Output != "" {
		outputPreview = execResult.Output
		if len(outputPreview) > 4096 {
			outputPreview = outputPreview[len(outputPreview)-4096:]
		}
	}

	reviewPrompt := cfg.Agent.ReviewPrompt
	if reviewPrompt == "" {
		reviewPrompt = "You are a code reviewer. Review the following executor output for the task described below."
	}

	prompt := fmt.Sprintf(`%s

## Task
Title: %s
Body: %s

## Executor Output (last 4KB)
%s

Respond with a JSON object: {"passed": true/false, "feedback": "..."}
If the output looks correct and complete, set passed=true. Otherwise set passed=false with specific feedback.`,
		reviewPrompt, task.Title, task.Body, outputPreview)

	reviewTurns := cfg.Agent.ReviewMaxTurns
	if reviewTurns <= 0 {
		reviewTurns = 3
	}

	result, err := o.runner.Run(ctx, types.RunOpts{
		Prompt:         prompt,
		MaxTurns:       reviewTurns,
		PermissionMode: "bypassPermissions",
	})

	if result != nil {
		o.costTracker.Record(cost.SessionCost{
			TaskID:    task.ID,
			SessionID: fmt.Sprintf("review-%s-%d", task.ID, time.Now().UnixMilli()),
			CostUSD:   result.CostUSD,
			TokensIn:  result.TokensIn,
			TokensOut: result.TokensOut,
		})
	}

	o.recordTrace(ctx, "reviewer", task.ID, result, prompt)

	if err != nil {
		o.logger.Warn("reviewer agent failed, defaulting to passed", "task_id", task.ID, "error", err)
		return true, ""
	}

	var rv reviewResult
	if err := json.Unmarshal([]byte(result.Output), &rv); err != nil {
		o.logger.Debug("failed to parse review response, defaulting to passed", "task_id", task.ID, "output", result.Output)
		return true, ""
	}

	o.logger.Info("review result", "task_id", task.ID, "passed", rv.Passed, "feedback", rv.Feedback)
	return rv.Passed, rv.Feedback
}

func (o *Orchestrator) executePromoteKnowledge(action Action) error {
	root := o.cfg.Workspace.Root
	if root == "" {
		root = "."
	}
	dir := filepath.Join(root, "docs", "exec-plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating exec-plans directory: %w", err)
	}

	sanitized := sanitizeFilename(action.Summary)
	if len(sanitized) > 60 {
		sanitized = sanitized[:60]
	}
	filename := fmt.Sprintf("%s-%s.md", time.Now().Format("2006-01-02"), sanitized)
	path := filepath.Join(dir, filename)

	if err := os.WriteFile(path, []byte(action.Summary), 0o644); err != nil {
		return fmt.Errorf("writing knowledge file: %w", err)
	}
	o.logger.Info("promoted knowledge", "path", path)

	o.loadProjectContext()
	return nil
}

func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

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

		if !o.depsAreMet(task.ID) {
			o.logger.Debug("task has unmet dependencies, skipping", "task_id", task.ID)
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
			case rules.ActionRequirePlan:
				hasPlanApproved := false
				for _, l := range task.Labels {
					if l == "plan-approved" {
						hasPlanApproved = true
						break
					}
				}
				if !hasPlanApproved {
					o.logger.Info("task requires plan, skipping",
						"task_id", task.ID, "identifier", task.Identifier)
					_ = o.tracker.AddLabel(ctx, task.ID, "needs-plan")
					o.publish(types.Event{Type: "task.needs_plan", TaskID: task.ID})
					skip = true
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
		dispatchProfile := ""
		if ri := o.retryInfo(task.ID); ri != nil && strings.HasPrefix(ri.LastError, "review failed: ") {
			dispatchProfile = "debugger"
		}
		o.wg.Add(1)
		go o.dispatch(ctx, task, snap, dispatchProfile)
		o.recordAudit(ctx, "task.dispatched.fallback", task.ID, nil)
	}
}

func (o *Orchestrator) dispatch(ctx context.Context, task types.Task, snap cfgSnapshot, profileNames ...string) {
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
		errStr := err.Error()
		o.recordAuditWithCost(ctx, "task.failed", task.ID, nil, nil, &errStr)
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
			errStr := err.Error()
			o.recordAuditWithCost(ctx, "task.failed", task.ID, nil, nil, &errStr)
			o.publish(types.Event{Type: "task.failed", TaskID: task.ID, Data: err.Error()})
			return
		}
	}

	// Resolve agent profile
	profileName := "coder"
	if len(profileNames) > 0 && profileNames[0] != "" {
		profileName = profileNames[0]
	}
	var profile *config.AgentProfile
	if cfg.Agent.Profiles != nil {
		if p, ok := cfg.Agent.Profiles[profileName]; ok {
			profile = &p
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
	if profile != nil && profile.PromptPrefix != "" {
		prompt = profile.PromptPrefix + "\n\n" + prompt
	}
	if profile != nil && profile.PromptSuffix != "" {
		prompt = prompt + "\n\n" + profile.PromptSuffix
	}
	fullPrompt := buildFullPrompt(o.userConstraints, cfg.System.Constraints, prompt)

	// Append reviewer feedback for retries
	if ri := o.retryInfo(task.ID); ri != nil && strings.HasPrefix(ri.LastError, "review failed: ") {
		fullPrompt += "\n\n## Previous Attempt Feedback\n\n" + strings.TrimPrefix(ri.LastError, "review failed: ")
	}

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

	runOpts, _ := o.resolveProfile(profileName, wsPath)
	runOpts.Prompt = fullPrompt
	result, err := o.runner.Run(ctx, runOpts)

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
		errStr := err.Error()
		var costPtr *float64
		if result != nil && result.CostUSD > 0 {
			costPtr = &result.CostUSD
		}
		o.recordAuditWithCost(ctx, "task.failed", task.ID, nil, costPtr, &errStr)
		o.publish(types.Event{Type: "task.failed", TaskID: task.ID, Data: err.Error()})
		return
	}

	if result.ExitCode != 0 {
		exitErr := fmt.Errorf("claude exited with code %d", result.ExitCode)
		o.recordFailure(task.ID, exitErr)
		ri := o.retryInfo(task.ID)
		o.logger.Error("agent exited non-zero",
			"task_id", task.ID,
			"exit_code", result.ExitCode,
			"attempt", ri.Attempts,
			"next_retry_at", ri.NextRetryAt,
		)
		_ = o.tracker.AddComment(ctx, task.ID,
			fmt.Sprintf("Claude exited with code %d. Retry attempt %d, next at %s.",
				result.ExitCode, ri.Attempts, ri.NextRetryAt.Format(time.RFC3339)))
		errStr := exitErr.Error()
		o.recordAuditWithCost(ctx, "task.failed", task.ID, nil, &result.CostUSD, &errStr)
		o.publish(types.Event{Type: "task.failed", TaskID: task.ID, Data: exitErr.Error()})
		return
	}

	// Review step (if enabled)
	if cfg.Agent.ReviewEnabled {
		passed, feedback := o.runReview(ctx, task, result)
		if !passed {
			maxReviewRetries := cfg.Agent.ReviewMaxRetries
			if maxReviewRetries <= 0 {
				maxReviewRetries = 1
			}
			o.mu.Lock()
			o.reviewRetries[task.ID]++
			retryCount := o.reviewRetries[task.ID]
			o.mu.Unlock()

			if retryCount > maxReviewRetries {
				o.logger.Warn("review failed but max retries exceeded, completing anyway",
					"task_id", task.ID, "retries", retryCount)
				_ = o.tracker.AddLabel(ctx, task.ID, "review-skipped")
			} else {
				o.recordFailure(task.ID, fmt.Errorf("review failed: %s", feedback))
				o.recordAuditWithCost(ctx, "task.review_failed", task.ID, nil, &result.CostUSD, &feedback)
				o.publish(types.Event{Type: "task.review_failed", TaskID: task.ID, Data: feedback})
				_ = o.tracker.AddComment(ctx, task.ID,
					fmt.Sprintf("Review failed: %s. Retrying.", feedback))
				return
			}
		} else {
			o.recordAudit(ctx, "task.review_passed", task.ID, strPtr("review"))
		}
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

	o.recordTrace(ctx, "executor", task.ID, result, fullPrompt)
	o.recordAuditWithCost(ctx, "task.completed", task.ID, nil, &result.CostUSD, nil)
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

	// Send a protocol-level ack so the caller can reset its timeout without
	// surfacing any text to the user. The real reply follows as a channel.followup event.
	if o.channelMgr != nil {
		_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
			Ack:      true,
			ThreadID: msg.ThreadID,
		})
	}

	// Extract model tag before routing (e.g. [model:claude-sonnet-4-6])
	model, cleanedText := extractModelTag(msg.Text)
	msg.Text = cleanedText

	// Plan/build routing -- must be checked before fast path
	if strings.Contains(msg.Text, "[system:build]") {
		o.handleBuildMessage(ctx, msg, model)
		return
	}
	if strings.Contains(msg.Text, "[system:plan]") {
		o.handlePlanMessage(ctx, msg, model)
		return
	}

	// Fast path for lightweight queries (status checks, /fast messages)
	if strings.Contains(msg.Text, "[system:status]") || strings.Contains(msg.Text, "[system:fast]") {
		o.handleLeanMessage(ctx, msg, model)
		return
	}

	if o.tracker == nil {
		o.handleLeanMessage(ctx, msg, model)
		return
	}

	tasks, err := o.tracker.ListActive(ctx)
	if err != nil {
		o.logger.Error("failed to fetch tasks for user message", "error", err)
		o.sendFollowUp(ctx, msg, "Failed to fetch current tasks.")
		return
	}

	snap := o.buildStateSnapshot(tasks)
	snap.UserMessage = buildUserMessageContext(msg)
	snap.SourceChannel = msg.ChannelKind

	// Inject latest plan context so agent mode can reference plans created in plan mode
	if o.planStore != nil {
		slug := o.projectSlug()
		if draft, err := o.planStore.LatestDraft(slug); err == nil && draft != nil {
			snap.ActivePlan = &PlanContext{
				Path:    draft.Path,
				Content: draft.Body,
				Status:  string(draft.Frontmatter.Status),
			}
		}
		if metas, err := o.planStore.List(slug); err == nil {
			for _, m := range metas {
				snap.PlanHistory = append(snap.PlanHistory, PlanMetaSummary{
					Path:    m.Path,
					Title:   m.Title,
					Status:  string(m.Status),
					Updated: m.Updated,
				})
			}
		}
	}

	if o.orchAgent == nil {
		o.handleLeanMessage(ctx, msg, model)
		return
	}

	o.logger.Debug("consulting orchestrator agent for user message")

	onStream := func(delta string) {
		if o.channelMgr != nil {
			_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
				StreamDelta: delta,
				ThreadID:    msg.ThreadID,
			})
		}
	}
	actions, err := o.orchAgent.ConsultWithRepairStreaming(ctx, snap, onStream)
	o.recordOrchCost()

	if o.channelMgr != nil {
		_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
			StreamDone: true,
			ThreadID:   msg.ThreadID,
		})
	}

	if err != nil {
		o.logger.Warn("orchestrator consultation failed for user message", "error", err)
		o.sendFollowUp(ctx, msg, "I couldn't process your message. The orchestrator encountered an error.")
		return
	}

	if actions == nil {
		o.sendFollowUp(ctx, msg, "I couldn't understand your request. Please try again.")
		return
	}

	hasDisplay := msg.ChannelKind == "prism" && slices.ContainsFunc(actions, func(a Action) bool {
		return a.Type == ActionDisplay
	})

	// Execute actions -- replies and display frames are sent as follow-up notifications
	for i := range actions {
		if actions[i].Type == ActionReply && o.channelMgr != nil {
			if hasDisplay && strings.TrimSpace(actions[i].Body) == "" {
				continue
			}
			o.sendFollowUp(ctx, msg, actions[i].Body)
			o.recordAudit(ctx, "channel.reply_sent", "", strPtr("reply"))
		}
		if actions[i].Type == ActionDisplay && o.channelMgr != nil {
			component := map[string]any{"kind": actions[i].DisplayKind}
			if actions[i].DisplayContent != "" {
				component["content"] = actions[i].DisplayContent
			}
			if actions[i].DisplayTitle != "" {
				component["title"] = actions[i].DisplayTitle
			}
			if actions[i].DisplayLanguage != "" {
				component["language"] = actions[i].DisplayLanguage
			}
			if actions[i].DisplayData != nil {
				if m, ok := actions[i].DisplayData.(map[string]any); ok {
					for k, v := range m {
						component[k] = v
					}
				} else {
					component["data"] = actions[i].DisplayData
				}
			}
			_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
				Display:   component,
				DisplayID: newDisplayID(),
				ThreadID:  msg.ThreadID,
			})
			o.recordAudit(ctx, "channel.display_sent", "", strPtr("display"))
		}
	}

	// Execute non-reply/non-display actions via the standard path
	var otherActions []Action
	for _, a := range actions {
		if a.Type != ActionReply && a.Type != ActionDisplay {
			otherActions = append(otherActions, a)
		}
	}
	o.executeActions(ctx, tasks, otherActions)

	o.recordAudit(ctx, "channel.user_message", "", strPtr("handle_user_message"))
}

// sendFollowUp sends a follow-up message after the initial protocol ack.
// It carries the original ThreadID so the channel client can correlate the
// follow-up with the pending request and resolve it as the real response.
func (o *Orchestrator) sendFollowUp(ctx context.Context, original channel.IncomingMessage, text string) {
	if o.channelMgr == nil {
		return
	}
	_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
		Text:      text,
		ThreadID:  original.ThreadID,
		EventType: "channel.followup",
	})
}

var modelTagRe = regexp.MustCompile(`\[model:([^\]]+)\]`)

func extractModelTag(text string) (model, cleaned string) {
	m := modelTagRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return "", text
	}
	cleaned = modelTagRe.ReplaceAllString(text, " ")
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	return strings.TrimSpace(m[1]), cleaned
}

const leanCostTaskID = "__lean__"

func buildLeanPrompt(projectCtx *ProjectContext, msg channel.IncomingMessage) string {
	var prompt strings.Builder

	prompt.WriteString("## Constraints\n\n")
	prompt.WriteString("You are in FAST mode with exactly ONE turn. You MUST respond directly with text. ")
	prompt.WriteString("Do NOT use tools (Read, Grep, Glob, Bash, Task, etc.) — they will not execute and their raw JSON will leak to the user. ")
	prompt.WriteString("Answer from the project context below and your general knowledge. ")
	prompt.WriteString("If the question requires exploring the codebase or running commands, say so clearly and suggest the user switch to Agent mode.\n\n")

	if projectCtx != nil && projectCtx.ProjectSummary != "" {
		prompt.WriteString("## Project Context\n\n")
		prompt.WriteString(projectCtx.ProjectSummary)
		prompt.WriteString("\n\n")
	}

	if msg.ChannelKind == "prism" {
		prompt.WriteString("## Rich Display Output\n\n")
		prompt.WriteString("You are responding through Prism, which supports rich visual artifacts. ")
		prompt.WriteString("To send a visual display, include a fenced block in your response:\n\n")
		prompt.WriteString("```prism-display\n{\"kind\":\"html\",\"content\":\"<full self-contained HTML>\"}\n```\n\n")
		prompt.WriteString("Supported kinds: html, markdown, code, data, chart. ")
		prompt.WriteString("For HTML: inline all CSS/JS, use dark theme (#1e1e1e background, #d4d4d4 text). ")
		prompt.WriteString("Always include a prism-display block for visual answers (dashboards, reports, tables, code). ")
		prompt.WriteString("You may also include regular text outside the block for the chat.\n\n")
	}

	prompt.WriteString("## User Message\n\n")
	prompt.WriteString(msg.Text)

	if len(msg.Files) > 0 {
		umc := buildUserMessageContext(msg)
		for _, f := range umc.Files {
			prompt.WriteString("\n\n## Attached File\n\n")
			prompt.WriteString(f)
		}
	}

	return prompt.String()
}

func (o *Orchestrator) handleLeanMessage(ctx context.Context, msg channel.IncomingMessage, model string) {
	o.logger.Info("handling lean message", "sender", msg.SenderID, "text_len", len(msg.Text))

	promptText := buildLeanPrompt(o.projectCtx, msg)

	root := o.cfg.Workspace.Root
	wsPath := ""
	if root != "" && root != "." {
		absRoot := root
		if !filepath.IsAbs(root) {
			if cwd, err := os.Getwd(); err == nil {
				absRoot = filepath.Join(cwd, root)
			}
		}
		if info, err := os.Stat(absRoot); err == nil && info.IsDir() {
			wsPath = absRoot
		}
	}

	onStream := func(delta string) {
		if o.channelMgr != nil {
			_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
				StreamDelta: delta,
				ThreadID:    msg.ThreadID,
			})
		}
	}

	result, err := o.runner.Run(ctx, types.RunOpts{
		WorkspacePath:  wsPath,
		Prompt:         promptText,
		MaxTurns:       1,
		Model:          model,
		PermissionMode: "bypassPermissions",
		OnStream:       onStream,
	})

	if o.channelMgr != nil {
		_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
			StreamDone: true,
			ThreadID:   msg.ThreadID,
		})
	}

	if result != nil {
		o.costTracker.Record(cost.SessionCost{
			TaskID:    leanCostTaskID,
			SessionID: result.SessionID,
			TokensIn:  result.TokensIn,
			TokensOut: result.TokensOut,
			CostUSD:   result.CostUSD,
		})
	}

	if err != nil {
		o.logger.Error("lean message agent failed", "error", err)
		if result == nil || result.Output == "" {
			o.sendFollowUp(ctx, msg, "Failed to process your message.")
			var costPtr *float64
			if result != nil && result.CostUSD > 0 {
				costPtr = &result.CostUSD
			}
			errStr := err.Error()
			o.recordAuditWithCost(ctx, "channel.lean_message", "", strPtr("lean_message"), costPtr, &errStr)
			return
		}
	}

	responseText := ""
	if result != nil {
		responseText = result.Output
	}
	cleanText, displays := extractLeanDisplayBlocks(responseText)

	for _, comp := range displays {
		if o.channelMgr != nil {
			_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
				Display:   comp,
				DisplayID: newDisplayID(),
				ThreadID:  msg.ThreadID,
			})
		}
	}

	o.sendFollowUp(ctx, msg, strings.TrimRight(cleanText, "\n"))
	o.recordTrace(ctx, "lean", "", result, promptText)
	var costPtr *float64
	if result != nil && result.CostUSD > 0 {
		costPtr = &result.CostUSD
	}
	o.recordAuditWithCost(ctx, "channel.lean_message", "", strPtr("lean_message"), costPtr, nil)
}

// extractLeanDisplayBlocks scans text for ```prism-display fenced blocks
// containing JSON display component objects. Returns the text with those
// blocks removed and a slice of parsed display components.
//
// Format:
//
//	```prism-display
//	{"kind":"html","content":"<h1>Hello</h1>"}
//	```
func extractLeanDisplayBlocks(text string) (string, []map[string]any) {
	var displays []map[string]any
	var cleaned strings.Builder
	lines := strings.Split(text, "\n")
	inBlock := false
	var blockBuf strings.Builder

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock && (trimmed == "```prism-display" || trimmed == "``` prism-display") {
			inBlock = true
			blockBuf.Reset()
			continue
		}
		if inBlock {
			if trimmed == "```" {
				inBlock = false
				var comp map[string]any
				if err := json.Unmarshal([]byte(blockBuf.String()), &comp); err == nil {
					if _, hasKind := comp["kind"]; hasKind {
						displays = append(displays, comp)
					}
				}
				continue
			}
			blockBuf.WriteString(line)
			blockBuf.WriteString("\n")
			continue
		}
		cleaned.WriteString(line)
		cleaned.WriteString("\n")
	}
	return cleaned.String(), displays
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

const maxDocBytes = 8 * 1024

func (o *Orchestrator) loadProjectContext() {
	pctx := &ProjectContext{}

	root := o.cfg.Workspace.Root
	if root == "" || root == "." {
		wd, err := os.Getwd()
		if err != nil {
			o.logger.Warn("failed to get working directory for file tree", "error", err)
			o.projectCtx = pctx
			return
		}
		root = wd
	}

	tree, err := generateFileTree(root, 6)
	if err != nil {
		o.logger.Warn("failed to generate file tree", "error", err)
	} else {
		pctx.FileTree = tree
	}

	pctx.ProjectSummary = readDocFile("CLAUDE.md")
	pctx.Architecture = readDocFile(filepath.Join("docs", "plans", "architecture.md"))
	pctx.Implementation = readDocFile(filepath.Join("docs", "plans", "implementation.md"))
	pctx.Knowledge = loadKnowledge(filepath.Join(root, "docs", "exec-plans"))

	o.projectCtx = pctx
}

const maxKnowledgeBytes = 8 * 1024

func loadKnowledge(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	// Filter to .md files, sorted by name (date prefix gives chronological order)
	var mdFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			mdFiles = append(mdFiles, filepath.Join(dir, e.Name()))
		}
	}
	if len(mdFiles) == 0 {
		return ""
	}

	// Take last 5 (most recent)
	start := 0
	if len(mdFiles) > 5 {
		start = len(mdFiles) - 5
	}
	mdFiles = mdFiles[start:]

	var b strings.Builder
	for _, f := range mdFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if b.Len()+len(data) > maxKnowledgeBytes {
			break
		}
		b.WriteString("### ")
		b.WriteString(filepath.Base(f))
		b.WriteByte('\n')
		b.Write(data)
		b.WriteString("\n\n")
	}
	return b.String()
}

func readDocFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := string(data)
	if len(s) > maxDocBytes {
		return s[:maxDocBytes] + "\n[truncated]"
	}
	return s
}

const maxTreeBytes = 8 * 1024

var skipDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true, "workspaces": true,
	".idea": true, ".vscode": true, ".claude": true, ".cursor": true,
}

var skipFileExts = map[string]bool{
	".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".out": true, ".swp": true, ".swo": true,
}

var skipFileNames = map[string]bool{
	".DS_Store": true, "Thumbs.db": true,
}

type dirEntry struct {
	name  string
	isDir bool
}

func generateFileTree(root string, maxDepth int) (string, error) {
	if maxDepth <= 0 {
		maxDepth = 6
	}

	// Collect all entries grouped by parent directory.
	children := make(map[string][]dirEntry)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}

		name := d.Name()
		depth := strings.Count(rel, "/") + 1

		if d.IsDir() {
			if skipDirs[name] {
				return filepath.SkipDir
			}
			if depth > maxDepth {
				return filepath.SkipDir
			}
			parent := filepath.ToSlash(filepath.Dir(rel))
			children[parent] = append(children[parent], dirEntry{name: name, isDir: true})
			return nil
		}

		// Skip filtered files
		if skipFileNames[name] || skipFileExts[filepath.Ext(name)] {
			return nil
		}
		if depth > maxDepth {
			return nil
		}

		parent := filepath.ToSlash(filepath.Dir(rel))
		children[parent] = append(children[parent], dirEntry{name: name, isDir: false})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walking directory: %w", err)
	}

	// Sort each group: directories first, then files, both alphabetical.
	for key := range children {
		entries := children[key]
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].isDir != entries[j].isDir {
				return entries[i].isDir
			}
			return entries[i].name < entries[j].name
		})
	}

	// Render tree via DFS.
	var b strings.Builder
	truncated := false

	var render func(parent string, depth int)
	render = func(parent string, depth int) {
		if truncated {
			return
		}
		for _, e := range children[parent] {
			if truncated {
				return
			}
			indent := strings.Repeat("  ", depth)
			display := e.name
			if e.isDir {
				display += "/"
			}
			line := indent + display + "\n"
			if b.Len()+len(line) > maxTreeBytes {
				truncated = true
				return
			}
			b.WriteString(line)
			if e.isDir {
				childPath := e.name
				if parent != "." {
					childPath = parent + "/" + e.name
				}
				render(childPath, depth+1)
			}
		}
	}

	render(".", 0)

	result := b.String()
	if truncated {
		result += "... (truncated)\n"
	}
	return result, nil
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

// --- Plan mode handlers ---

func (o *Orchestrator) initPlanStore() {
	home := o.homeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			o.logger.Warn("failed to get home dir for plan store", "error", err)
			return
		}
	}
	store, err := plans.NewStore(home)
	if err != nil {
		o.logger.Warn("failed to init plan store", "error", err)
		return
	}
	o.planStore = store
}

func (o *Orchestrator) projectSlug() string {
	if o.cfg.Tracker.Repo != "" {
		return o.cfg.Tracker.Repo
	}
	return "default"
}

// extractPlanBlock pulls the markdown content from an ```anthem-plan fenced block.
func extractPlanBlock(output string) string {
	const open = "```anthem-plan\n"
	start := strings.Index(output, open)
	if start == -1 {
		const openAlt = "```anthem-plan\r\n"
		start = strings.Index(output, openAlt)
		if start == -1 {
			return ""
		}
		start += len(openAlt)
	} else {
		start += len(open)
	}
	end := strings.Index(output[start:], "\n```")
	if end == -1 {
		return output[start:]
	}
	return output[start : start+end]
}

const explorerCostTaskID = "__explorer__"

func (o *Orchestrator) handlePlanMessage(ctx context.Context, msg channel.IncomingMessage, model string) {
	o.logger.Info("handling plan message", "sender", msg.SenderID, "text_len", len(msg.Text))

	cleanText := strings.Replace(msg.Text, "[system:plan]", "", 1)
	cleanText = strings.TrimSpace(cleanText)

	if o.orchAgent == nil {
		o.sendFollowUp(ctx, msg, "Plan mode requires the orchestrator agent to be configured.")
		return
	}

	onStream := func(delta string) {
		if o.channelMgr != nil {
			_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
				StreamDelta: delta,
				ThreadID:    msg.ThreadID,
			})
		}
	}

	snap := o.buildPlanSnapshot(ctx, msg, cleanText)

	// Phase 1: Scout — identify areas needing deep research
	explores, userMsg, scoutErr := o.orchAgent.ScoutPlan(ctx, snap, model, onStream)
	o.recordOrchCost()

	if o.channelMgr != nil {
		_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
			StreamDone: true,
			ThreadID:   msg.ThreadID,
		})
	}

	// Fallback: if scout failed or returned 0 explores, use single-run ConsultPlan
	if scoutErr != nil || len(explores) == 0 {
		if scoutErr != nil {
			o.logger.Warn("scout phase failed, falling back to single-run plan", "error", scoutErr)
		} else {
			o.logger.Info("scout returned 0 explores, using single-run plan mode")
		}
		o.handlePlanFallback(ctx, msg, snap, model, onStream)
		return
	}

	// Send scout message to user while explorers run
	if userMsg != "" {
		o.sendFollowUp(ctx, msg, userMsg)
	}

	// Phase 2: Run explorer subagents in parallel
	o.logger.Info("launching explorer subagents", "count", len(explores))
	fileTree := ""
	if snap.Project != nil {
		fileTree = snap.Project.FileTree
	}
	findings := o.runExplorers(ctx, explores, model, fileTree, func(status string) {
		o.sendFollowUp(ctx, msg, status)
	})

	// Phase 3: Synthesize plan from explorer findings
	o.logger.Info("synthesizing plan from explorer findings", "findings_count", len(findings))
	output, err := o.orchAgent.SynthesizePlan(ctx, snap, findings, model, onStream)
	o.recordOrchCost()

	if o.channelMgr != nil {
		_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
			StreamDone: true,
			ThreadID:   msg.ThreadID,
		})
	}

	if err != nil {
		o.logger.Warn("synthesis phase failed, falling back to single-run plan", "error", err)
		o.handlePlanFallback(ctx, msg, snap, model, onStream)
		return
	}

	o.finalizePlan(ctx, msg, output)
}

// buildPlanSnapshot constructs the state snapshot with plan context for plan mode.
func (o *Orchestrator) buildPlanSnapshot(ctx context.Context, msg channel.IncomingMessage, cleanText string) StateSnapshot {
	var tasks []types.Task
	if o.tracker != nil {
		var err error
		tasks, err = o.tracker.ListActive(ctx)
		if err != nil {
			o.logger.Warn("plan mode: failed to list tasks", "error", err)
		}
	}
	snap := o.buildStateSnapshot(tasks)
	snap.UserMessage = &UserMessageContext{Text: cleanText}
	snap.SourceChannel = msg.ChannelKind

	if o.planStore != nil {
		slug := o.projectSlug()
		if draft, err := o.planStore.LatestDraft(slug); err == nil && draft != nil {
			snap.ActivePlan = &PlanContext{
				Path:    draft.Path,
				Content: draft.Body,
				Status:  string(draft.Frontmatter.Status),
			}
		}
		if metas, err := o.planStore.List(slug); err == nil {
			for _, m := range metas {
				snap.PlanHistory = append(snap.PlanHistory, PlanMetaSummary{
					Path:    m.Path,
					Title:   m.Title,
					Status:  string(m.Status),
					Updated: m.Updated,
				})
			}
		}
	}
	return snap
}

// handlePlanFallback runs the original single-run ConsultPlan path.
func (o *Orchestrator) handlePlanFallback(ctx context.Context, msg channel.IncomingMessage, snap StateSnapshot, model string, onStream func(string)) {
	output, err := o.orchAgent.ConsultPlan(ctx, snap, model, onStream)
	o.recordOrchCost()

	if o.channelMgr != nil {
		_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
			StreamDone: true,
			ThreadID:   msg.ThreadID,
		})
	}

	if err != nil {
		o.logger.Warn("plan consult failed", "error", err)
		o.sendFollowUp(ctx, msg, "Plan consultation failed. Please try again.")
		return
	}

	o.finalizePlan(ctx, msg, output)
}

// finalizePlan extracts, saves, and broadcasts the plan artifact.
func (o *Orchestrator) finalizePlan(ctx context.Context, msg channel.IncomingMessage, output string) {
	planContent := extractPlanBlock(output)
	if planContent == "" {
		planContent = output
	}

	var planPath string
	if o.planStore != nil {
		slug := o.projectSlug()
		if draft, _ := o.planStore.LatestDraft(slug); draft != nil {
			if err := o.planStore.Update(draft.Path, planContent); err != nil {
				o.logger.Warn("failed to update plan", "error", err)
			}
			planPath = draft.Path
		} else {
			title := extractPlanTitle(planContent)
			if title == "" {
				title = "Plan"
			}
			path, err := o.planStore.Save(slug, title, planContent)
			if err != nil {
				o.logger.Warn("failed to save plan", "error", err)
			}
			planPath = path
		}
	}

	if o.channelMgr != nil {
		component := map[string]any{
			"kind":     "plan",
			"content":  planContent,
			"planPath": planPath,
		}
		_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
			Display:   component,
			DisplayID: newDisplayID(),
			ThreadID:  msg.ThreadID,
		})

		if planPath != "" {
			o.sendFollowUp(ctx, msg, buildPlanCard(planContent, planPath))
		}
	}

	o.recordAudit(ctx, "channel.plan_proposed", "", strPtr("plan"))
}

// focusToProfile maps an ExploreRequest.Focus category to a profile name.
func focusToProfile(focus string) string {
	switch focus {
	case "security":
		return "security-explorer"
	case "tests":
		return "test-explorer"
	default:
		return "explorer"
	}
}

// resolveProfile builds RunOpts by merging base config with a named profile,
// then prepares the workspace with MCP config and skills.
func (o *Orchestrator) resolveProfile(profileName string, wsPath string) (types.RunOpts, error) {
	cfg := o.cfg

	permMode := cfg.Agent.PermissionMode
	if cfg.Agent.SkipPermissions {
		permMode = "bypassPermissions"
	}

	opts := types.RunOpts{
		WorkspacePath:  wsPath,
		MaxTurns:       cfg.Agent.MaxTurns,
		AllowedTools:   cfg.Agent.AllowedTools,
		Model:          cfg.Agent.Model,
		StallTimeoutMS: cfg.Agent.StallTimeoutMS,
		PermissionMode: permMode,
		DeniedTools:    append([]string{}, cfg.Agent.DeniedTools...),
		AdditionalDirs: cfg.Agent.AdditionalDirs,
	}

	var profile *config.AgentProfile
	if cfg.Agent.Profiles != nil {
		if p, ok := cfg.Agent.Profiles[profileName]; ok {
			profile = &p
		}
	}

	if profile != nil {
		if len(profile.AllowedTools) > 0 {
			opts.AllowedTools = profile.AllowedTools
		}
		if len(profile.DeniedTools) > 0 {
			opts.DeniedTools = append(opts.DeniedTools, profile.DeniedTools...)
		}
		if profile.Model != "" {
			opts.Model = profile.Model
		}
		if profile.MaxTurns > 0 {
			opts.MaxTurns = profile.MaxTurns
		}
	}

	// Resolve MCP servers: profile refs looked up in the global registry
	var profileMCPRefs []string
	if profile != nil {
		profileMCPRefs = profile.MCPRefs
	}
	mcpServers := harness.ResolveMCPServers(cfg.Agent.MCPServers, nil, profileMCPRefs)
	if err := harness.WriteMCPConfig(wsPath, mcpServers); err != nil {
		o.logger.Warn("failed to write MCP config", "profile", profileName, "error", err)
	}

	// Resolve skills: global baseline + profile refs
	var profileSkillRefs []string
	if profile != nil {
		profileSkillRefs = profile.SkillRefs
	}
	allSkills := harness.ResolveSkillRefs(cfg.Agent.Skills, profileSkillRefs)
	if err := harness.PrepareSkills(wsPath, allSkills, o.builtinSkillsDir(), o.logger); err != nil {
		o.logger.Warn("failed to prepare skills", "profile", profileName, "error", err)
	}

	return opts, nil
}

// builtinSkillsDir returns the path to Anthem's built-in skill templates.
func (o *Orchestrator) builtinSkillsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".anthem", "skills")
}

// runExplorers spawns parallel Claude Code processes for focused codebase research.
func (o *Orchestrator) runExplorers(ctx context.Context, reqs []ExploreRequest, model string, fileTree string, onStatus func(string)) []ExploreResult {
	results := make([]ExploreResult, len(reqs))
	var wg sync.WaitGroup

	for i, req := range reqs {
		wg.Add(1)
		go func(idx int, r ExploreRequest) {
			defer wg.Done()

			prompt := BuildExplorerPrompt(r, fileTree)

			o.logger.Info("explorer started", "index", idx, "focus", r.Focus, "scope", r.Scope)

			profileName := focusToProfile(r.Focus)
			opts, _ := o.resolveProfile(profileName, "")
			opts.Prompt = prompt
			opts.Model = model
			opts.PermissionMode = "bypassPermissions"
			opts.MaxTurns = o.orchAgent.explorerMaxTurns

			result, err := o.runner.Run(ctx, opts)

			if err != nil {
				o.logger.Warn("explorer failed", "index", idx, "error", err)
				results[idx] = ExploreResult{
					Query: r.Query,
					Error: fmt.Sprintf("explorer failed: %v", err),
				}
				onStatus(fmt.Sprintf("Explorer %d/%d failed: %s", idx+1, len(reqs), r.Focus))
				return
			}

			if o.costTracker != nil {
				o.costTracker.Record(cost.SessionCost{
					TaskID:    explorerCostTaskID,
					SessionID: result.SessionID,
					TokensIn:  result.TokensIn,
					TokensOut: result.TokensOut,
					CostUSD:   result.CostUSD,
				})
			}

			findings, _ := parseExplorerFindings(result.Output)
			findings.Query = r.Query
			results[idx] = *findings

			o.logger.Info("explorer completed", "index", idx, "focus", r.Focus, "files_read", len(findings.FilesRead))
			onStatus(fmt.Sprintf("Explorer %d/%d complete: %s", idx+1, len(reqs), r.Focus))
		}(i, req)
	}

	wg.Wait()
	return results
}

func (o *Orchestrator) handleBuildMessage(ctx context.Context, msg channel.IncomingMessage, model string) {
	o.logger.Info("handling build message", "sender", msg.SenderID, "text_len", len(msg.Text))

	cleanText := strings.Replace(msg.Text, "[system:build]", "", 1)
	planPath := strings.TrimSpace(cleanText)

	if o.orchAgent == nil {
		o.sendFollowUp(ctx, msg, "Build mode requires the orchestrator agent to be configured.")
		return
	}

	// Load the plan content
	var planContent string
	if o.planStore != nil && planPath != "" {
		plan, err := o.planStore.Load(planPath)
		if err != nil {
			o.logger.Warn("failed to load plan for build", "path", planPath, "error", err)
			o.sendFollowUp(ctx, msg, "Could not load the plan file. Please try again.")
			return
		}
		planContent = plan.Body
		_ = o.planStore.SetStatus(planPath, plans.StatusBuilding)
	} else if o.planStore != nil {
		// Fall back to latest draft
		draft, err := o.planStore.LatestDraft(o.projectSlug())
		if err != nil || draft == nil {
			o.sendFollowUp(ctx, msg, "No plan found to build. Create a plan first using Plan mode.")
			return
		}
		planContent = draft.Body
		planPath = draft.Path
		_ = o.planStore.SetStatus(draft.Path, plans.StatusBuilding)
	}

	onStream := func(delta string) {
		if o.channelMgr != nil {
			_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
				StreamDelta: delta,
				ThreadID:    msg.ThreadID,
			})
		}
	}

	// Build snapshot with plan injected
	var tasks []types.Task
	if o.tracker != nil {
		var err error
		tasks, err = o.tracker.ListActive(ctx)
		if err != nil {
			o.logger.Warn("build mode: failed to list tasks", "error", err)
		}
	}
	snap := o.buildStateSnapshot(tasks)
	snap.UserMessage = &UserMessageContext{Text: "Build the approved plan."}
	snap.SourceChannel = msg.ChannelKind
	snap.ActivePlan = &PlanContext{
		Path:    planPath,
		Content: planContent,
		Status:  string(plans.StatusBuilding),
	}

	actions, err := o.orchAgent.ConsultBuild(ctx, snap, model, onStream)
	o.recordOrchCost()

	if o.channelMgr != nil {
		_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
			StreamDone: true,
			ThreadID:   msg.ThreadID,
		})
	}

	if err != nil {
		o.logger.Warn("build consult failed", "error", err)
		o.sendFollowUp(ctx, msg, "Build failed. The orchestrator could not process the plan.")
		if o.planStore != nil && planPath != "" {
			_ = o.planStore.SetStatus(planPath, plans.StatusDraft)
		}
		return
	}

	// Execute all actions (create_subtasks, reply, display)
	o.executePlanBuildActions(ctx, msg, tasks, actions)

	// Mark plan as done
	if o.planStore != nil && planPath != "" {
		_ = o.planStore.SetStatus(planPath, plans.StatusDone)
	}

	o.recordAudit(ctx, "channel.plan_built", "", strPtr("build"))
}

// executePlanBuildActions handles actions from a build consult, which uses
// the same action types as HandleUserMessage (reply, display, create_subtasks).
func (o *Orchestrator) executePlanBuildActions(ctx context.Context, msg channel.IncomingMessage, tasks []types.Task, actions []Action) {
	for i := range actions {
		switch actions[i].Type {
		case ActionReply:
			if o.channelMgr != nil {
				o.sendFollowUp(ctx, msg, actions[i].Body)
			}
		case ActionDisplay:
			if o.channelMgr != nil {
				component := map[string]any{"kind": actions[i].DisplayKind}
				if actions[i].DisplayContent != "" {
					component["content"] = actions[i].DisplayContent
				}
				if actions[i].DisplayTitle != "" {
					component["title"] = actions[i].DisplayTitle
				}
				if actions[i].DisplayLanguage != "" {
					component["language"] = actions[i].DisplayLanguage
				}
				if actions[i].DisplayData != nil {
					if m, ok := actions[i].DisplayData.(map[string]any); ok {
						for k, v := range m {
							component[k] = v
						}
					} else {
						component["data"] = actions[i].DisplayData
					}
				}
				_ = o.channelMgr.Broadcast(ctx, channel.OutgoingMessage{
					Display:   component,
					DisplayID: newDisplayID(),
					ThreadID:  msg.ThreadID,
				})
			}
		default:
			// create_subtasks and others go through standard execution
		}
	}

	var otherActions []Action
	for _, a := range actions {
		if a.Type != ActionReply && a.Type != ActionDisplay {
			otherActions = append(otherActions, a)
		}
	}
	o.executeActions(ctx, tasks, otherActions)
}

// extractPlanTitle pulls the first H1 heading from plan markdown.
func extractPlanTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return ""
}

// extractPlanOverview returns the first non-empty paragraph after the H1 heading.
func extractPlanOverview(content string) string {
	lines := strings.Split(content, "\n")
	pastTitle := false
	var para []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !pastTitle {
			if strings.HasPrefix(trimmed, "# ") {
				pastTitle = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			break
		}
		if trimmed == "" {
			if len(para) > 0 {
				break
			}
			continue
		}
		para = append(para, trimmed)
	}
	return strings.Join(para, " ")
}

var taskHeadingRe = regexp.MustCompile(`^###\s+\d+[\.\)]\s*(.+)`)

// extractPlanTasks returns the titles of numbered ### task headings.
func extractPlanTasks(content string) []string {
	var tasks []string
	for _, line := range strings.Split(content, "\n") {
		if m := taskHeadingRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			tasks = append(tasks, m[1])
		}
	}
	return tasks
}

// buildPlanCard serializes plan metadata as a [plan-card] tagged JSON string
// for Prism to render as a Cursor-style card in the chat.
func buildPlanCard(planContent, planPath string) string {
	title := extractPlanTitle(planContent)
	if title == "" {
		title = "Plan"
	}
	card := map[string]any{
		"title":    title,
		"overview": extractPlanOverview(planContent),
		"tasks":    extractPlanTasks(planContent),
		"planPath": planPath,
	}
	data, _ := json.Marshal(card)
	return "[plan-card]" + string(data) + "[/plan-card]"
}
