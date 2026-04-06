# Frontier Implementation Plan

Anthem's competitive gap analysis identified 11 improvements across three tiers. This document is the working checklist. Mark items `[x]` as they are completed and run tests after each tier.

## Decisions (locked in)

- **Multi-LLM**: Clean driver abstraction only. No second driver yet, but the interface is properly separated so one can be added later.
- **DAG planning**: Add `DependsOn` edges inside waves. Backward-compatible -- waves without edges keep working. Matches CC Mirror's approach.
- **Lean path**: Route through the `AgentRunner` driver abstraction with minimal context. Same speed, unified observability and cost tracking.
- **Memory**: Implement `promote_knowledge` action. Zero runtime overhead, improves orchestrator planning quality over time.
- **Reviewer loop**: Add executor -> reviewer -> retry loop for code quality. Lightweight single-turn review agent checks output before marking complete.
- **Orchestrator awareness**: Let the orchestrator use Claude Code's built-in tools (Read, Grep, Glob) during planning instead of planning blind from static docs.
- **Agent profiles**: Dispatch with different prompt templates, tool configs, and models per task type (architect/coder/tester/debugger).
- **Decision traces**: Capture every LLM request/response pair in a queryable trace table for post-hoc analysis.

## Tier 1: Fix Broken Internals

- [x] **1a. Fix audit log gaps**
  - Files: `internal/orchestrator/orchestrator.go`, `internal/audit/audit.go`
  - `task.completed` and `task.failed` are published to EventBus but never written to audit DB. Add `recordAudit` calls in the `dispatch` goroutine's completion/failure paths (around lines where `o.events.Publish` is called for these events).
  - `recordAudit` never sets `CostUSD` on rows. Pass `costTracker.TaskCost(taskID)` into audit records for cost-bearing events.
  - Populate `WaveSpentUSD` in `buildStateSnapshot` by summing costs of frontier task IDs.
  - Populate `RecentEvents` in `buildStateSnapshot` by querying the last N audit events (e.g., 10).
  - Update tests.

- [x] **1b. Fix SQL event type mismatch**
  - File: `internal/audit/audit.go`
  - `SummaryForWave` queries `event_type IN ('dispatch','completed','failed')` but live code records `task.dispatched`, `task.completed`, `task.failed`. Update the SQL to match the actual event type strings.
  - Update tests.

- [x] **1c. Remove or implement dead config/code**
  - Files: `internal/orchestrator/orchestrator.go`, `internal/config/config.go`, `internal/config/validator.go`, `internal/orchestrator/contract.go`, `internal/rules/engine.go`, `cmd/anthem/main.go`
  - `require_plan` rule action: validated in config but no enforcement branch in `mechanicalDispatch`. Add enforcement: skip dispatch if no plan comment on issue, add `needs-plan` label, publish `task.needs_plan` event.
  - `workflow_changes_require_approval`: config field exists, never referenced. Remove from config struct and validator.
  - `linear` tracker kind: validated but `createTracker` in `main.go` only handles `github` and `local_json`. Remove `linear` from validator (can re-add when implemented).
  - `RiskForAction`: defined and tested but never gates anything at runtime. Wire into `executeActions`: log a warning for high-risk actions, skip medium/high-risk actions if a `system.require_approval_for_risky_actions` config flag is set (default false for backward compat).
  - Update tests.

## Tier 2: Architectural Upgrades

- [x] **2a. Clean multi-LLM driver abstraction**
  - Files: `internal/agent/claude/driver.go`, `cmd/anthem/main.go`, `internal/config/config.go`
  - Extract the hard-coded `"claude"` binary name in `driver.go` `execute()` into a configurable `binary` field on `Driver`. `NewDriver(pm, logger, binary)` defaults to `"claude"` if empty.
  - Wire `cfg.Agent.Command` from config into `claude.NewDriver(pm, logger, cfg.Agent.Command)` in `main.go`.
  - Document in config.go that `agent.command` now drives the main driver binary, not just the lean path.
  - Update driver tests.

- [x] **2b. DAG edges inside waves**
  - Files: `internal/orchestrator/contract.go`, `internal/orchestrator/orchagent.go`, `internal/orchestrator/orchestrator.go`, `internal/orchestrator/state.go`
  - Add `DependsOn []string json:"depends_on,omitempty"` to `SubtaskDef` in `contract.go`.
  - Add `DependsOn []string json:"depends_on,omitempty"` to `TaskSummary` in `orchagent.go`.
  - Add `taskDeps map[string][]string` to `Orchestrator` struct. When `executeActions` processes `ActionCreateSubtasks`, store each subtask's `DependsOn` list keyed by the newly created issue ID.
  - Persist `taskDeps` in `state.json` via `state.go` (add `TaskDeps` field to `PersistentState`). Restore on `LoadAndReconcile`.
  - In `buildStateSnapshot`, populate `TaskSummary.DependsOn` from `taskDeps`.
  - In `mechanicalDispatch`, before dispatching a task, check that all IDs in `taskDeps[task.ID]` have terminal status. Skip (with debug log) if any dep is non-terminal.
  - Same check in `executeActions` for `ActionDispatch`.
  - Update `isWaveExhausted`: tasks with unmet deps that are waiting on in-progress deps should NOT prevent wave exhaustion.
  - In `buildSystemPrompt` in `orchagent.go`, add instruction that `create_subtasks` accepts `depends_on: ["task-id"]` for execution ordering.
  - Update tests (contract validation, dispatch ordering, wave exhaustion with deps).

- [x] **2c. Unified lean path through driver**
  - Files: `internal/orchestrator/orchestrator.go`
  - Rewrite `handleLeanMessage` to use `o.runner.Run(ctx, types.RunOpts{...})` instead of raw `exec.CommandContext`.
  - Extract existing prompt construction into a `buildLeanPrompt(projectCtx, msg)` helper.
  - Use `MaxTurns: 1` (single inference, no tool use), `PermissionMode: "bypassPermissions"`.
  - Feed `result.CostUSD`, `result.TokensIn`, `result.TokensOut` into `costTracker.Record` under synthetic task ID `__lean__`.
  - Use `result.Output` with `extractLeanDisplayBlocks` as before.
  - Use `OnStream` callback for streaming deltas to channels.
  - `recordAudit` with `channel.lean_message` event now includes cost data.
  - Remove the raw `exec.CommandContext` code path entirely.
  - Update lean_test.go.

- [ ] **2d. Implement promote_knowledge**
  - Files: `internal/orchestrator/orchestrator.go`, `internal/orchestrator/orchagent.go`, `internal/orchestrator/contract.go`
  - In `contract.go`: remove `promote_knowledge` from any `SchemaOnly` check so `ValidateAction` passes it.
  - In `executeActions` switch: replace the `ActionPromoteKnowledge` not-implemented log with actual file write to `docs/exec-plans/<date>-<sanitized-summary>.md`.
  - In `loadProjectContext` in `orchestrator.go`: after loading the three existing docs, scan `docs/exec-plans/*.md`, concatenate the most recent 5 files (capped at 8KB total) into `ProjectContext.Knowledge`.
  - In `orchagent.go`: add `Knowledge string` to `ProjectContext`. In `buildSystemPrompt`, include `## Past Run Summaries` section if non-empty.
  - Update orchestrator prompt to describe when to use `promote_knowledge` (after architectural discoveries, recurring patterns, solved edge cases).
  - Update tests.

- [ ] **2e. Executor-reviewer agent loop**
  - Files: `internal/orchestrator/orchestrator.go`, `internal/config/config.go`
  - Add to `AgentConfig`: `ReviewEnabled bool`, `ReviewMaxTurns int` (default 1), `ReviewPrompt string`, `ReviewMaxRetries int` (default 1).
  - In `dispatch` goroutine, after executor `Run` succeeds (ExitCode == 0) and before marking complete: if `ReviewEnabled`, call `o.runReview(ctx, task, result)`.
  - Implement `runReview`: build review prompt with task title/body + executor output summary (last 4KB of `result.Output`). Run via `o.runner.Run` with `MaxTurns: 1`, `PermissionMode: "bypassPermissions"`. Parse JSON response `{"passed": true/false, "feedback": "..."}`. Default to passed if parse fails.
  - If review fails: `recordFailure` with feedback as error, publish `task.review_failed`. Track `reviewRetries` count. If count > `ReviewMaxRetries`, complete anyway with `review-skipped` label.
  - If review passes: `recordAudit` with `task.review_passed`.
  - In `buildFullPrompt`: if task is a retry with reviewer feedback in `lastError`, append `## Previous Attempt Feedback` section.
  - Record reviewer costs under the task's own cost (add to same task ID in costTracker).
  - Update tests.

## Tier 3: High-Value Competitive Features

- [ ] **3a. Orchestrator codebase awareness**
  - Files: `internal/orchestrator/orchagent.go`, `internal/config/config.go`
  - Update `buildSystemPrompt` role section: change from "stateless allocator" to "intelligent task orchestrator with codebase access." Instruct it to use built-in tools (Read, Grep, Glob) to explore before planning, then return JSON actions.
  - Add `OrchestratorMaxTurns int` to `OrchestratorConfig` (default 10). Wire into `RunOpts.MaxTurns` for orchestrator consults.
  - Verify that `parseActions` still works when the orchestrator uses tools (the Claude driver's `parseStdout` returns final `result.Output` text, not raw stream). Add a test with tool-use interleaved output.
  - Log a warning if a single orchestrator consult exceeds 20K tokens.
  - Update tests.

- [ ] **3b. Specialist agent profiles**
  - Files: `internal/config/config.go`, `internal/orchestrator/contract.go`, `internal/orchestrator/orchestrator.go`, `internal/orchestrator/orchagent.go`
  - Add `AgentProfile` struct: `PromptPrefix`, `PromptSuffix`, `AllowedTools`, `DeniedTools`, `Model`, `MaxTurns`, `ReviewEnabled`.
  - Add `Profiles map[string]AgentProfile` to `AgentConfig`. Bake default profiles into `DefaultConfig()`: `coder` (default), `architect` (read-only tools), `tester` (test-focused prompt), `debugger` (fix-focused prompt).
  - Add `Profile string json:"profile,omitempty"` to `Action` in `contract.go`.
  - In `dispatch`: resolve profile from `action.Profile` (fallback to "coder"). Merge profile settings with base config (prepend PromptPrefix, append PromptSuffix, override tools/model/turns if specified).
  - In `buildSystemPrompt`: tell orchestrator about available profiles and when to use each.
  - In reviewer loop (2e): when review fails, retry dispatch uses `debugger` profile.
  - Add profile config example to default WORKFLOW.md template.
  - Update tests.

- [ ] **3c. Full decision trace system**
  - Files: `internal/audit/schema.go`, `internal/audit/audit.go`, `internal/orchestrator/orchestrator.go`
  - Add `traces` table to audit DB schema with columns: id, timestamp, trace_type, task_id, session_id, wave_id, prompt_hash, prompt_preview (500 chars), response_preview (500 chars), tokens_in, tokens_out, cost_usd, duration_ms, actions_json, reasoning, review_passed, review_feedback, metadata. Indexes on task_id, wave_id, trace_type, timestamp.
  - Add `TraceRecord` struct and `RecordTrace(trace TraceRecord) error` method to audit logger.
  - After every orchestrator consult: record trace with type "orchestrator".
  - After every executor dispatch completion: record trace with type "executor".
  - After every reviewer run: record trace with type "reviewer".
  - After every lean message: record trace with type "lean".
  - Add query methods: `TracesForTask`, `TracesForWave`, `RecentTraces`, `TraceStats`.
  - Update tests.

## Execution Order

1a -> 1b -> 1c -> 2a -> 2c -> 2b -> 2d -> 2e -> 3c -> 3b -> 3a

Tier 1 is sequential (1b depends on 1a audit fixes). Tier 2: 2a first (driver abstraction), then 2c (lean path needs driver), then 2b and 2d (independent), then 2e (reviewer needs driver). Tier 3: 3c first (traces provide visibility), then 3b (profiles are simpler), then 3a (orchestrator awareness is the capstone).

## Testing

Run `go test ./...` after each completed task. Run `go vet ./...` and `golangci-lint run` after each tier. All new features must have table-driven tests using existing mock patterns (MockRunner, MockEventBus, MockTracker, MockWorkspaceManager).
