# Anthem -- Claude Code Context

## What Is Anthem

Anthem is an open-source, Go-based agent orchestrator -- an alternative to OpenAI Symphony, purpose-built for Claude Code users. It polls a task board (GitHub Issues by default, swappable via adapters), spawns Claude Code agents in isolated workspaces, enforces rules/approval flows, and provides a real-time dashboard. Its key differentiator is VOICE.md -- a self-evolving personality system inspired by OpenClaw's SOUL.md -- paired with a two-tier constraints system that separates safety guardrails from personality.

## Plans and Architecture Docs

Read these documents thoroughly before writing any code:

- `docs/plans/architecture.md` -- Full system architecture with mermaid diagrams, all 15 components, Go interface definitions, data types, CLI spec, cross-platform details, event bus, rate limiting, hook failure handling
- `docs/plans/implementation.md` -- Scaffold structure, implementation order with specific steps, phase breakdown, dependency list, testing strategy

These are the source of truth for what to build and how.

## Design Decisions (Locked In -- Do Not Change)

- **Language**: Go (latest stable)
- **Module path**: `github.com/rauriemo/anthem`
- **Cross-platform**: Windows-first, all three OS from day 1. Use build tags for platform-specific process management (Job Objects on Windows, process groups on Unix). Everything else in Go is cross-platform by default.
- **VOICE.md location**: Global at `~/.anthem/VOICE.md` (shared across ALL projects, not per-project). The voice is user-scoped, not repo-scoped. VOICE.md is pure personality -- Identity, Personality, and User Context only. No safety rules in VOICE.md.
- **Constraints**: Two-tier system replacing the old `[CORE]` sections in VOICE.md:
  - **User-level**: `~/.anthem/constraints.yaml` -- global safety rules (e.g. "never force-push to main"). Loaded by the CLI, passed to the orchestrator.
  - **Project-level**: `system.constraints` list in WORKFLOW.md front matter -- project-specific rules (e.g. "run tests before opening a PR").
  - **Meta-constraint**: Anthem always appends a hardcoded constraint: "Do not modify constraint definitions in WORKFLOW.md system.constraints or ~/.anthem/constraints.yaml". This prevents agents from removing their own guardrails.
  - Both tiers are combined under a `## Constraints (non-negotiable)` header in the prompt, placed between voice content and the task template.
- **WORKFLOW.md location**: Per-project, typically `./WORKFLOW.md` in repo root
- **Global state root**: `~/.anthem/` for all global state (VOICE.md, constraints.yaml, state.json, voice-changelog.md). Resolves via `os.UserHomeDir()` on all platforms.
- **GitHub auth**: `GITHUB_TOKEN` env var as primary, fallback to `gh auth token` CLI command. No custom credential storage -- no tokens in `~/.anthem/`.
- **Dashboard**: Deferred to Phase 3 (tech choice TBD between HTMX and SPA)
- **Voice changelog**: Log all VOICE.md changes with reasons to `~/.anthem/voice-changelog.md` + post diff as issue comment
- **Voice self-evolution**: VOICE.md is freely editable by agents (pure personality, no safety-critical sections). The self-evolution instruction tells agents they may update `~/.anthem/VOICE.md` if they discover persistent patterns about user preferences, communication style, or working habits. Safety guardrails live in the constraints system, not in VOICE.md.
- **Testing**: Interface-based mocks (no mocking framework -- just simple structs satisfying interfaces), table-driven tests, `//go:build integration` tagged tests for external services, `testdata/` fixtures, CI from day 1
- **Logging**: Use `log/slog` (Go stdlib) for structured logging
- **Error handling**: Wrap errors with `fmt.Errorf("context: %w", err)`. Never swallow errors silently.
- **No global state in code**: Pass dependencies via constructor injection.
- **System guardrails**: The `system:` config block lives in WORKFLOW.md front matter (per-project policy). Safe defaults: `workflow_changes_require_approval: true`. Project-level constraints are defined as a `constraints` list in the `system:` block.
- **Bootstrapping**: `~/.anthem/`, default VOICE.md, and default constraints.yaml are auto-created on first run. If VOICE.md is missing at runtime, warn and continue without personality. If constraints.yaml is missing, continue with empty user constraints.
- **Template engine**: Use sprig (`github.com/Masterminds/sprig/v3`) function map for WORKFLOW.md body rendering -- provides `lower`, `upper`, `replace`, `default`, `join`, etc.
- **EventBus**: `Publish` must be non-blocking. Buffered channels per subscriber, drop oldest on overflow. The orchestrator loop must never stall on slow observers.

## Coding Standards

- No unnecessary comments. Don't narrate what code does. Only comment non-obvious intent, trade-offs, or constraints.
- Table-driven tests for all unit tests.
- Every external dependency (GitHub API, Claude Code CLI, filesystem) is behind an interface.
- Wrap errors with context: `fmt.Errorf("loading config: %w", err)`
- Use `log/slog` for all logging.
- No global mutable state -- dependency injection via constructors.

## Current Status

**Phase**: Phase 1 — Foundation (**complete**, tested end-to-end)
**Scaffold**: Complete (all 11 steps done)
**Phase 1**: Complete. All 10 steps implemented and verified with a live GitHub issue:
- Step 1: WORKFLOW.md parser — YAML front matter + sprig templates, `$ENV_VAR` expansion, validation. 30 table-driven tests.
- Step 2: VOICE.md parser — section extraction, self-evolution instruction.
- Step 3: `~/.anthem/` bootstrapping — auto-create directory, default VOICE.md, and default constraints.yaml on first run.
- Step 4: `anthem init` — creates starter WORKFLOW.md + bootstraps `~/.anthem/VOICE.md` + `~/.anthem/constraints.yaml`.
- Step 5: GitHubTracker — ListActive, GetTask, UpdateStatus, AddComment, AddLabel, RemoveLabel. Auth via `GITHUB_TOKEN` / `gh auth token`. Rate limit monitoring.
- Step 6: Claude Code driver — stream-json parsing, stall detection, cross-platform process management (Windows Job Objects / Unix pgid).
- Step 7: Orchestrator loop — poll, sort by priority/created_at/id, claim, dispatch with concurrency control (global + per-label limits).
- Step 8: EventBus — in-process fan-out, buffered channels, non-blocking publish.
- Step 9: CLI wiring — `anthem run`, `anthem validate`, `anthem init`, `anthem version` fully wired.
- Step 10: E2E test — verified with live GitHub issue: pickup, Claude Code execution, issue closure.

**Post-Phase 1 hardening**:
- ETag-based conditional requests for `ListActive` — avoids burning GitHub API rate limit on unchanged responses. Uses `etagTransport` round-tripper to inject `If-None-Match` and cache results on 304.
- Rate limit throttling — `ShouldThrottle()` method on `GitHubTracker` (and `IssueTracker` interface). When remaining < limit/10, sets throttle until reset time. Orchestrator `tick()` checks via type assertion and skips when throttled.
- Auto-bootstrap in `anthem run` — creates `~/.anthem/` and default `VOICE.md` before loading workflow, reusing existing helpers. Extracted into testable `bootstrapDir()` function.

**Pre-Phase 2: Constraints separation**:
- Separated safety constraints from VOICE.md into a two-tier system: user-level `~/.anthem/constraints.yaml` + project-level `system.constraints` in WORKFLOW.md.
- VOICE.md is now pure personality (Identity, Personality, User Context). No `[CORE]` sections.
- Prompt pipeline wired: voice content + constraints block + task template are combined in the orchestrator's `dispatch()` method.
- Constraints loader (`internal/constraints/`) reads YAML, handles missing files gracefully.
- Meta-constraint hardcoded by Anthem prevents agents from editing their own constraints.
- Removed `voice_core_immutable` and `voice_changes_require_approval` from config schema.
- Bootstrap and `anthem init` now also create `~/.anthem/constraints.yaml`.

**Bugs fixed during Phase 1 testing**:
- Claude Code CLI requires `--verbose` when combining `-p` with `--output-format stream-json`.
- Claude Code CLI requires `--dangerously-skip-permissions` for autonomous file writes in `-p` mode.
- Stream-json parser rewritten to match actual output format: `total_cost_usd` (not `cost_usd`), token counts nested in `usage` object, `is_error` boolean (not `exitCode` int), `result` field is a string (not a nested object).
- Orchestrator now manages label lifecycle: `todo` -> `in-progress` -> `done`, with issue closure on completion and error comments on failure.

**Next step**: Phase 2 step 1 — rules engine enhancements (require_plan, auto_assign, max_cost enforcement in dispatch).

Update this section as phases are completed.

## Reference: OpenAI Symphony

When making implementation decisions, reference the Symphony codebase for proven patterns:
- Repository: https://github.com/openai/openai-agents-python
- Directory: `examples/agents/symphony/`
- Relevant patterns: orchestrator loop, tracker adapters, workspace isolation, config parsing from markdown front matter
