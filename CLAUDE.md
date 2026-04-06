# Anthem -- Claude Code Context

## What Is Anthem

Anthem is an open-source agent orchestrator for Claude Code -- an alternative to OpenAI Symphony with a key differentiator: a hybrid architecture where a Go daemon handles mechanical reliability (polling, process management, workspace isolation, retry, state) and an AI orchestrator agent (Phase 3) sits on top for intelligence (user communication, task decomposition, parallel planning). The orchestrator agent uses VOICE.md for personality and learns the user over time. Executor agents are headless coding workers that get harnesses (WORKFLOW.md, skills, MCP tools, constraints), not personality.

## Plans and Architecture Docs

Read these documents thoroughly before writing any code:

- `docs/plans/architecture.md` -- Full system architecture with mermaid diagrams, all 15 components, Go interface definitions, data types, CLI spec, cross-platform details, event bus, rate limiting, hook failure handling
- `docs/plans/implementation.md` -- Scaffold structure, implementation order with specific steps, phase breakdown, dependency list, testing strategy

These are the source of truth for what to build and how.

## Design Decisions (Locked In -- Do Not Change)

- **Language**: Go (latest stable)
- **Module path**: `github.com/rauriemo/anthem`
- **Cross-platform**: Windows-first, all three OS from day 1. Use build tags for platform-specific process management (Job Objects on Windows, process groups on Unix). Everything else in Go is cross-platform by default.
- **Hybrid architecture**: Go daemon = mechanical reliability layer (polling, process management, workspace isolation, retry, state persistence). Orchestrator agent (Phase 3) = intelligence layer (Claude session with VOICE.md personality for user communication, task decomposition, parallel planning). The Go daemon exposes a tool interface for the orchestrator agent.
- **VOICE.md**: Global at `~/.anthem/VOICE.md`. Applies **only** to the orchestrator agent (Phase 3), NOT executor agents. Executors get harnesses (WORKFLOW.md template, skills, MCP tools, constraints) -- not personality. Voice gives the orchestrator personality for user communication and helps it learn user preferences for better task management.
- **Constraints**: Two-tier system:
  - **User-level**: `~/.anthem/constraints.yaml` -- global safety rules (e.g. "never force-push to main"). Loaded by the CLI, passed to the orchestrator.
  - **Project-level**: `system.constraints` list in WORKFLOW.md front matter -- project-specific rules (e.g. "run tests before opening a PR").
  - **Meta-constraint**: Anthem always appends a hardcoded constraint: "Do not modify constraint definitions in WORKFLOW.md system.constraints or ~/.anthem/constraints.yaml". This prevents agents from removing their own guardrails.
  - Both tiers are combined under a `## Constraints (non-negotiable)` header in the executor agent's prompt.
- **WORKFLOW.md location**: Per-project, typically `./WORKFLOW.md` in repo root
- **Global state root**: `~/.anthem/` for all global state (VOICE.md, constraints.yaml, state.json, voice-changelog.md, `plans/` directory for stored plan artifacts). Resolves via `os.UserHomeDir()` on all platforms.
- **GitHub auth**: `GITHUB_TOKEN` env var as primary, fallback to `gh auth token` CLI command. No custom credential storage -- no tokens in `~/.anthem/`.
- **Dashboard**: Fulfilled by Prism (separate repo, visual workstation with React frontend + FastAPI backend). No embedded dashboard needed in the Anthem binary.
- **Voice changelog**: Log all VOICE.md changes with reasons to `~/.anthem/voice-changelog.md`. Wired in Phase 3a via the `update_voice` contract action.
- **Testing**: Interface-based mocks (no mocking framework -- just simple structs satisfying interfaces), table-driven tests, `//go:build integration` tagged tests for external services, `testdata/` fixtures, CI from day 1
- **Logging**: Use `log/slog` (Go stdlib) for structured logging
- **Error handling**: Wrap errors with `fmt.Errorf("context: %w", err)`. Never swallow errors silently.
- **No global state in code**: Pass dependencies via constructor injection.
- **System guardrails**: The `system:` config block lives in WORKFLOW.md front matter (per-project policy). Project-level constraints are defined as a `constraints` list in the `system:` block. `require_approval_for_risky_actions: true` gates medium/high-risk contract actions with an audit trail.
- **Bootstrapping**: `~/.anthem/`, default VOICE.md, and default constraints.yaml are auto-created on first run. If VOICE.md is missing at runtime, warn and continue without personality. If constraints.yaml is missing, continue with empty user constraints.
- **Template engine**: Use sprig (`github.com/Masterminds/sprig/v3`) function map for WORKFLOW.md body rendering -- provides `lower`, `upper`, `replace`, `default`, `join`, etc.
- **EventBus**: `Publish` must be non-blocking. Buffered channels per subscriber, drop oldest on overflow. The orchestrator loop must never stall on slow observers.
- **Orchestrator module pattern**: All dispatch, reconciliation, and state logic lives in a single `orchestrator.go` file (matching Symphony's single-module pattern). No separate dispatch.go or reconciler.go files.
- **Orchestrator modes (Phase 3a+)**: The orchestrator supports three channel modes: **Plan** (iterative markdown planning, artifacts saved to `~/.anthem/plans/{project-slug}/`, no JSON actions), **Build** (reads approved plan, MUST emit `create_subtasks` to create GitHub issues for executor dispatch), and **Agent** (default — full JSON actions with plan context injected via `ActivePlan`/`PlanHistory` in StateSnapshot, can also edit code directly via Claude Code). The Go daemon validates and executes all actions. Falls back to Phase 2 mechanical dispatch on failure.
- **Contract-first tool surface (Phase 3a)**: Orchestrator-daemon communication uses a stable contract of explicitly defined actions with schemas, risk levels, and idempotency guarantees. Transport is JSON structured output now, MCP later. No read-actions -- the daemon pushes state via compact snapshots.
- **Three-layer state model (Phase 3a)**: (1) Event Log -- append-only SQLite audit log at `~/.anthem/audit.db`, operational events outside the repo. (2) State Snapshot -- compact in-memory view pushed to orchestrator each tick. (3) Knowledge Artifacts -- curated summaries in repo `docs/exec-plans/`. Operations log lives with the daemon; reasoning memory lives with the repo.
- **SQLite audit log (Phase 3a)**: `modernc.org/sqlite` (pure Go, no CGo) for the canonical audit log. Records dispatches, retries, cancellations, cost events, wave transitions, orchestrator actions, and voice updates.
- **Wave model (Phase 3a)**: Orchestrator plans tasks in waves. Wave boundary = "current planned frontier exhausted" (all tasks terminal or non-runnable). Daemon detects exhaustion and prompts orchestrator to replan.
- **Task lifecycle state machine (Phase 3a)**: Formalized states replacing the loose string enum: queued, planned, running, blocked, retryQueued, needsApproval, completed, failed, canceled, skipped. Explicit `Transition(from, to)` validation enforced by the daemon. `StatusToLabel()` / `LabelToStatus()` mapping layer between internal states and tracker labels. `Transition()` validates daemon-initiated changes only; external tracker changes (user moves kanban card) are reconciled directly.
- **Modular channel system (Phase 3b)**: Two-way communication between orchestrator and user via pluggable channel adapters. `Channel` interface (`Kind`, `Start`, `Send`, `Incoming`, `Close`) mirrors the `IssueTracker` adapter pattern. Global credentials in `~/.anthem/channels.yaml`, per-project channel targets in WORKFLOW.md `channels:` block. Two adapters shipped: Slack (Socket Mode, client-side -- Anthem connects out) and Dispatch (WebSocket server, server-side -- Anthem listens, Dispatch connects in). WhatsApp deferred to Phase 4.
- **Dispatch channel adapter**: WebSocket server adapter for the [Dispatch](https://github.com/rauriemo/dispatch) voice-first command channel. Anthem listens on a configured address; Dispatch connects and authenticates with a shared token. Protocol uses JSON text frames: auth handshake (`auth`/`auth_ok`/`auth_fail`), request-response chat correlated by UUID (`req`/`res` with `id` field), and server-push events (`event` with event type). `OutgoingMessage` carries an `EventType` field (set by EventBridge) so the adapter can include the event type in push frames. Multiple concurrent Dispatch clients supported; thread-to-connection mapping routes replies to the correct client.
- **Multi-format task decomposition (Phase 3b)**: User sends feature descriptions through channels as plain text prompts, markdown files, mermaid flowcharts, diagrams, or images. The orchestrator agent decomposes into GitHub issues via the `create_subtasks` contract action. Claude's multimodal capabilities handle image-based inputs.
- **Audit-log maintenance signals (Phase 3b)**: Periodic scanner queries `audit.db` for health signals (repeated failures, stale tasks, budget anomalies, drift). Notifies user via channel with approval gate. Configurable auto-approve per maintenance type in WORKFLOW.md `maintenance:` block.
- **DAG edges inside waves (Phase 4)**: Tasks declare `depends_on` using **1-based ordinal numbers** (e.g. `[1, 2]` meaning "depends on subtask #1 and #2 in this batch") in `create_subtasks`. The daemon remaps ordinals to real GitHub issue IDs after creation and enforces ordering — tasks with unmet deps are not dispatched. The first configured active label (e.g. `todo`) is auto-added to newly created subtasks if missing. Waves remain as planning horizons; edges add ordering within them. Stored in `state.json` alongside retry state.
- **Unified lean path (Phase 4)**: `handleLeanMessage` routes through the `AgentRunner` driver (same as executors) with `MaxTurns: 1`. No orchestrator session, no snapshot, no JSON contract. Costs tracked under synthetic task ID `__lean__`. Enables multi-LLM support and observability for the fast path.
- **Executor-reviewer loop (Phase 4)**: After executor completion, an optional reviewer agent checks the output. If it flags issues, the task re-enters the retry queue with feedback. Opt-in via `review_enabled: true` in WORKFLOW.md `agent:` block. `review_max_turns` (default 3) gives the reviewer enough turns to read files and verify. `review_max_retries` (default 1) prevents infinite loops.
- **Specialist agent profiles (Phase 4)**: Named profiles (architect, coder, tester, debugger) with different prompt templates, tool configs, and model settings. The orchestrator selects a profile when dispatching via `profile` field on the `dispatch` action. Reviewer-failed retries automatically use the `debugger` profile.
- **Decision trace system (Phase 4)**: `traces` table in `audit.db` captures every LLM interaction (orchestrator consults, executor runs, reviewer judgments, lean messages) with prompt/response previews, token counts, cost, duration, and linked task/wave/session IDs. Query methods enable post-hoc debugging of agent decisions.
- **Orchestrator codebase awareness (Phase 4)**: The orchestrator agent uses Claude Code's built-in tools (Read, Grep, Glob) during planning instead of working blind from static docs. The prompt role changes from "stateless allocator" to "intelligent orchestrator with codebase access." `orchestrator.max_turns` config (default 10) controls exploration depth.

## Coding Standards

- No unnecessary comments. Don't narrate what code does. Only comment non-obvious intent, trade-offs, or constraints.
- Table-driven tests for all unit tests.
- Every external dependency (GitHub API, Claude Code CLI, filesystem) is behind an interface.
- Wrap errors with context: `fmt.Errorf("loading config: %w", err)`
- Use `log/slog` for all logging.
- No global mutable state -- dependency injection via constructors.

## Current Status

**Phase**: Phase 4 — Frontier Implementation (**complete**)
**Working plan**: `docs/plans/frontier-implementation.md` — completed checklist with detailed steps and file paths.
**Scaffold**: Complete (all 11 steps done)
**Phase 1**: Complete. All 10 steps implemented and verified with a live GitHub issue (pickup -> Claude Code execution -> issue closure -> label lifecycle).

**Post-Phase 1 work completed**:
- ETag-based conditional requests for `ListActive` (304 caching).
- Rate limit throttling (`ShouldThrottle()` on `GitHubTracker`).
- Auto-bootstrap in `anthem run`.
- Two-tier constraints system (`~/.anthem/constraints.yaml` + `system.constraints` in WORKFLOW.md), meta-constraint protection.
- Deleted vestigial `[CORE]` code from voice module (`core.go` removed).
- Quality audit fixes: errcheck, gofmt, unused fields, ETag mutex, nil guards, strings.Builder optimization.
- Agent permission model documented (architecture.md section 8b).
- Deleted empty stub files `dispatch.go` and `reconciler.go` from orchestrator (real logic in `orchestrator.go`).
- CI lint fix: `golangci-lint` built from source for Go 1.26 compatibility.

**Phase 2 completed** (Go Daemon Reliability Layer):
- Rules engine: TitlePattern regex matching with compiled cache, AutoAssign comment posting, MaxCost budget enforcement with `exceeded-budget` label and cost tracker integration.
- Workspace manager: production implementation replacing mock, per-task directories, hook lifecycle (after_create fails immediately, before_run retries 3x, after_complete warn-only), cross-platform shell execution, CleanupTerminal for startup cleanup.
- Retry and backoff: per-task RetryInfo, exponential backoff (10s * 2^(n-1) capped at max_retry_backoff_ms), 1s continuation delay, stall detection in reconcile (2x stall timeout).
- Graceful shutdown: WaitGroup drain (10s timeout), claim release with fresh context, state save before exit.
- State persistence: atomic write to `~/.anthem/state.json`, versioned schema, LoadAndReconcile on startup (restores retry queue skipping terminal tasks, restores cost sessions).
- Config hot-reload: fsnotify watcher on directory (catches editor delete+create), 100ms debounce, validates before applying, configSnapshot pattern for dispatch goroutines.
- New files: `internal/orchestrator/retry.go`, `internal/orchestrator/state_test.go`, `internal/config/watcher_test.go`.
- New dependency: `github.com/fsnotify/fsnotify`.

**Phase 3a completed** (Contract + Audit + Orchestrator Core):
- Tool contract: 10 action types (dispatch, skip, comment, update_voice, request_approval, close_wave, create_subtasks, promote_knowledge, reply, request_maintenance) with risk classification (low/medium/high), validation, idempotency flags. All actions fully implemented.
- SQLite audit log: append-only event log at `~/.anthem/audit.db` via `modernc.org/sqlite` (pure Go, no CGo). AuditLogger interface with Record, Query, RecentByTask, SummaryForWave. WAL mode, mutex-serialized writes, busy timeout. Injected into Orchestrator, closed on shutdown.
- Task lifecycle state machine: 10 formalized states (queued, planned, running, blocked, retryQueued, needsApproval, completed, failed, canceled, skipped) replacing StatusActive/StatusPending. Transition(from, to) validation, StatusToLabel/LabelToStatus mapping, TerminalReason field on Task.
- Executor prompt fix: removed VOICE.md from buildFullPrompt -- executors get constraints + WORKFLOW.md only. Voice is orchestrator-only.
- Driver permission fixes: replaced hardcoded --dangerously-skip-permissions with config-driven PermissionMode (default dontAsk). Added DeniedTools to RunOpts. Added ContinueOpts to Continue() with workspace, stall timeout, allowed tools, permission mode. Fixed RunResult.Output population from stream event's result text (string or content block array). Added PermissionMode/SkipPermissions/DeniedTools to AgentConfig.
- Orchestrator session manager: OrchestratorAgent with Start/Consult/Refresh. Builds system prompt with voice + action schema + wave model. Receives compact StateSnapshot, returns structured JSON actions. parseActions with brace-counting JSON extraction. ConsultWithRepair sends repair prompt on parse failure, falls back to nil (triggers mechanical dispatch). Token tracking for session refresh threshold.
- Wave-aware tick loop: dirty-snapshot gating (SHA256 hash of task IDs+statuses+wave, skip consult on unchanged state). Wave tracking with frontier exhaustion detection. Fallback to Phase 2 mechanical dispatch when orchestrator is nil, disabled, or fails. executeActions validates each action against contract, dispatches tasks, updates tracker, records audit events. OrchestratorConfig (enabled, max_context_tokens, stall_timeout_ms) in config.go. main.go wiring creates audit logger + orchestrator agent + passes to Opts.
- Voice self-evolution: update_voice contract action triggers voice.LoadFile -> voice.Merge -> write -> voice.AppendChangelog -> audit event. Updates in-memory voiceContent on both Orchestrator and OrchestratorAgent.
- New files: `internal/orchestrator/contract.go`, `internal/orchestrator/orchagent.go`, `internal/orchestrator/integration_test.go`, `internal/orchestrator/voice_test.go`, `internal/audit/audit.go`, `internal/audit/schema.go`, `internal/audit/audit_test.go`.
- New dependency: `modernc.org/sqlite`.

**Phase 3b completed** (Channels + Task Decomposition + Maintenance):
- Channel system: `Channel` interface (Kind, Start, Send, Incoming, Close), `IncomingMessage`/`OutgoingMessage`/`File` types. `Manager` with Register, Start, Broadcast, Incoming, Close — mutex-protected channel slice, buffered merged incoming channel (size 64), per-channel fan-in goroutines. `EventBridge` subscriber routes internal EventBus events to external channels with configurable event type filter and `FormatEvent` for human-readable markdown.
- Channel config: global credentials in `~/.anthem/channels.yaml` (`ChannelsConfig` with optional `SlackCredentials`), per-project targets in WORKFLOW.md `channels:` block (`ChannelTargetConfig` with kind, target, events). `LoadCredentials` returns nil for missing file (channels are optional).
- Slack adapter: Socket Mode via `github.com/slack-go/slack` (pure WebSocket, no HTTP server). Handles `EventsAPIEvent` message callbacks, filters by channel ID, ignores bot messages and subtypes. Downloads file attachments with Authorization header, 10MB cap. Outbound via `PostMessageContext` with thread support.
- Dispatch adapter (post-3b): WebSocket server at `internal/channel/dispatch/adapter.go` using `gorilla/websocket`. Anthem listens on configured address, Dispatch clients connect and authenticate via shared token. JSON text frame protocol: `{"type":"auth","token":"..."}` -> `{"type":"auth_ok"}`, `{"type":"req","id":"<uuid>","text":"..."}` -> `IncomingMessage{ThreadID: id}`, `Send()` with ThreadID routes `{"type":"res","id":"...","text":"..."}` to correct connection, `Send()` without ThreadID broadcasts `{"type":"event","event":"<type>","text":"..."}` to all. Thread-to-connection mapping with cleanup on disconnect. `OutgoingMessage.EventType` field added (set by EventBridge, used by Dispatch adapter for event frames, ignored by Slack adapter).
- New contract actions: `ActionReply` (channel response, low risk, idempotent) requires body. `ActionRequestMaintenance` (maintenance proposal, medium risk) requires maintenance_type and reason, optional auto_approvable. Both added to allActionTypes, RiskForAction, ValidateAction, IsIdempotent.
- `create_subtasks` implementation: removed from SchemaOnly. `CreateIssue(ctx, title, body, labels) (string, error)` added to `IssueTracker` interface, implemented in `GitHubTracker` (Issues.Create), `LocalJSONTracker` (append to JSON), and `MockTracker`. `executeActions` case creates issues for each SubtaskDef, records audit event.
- HandleUserMessage on Orchestrator: fetches tasks, builds StateSnapshot with `UserMessageContext` (text + file contents as strings for text/JSON/YAML, `[image: name]` placeholders for images, `[file: name, type: mime]` for binary, 50KB truncation). Consults orchestrator agent, sends reply actions with thread ID via channel manager, executes non-reply actions. Error replies on failure. `StartChannelListener(ctx)` goroutine reads from channel manager's incoming.
- Maintenance scanner: `Scanner` in `internal/maintenance/` queries audit log periodically (configurable `scan_interval_ms`). Four signal types: `repeated_failure` (>= failure_threshold in 24h), `stale_task` (dispatched > stale_threshold_hours with no completion), `budget_anomaly` (cost > cost_anomaly_multiplier * average), `drift` (completed then re-dispatched). AutoApprove per signal kind from config. Publishes `maintenance.suggested` events.
- Extended orchestrator system prompt: added reply and request_maintenance action descriptions, Channel Messages section (intent understanding, feature decomposition, command execution, status queries, plan/maintenance approval), Multi-Format Input section (text, markdown, mermaid, ASCII, images, mixed).
- Maintenance config: `MaintenanceConfig` with scan_interval_ms (default 600000), failure_threshold (default 3), stale_threshold_hours (default 24), cost_anomaly_multiplier (default 2.0), auto_approve list.
- main.go wiring: loads channel credentials, creates Channel Manager, registers Slack adapters from config, starts channel manager + EventBridge + maintenance scanner + channel listener. All with graceful shutdown via deferred Close.
- Project context enrichment: orchestrator agent now receives the project file tree, CLAUDE.md, architecture.md, and implementation.md in every StateSnapshot via the ProjectContext struct. Loaded once at startup and on config hot-reload. File tree generated by walking workspace root with depth limit and exclusion patterns. Doc contents truncated at 8KB. This gives the orchestrator the same codebase awareness as executor agents for informed task decomposition.
- New files: `internal/channel/channel.go`, `internal/channel/manager.go`, `internal/channel/config.go`, `internal/channel/bridge.go`, `internal/channel/slack/adapter.go`, `internal/channel/dispatch/adapter.go`, `internal/maintenance/scanner.go`, `internal/orchestrator/context_test.go`, plus test files.
- New dependencies: `github.com/slack-go/slack`, `github.com/gorilla/websocket` (promoted from indirect to direct for Dispatch adapter).

**Phase C completed** (Prism LLM Token Streaming):
- Callback-based streaming: `OnStream func(delta string)` added to `RunOpts` and `ContinueOpts`. Claude driver calls it for each `assistant` stream event's content. Optional (nil = no streaming), no interface change.
- Driver integration: `parseStdout` handles `assistant` events before the `result` check, forwarding content deltas via the callback. `Continue()` passes `OnStream` through to `execute()`.
- Channel types: `OutgoingMessage` extended with `StreamDelta` (string) and `StreamDone` (bool) fields for stream frame routing.
- Prism adapter: new `stream` frame type (`{"type":"stream","text":"...","thread":"...","done":bool}`). `sendStream()` method routes to thread connection or broadcasts. `Send()` checks for stream messages before existing display/text logic. `frame` struct extended with `Done` field.
- Other adapters: Dispatch and Slack `Send()` methods early-return nil for stream messages (no streaming support needed).
- Orchestrator streaming: `StartStreaming`, `ConsultStreaming`, `ConsultWithRepairStreaming` methods on `OrchestratorAgent` mirror non-streaming variants but accept `onStream` callback and pass through `RunOpts`/`ContinueOpts`. Existing non-streaming methods unchanged (used by `tick()`).
- HandleUserMessage: replaced `ConsultWithRepair` with `ConsultWithRepairStreaming`, broadcasting `StreamDelta` messages during consult and `StreamDone` after completion. Final `res`/`display` frames still sent as before for chat history.
- Tests: driver streaming callback test, Prism stream frame format test, orchestrator agent streaming passthrough test, HandleUserMessage streaming integration test.

**Phase 4 completed** (Frontier Implementation):
- Audit log gaps: `task.completed`/`task.failed` now recorded to DB with `CostUSD`, `WaveSpentUSD` and `RecentEvents` populated in snapshot, SQL event type mismatch fixed in `SummaryForWave`.
- Dead config cleanup: `require_plan` enforced in `mechanicalDispatch` (skip + `needs-plan` label + `task.needs_plan` event), `workflow_changes_require_approval` removed (never implemented), `linear` tracker kind removed, `RiskForAction` wired into `executeActions` with `require_approval_for_risky_actions` config flag and audit trail.
- Multi-LLM driver abstraction: configurable `binary` field on Driver (default "claude"), `NewDriver(pm, logger, binary)`, `agent.command` config wired through.
- DAG edges inside waves: `DependsOn` on `SubtaskDef`/`TaskSummary`, `taskDeps` map persisted in `state.json`, dispatch ordering enforced in `mechanicalDispatch` and `executeActions`, `isWaveExhausted` ignores dep-blocked tasks, prompt updated.
- Unified lean path: `handleLeanMessage` routes through `AgentRunner.Run` with `MaxTurns: 1`, cost tracked under `__lean__`, raw `exec.CommandContext` removed.
- promote_knowledge: file write to `docs/exec-plans/<date>-<summary>.md`, `loadKnowledge` scans recent 5 files (8KB cap), `ProjectContext.Knowledge` field, prompt guidance.
- Executor-reviewer loop: `runReview` with configurable `ReviewMaxTurns` (default 3), JSON `{passed, feedback}` response, retry with `debugger` profile on failure, `review-skipped` label on max retries, `Previous Attempt Feedback` in retry prompt, costs under same task ID.
- Decision trace system: `traces` table in audit DB (17 columns, 4 indexes), `RecordTrace`/`TracesForTask`/`TracesForWave`/`RecentTraces`/`GetTraceStats`, traces recorded for orchestrator/executor/reviewer/lean.
- Specialist agent profiles: `AgentProfile` struct (7 fields), 4 default profiles (coder/architect/tester/debugger), `profile` field on dispatch action, profile merge in dispatch (prompt prefix/suffix, tools, model, turns), debugger auto-select on review failure, WORKFLOW.md template example.
- Orchestrator codebase awareness: role updated to "intelligent orchestrator with codebase access", built-in tools (Read/Grep/Glob) during planning, `OrchestratorMaxTurns` (default 10), `warnIfHighTokens` on all consult paths, `parseActions` handles tool-use interleaved output.

**Phase 5 (future)**: WhatsApp channel adapter, GoReleaser binaries, code signing, demo video.

**Post-Phase 4 work completed** (Plan Modes, Model Selection, Label Automation):
- Plan/build/agent channel modes: `[system:plan]` routes to markdown planning (saved to `~/.anthem/plans/`), `[system:build]` routes to subtask creation from approved plan, default routes to full agent mode with plan context injected.
- Model selection: `[model:xxx]` tag parsed from messages and threaded to all execution paths (lean, plan, build, agent). Supports all Claude model variants.
- Auto-label on subtask creation: first configured active label (e.g. `todo`) auto-added to newly created issues when not already present. Ensures immediate kanban/dispatch visibility.
- DependsOn ordinal remapping: `create_subtasks` uses 1-based ordinals in `depends_on`; daemon remaps to real GitHub issue IDs in a two-pass creation flow.
- Strengthened build-mode prompt: explicitly requires `create_subtasks` action emission, forbids completion hallucination.
- Plan context injection in agent mode: `ActivePlan` (latest draft) and `PlanHistory` (all plans) injected into StateSnapshot for seamless plan-to-agent handoff.

Update this section as phases are completed.

## Reference: Prism Channel Protocol

Prism uses the same WebSocket JSON frame protocol as Dispatch, extended with:
- `display` frames: `{"type":"display","component":{...},"thread":"<id>"}` for A2UI structured visual content
- `stream` frames (Phase C): `{"type":"stream","text":"<delta>","thread":"<id>","done":false}` for incremental LLM output
- When `done: true`, Prism knows the stream is complete and can finalize the message

The Prism adapter at `internal/channel/prism/adapter.go` handles `Send()` for `res`, `event`, and `display` frame types. Phase C adds `stream` frame support.

## Reference: OpenAI Symphony

When making implementation decisions, reference Symphony's codebase for proven patterns:
- Repository: https://github.com/openai/symphony
- Language: Elixir (GenServer-based orchestrator)
- Spec: `SPEC.md` in repo root
- Key patterns: single-module orchestrator, tracker adapters, workspace isolation, config parsing from markdown front matter
- Key difference: Symphony has no personality/voice concept. Anthem adds the orchestrator agent layer on top (Phase 3).
