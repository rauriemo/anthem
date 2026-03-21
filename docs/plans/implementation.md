# Anthem -- Full Project Scaffold + Implementation Plan

## Design Decisions (Locked In)

- **Language**: Go (latest stable)
- **Module path**: `github.com/rauriemo/anthem`
- **Cross-platform**: Windows-first, all three OS from day 1 (build tags for process management)
- **VOICE.md location**: Global at `~/.anthem/VOICE.md` (shared across all projects, not per-project)
- **WORKFLOW.md location**: Per-project, typically `./WORKFLOW.md` in repo root
- **Global state root**: `~/.anthem/` (VOICE.md, state.json, voice-changelog.md)
- **GitHub auth**: `GITHUB_TOKEN` env var, fallback to `gh auth token` command. No custom credential storage.
- **Dashboard**: Deferred to Phase 3 (tech choice TBD)
- **Voice changelog**: Yes -- changelog file at `~/.anthem/voice-changelog.md` + issue comments when voice evolves
- **Testing**: Interface-based mocks (no mocking framework), table-driven tests, `//go:build integration` tagged tests for external services, `testdata/` fixtures, CI from day 1
- **Logging**: Use `log/slog` (Go stdlib) for structured logging
- **Error handling**: Wrap errors with `fmt.Errorf("context: %w", err)`. Never swallow errors silently.
- **No global state**: Pass dependencies via constructor injection.

## Scaffold Structure

```
anthem/
  cmd/anthem/
    main.go                          # Cobra CLI entrypoint
  internal/
    types/
      task.go                        # Task, RunResult, RunOpts, TokenCount, Status
    config/
      config.go                      # Config struct matching WORKFLOW.md YAML schema
      loader.go                      # YAML front matter parser + Go template body
      validator.go                   # Required field validation
      watcher.go                     # fsnotify hot-reload (stub for Phase 2)
    voice/
      voice.go                       # VOICE.md parser, section extraction
      core.go                        # [CORE] tag detection + immutability enforcement
      merge.go                       # Section merge logic for concurrent edits
      changelog.go                   # Voice change logging with reasons
    tracker/
      tracker.go                     # IssueTracker interface definition
      github/
        github.go                    # GitHubTracker adapter (go-github)
      local/
        local.go                     # LocalJSONTracker adapter (tasks.json)
    orchestrator/
      orchestrator.go                # Core loop: poll, sort, claim, dispatch
      reconciler.go                  # Active run reconciliation
      dispatch.go                    # Worker dispatch + concurrency control
      state.go                       # State persistence (~/.anthem/state.json)
      events.go                      # EventBus interface + in-process implementation
    rules/
      engine.go                      # Rules engine: label matching, actions
      approval.go                    # require_approval, require_plan flows
    workspace/
      manager.go                     # Directory creation, hooks, cleanup, VOICE.md copy
      safety.go                      # Path safety invariants
      lock.go                        # Concurrent file locking
    agent/
      agent.go                       # AgentRunner interface definition
      claude/
        driver.go                    # Claude Code CLI driver (stream-json, session resume)
        parser.go                    # stream-json event parser, cost extraction
        process.go                   # ProcessManager interface (no build tags)
        process_windows.go           # //go:build windows -- Job Object implementation
        process_unix.go              # //go:build !windows -- Process group implementation
    dashboard/
      server.go                      # HTTP server skeleton (Phase 3)
      api.go                         # REST API route definitions (Phase 3)
    logging/
      logger.go                      # Structured JSON logger (slog wrapper)
    cost/
      tracker.go                     # Token/cost accounting, budget enforcement
  testdata/
    workflow.md                      # Example WORKFLOW.md for tests
    voice.md                         # Example VOICE.md for tests
    tasks.json                       # Example tasks.json for LocalJSONTracker tests
  WORKFLOW.md.example                # User-facing example workflow
  VOICE.md.example                   # User-facing example voice
  go.mod
  go.sum
  Makefile                           # build, test, lint, install targets
  .github/workflows/ci.yml          # GitHub Actions: test, vet, lint on PR
  .golangci.yml                      # Linter config
  README.md
```

## Key Interfaces (defined in scaffold)

**`internal/tracker/tracker.go`** -- IssueTracker:
- `ListActive(ctx) ([]Task, error)`
- `GetTask(ctx, id) (*Task, error)`
- `UpdateStatus(ctx, id, status) error`
- `AddComment(ctx, id, body) error`
- `AddLabel(ctx, id, label) error`
- `RemoveLabel(ctx, id, label) error`

**`internal/agent/agent.go`** -- AgentRunner:
- `Run(ctx, RunOpts) (*RunResult, error)`
- `Continue(ctx, sessionID, prompt) (*RunResult, error)`
- `Kill(pid int) error`

**`internal/agent/claude/process.go`** -- ProcessManager:
- `Start(cmd *exec.Cmd) error`
- `Terminate(cmd *exec.Cmd) error`
- `Kill(cmd *exec.Cmd) error`

**`internal/workspace/manager.go`** -- WorkspaceManager:
- `Prepare(ctx, task) (workspacePath string, error)`
- `RunHook(ctx, hookName, workspacePath) error`
- `Cleanup(ctx, taskID) error`

**`internal/orchestrator/events.go`** -- EventBus:
- `Publish(event Event)`
- `Subscribe() <-chan Event`

## Implementation Order

### Scaffold (do first)

1. Initialize `go.mod` (`github.com/rauriemo/anthem`), create full directory tree
2. Create `Makefile` with build, test, lint, install targets
3. Create `.github/workflows/ci.yml` for GitHub Actions (go test, go vet, golangci-lint)
4. Create `.golangci.yml` linter config
5. Define all core domain types in `internal/types/` (Task, RunResult, RunOpts, TokenCount, Status enums)
6. Define all interfaces: IssueTracker, AgentRunner, ProcessManager, WorkspaceManager, EventBus
7. Create mock implementations of all interfaces for testing
8. Define Config struct matching WORKFLOW.md YAML schema (including `system:` block, `agent.max_concurrent`, `agent.max_concurrent_per_label`), parser skeleton, validator skeleton
9. Define VoiceConfig struct, parser skeleton with section extraction and [CORE] detection
10. Wire up Cobra CLI skeleton: `anthem init`, `anthem run`, `anthem validate`, `anthem status`, `anthem version`
11. Create `WORKFLOW.md.example`, `VOICE.md.example`, `testdata/` fixtures

### Phase 1: Foundation (Core Loop + GitHub + Claude Code + VOICE.md)

Single task end-to-end: poll GitHub Issues, render `~/.anthem/VOICE.md` + self-evolution instruction + `WORKFLOW.md` prompt, spawn Claude Code, update issue on completion.

1. Implement WORKFLOW.md parser -- YAML front matter + Go template body with sprig function map, `$ENV_VAR` expansion, validation, `system:` block with safe defaults
2. Implement VOICE.md parser -- read from `~/.anthem/VOICE.md`, section extraction, [CORE] tag detection, prepend to prompt with self-evolution instruction referencing **workspace copy** (`.anthem/VOICE.md`) injected between voice and task content
3. Implement `~/.anthem/` bootstrapping -- auto-create directory and default VOICE.md on first run, warn and continue if VOICE.md missing
4. Implement `anthem init` -- create starter `./WORKFLOW.md` from template + bootstrap `~/.anthem/VOICE.md`
5. Implement GitHubTracker -- ListActive, GetTask, UpdateStatus, AddComment, AddLabel, RemoveLabel
   - Auth: `GITHUB_TOKEN` env var, fallback to `gh auth token`
   - Rate limiting: parse `X-RateLimit-*` headers, use ETags for conditional requests, slow polling when near limit
6. Implement Claude Code driver -- stream-json output parsing, session resume, cost parsing, stall timeout
   - ProcessManager with platform implementations: `process_windows.go` (Job Objects) + `process_unix.go` (process groups)
7. Implement orchestrator loop -- poll, sort by priority/created_at/id, claim, dispatch, basic reconciliation
8. Implement EventBus -- in-process fan-out with buffered channels per subscriber. `Publish` must be **non-blocking** (drop oldest event if subscriber buffer full, log warning). The orchestrator loop must never stall on slow observers.
9. Wire up CLI commands to orchestrator (`anthem run`, `anthem validate`)
10. End-to-end test with mock tracker + mock agent runner

### Phase 2: Rules + Workspace + Self-Evolution

1. Rules engine -- label matching, require_approval, require_plan, auto_assign, max_cost
2. Workspace manager -- directory lifecycle, hooks with failure handling (retry before_run 3x, fail after_create, warn-only after_complete), path safety, concurrent file locking
3. VOICE.md copy-diff-merge -- copy `~/.anthem/VOICE.md` into workspace before run, diff after run, apply [CORE] enforcement, section merge for concurrent edits
4. Voice changelog -- log changes to `~/.anthem/voice-changelog.md` + post diff as issue comment
5. Workflow self-modification guardrail
6. Retry/backoff, stall recovery
7. Graceful shutdown -- cross-platform signal handling via ProcessManager, state persistence to `~/.anthem/state.json`, restart reconciliation
8. Config hot-reload via fsnotify

### Phase 3: Dashboard + Observability + Cost Tracking

1. Status HTTP API endpoints
2. Web dashboard (tech TBD -- HTMX or SPA)
3. WebSocket event stream (subscribes to EventBus)
4. Structured JSON logging via slog with configurable sinks
5. Cost tracking (per-task, aggregate, budget enforcement)
6. Voice/rule change review UI

### Phase 4: Polish + Community

1. LocalJSON tracker adapter (for offline/testing use)
2. Example WORKFLOW.md + VOICE.md templates
3. README, CONTRIBUTING.md
4. CI/CD pipeline, cross-platform release binaries via GoReleaser (Windows/macOS/Linux)
5. Demo video

### Future Enhancements (Post Phase 4)

- GitHub webhook support as alternative to polling (instant detection, lower API usage)
- GitHub App authentication for production/org use
- Multi-instance distributed claim locking

## Dependencies (go.mod)

- `github.com/spf13/cobra` -- CLI framework
- `github.com/google/go-github` -- GitHub API client (use latest stable version at scaffold time)
- `golang.org/x/oauth2` -- GitHub token auth
- `github.com/fsnotify/fsnotify` -- File watching for hot-reload
- `gopkg.in/yaml.v3` -- YAML parsing for WORKFLOW.md front matter
- `github.com/Masterminds/sprig/v3` -- Template function library (same as Helm) for WORKFLOW.md body rendering
- `github.com/stretchr/testify` -- Test assertions (standard in Go OSS)
- `golang.org/x/sync` -- errgroup for concurrent operations

## Testing Strategy

- **Unit tests**: Table-driven, interface-based mocks. Every package has `*_test.go` files. No external mocking framework -- just simple structs that satisfy interfaces.
- **Integration tests**: Tagged with `//go:build integration`. Require real GitHub API or Claude Code CLI. Skipped in default `go test ./...`.
- **E2E tests**: Use `LocalJSONTracker` + mock `AgentRunner` to test the full orchestrator loop without external dependencies.
- **Test fixtures**: `testdata/` directories with sample WORKFLOW.md, VOICE.md, and tasks.json files.
- **CI**: GitHub Actions runs `go test ./...`, `go vet ./...`, `golangci-lint run` on every PR.

## Reference: OpenAI Symphony

When in doubt about implementation patterns, reference Symphony's codebase:
- Repository: https://github.com/openai/openai-agents-python
- Directory: `examples/agents/symphony/`
- Key patterns: orchestrator loop, tracker adapters, workspace isolation, config parsing
