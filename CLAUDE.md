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
- **Dashboard**: Deferred to Phase 3 (tech choice TBD between HTMX and SPA)
- **Voice changelog**: Log all VOICE.md changes with reasons to `~/.anthem/voice-changelog.md` (Phase 3, when orchestrator agent is implemented)
- **Testing**: Interface-based mocks (no mocking framework -- just simple structs satisfying interfaces), table-driven tests, `//go:build integration` tagged tests for external services, `testdata/` fixtures, CI from day 1
- **Logging**: Use `log/slog` (Go stdlib) for structured logging
- **Error handling**: Wrap errors with `fmt.Errorf("context: %w", err)`. Never swallow errors silently.
- **No global state in code**: Pass dependencies via constructor injection.
- **System guardrails**: The `system:` config block lives in WORKFLOW.md front matter (per-project policy). Safe defaults: `workflow_changes_require_approval: true`. Project-level constraints are defined as a `constraints` list in the `system:` block.
- **Bootstrapping**: `~/.anthem/`, default VOICE.md, and default constraints.yaml are auto-created on first run. If VOICE.md is missing at runtime, warn and continue without personality. If constraints.yaml is missing, continue with empty user constraints.
- **Template engine**: Use sprig (`github.com/Masterminds/sprig/v3`) function map for WORKFLOW.md body rendering -- provides `lower`, `upper`, `replace`, `default`, `join`, etc.
- **EventBus**: `Publish` must be non-blocking. Buffered channels per subscriber, drop oldest on overflow. The orchestrator loop must never stall on slow observers.
- **Orchestrator module pattern**: All dispatch, reconciliation, and state logic lives in a single `orchestrator.go` file (matching Symphony's single-module pattern). No separate dispatch.go or reconciler.go files.

## Coding Standards

- No unnecessary comments. Don't narrate what code does. Only comment non-obvious intent, trade-offs, or constraints.
- Table-driven tests for all unit tests.
- Every external dependency (GitHub API, Claude Code CLI, filesystem) is behind an interface.
- Wrap errors with context: `fmt.Errorf("loading config: %w", err)`
- Use `log/slog` for all logging.
- No global mutable state -- dependency injection via constructors.

## Current Status

**Phase**: Phase 2 — Go Daemon Reliability Layer (**starting**)
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

**Phase 2 implementation order** (see `docs/plans/implementation.md` for details):
1. Rules engine completion — wire `auto_assign`, `max_cost`, `TitlePattern` regex matching
2. Real workspace manager — replace mock, per-task dirs, hook lifecycle
3. Retry and backoff — exponential backoff, stall recovery
4. Graceful shutdown — WaitGroup drain, agent termination, claim release
5. State persistence — `~/.anthem/state.json`, load/save, startup reconciliation
6. Config hot-reload — fsnotify watcher

**Phase 3 preview**: Orchestrator agent (Claude session with VOICE.md), tool interface, voice self-evolution, task decomposition, dashboard.

Update this section as phases are completed.

## Reference: OpenAI Symphony

When making implementation decisions, reference Symphony's codebase for proven patterns:
- Repository: https://github.com/openai/symphony
- Language: Elixir (GenServer-based orchestrator)
- Spec: `SPEC.md` in repo root
- Key patterns: single-module orchestrator, tracker adapters, workspace isolation, config parsing from markdown front matter
- Key difference: Symphony has no personality/voice concept. Anthem adds the orchestrator agent layer on top (Phase 3).
