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
- **Global state root**: `~/.anthem/` for all global state (VOICE.md, constraints.yaml, state.json, voice-changelog.md). Resolves via `os.UserHomeDir()` on all platforms.
- **GitHub auth**: `GITHUB_TOKEN` env var as primary, fallback to `gh auth token` CLI command. No custom credential storage -- no tokens in `~/.anthem/`.
- **Dashboard**: Deferred to Phase 4 (tech choice TBD between HTMX and SPA)
- **Voice changelog**: Log all VOICE.md changes with reasons to `~/.anthem/voice-changelog.md`. Wired in Phase 3a via the `update_voice` contract action.
- **Testing**: Interface-based mocks (no mocking framework -- just simple structs satisfying interfaces), table-driven tests, `//go:build integration` tagged tests for external services, `testdata/` fixtures, CI from day 1
- **Logging**: Use `log/slog` (Go stdlib) for structured logging
- **Error handling**: Wrap errors with `fmt.Errorf("context: %w", err)`. Never swallow errors silently.
- **No global state in code**: Pass dependencies via constructor injection.
- **System guardrails**: The `system:` config block lives in WORKFLOW.md front matter (per-project policy). Safe defaults: `workflow_changes_require_approval: true`. Project-level constraints are defined as a `constraints` list in the `system:` block.
- **Bootstrapping**: `~/.anthem/`, default VOICE.md, and default constraints.yaml are auto-created on first run. If VOICE.md is missing at runtime, warn and continue without personality. If constraints.yaml is missing, continue with empty user constraints.
- **Template engine**: Use sprig (`github.com/Masterminds/sprig/v3`) function map for WORKFLOW.md body rendering -- provides `lower`, `upper`, `replace`, `default`, `join`, etc.
- **EventBus**: `Publish` must be non-blocking. Buffered channels per subscriber, drop oldest on overflow. The orchestrator loop must never stall on slow observers.
- **Orchestrator module pattern**: All dispatch, reconciliation, and state logic lives in a single `orchestrator.go` file (matching Symphony's single-module pattern). No separate dispatch.go or reconciler.go files.
- **Orchestrator-as-allocator (Phase 3a)**: The orchestrator agent is a stateless allocator -- it proposes actions, the Go daemon validates and executes them. The daemon is the authority. If the orchestrator session fails, the daemon falls back to Phase 2 mechanical dispatch. No reliance on long-running context windows.
- **Contract-first tool surface (Phase 3a)**: Orchestrator-daemon communication uses a stable contract of explicitly defined actions with schemas, risk levels, and idempotency guarantees. Transport is JSON structured output now, MCP later. No read-actions -- the daemon pushes state via compact snapshots.
- **Three-layer state model (Phase 3a)**: (1) Event Log -- append-only SQLite audit log at `~/.anthem/audit.db`, operational events outside the repo. (2) State Snapshot -- compact in-memory view pushed to orchestrator each tick. (3) Knowledge Artifacts -- curated summaries in repo `docs/exec-plans/`. Operations log lives with the daemon; reasoning memory lives with the repo.
- **SQLite audit log (Phase 3a)**: `modernc.org/sqlite` (pure Go, no CGo) for the canonical audit log. Records dispatches, retries, cancellations, cost events, wave transitions, orchestrator actions, and voice updates.
- **Wave model (Phase 3a)**: Orchestrator plans tasks in waves. Wave boundary = "current planned frontier exhausted" (all tasks terminal or non-runnable). Daemon detects exhaustion and prompts orchestrator to replan.
- **Task lifecycle state machine (Phase 3a)**: Formalized states replacing the loose string enum: queued, planned, running, blocked, retryQueued, needsApproval, completed, failed, canceled, skipped. Explicit `Transition(from, to)` validation enforced by the daemon. `StatusToLabel()` / `LabelToStatus()` mapping layer between internal states and tracker labels. `Transition()` validates daemon-initiated changes only; external tracker changes (user moves kanban card) are reconciled directly.
- **Modular channel system (Phase 3b)**: Two-way communication between orchestrator and user via pluggable channel adapters. `Channel` interface (`Kind`, `Start`, `Send`, `Incoming`, `Close`) mirrors the `IssueTracker` adapter pattern. Global credentials in `~/.anthem/channels.yaml`, per-project channel targets in WORKFLOW.md `channels:` block. Slack (Socket Mode) ships first; WhatsApp deferred to Phase 4 (needs dashboard HTTP server for webhooks).
- **Multi-format task decomposition (Phase 3b)**: User sends feature descriptions through channels as plain text prompts, markdown files, mermaid flowcharts, diagrams, or images. The orchestrator agent decomposes into GitHub issues via the `create_subtasks` contract action. Claude's multimodal capabilities handle image-based inputs.
- **Audit-log maintenance signals (Phase 3b)**: Periodic scanner queries `audit.db` for health signals (repeated failures, stale tasks, budget anomalies, drift). Notifies user via channel with approval gate. Configurable auto-approve per maintenance type in WORKFLOW.md `maintenance:` block.

## Coding Standards

- No unnecessary comments. Don't narrate what code does. Only comment non-obvious intent, trade-offs, or constraints.
- Table-driven tests for all unit tests.
- Every external dependency (GitHub API, Claude Code CLI, filesystem) is behind an interface.
- Wrap errors with context: `fmt.Errorf("loading config: %w", err)`
- Use `log/slog` for all logging.
- No global mutable state -- dependency injection via constructors.

## Current Status

**Phase**: Phase 3a — Contract + Audit + Orchestrator Core (**complete**)
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
- Tool contract: 8 action types (dispatch, skip, comment, update_voice, request_approval, close_wave, create_subtasks, promote_knowledge) with risk classification (low/medium/high), validation, idempotency flags. Schema-only actions (create_subtasks, promote_knowledge) return ErrNotImplemented, logged and skipped.
- SQLite audit log: append-only event log at `~/.anthem/audit.db` via `modernc.org/sqlite` (pure Go, no CGo). AuditLogger interface with Record, Query, RecentByTask, SummaryForWave. WAL mode, mutex-serialized writes, busy timeout. Injected into Orchestrator, closed on shutdown.
- Task lifecycle state machine: 10 formalized states (queued, planned, running, blocked, retryQueued, needsApproval, completed, failed, canceled, skipped) replacing StatusActive/StatusPending. Transition(from, to) validation, StatusToLabel/LabelToStatus mapping, TerminalReason field on Task.
- Executor prompt fix: removed VOICE.md from buildFullPrompt -- executors get constraints + WORKFLOW.md only. Voice is orchestrator-only.
- Driver permission fixes: replaced hardcoded --dangerously-skip-permissions with config-driven PermissionMode (default dontAsk). Added DeniedTools to RunOpts. Added ContinueOpts to Continue() with workspace, stall timeout, allowed tools, permission mode. Fixed RunResult.Output population from stream event's result text (string or content block array). Added PermissionMode/SkipPermissions/DeniedTools to AgentConfig.
- Orchestrator session manager: OrchestratorAgent with Start/Consult/Refresh. Builds system prompt with voice + action schema + wave model. Receives compact StateSnapshot, returns structured JSON actions. parseActions with brace-counting JSON extraction. ConsultWithRepair sends repair prompt on parse failure, falls back to nil (triggers mechanical dispatch). Token tracking for session refresh threshold.
- Wave-aware tick loop: dirty-snapshot gating (SHA256 hash of task IDs+statuses+wave, skip consult on unchanged state). Wave tracking with frontier exhaustion detection. Fallback to Phase 2 mechanical dispatch when orchestrator is nil, disabled, or fails. executeActions validates each action against contract, dispatches tasks, updates tracker, records audit events. OrchestratorConfig (enabled, max_context_tokens, stall_timeout_ms) in config.go. main.go wiring creates audit logger + orchestrator agent + passes to Opts.
- Voice self-evolution: update_voice contract action triggers voice.LoadFile -> voice.Merge -> write -> voice.AppendChangelog -> audit event. Updates in-memory voiceContent on both Orchestrator and OrchestratorAgent.
- New files: `internal/orchestrator/contract.go`, `internal/orchestrator/orchagent.go`, `internal/orchestrator/integration_test.go`, `internal/orchestrator/voice_test.go`, `internal/audit/audit.go`, `internal/audit/schema.go`, `internal/audit/audit_test.go`.
- New dependency: `modernc.org/sqlite`.

**Phase 3b (next)** (Channels + Task Decomposition + Maintenance):
- Two-way channel system: modular `Channel` interface, Channel Manager, EventBridge for outbound event notifications. Slack adapter via Socket Mode (no HTTP server needed).
- Multi-format task decomposition: user sends prompts, markdown, flowcharts, or diagrams via channel; orchestrator decomposes into GitHub issues via `create_subtasks` (implements the currently schema-only action).
- Audit-log maintenance signals: periodic scanner detects repeated failures, stale tasks, budget anomalies, drift; notifies user via channel with approval gate (configurable auto-approve per type).
- `require_plan` folded into channel conversation flow (orchestrator proposes plan via reply, user approves, dispatch begins).
- New contract actions: `ActionReply` (channel response), `ActionRequestMaintenance` (maintenance proposal with approval gate).
- New packages: `internal/channel/`, `internal/channel/slack/`, `internal/maintenance/`.
- New dependency: `github.com/slack-go/slack`.

**Phase 4**: Dashboard + status API + WebSocket streaming via EventBus, knowledge promotion (`promote_knowledge` action), DAG execution plans, WhatsApp channel adapter, example templates, CONTRIBUTING.md, GoReleaser cross-platform binaries, code signing, demo video.

Update this section as phases are completed.

## Reference: OpenAI Symphony

When making implementation decisions, reference Symphony's codebase for proven patterns:
- Repository: https://github.com/openai/symphony
- Language: Elixir (GenServer-based orchestrator)
- Spec: `SPEC.md` in repo root
- Key patterns: single-module orchestrator, tracker adapters, workspace isolation, config parsing from markdown front matter
- Key difference: Symphony has no personality/voice concept. Anthem adds the orchestrator agent layer on top (Phase 3).
