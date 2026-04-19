# Anthem -- Claude Code Context

## What Is Anthem

Anthem is an open-source **project agent runtime**. Each project gets one Anthem instance that exposes a persistent orchestrator agent plus a roster of guest specialists. Every interaction happens in one of four explicit modes: **Chat**, **Plan**, **Execute**, or **Loop**.

Under the hood Anthem is a hybrid: a Go daemon owns reliability and control flow (channels, state, concurrency, audit, workspace isolation, step advancement in Execute, tracker polling in Loop), and a Claude orchestrator agent owns intelligence (conversation, plan synthesis, step content, user context). Guest agents are headless specialists dispatched by either mode router or plan runner, with harnesses (WORKFLOW.md, skills, MCP tools, constraints) injected per task.

The orchestrator uses a two-file identity model: project-specific character from `agents/orchestrator.md`, shared user knowledge from `~/.anthem/VOICE.md`. All agents (orchestrator and guests) receive `VOICE.md` user context so they benefit from shared learning.

**Canonical mode reference:** [`docs/architecture/modes.md`](docs/architecture/modes.md).
**Runtime architecture:** [`docs/plans/architecture.md`](docs/plans/architecture.md) (sections 16–18 cover mode router, ExecutionBackend, and the Execute runtime).

## Plans and Architecture Docs

Read these documents thoroughly before writing any code:

- `docs/architecture/modes.md` -- Canonical description of the four modes (Chat, Plan, Execute, Loop), mode selection, Execute v1 contract, event protocol
- `docs/plans/architecture.md` -- Full system architecture with mermaid diagrams, runtime planes, component catalog, mode router, ExecutionBackend, Execute runtime, CLI spec, cross-platform details, event bus
- `docs/plans/implementation.md` -- Scaffold structure, phase history, dependency list, testing strategy

These are the source of truth for what to build and how.

## Design Decisions (Locked In -- Do Not Change)

- **Language**: Go (latest stable)
- **Module path**: `github.com/rauriemo/anthem`
- **Cross-platform**: Windows-first, all three OS from day 1. Build tags for platform-specific process management.
- **Project agent runtime**: Default `anthem run` boots a conversational orchestrator with a guest roster and channels. No tracker required. Loop is an opt-in `ExecutionBackend`, not the core identity.
- **Four-mode grammar**: `Mode` enum in `internal/types/task.go` — `ModeChat` / `ModePlan` / `ModeExecute` / `ModeLoop`. `Orchestrator.CurrentMode` is observable state. `[system:<mode>]` tag selects; untagged = Chat. Legacy tags (`fast`, `agent`, `build`) remap for compatibility.
- **ExecutionBackend abstraction**: `internal/backend/backend.go` — `Start` / `Stop` / `QueueWork` / `ActiveWork` / `OnProgress` + `LoopHost` callback. `GitHubLoopBackend` is the shipping implementation. Loop is pluggable.
- **Execute mechanical split**: Code owns step state, advancement, gates, artifact registration, event emission, failure semantics. Agents own plan compilation, per-step context/prompts, and content. No autonomous retries in v1.
- **ArtifactProvider abstraction**: `internal/execute/artifacts.go` — `ContextArtifactProvider` (reads `.context/features/<feature>/artifacts.yaml`) or `FilesystemArtifactProvider` (mtime fallback).
- **Execute event protocol**: Stable `execution.*` events (`plan_loaded`, `step_started`, `gate_opened`, `plan_completed`, etc.) emitted on the same channel pipe as chat. Prism routes by EventType.
- **Hybrid architecture**: Go daemon = reliability (channels, state, concurrency, audit, workspace isolation, retry, step advancement, tracker polling). Orchestrator agent = intelligence (Claude session with orchestrator persona + user context, task decomposition, plan synthesis, step content). Executors = headless Claude Code workers with harnesses.
- **Two-file identity model**: `agents/orchestrator.md` holds the project-specific orchestrator character (Identity, Personality, Your Focus, Coordination). `~/.anthem/VOICE.md` holds shared user knowledge (communication style, habits, expertise) -- facts and stable preferences, not project state or agent self-description. Both are injected into orchestrator prompts. Guest agents receive `VOICE.md` user context. `orchestrator.md` lives in `agents/` for authoring consistency but is **not** a guest -- it's excluded from GuestIndex and never appears in Prism's roster.
- **Section routing**: `update_voice` uses explicit allowlists. Agent-owned sections (Identity, Personality, Your Focus, Coordination) route to `agents/orchestrator.md`. All other sections default to `~/.anthem/VOICE.md`. Routing decisions are logged.
- **Migration**: On startup, `voice.MigrateVoiceToOrchestrator()` moves Identity/Personality from VOICE.md to `agents/orchestrator.md` if orchestrator.md doesn't exist. If both exist, orchestrator.md wins and VOICE.md is not auto-pruned (warning logged).
- **Constraints**: Two-tier (`~/.anthem/constraints.yaml` + `system.constraints` in WORKFLOW.md) with meta-constraint protection.
- **Global state root**: `~/.anthem/` (VOICE.md, constraints.yaml, state.json, audit.db, plans/, voice-changelog.md). Project-level: `agents/orchestrator.md`
- **GitHub auth**: `GITHUB_TOKEN` env var, fallback to `gh auth token`.
- **Dashboard**: Fulfilled by Prism (separate repo).
- **Testing**: Interface-based mocks, table-driven tests, `//go:build integration` tagged tests, `testdata/` fixtures.
- **Logging**: `log/slog` (Go stdlib) for structured logging.
- **Error handling**: Wrap with `fmt.Errorf("context: %w", err)`. Never swallow errors.
- **No global state in code**: Dependency injection via constructors.
- **Template engine**: sprig (`github.com/Masterminds/sprig/v3`) for WORKFLOW.md body rendering.
- **Orchestrator module pattern**: All dispatch/reconciliation/state logic in `orchestrator.go`. Mode router (`detectMode` + `HandleUserMessage`) dispatches to `handleChat` / `handlePlan` / `handleExecute` / `handleLoop`.
- **Contract-first tool surface**: 12 action types with schemas, risk levels, idempotency. JSON structured output. `update_agent_meta` writes YAML frontmatter; `update_voice` extended with optional `agent_file` for guest targeting.
- **Three-layer state**: Event Log (SQLite audit), State Snapshot (in-memory), Knowledge Artifacts (repo docs).
- **Channel system**: Pluggable adapters (Slack Socket Mode, Dispatch WebSocket, Prism WebSocket).
- **Agent profiles**: Named profiles (coder/architect/tester/debugger/explorer) composing MCP server refs + skill refs.
- **Explorer subagents**: Three-phase plan pipeline (scout -> parallel explore -> synthesize).

Full details in `docs/plans/architecture.md`.

## Coding Standards

- No unnecessary comments. Don't narrate what code does. Only comment non-obvious intent, trade-offs, or constraints.
- Table-driven tests for all unit tests.
- Every external dependency (GitHub API, Claude Code CLI, filesystem) is behind an interface.
- Wrap errors with context: `fmt.Errorf("loading config: %w", err)`
- Use `log/slog` for all logging.
- No global mutable state -- dependency injection via constructors.

## Current Status

**Shipped:** Phase 1 → Phase 5 (Mode refactor + Execute v1).

Cumulative capabilities across all shipped phases: GitHub issue-driven dispatch (as Loop mode), rules engine, retry/backoff, state persistence, config hot-reload, graceful shutdown, contract-based orchestrator agent, SQLite audit log, wave-aware dispatch, task lifecycle state machine, Slack + Dispatch + Prism channel adapters, LLM token streaming, maintenance scanner, DAG edges in Loop, multi-LLM driver, executor-reviewer loop, specialist agent profiles, decision traces, orchestrator codebase awareness, explorer subagent pipeline, embedded skills (6 SKILL.md packages via go:embed), guest agent registry, context reporting + hydration, MCP platform, **four-mode router (Chat/Plan/Execute/Loop)**, **ExecutionBackend abstraction**, **Execute v1 runtime** (linear handoff chains, approval gates, artifact providers, stable event protocol), **conversational Chat as default run path**.

See `docs/plans/architecture.md` (sections 16–18) and `docs/architecture/modes.md` for current architecture details. `docs/plans/implementation.md` retains historical phase notes.

**Phase 6 (deferred — Execute v2):** parallel DAG branches, `for_each` fan-out over manifest artifacts, richer artifact taxonomy, per-item revision actions in approval gates, native Prism renderers for image-gallery / animation-preview / scene-review kinds, optional autonomous retry policy.

**Phase 7 (future):** WhatsApp channel adapter, GoReleaser binaries, code signing, demo video.

## MCP platform (guest agents + Conduit)

**Shipped today (Claude Code as runtime):**

- **Guest frontmatter** (`internal/guests/guests.go`): `allowed_tools`, `mcp_servers` (map: server id → `mcpconfig.MCPServerRef`), `http_tools` (`HTTPToolConfig`). HTTP tools are parsed; in-process HTTP brokering is not wired yet.
- **`.mcp.json`:** Before guest runs, Anthem merges **global** `agent.mcp_servers` (config) with the guest’s `mcp_servers` (guest wins on key collision) and writes `{projectRoot}/.mcp.json` via `internal/harness.WriteMCPConfig` (same JSON contract as Conduit `mcpbridge`). **Parallel multi-guest dispatch** merges **all** selected guests’ servers once, then writes a single `.mcp.json` before starting goroutines.
- **Claude Code** loads MCP servers from that file and performs tool calls natively (no Anthem `Pool.CallTool` loop in v1).
- **Tool id shape:** `mcp__{yaml-map-key}__{UnityOrServerToolName}` — e.g. server key `unity-mcp` → `mcp__unity-mcp__Unity_GetConsoleLogs`. Allowlist wildcards: `mcp__unity-mcp__*`, `http__image_gen__*`, or exact names like `WebSearch`.
- **Feature context (v2):** `.context/features/{active_feature}/` now includes six file types: `plan.md`, `decisions.yaml`, `artifacts.yaml` (schema v2 with metadata/depends_on/consumers/updated_at/updated_by), `task-state.yaml` (schema v2 with progress/blocked_on/dependencies/produces), `changelog.yaml` (append-only activity log), and `metadata-keys.yaml` (canonical key registry per artifact type). When `active_feature` is set, `featurewriter.go` updates `task-state.yaml` on dispatch (`SetTaskActive` with optional deps/produces) and after the run (`UpdateTaskState` with optional progress/blocked_on/produces + `AppendChangelog` + `AppendArtifact` from parsed `context_report`). `UpdateTaskProgress` allows mid-task progress updates.
- **context_report parsing:** `contextreport.go` provides `ParseContextReport(output)` which extracts the last `{"context_report": ...}` JSON block from agent output. The report includes action, summary, optional artifact with rich metadata, progress, blocked_on, and produces. If no report is found, a generic `task_completed` changelog entry is created from truncated output.
- **Hydration split:** `HydrateFeatureContext()` produces two-section output: `### Recent Activity (what changed)` (last 15 changelog entries, newest first) then `### Current State (what exists now)` with `#### Agent Activity` (rich status with progress, dependencies, produces) and `#### Available Artifacts` (rich metadata, lineage, consumers).
- **Agent instructions:** `buildGuestPrompt()` injects a `## Context Reporting` block when feature context is active, teaching agents the `context_report` JSON format, canonical metadata keys from the registry, and stable ID requirements.
- **Metadata key registry:** `metadata-keys.yaml` defines recommended keys per artifact type (sprite, document, scene, audio). `AppendArtifactValidated()` logs warnings for unknown keys but does not reject them (Phase 1 permissive). `ReadMetadataKeys()` loads the registry.

**Deferred (design in repo plan):** In-process **brokered MCP** (`mcpclient.Pool` + intercept `tool_use`) and **brokered HTTP** inside Anthem. See Conduit for the reusable Pool when that ships.

**Unity Editor MCP:** Prefer **Unity’s official MCP** with `com.unity.ai.assistant` **2.x**: the stdio entrypoint is the **relay** binary with `--mcp` (installed under `~/.unity/relay/`, on Windows `%USERPROFILE%\.unity\relay\relay_win.exe`). User must approve external clients in **Edit → Project Settings → AI → Unity MCP**. Docs: [Unity MCP overview](https://docs.unity3d.com/Packages/com.unity.ai.assistant@2.0/manual/unity-mcp-overview.html), [Get started](https://docs.unity3d.com/Packages/com.unity.ai.assistant@2.0/manual/unity-mcp-get-started.html). Tool names vary by package version (e.g. `Unity_GetConsoleLogs`, `Unity_RunCommand`); do not assume older doc examples like `Unity_ManageScene` exist.

**Primary code paths:** `internal/orchestrator/orchestrator.go` (guest mention + MCP write + `executeUpdateVoice` routing + `executeUpdateAgentMeta` + `[system:respec]` routing), `internal/orchestrator/orchagent.go` (`buildSystemPrompt` with `orchPersona` + `userContext`), `internal/orchestrator/respec.go` (`handleRespecMessage`, `buildRespecSystemPrompt`, `parseRespecTarget`, session tracking), `internal/orchestrator/guestdispatch.go` (parallel guests + MCP merge + `userContext` injection), `internal/orchestrator/featurewriter.go`, `internal/harness/harness.go` (`WriteMCPConfig`, `MergeGuestServers`), `internal/guests/guests.go` (`LoadOrchestratorPersona`, `LoadAgentFrontmatter`, `ScanDirectory` with orchestrator exclusion), `internal/voice/voice.go` (`IsAgentSection`, `UpdateAgentSection`, `UpdateAgentFrontmatter`), `internal/voice/migrate.go` (`MigrateVoiceToOrchestrator`).

**Security (unchanged intent):** `auth_scheme` for HTTP tools: bearer-only at parse time where enforced; never persist auth tokens in YAML or `.mcp.json` — only env var names. Deny-by-default allowlists when `allowed_tools` is empty but tools are declared.

**Full workflow spec** (HTTP artist, artifacts, notifications): see the MCP platform plan in `.cursor/plans/` / project planning docs; implementation is incremental relative to that doc.

## Guest Agents (Shipped)

Guest agents are lightweight persona definitions (markdown files with YAML frontmatter) in a project's `agents/` directory. Anthem scans this directory on boot (explicitly skipping `orchestrator.md`), generates `.agents-index.json`, and advertises the roster to Prism via `guest_agents` on `auth_ok`. `orchestrator.md` is loaded separately via `guests.LoadOrchestratorPersona()` and injected into orchestrator prompts only.

Full spec: `docs/plans/guest-agents.md`

### Review specs (per-agent approval UI contract)

Agents can declare a `review:` block in their frontmatter telling Prism how to render the approval gate that follows their step. The block is media-shape-based (`kind: image-gallery | video-preview | document | …`) and composable (`panels: [viewer, metadata-table, …]`). Full contract + cookbook in [`docs/architecture/review-kinds.md`](docs/architecture/review-kinds.md). Validate locally with `anthem validate-agents --dir agents/`.

### Orchestration runtime (`internal/orchestrator/`)

- **ConvoBuffer** (`convobuffer.go`): Per-channel 10-round ring buffer. `RecordUserMessage` finalizes the current round and starts a new one; `RecordResponse` appends speaker-labeled responses. `FormatHistory` renders 3 most recent rounds with 200-char truncation (backward-compatible default). `FormatHistoryN(rounds, maxRounds, truncLen)` provides parameterized formatting. `HasGuestSpoken(key, guestID)` detects first-turn guests for expanded history context. Fed into routing calls and guest prompts.
- **SharedContext** (`sharedcontext.go`): Per-channel session knowledge document. Optionally file-backed at `.context/session-context.yaml` via `NewSharedContextWithFile()` -- loads existing data on startup, persists atomically on every `Update()`. Falls back to in-memory-only via `NewSharedContext()`. `Snapshot()` returns a copy of all entries. Updated every round across all modes. The routing call returns context updates; a post-round summarization call also runs when guests are active. Loop mode additionally extracts `context_update` from `OrchestratorResponse`.
- **Guest dispatch** (`guestdispatch.go`): Unified dispatch for all modes (chat/plan/loop). Routing always fires when 1+ guests are active via `routeToGuests` (haiku decides selection, directed text, and orchestrator participation). `RoutingResult` includes `DirectedText map[string]string` for per-guest focus extraction. `GuestPromptOpts.FocusText` appends a `## Your Focus` section after the user message. Fallback: if routing fails, broadcast to all with no focus. First-turn guests receive a **neutral onboarding preamble** (not raw history) that tells the guest they were just added and must wait for direction before taking action; the historical "expanded history" path was removed to prevent leaking task-bearing prior context into a first turn. `dispatchSelectedGuests` manages concurrency (semaphore of 3), plan edit serialization (`planEditMu`), and ConvoBuffer recording. `suggestGuestToInvite` evaluates post-round specialist suggestions (feature-flagged via `EnableGuestSuggestions`).
- **Mode-aware guest activation** (`orchestrator.go`, `execcompile.go`, `execute/runner.go`, `channel/channel.go`): Orchestrator-driven guest dispatch is gated off in Execute mode and on execplan gate clicks (`dispatchBlockedByMode`). In Plan mode, guests run with `RunOpts.ToolsDisabled = true` and `AllowedTools = []` (the `claude/driver.go` harness translates `ToolsDisabled` into `--disallowedTools '*'`, which dominates every other tool-resolution path). Invite-intent auto-activation is restricted to Chat mode and biases toward false negatives. `PlanRunner` is the sole guest activator in Execute mode: it broadcasts `ActivateGuest` at step start and `DeactivateGuest` at step end (approve/abort deactivate, revise retains). The Plan→Execute handoff in `handleExecutePlanApproval` broadcasts `DeactivateGuest` for every plan-time active guest before `PlanRunner` spawns so the Prism roster starts empty.
- **Wire protocol**: `active_guests` and `mention` on inbound `req` frames; `guest_id` on outbound `res` and `stream` frames (including `StreamDelta` and `StreamDone`). `display_ids` on outbound `res` frames when the response includes artifacts alongside text (collected during artifact broadcast, attached to the text message for frontend linking). @-mention bypasses routing and orchestrator.
- **StateSnapshot extensions**: `ActiveGuestsSummary`, `SharedContext`, `ConversationHistory` injected when guests are active. System prompt gains "Active Specialists" awareness section. `OrchestratorResponse.ContextUpdate` extracted and applied to SharedContext.

Key Anthem responsibilities:
- `internal/guests/` package: scan `agents/` (skip `orchestrator.md`), parse frontmatter, generate index, load persona on demand, `LoadOrchestratorPersona()` for orchestrator identity, `LoadAgentFrontmatter()` for generic frontmatter loading
- `internal/voice/` package: section routing (`IsAgentSection`), `UpdateAgentSection()` for writing to agent .md files, `UpdateAgentFrontmatter()` for YAML frontmatter merging, `MigrateVoiceToOrchestrator()` for first-run migration
- `internal/orchestrator/respec.go`: conversational `/respec` flow -- `[system:respec]` tag routing, per-channel session tracking (`activeRespecs`), cancel handling, write-per-phase action execution. Uses `update_agent_meta` for frontmatter and `update_voice` with `agent_file` for body sections
- WebSocket protocol: `guest_agents` in auth, `active_guests`/`mention` in req, `guest_id`/`suggest_guest` in res
- Orchestrator: compressed context injection (roster summary for all active guests, full persona only for the responding guest), capability-matching suggestions. Orchestrator prompts receive `orchPersona` (from `orchestrator.md`) + `userContext` (from `VOICE.md`). Guest prompts receive `userContext`.
- Config: `guests` allowlist and `max_active_guests` in WORKFLOW.md frontmatter
- Fallback: `~/.anthem/agents/` for projects without an `agents/` directory

## Reference: Prism Channel Protocol

Prism uses the same WebSocket JSON frame protocol as Dispatch, extended with:
- `display` frames: `{"type":"display","component":{...},"thread":"<id>"}` for A2UI structured visual content. Display components include `kind:plan` (markdown plan artifacts with stable `displayId = "plan:<UUID>"`, upserted in place across Plan-mode refinements) and `kind:execplan` (compiled `ExecutionPlan` preview emitted by `handleExecuteMarkdown`, with stable `displayId = "execplan:<UUID>"` and fields `planId`, `compileGeneration`, `stepCount`, `gateCount`, and `body` JSON of steps/gates).
- `stream` frames (Phase C): `{"type":"stream","text":"<delta>","thread":"<id>","done":false}` for incremental LLM output. When `done: true`, Prism knows the stream is complete and can finalize the message.
- `plan-card` messages: `[plan-card]{"title":"...","overview":"...","tasks":[...],"planPath":"..."}[/plan-card]` embedded in `res` frame text. The Prism backend (`_handle_anthem_message` in `main.py`) converts `res` frames to `chat` type before forwarding to the frontend. Prism's `PlanCard.tsx` parses these and renders a structured card with title, task list, "View Plan" (opens artifact pane), model dropdown, and "Build" button.
- Execute-mode approval grammar: Prism's `ExecutionPlanView` buttons emit `[gate:<approve|revise|abort>:execplan:<planID>:<compileGeneration>]` which `handleExecutePlanApproval` parses via `execPlanGateRe`. Typed `"approve"/"go"/"dispatch"` text is intentionally **not** honored — buttons are the only control plane for dispatching a compiled plan.
- `[system:plan:new]` tag: archives the current plan draft (`StatusArchived`, filtered out of `LatestDraft`) and continues the turn as a fresh Plan mode message. Prism exposes this as the `/newplan` slash command.

The Prism adapter at `internal/channel/prism/adapter.go` handles `Send()` for `res`, `event`, `display`, and `stream` frame types.

## Reference: OpenAI Symphony

When making implementation decisions, reference Symphony's codebase for proven patterns:
- Repository: https://github.com/openai/symphony
- Language: Elixir (GenServer-based orchestrator)
- Spec: `SPEC.md` in repo root
- Key patterns: single-module orchestrator, tracker adapters, workspace isolation, config parsing from markdown front matter
- Key difference: Symphony has no personality/voice concept. Anthem adds the orchestrator agent layer on top (Phase 3).
