# Anthem -- Claude Code Context

## What Is Anthem

Anthem is an open-source, Go-based agent orchestrator -- an alternative to OpenAI Symphony, purpose-built for Claude Code users. It polls a task board (GitHub Issues by default, swappable via adapters), spawns Claude Code agents in isolated workspaces, enforces rules/approval flows, and provides a real-time dashboard. Its key differentiator is VOICE.md -- a self-evolving personality system inspired by OpenClaw's SOUL.md, with immutable `[CORE]` section protection that OpenClaw lacks.

## Plans and Architecture Docs

Read these documents thoroughly before writing any code:

- `docs/plans/architecture.md` -- Full system architecture with mermaid diagrams, all 15 components, Go interface definitions, data types, CLI spec, cross-platform details, event bus, rate limiting, hook failure handling
- `docs/plans/implementation.md` -- Scaffold structure, implementation order with specific steps, phase breakdown, dependency list, testing strategy

These are the source of truth for what to build and how.

## Design Decisions (Locked In -- Do Not Change)

- **Language**: Go (latest stable)
- **Module path**: `github.com/rauriemo/anthem`
- **Cross-platform**: Windows-first, all three OS from day 1. Use build tags for platform-specific process management (Job Objects on Windows, process groups on Unix). Everything else in Go is cross-platform by default.
- **VOICE.md location**: Global at `~/.anthem/VOICE.md` (shared across ALL projects, not per-project). The voice is user-scoped, not repo-scoped.
- **WORKFLOW.md location**: Per-project, typically `./WORKFLOW.md` in repo root
- **Global state root**: `~/.anthem/` for all global state (VOICE.md, state.json, voice-changelog.md). Resolves via `os.UserHomeDir()` on all platforms.
- **GitHub auth**: `GITHUB_TOKEN` env var as primary, fallback to `gh auth token` CLI command. No custom credential storage -- no tokens in `~/.anthem/`.
- **Dashboard**: Deferred to Phase 3 (tech choice TBD between HTMX and SPA)
- **Voice changelog**: Log all VOICE.md changes with reasons to `~/.anthem/voice-changelog.md` + post diff as issue comment
- **Voice self-evolution**: Copy-diff-merge approach. Copy `~/.anthem/VOICE.md` into workspace as `.anthem/VOICE.md` before each run, diff after run, apply [CORE] enforcement + section merge, write back to global file. The self-evolution instruction in the prompt must reference the **workspace copy** (`.anthem/VOICE.md`), NOT the global path -- this ensures edits go through the merge pipeline.
- **Testing**: Interface-based mocks (no mocking framework -- just simple structs satisfying interfaces), table-driven tests, `//go:build integration` tagged tests for external services, `testdata/` fixtures, CI from day 1
- **Logging**: Use `log/slog` (Go stdlib) for structured logging
- **Error handling**: Wrap errors with `fmt.Errorf("context: %w", err)`. Never swallow errors silently.
- **No global state in code**: Pass dependencies via constructor injection.
- **System guardrails**: The `system:` config block lives in WORKFLOW.md front matter (per-project policy). Safe defaults: `workflow_changes_require_approval: true`, `voice_changes_require_approval: false`, `voice_core_immutable: true`.
- **Bootstrapping**: `~/.anthem/` and default VOICE.md are auto-created on first run. If VOICE.md is missing at runtime, warn and continue without personality.
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

**Phase**: Phase 1 — Foundation (in progress)
**Scaffold**: Complete (all 11 steps done, `go build ./...` and `go vet ./...` pass clean)
**Phase 1 progress**:
- Step 1: WORKFLOW.md parser — **done**. Full implementation with YAML front matter parsing, `$ENV_VAR` expansion, Go template rendering with sprig function map, validation (required fields, valid tracker kinds, valid rule actions, rule-specific constraints, multiple errors accumulated). 30 table-driven tests all passing.
- Step 2: VOICE.md parser — scaffold exists (`internal/voice/`), not yet Phase 1 implementation
- Step 3: `~/.anthem/` bootstrapping — not started
- Step 4: `anthem init` — not started
- Step 5: GitHubTracker — scaffold stub exists, not implemented
- Step 6: Claude Code driver — scaffold stub exists, not implemented
- Step 7: Orchestrator loop — scaffold stub exists, not implemented
- Step 8: EventBus — mock exists, real implementation not started
- Step 9: CLI wiring — Cobra skeleton exists, not wired to orchestrator
- Step 10: E2E test — not started

**Next step**: Phase 1 step 2 — implement VOICE.md parser (read from `~/.anthem/VOICE.md`, section extraction, `[CORE]` tag detection, prepend to prompt with self-evolution instruction referencing workspace copy).

Update this section as phases are completed.

## Reference: OpenAI Symphony

When making implementation decisions, reference the Symphony codebase for proven patterns:
- Repository: https://github.com/openai/openai-agents-python
- Directory: `examples/agents/symphony/`
- Relevant patterns: orchestrator loop, tracker adapters, workspace isolation, config parsing from markdown front matter
