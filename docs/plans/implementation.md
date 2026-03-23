# Anthem -- Full Project Scaffold + Implementation Plan

## Design Decisions (Locked In)

- **Language**: Go (latest stable)
- **Module path**: `github.com/rauriemo/anthem`
- **Cross-platform**: Windows-first, all three OS from day 1 (build tags for process management)
- **Hybrid architecture**: Go daemon = mechanical reliability layer (polling, process mgmt, workspace, retry, state). Orchestrator agent (Phase 3) = intelligence layer (Claude with VOICE.md, user communication, task decomposition). Go daemon exposes tool interface for the orchestrator agent.
- **VOICE.md**: Global at `~/.anthem/VOICE.md`. Orchestrator-agent only (Phase 3) -- not applied to executor agents. Executors get harnesses (WORKFLOW.md, skills, MCP tools, constraints), not personality.
- **WORKFLOW.md location**: Per-project, typically `./WORKFLOW.md` in repo root
- **Global state root**: `~/.anthem/` (VOICE.md, constraints.yaml, state.json, voice-changelog.md)
- **GitHub auth**: `GITHUB_TOKEN` env var, fallback to `gh auth token` command. No custom credential storage.
- **Dashboard**: Deferred to Phase 3b (tech choice TBD)
- **Voice changelog**: Changelog at `~/.anthem/voice-changelog.md`, wired in Phase 3a via `update_voice` contract action
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
      watcher.go                     # fsnotify hot-reload, debounced directory watcher
    voice/
      voice.go                       # VOICE.md parser, section extraction
      merge.go                       # Section merge logic (used by orchestrator agent in Phase 3)
      changelog.go                   # Voice change logging with reasons (Phase 3)
    tracker/
      tracker.go                     # IssueTracker interface definition
      github/
        github.go                    # GitHubTracker adapter (go-github)
      local/
        local.go                     # LocalJSONTracker adapter (tasks.json)
    orchestrator/
      orchestrator.go                # Core loop: poll, sort, claim, dispatch, reconcile, shutdown
      retry.go                       # RetryInfo, exponential backoff, retry eligibility
      state.go                       # State persistence (~/.anthem/state.json), LoadAndReconcile
      events.go                      # EventBus interface + in-process implementation
      contract.go                    # Phase 3a: action types, schemas, risk classification, validation
      contract_test.go               # Phase 3a: contract validation tests
      orchagent.go                   # Phase 3a: OrchestratorAgent session manager (Start/Consult/Refresh)
      orchagent_test.go              # Phase 3a: orchestrator session tests
    audit/
      audit.go                       # Phase 3a: AuditLogger interface + SQLite implementation
      schema.go                      # Phase 3a: SQLite schema and migrations
      audit_test.go                  # Phase 3a: audit log tests
    rules/
      engine.go                      # Rules engine: label matching, actions
      approval.go                    # require_approval, require_plan flows
    constraints/
      loader.go                      # User-level constraints loader (~/.anthem/constraints.yaml)
    workspace/
      manager.go                     # Directory creation, hooks, cleanup, CleanupTerminal
      safety.go                      # Path safety invariants (ValidatePath)
      lock.go                        # Concurrent file locking (FileLock)
    agent/
      agent.go                       # AgentRunner interface definition
      claude/
        driver.go                    # Claude Code CLI driver (stream-json, session resume)
        parser.go                    # stream-json event parser, cost extraction
        process.go                   # ProcessManager interface (no build tags)
        process_windows.go           # //go:build windows -- Job Object implementation
        process_unix.go              # //go:build !windows -- Process group implementation
    dashboard/
      server.go                      # HTTP server skeleton (Phase 3b)
      api.go                         # REST API route definitions (Phase 3b)
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
- `Continue(ctx, sessionID, prompt, opts ContinueOpts) (*RunResult, error)` -- Phase 3a: signature changes to accept ContinueOpts (workspace, permissions, stall timeout, allowed tools)
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
9. Define VoiceConfig struct, parser skeleton with section extraction
10. Wire up Cobra CLI skeleton: `anthem init`, `anthem run`, `anthem validate`, `anthem status`, `anthem version`
11. Create `WORKFLOW.md.example`, `VOICE.md.example`, `testdata/` fixtures

### Phase 1: Foundation (Core Loop + GitHub + Claude Code + VOICE.md)

Single task end-to-end: poll GitHub Issues, render constraints + WORKFLOW.md prompt, spawn Claude Code, update issue on completion.

1. Implement WORKFLOW.md parser -- YAML front matter + Go template body with sprig function map, `$ENV_VAR` expansion, validation, `system:` block with safe defaults
2. Implement VOICE.md parser -- read from `~/.anthem/VOICE.md`, section extraction (voice wiring into prompt deferred to Phase 3 orchestrator agent)
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

### Phase 2: Go Daemon Reliability Layer (COMPLETE)

**Pre-Phase 2 changes completed**:
- `[CORE]` enforcement in VOICE.md replaced by two-tier constraints system (`~/.anthem/constraints.yaml` + `system.constraints` in WORKFLOW.md). VOICE.md is now pure personality. See `internal/constraints/` and the orchestrator's `buildConstraints()` / `buildFullPrompt()` functions.
- Quality audit: errcheck fixes, gofmt, unused field removal, ETag mutex, nil guards, strings.Builder optimization, .gitattributes.
- Agent permission model documented in architecture.md (section 8b).
- Deleted `internal/voice/core.go` (vestigial `[CORE]` code).
- Deleted `internal/orchestrator/dispatch.go` and `reconciler.go` (empty stubs -- real logic lives in `orchestrator.go`, matching Symphony's single-module pattern).
- Fixed 304 ETag bug: `etagTransport` now only caches list endpoints, preventing noisy WARN logs during reconciliation.

**Phase 2 implementation (all 6 steps complete):**

1. Rules engine -- TitlePattern regex matching with compiled cache in `Engine`, AutoAssign comment posting, MaxCost budget enforcement via `cost.Tracker` integration (adds `exceeded-budget` label on overspend). `require_plan` deferred to Phase 3.
2. Workspace manager -- production `Manager` in `internal/workspace/manager.go` replacing `MockWorkspaceManager`. Per-task directories under `workspace.root`, hook lifecycle (retry `before_run` 3x, fail `after_create`, warn-only `after_complete`), `CleanupTerminal` for startup cleanup. Cross-platform shell execution via `runtime.GOOS`.
3. Retry and backoff -- `RetryInfo` in `internal/orchestrator/retry.go`, exponential backoff `min(10s * 2^(attempt-1), max_retry_backoff_ms)`, `isRetryEligible` gate in tick loop, 1s continuation delay for retried tasks, stall detection in reconcile releasing claims beyond 2x stall timeout.
4. Graceful shutdown -- `sync.WaitGroup` drain of dispatch goroutines (10s timeout), `releaseClaims` with fresh 5s context, `saveState` before exit. `Shutdown()` method for testability.
5. State persistence -- `OrchestratorState` with versioned schema, atomic write (temp+rename) to `~/.anthem/state.json`, `LoadAndReconcile` on startup (restores retry queue skipping terminal tasks, restores cost sessions).
6. Config hot-reload -- `fsnotify` watcher on directory (catches editor delete+create), 100ms debounce, validates before applying, `ReloadConfig` under mutex, `configSnapshot` pattern for dispatch goroutines.

### Phase 3a: Contract + Audit + Orchestrator Core (COMPLETE)

Intelligence layer built using orchestrator-as-allocator architecture. Daemon is the authority; orchestrator proposes via contract. All 9 steps completed.

1. **Tool contract** (`internal/orchestrator/contract.go` + `contract_test.go`) -- 8 action types with schemas, risk classification (low/medium/high), ValidateAction, IsIdempotent, SchemaOnly. Schema-only actions (create_subtasks, promote_knowledge) return ErrNotImplemented.
2. **SQLite audit log** (`internal/audit/audit.go`, `schema.go`, `audit_test.go`) -- append-only event log at `~/.anthem/audit.db` via `modernc.org/sqlite`. AuditLogger interface: Record, Query, RecentByTask, SummaryForWave, Close. WAL mode, mutex-serialized writes. Injected into Orchestrator, closed on shutdown.
3. **Task lifecycle state machine** (`internal/types/task.go` + `task_test.go`) -- 10 formalized states (queued, planned, running, blocked, retryQueued, needsApproval, completed, failed, canceled, skipped). Transition(from, to) validation. StatusToLabel/LabelToStatus mapping. TerminalReason field. All references to StatusActive/StatusPending replaced.
4. **Executor prompt fix** (`internal/orchestrator/orchestrator.go`) -- removed VOICE.md from buildFullPrompt. Executors get constraints + WORKFLOW.md only.
5. **Agent driver fix** (`internal/agent/claude/driver.go`, `agent.go`, `mock.go`, `types/task.go`, `config.go`) -- config-driven PermissionMode (default dontAsk), DeniedTools. ContinueOpts on Continue(). RunResult.Output populated from result stream event. PermissionMode/SkipPermissions/DeniedTools in AgentConfig.
6. **Orchestrator session manager** (`internal/orchestrator/orchagent.go` + `orchagent_test.go`) -- OrchestratorAgent: Start, Consult, Refresh. StateSnapshot with Serialize(). parseActions with brace-counting. ConsultWithRepair repair loop. Token tracking for refresh threshold.
7. **Tick loop wiring** (`orchestrator.go`, `config.go`, `main.go`) -- OrchestratorConfig (enabled, max_context_tokens, stall_timeout_ms). Dirty-snapshot gating (SHA256 hash). Wave struct with frontier exhaustion. mechanicalDispatch fallback. executeActions validates against contract. main.go creates audit logger + orchestrator agent.
8. **Voice self-evolution** (`orchestrator.go`) -- executeUpdateVoice: voice.LoadFile -> voice.Merge -> write -> voice.AppendChangelog -> audit event. Updates in-memory voiceContent on Orchestrator and OrchestratorAgent via SetVoiceContent.
9. **Documentation** -- CLAUDE.md, architecture.md, implementation.md, README.md all updated.

### Phase 3b: Dashboard + Advanced Features

1. Garbage collection from audit log (repeated failures, doc staleness, architecture violations)
2. Knowledge promotion to `docs/exec-plans/completed/`
3. Plans as DAG artifacts with dependency edges
4. Dashboard + status API + WebSocket streaming via EventBus
5. `require_plan` rule with pause/resume flow
6. Drift detection from audit log queries
7. Task decomposition (user describes feature, orchestrator breaks into subtasks)

### Phase 4: Polish + Community

1. Example WORKFLOW.md + VOICE.md templates
2. README, CONTRIBUTING.md
3. CI/CD pipeline, cross-platform release binaries via GoReleaser (Windows/macOS/Linux)
4. Code sign Windows release binaries in GitHub Actions (SignPath.io -- free for OSS, or Azure Trusted Signing)
5. Demo video

### Future Enhancements (Post Phase 4)

- GitHub webhook support as alternative to polling (instant detection, lower API usage)
- GitHub App authentication for production/org use
- Multi-instance distributed claim locking

## Dependencies (go.mod)

Current:
- `github.com/spf13/cobra` -- CLI framework
- `github.com/google/go-github/v68` -- GitHub API client
- `golang.org/x/oauth2` -- GitHub token auth
- `gopkg.in/yaml.v3` -- YAML parsing for WORKFLOW.md front matter
- `github.com/Masterminds/sprig/v3` -- Template function library (same as Helm) for WORKFLOW.md body rendering

Added in Phase 2:
- `github.com/fsnotify/fsnotify` -- File watching for config hot-reload

Added in Phase 3a:
- `modernc.org/sqlite` -- Pure Go SQLite for canonical audit log (no CGo, cross-platform)

## Testing Strategy

- **Unit tests**: Table-driven, interface-based mocks. Every package has `*_test.go` files. No external mocking framework -- just simple structs that satisfy interfaces.
- **Integration tests**: Tagged with `//go:build integration`. Require real GitHub API or Claude Code CLI. Skipped in default `go test ./...`.
- **E2E tests**: Use `LocalJSONTracker` + mock `AgentRunner` to test the full orchestrator loop without external dependencies.
- **Test fixtures**: `testdata/` directories with sample WORKFLOW.md, VOICE.md, and tasks.json files.
- **CI**: GitHub Actions runs `go test ./...`, `go vet ./...`, `golangci-lint run` on every PR.

## Developer Notes

- **Windows Smart App Control**: `go run` compiles to a temp directory, which Windows Smart App Control may block as an unsigned executable. Use `go build -o anthem.exe ./cmd/anthem` and run the binary directly instead.

## Reference: OpenAI Symphony

When in doubt about implementation patterns, reference Symphony's codebase:
- Repository: https://github.com/openai/symphony
- Language: Elixir (GenServer-based orchestrator)
- Spec: `SPEC.md` in repo root defines the language-agnostic service specification
- Key patterns: single-module orchestrator (all dispatch/reconcile/state in one module), tracker adapters, workspace isolation, config parsing from markdown front matter
- Key difference: Symphony has no personality/voice concept -- its orchestrator is pure code. Anthem adds the orchestrator agent layer on top (Phase 3).
