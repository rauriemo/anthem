# Anthem -- Claude Agent Orchestrator

An open-source alternative to OpenAI Symphony, built in Go, designed for Claude Code users.

## Design Decisions (Locked In)

- **Language**: Go (latest stable)
- **Module path**: `github.com/rauriemo/anthem`
- **Cross-platform**: Windows-first, all three OS from day 1 (build tags for process management)
- **Hybrid architecture**: Go daemon handles the mechanical reliability layer (polling, process management, workspace isolation, retry, state persistence). An AI orchestrator agent (Phase 3) sits on top for intelligence -- user communication, task decomposition, parallel planning. The Go daemon exposes a tool interface for the orchestrator agent to call.
- **VOICE.md**: Global at `~/.anthem/VOICE.md`. Applies only to the orchestrator agent (Phase 3), not executor agents. Executors get project context from WORKFLOW.md, skills, and MCP tools -- harnesses, not personality. Voice gives the orchestrator personality for user communication and helps it learn the user's preferences for better task management.
- **WORKFLOW.md location**: Per-project, typically `./WORKFLOW.md` in repo root
- **Global state root**: `~/.anthem/` (VOICE.md, constraints.yaml, state.json, voice-changelog.md)
- **GitHub auth**: `GITHUB_TOKEN` env var, fallback to `gh auth token` command. No custom credential storage.
- **Dashboard**: Deferred to Phase 4 (tech choice TBD between HTMX and SPA)
- **Voice changelog**: Changelog file at `~/.anthem/voice-changelog.md`, wired in Phase 3a via the `update_voice` contract action
- **Testing**: Interface-based mocks (no mocking framework), table-driven tests, `//go:build integration` tagged tests for external services, `testdata/` fixtures, CI from day 1
- **Logging**: Use `log/slog` (stdlib) for structured logging
- **Orchestrator-as-allocator (Phase 3a)**: The orchestrator agent is a stateless allocator -- it proposes actions, the Go daemon validates and executes them. The daemon is the authority. If the orchestrator fails, the daemon falls back to Phase 2 mechanical dispatch.
- **Contract-first tool surface (Phase 3a)**: Orchestrator-daemon communication uses a stable contract of explicitly defined action types with schemas, risk levels, and idempotency guarantees. No read-actions -- the daemon pushes state via compact snapshots. Transport is JSON structured output now, MCP later.
- **Three-layer state model (Phase 3a)**: (1) Event Log -- append-only SQLite audit log at `~/.anthem/audit.db`. (2) State Snapshot -- compact in-memory view pushed to orchestrator. (3) Knowledge Artifacts -- curated summaries in repo `docs/exec-plans/`. Operations log lives with the daemon; reasoning memory lives with the repo.
- **SQLite audit log (Phase 3a)**: `modernc.org/sqlite` (pure Go, no CGo). Records dispatches, retries, cancellations, cost events, wave transitions, orchestrator actions, voice updates.
- **Wave model (Phase 3a)**: Orchestrator plans tasks in waves. Wave boundary = current planned frontier exhausted (all tasks terminal or non-runnable). Daemon detects exhaustion, prompts orchestrator to replan.
- **Task lifecycle state machine (Phase 3a)**: Formalized states: queued, planned, running, blocked, retryQueued, needsApproval, completed, failed, canceled, skipped. Explicit `Transition(from, to)` validation enforced by daemon. `StatusToLabel()` / `LabelToStatus()` mapping between internal states and tracker labels.
- **Modular channel system (Phase 3b)**: Two-way communication between orchestrator agent and user via pluggable channel adapters. `Channel` interface (`Kind`, `Start`, `Send`, `Incoming`, `Close`) mirrors the `IssueTracker` adapter pattern. Global credentials in `~/.anthem/channels.yaml`, per-project channel targets in WORKFLOW.md `channels:` block. One conversation per project (chat history scoped to repo). Slack (Socket Mode) ships first; WhatsApp deferred to Phase 4 (needs HTTP server for webhooks).
- **Multi-format task decomposition (Phase 3b)**: User sends feature descriptions through channels as plain text prompts, markdown files, mermaid flowcharts, diagrams, or images. The orchestrator agent decomposes into GitHub issues via the `create_subtasks` contract action. Claude's multimodal capabilities handle image-based inputs.
- **Audit-log maintenance signals (Phase 3b)**: Periodic scanner queries `audit.db` for health signals (repeated failures, stale tasks, budget anomalies, drift). Notifies user via channel with approval gate. Configurable auto-approve per maintenance type in WORKFLOW.md `maintenance:` block.

## Architecture Overview

Six layers (mirroring Symphony's proven design, adapted for Claude):

```mermaid
graph TD
  subgraph policy [Policy Layer]
    WF["WORKFLOW.md - per-project"]
    VOICE["VOICE.md - ~/.anthem/VOICE.md global"]
  end
  subgraph config [Config Layer]
    CFG["Config Loader + Validator"]
  end
  subgraph coordination [Coordination Layer]
    ORCH["Orchestrator Loop"]
    RULES["Rules Engine"]
    EVENTS["Event Bus"]
  end
  subgraph execution [Execution Layer]
    WS["Workspace Manager"]
    AGENT["Agent Runner - Claude Code"]
  end
  subgraph integration [Integration Layer]
    GH["GitHub Adapter"]
    LIN["Linear Adapter"]
    JSON["Local JSON Adapter"]
  end
  subgraph observability [Observability Layer]
    LOG["Structured Logger - slog"]
    DASH["Web Dashboard"]
    API["Status API"]
  end

  WF --> CFG
  VOICE --> CFG
  CFG --> ORCH
  ORCH --> RULES
  ORCH --> WS
  ORCH --> AGENT
  ORCH --> GH
  ORCH --> LIN
  ORCH --> JSON
  ORCH --> EVENTS
  EVENTS --> LOG
  EVENTS --> DASH
  EVENTS --> API
```

## Core Components

### 1. WORKFLOW.md (Policy Layer)

Same contract as Symphony: YAML front matter for configuration + markdown body as the prompt template rendered per task. Lives in each project root (`./WORKFLOW.md`).

```yaml
---
tracker:
  kind: github           # github | linear | local_json
  repo: "user/repo"      # GitHub: owner/repo
  labels:
    active: ["todo", "in-progress"]
    terminal: ["done", "canceled"]

polling:
  interval_ms: 10000

workspace:
  root: "./workspaces"

hooks:
  after_create: "git clone {{issue.repo_url}} ."
  before_run: "git pull origin main"

agent:
  command: "claude"       # Claude Code CLI
  max_turns: 5
  max_concurrent: 3                    # global cap on simultaneous agents, default 3
  max_concurrent_per_label:            # optional per-label caps
    planning: 1
  stall_timeout_ms: 300000
  max_retry_backoff_ms: 300000
  permission_mode: "dontAsk"           # default safe mode; "bypassPermissions" for trusted
  skip_permissions: false              # shorthand: true = bypassPermissions
  allowed_tools:                       # tools auto-approved in dontAsk mode
    - "Read"
    - "Edit"
    - "Grep"
    - "Glob"
    - "Bash(git *)"
    - "Bash(go test *)"

rules:
  - match:
      labels: ["planning"]
    action: require_approval
    approval_label: "approved"
  - match:
      labels: ["bug"]
    action: auto_assign

system:
  workflow_changes_require_approval: true   # default: true
  constraints:
    - "Follow the project existing code style and conventions"
    - "Run tests before opening a PR"

server:
  port: 8080
---

You are an expert software engineer working on {{issue.title}}.

Repository: {{issue.repo_url}}
Branch: anthem/{{issue.identifier}}

## Task
{{issue.body}}

## Rules
- Create a branch named `anthem/{{issue.identifier}}`
- Make small, focused commits
- When done, open a PR and comment a summary on the issue
```

### 2. VOICE.md (Personality Layer)

Anthem's differentiator: a self-evolving personality system inspired by OpenClaw's SOUL.md. Unlike Symphony (which has no personality concept), Anthem's orchestrator agent communicates with the user through a consistent identity, tone, and awareness.

**Scope**: VOICE.md applies only to the **orchestrator agent** (Phase 3), not executor agents. Executor agents are headless coding workers that receive project context from WORKFLOW.md, skills, and MCP tools -- they get harnesses, not personality. The orchestrator agent uses VOICE.md for two purposes: (1) communicating with the user in an appealing style, and (2) understanding the user's preferences and working patterns for better task management decisions.

**Location**: Global at `~/.anthem/VOICE.md`. The voice is the same across all projects -- it defines who the orchestrator is and how it relates to the user, which doesn't change between repos. WORKFLOW.md is project-specific; VOICE.md is user-specific.

**Pure personality**: VOICE.md contains only personality-related sections (Identity, Personality, User Context). Safety guardrails are handled by the separate constraints system (see below). This separation means the orchestrator agent can freely evolve personality without risk of removing safety rules.

**Bootstrapping**: On first run, if `~/.anthem/` doesn't exist, Anthem auto-creates it and writes a default `VOICE.md` template. The `anthem init` command creates `~/.anthem/VOICE.md`, `~/.anthem/constraints.yaml`, and a starter `./WORKFLOW.md`.

**Example VOICE.md:**

```markdown
# Voice

## Identity
Name: Aria
Role: Senior engineer and pair programmer
Specialty: Pragmatic problem-solving, ships fast

## Personality
- Direct and opinionated. Skip pleasantries, get to the point.
- Use dry humor when things go sideways.
- Think out loud like a pair programmer when explaining decisions.
- Prefer shipping over perfection. Call out over-engineering.
- Never say "Great question!" or "I'd be happy to help."

## User Context
- Prefers visual feedback quickly over perfect code.
- Iterates fast and prefers small, focused commits.
- Uses conventional commit format.
- Often works late; keep responses concise.
```

**Current state (Phase 1-2)**: VOICE.md is parsed and loaded by the Go daemon. It is available in the prompt pipeline but is not yet used by an AI orchestrator agent. The parsing, section extraction, and merge utilities are implemented in `internal/voice/`.

**Phase 3 -- orchestrator agent integration**: The orchestrator agent (a persistent Claude session) will use VOICE.md for personality when communicating with users via issue comments, status updates, and task management decisions. Self-evolution happens as the orchestrator agent learns the user's preferences over time.

**Self-evolution mechanism (Phase 3, copy-diff-merge):**

1. The orchestrator agent session has access to `~/.anthem/VOICE.md`
2. As it interacts with the user and observes patterns, it updates VOICE.md sections
3. Changes are applied via section-level merge logic (`internal/voice/merge.go`)
4. Every change is logged to `~/.anthem/voice-changelog.md` with timestamps, reason, and diff (`internal/voice/changelog.go`)

**Self-evolution examples:**

- After the user repeatedly asks for shorter explanations: adds "Keep explanations under 3 sentences" to Personality
- After working on several Unity tasks: adds "User's project uses Unity URP with isometric tilemaps" to User Context
- After the user rejects a refactor: adds "User prefers incremental changes over large refactors" to User Context

### 2b. Constraints (Safety Layer)

Safety guardrails are separated from personality into a two-tier constraints system:

**User-level constraints** (`~/.anthem/constraints.yaml`):
```yaml
constraints:
  - "Never force-push to main or master"
  - "Never commit secrets, credentials, API keys, or tokens"
  - "Always create a branch for changes -- never commit directly to main"
  - "Never run destructive commands without confirmation"
```

**Project-level constraints** (`system.constraints` in WORKFLOW.md):
```yaml
system:
  workflow_changes_require_approval: true
  constraints:
    - "Follow the project existing code style and conventions"
    - "Run tests before opening a PR"
    - "Keep commits small and focused on a single concern"
```

**How it works:**

- Both constraint tiers are combined under a `## Constraints (non-negotiable)` header in the prompt
- Anthem always appends a hardcoded **meta-constraint**: "Do not modify constraint definitions in WORKFLOW.md system.constraints or ~/.anthem/constraints.yaml" -- this prevents agents from removing their own guardrails
- Constraints are placed between voice content and the task template in the prompt
- Missing `constraints.yaml` is not an error -- Anthem continues with empty user constraints
- The `anthem init` and auto-bootstrap both create a default `constraints.yaml`

This design separates concerns: personality evolves freely, safety rules are immutable.

### 3. Config Loader + Validator

- Parses `WORKFLOW.md` front matter (YAML) and body (Go template)
- Supports `$ENV_VAR` indirection in YAML values
- Validates required fields before dispatch (tracker kind, agent command, workspace root)
- Applies safe defaults for `system:` block if not specified
- Template engine uses **sprig** (`github.com/Masterminds/sprig`) function map for rich template functions (`lower`, `upper`, `replace`, `default`, `join`, `trimPrefix`, etc.) -- same library used by Helm
- Hot-reloads on file change (fsnotify) -- keeps last valid config on parse failure

### 4. Issue Tracker Interface (Integration Layer)

```go
type Task struct {
    ID             string
    Identifier     string   // e.g. "GH-42" or "PROJ-123"
    Title          string
    Body           string
    Labels         []string
    Status         Status   // queued|planned|running|blocked|retryQueued|needsApproval|completed|failed|canceled|skipped
    Priority       int
    CreatedAt      time.Time
    RepoURL        string   // for workspace population
    Metadata       map[string]string
    TerminalReason string
}

type IssueTracker interface {
    ListActive(ctx context.Context) ([]Task, error)
    GetTask(ctx context.Context, id string) (*Task, error)
    UpdateStatus(ctx context.Context, id string, status string) error
    AddComment(ctx context.Context, id string, body string) error
    AddLabel(ctx context.Context, id string, label string) error
    RemoveLabel(ctx context.Context, id string, label string) error
}
```

**Shipped adapters:**

- `GitHubTracker` -- default, uses GitHub REST/GraphQL API via `go-github`
- `LocalJSONTracker` -- offline testing, reads/writes a `tasks.json` file
- Future community adapters: Linear, Trello, Jira

**GitHub authentication:**

- Primary: `GITHUB_TOKEN` environment variable (standard across CI systems, GitHub Actions, etc.)
- Fallback: shell out to `gh auth token` to piggyback on the user's existing gh CLI login
- No custom credential storage in `~/.anthem/` -- env vars and gh CLI are both safer than plaintext tokens

**GitHub API rate limiting:**

- Parse `X-RateLimit-Remaining` and `X-RateLimit-Reset` from every GitHub API response (`go-github` exposes this natively)
- When remaining drops below 10%, slow polling frequency to the reset time
- Use `If-None-Match` / ETags for `ListActive` calls -- GitHub returns `304 Not Modified` at no rate limit cost
- Log a warning when rate limiting kicks in

### 5. Orchestrator Loop (Coordination Layer)

Core loop runs every `polling.interval_ms`:

```mermaid
flowchart TD
  START["Tick Start"] --> RECONCILE["Reconcile Active Runs"]
  RECONCILE --> PREFLIGHT["Preflight Validate Config"]
  PREFLIGHT --> FETCH["Fetch Active Tasks"]
  FETCH --> SORT["Sort: priority, created_at, id"]
  SORT --> ELIGIBLE{"Eligible for dispatch?"}
  ELIGIBLE -->|Not running, not claimed, slots available| RULES_CHECK["Evaluate Rules"]
  ELIGIBLE -->|Skip| PUBLISH["Publish Events"]
  RULES_CHECK -->|Requires approval + not approved| SKIP["Skip, add waiting label"]
  RULES_CHECK -->|Passes| DISPATCH["Claim + Dispatch Worker"]
  DISPATCH --> PUBLISH
  SKIP --> PUBLISH
```

**Concurrency control:**

- Global max concurrent agents (`agent.max_concurrent`, default 3)
- Per-label caps (`agent.max_concurrent_per_label`, e.g., max 1 "planning" task at a time)
- In-memory claim map prevents double-dispatch

**Event bus:**

The orchestrator publishes events to an in-process event bus for dashboard/API consumption:

```go
type Event struct {
    Type      string    // "task.claimed", "task.completed", "agent.started", etc.
    TaskID    string
    Timestamp time.Time
    Data      any
}

type EventBus interface {
    Publish(event Event)
    Subscribe() <-chan Event
}
```

Implementation is a simple fan-out channel -- no external message broker needed for a single-binary tool. The dashboard and API subscribe to the event bus for real-time updates.

**Critical**: `Publish` must be **non-blocking**. The orchestrator loop calls `Publish` on every tick -- if a slow dashboard subscriber causes `Publish` to block, it stalls polling and dispatch. Implementation uses buffered channels per subscriber. If a subscriber's buffer is full, drop the oldest event and log a warning. The orchestrator's core loop must never be gated on observability consumers.

**Phase 3a changes to the orchestrator loop**: The `tick()` method is extended to optionally consult the orchestrator agent before dispatch. The flow becomes: reconcile -> fetch tasks -> build StateSnapshot -> check if snapshot changed (dirty-snapshot gating) -> if changed and orchestrator enabled, consult orchestrator -> validate returned actions against contract -> execute actions -> audit log. If the orchestrator is disabled, nil, or fails, the daemon falls back to Phase 2 mechanical dispatch (dispatch every eligible task). All dispatches (orchestrator-directed and fallback) are recorded in the audit log.

### 6. Rules Engine

Evaluated per-task before dispatch:

- **`require_approval`** -- Task must have an approval label before an agent is spawned. If missing, orchestrator adds a "waiting-for-approval" label and skips.
- **`auto_assign`** -- Automatically claim and dispatch without approval.
- **`require_plan`** -- Agent's first turn must produce a `plan.md`; orchestrator pauses execution and requests human approval before continuing.
- **`max_cost`** -- Token budget per task (tracked via Claude Code output).
- Rules are defined in `WORKFLOW.md` front matter, matched by labels, title patterns, or custom fields.

**System-level guardrail -- workflow self-modification:**

Agents can add/modify rules by editing `WORKFLOW.md` (e.g., via a task like "add a rule requiring approval for architecture labels"). A built-in meta-rule protects this:

```yaml
system:
  workflow_changes_require_approval: true  # default: true
```

When `true` (default):

- After an agent run completes, Anthem diffs the workspace `WORKFLOW.md` against the active config
- If changed, Anthem posts the diff as an issue comment for human review
- The new rules do NOT take effect until a human adds an approval label (e.g., `rule-approved`)
- On approval, Anthem applies the changes and hot-reloads

When `false` (user opt-in):

- Changes to `WORKFLOW.md` by agents are applied and hot-reloaded immediately with no approval gate

This ensures new users are protected by default while experienced users can remove the guardrail.

### 7. Workspace Manager (Execution Layer)

- One directory per task under `workspace.root`, named from sanitized task identifier
- Reused across retries (not deleted on success)
- Lifecycle hooks: `after_create` (clone/setup), `before_run` (pull/sync), `after_complete` (cleanup)
- Hard invariant: agent subprocess cwd = workspace path, which must resolve under workspace root
- Startup cleanup: fetch terminal tasks, remove their workspace dirs

**Hook failure handling:**

- `after_create` failure (e.g., `git clone` fails): Mark task as failed, clean up workspace, retry with backoff
- `before_run` failure (e.g., `git pull` fails): Retry the hook up to 3 times with short delays (network blip). If still failing, mark task as failed with backoff.
- `after_complete` failure: Log a warning but don't fail the task -- the work is already done. Cleanup is not critical path.

### 8. Agent Runner -- Claude Code Driver (Execution Layer)

Spawns Claude Code CLI in print mode (non-interactive):

```go
type AgentRunner interface {
    Run(ctx context.Context, opts RunOpts) (*RunResult, error)
    Continue(ctx context.Context, sessionID string, prompt string, opts ContinueOpts) (*RunResult, error)
    Kill(pid int) error
}

type RunOpts struct {
    WorkspacePath  string
    Prompt         string        // constraints + rendered WORKFLOW.md template
    MaxTurns       int
    AllowedTools   []string      // tool allowlist for auto-approval
    MCPConfig      string        // path to MCP server config file
    Model          string        // claude model override (optional)
    StallTimeoutMS int
    PermissionMode string        // "dontAsk" (default) or "bypassPermissions"
    DeniedTools    []string      // explicit tool deny list
}

type ContinueOpts struct {
    WorkspacePath  string
    StallTimeoutMS int
    AllowedTools   []string
    PermissionMode string
}

type RunResult struct {
    SessionID   string
    ExitCode    int
    Output      string          // response text from result stream event
    TokensIn    int
    TokensOut   int
    CostUSD     float64         // parsed from Claude's native cost output
    TurnsUsed   int
    Duration    time.Duration
}
```

**Actual Claude Code CLI invocation:**

```bash
# First run -- new session
claude -p "prompt here" \
  --output-format stream-json \
  --max-turns 10 \
  --allowedTools "Edit,Write,Shell,Grep" \
  --model claude-sonnet-4-20250514

# Continuation -- resume existing session
claude -p "continue working on the task" \
  --output-format stream-json \
  --resume SESSION_ID
```

Key implementation details:

- Uses `--output-format stream-json` for real-time output streaming (newline-delimited JSON events)
- Parses `{"type":"result"}` events for token counts, cost, and session ID
- Known bug: Claude Code may hang after final result event in stream-json mode; agent runner implements a post-result timeout (5s) and force-kills the process
- Multi-turn continuation uses `--resume SESSION_ID` to maintain context across turns
- `--allowedTools` auto-approves specified tools so the agent runs without interactive prompts
- MCP servers configured in `WORKFLOW.md` are written to a temp JSON file and passed via Claude Code's MCP config mechanism
- Stall detection: kills process if no stdout activity for `stall_timeout_ms`

**Phase 3a driver fixes (complete)**: `Run()` uses config-driven `PermissionMode` (default `dontAsk`) instead of hardcoded `--dangerously-skip-permissions`. Supports `DeniedTools` via `--deniedTools` flags. `Continue()` accepts `ContinueOpts` with workspace, stall timeout, allowed tools, and permission mode. `parseStdout` populates `RunResult.Output` from the `result` stream event's response text (string or content block array), required for the orchestrator session manager to parse actions.

**Cross-platform process management:**

```go
type ProcessManager interface {
    Start(cmd *exec.Cmd) error      // configures process group/job object + starts
    Terminate(cmd *exec.Cmd) error  // graceful termination
    Kill(cmd *exec.Cmd) error       // force kill entire process tree
}
```

- `process.go` defines the `ProcessManager` interface and shared types (no build tags)
- `process_windows.go` (`//go:build windows`): Uses Job Objects to manage the Claude Code process tree. `Start` creates a Job Object and assigns the process. `Terminate`/`Kill` calls `TerminateJobObject` to kill the entire tree.
- `process_unix.go` (`//go:build !windows`): Uses process groups. `Start` sets `SysProcAttr{Setpgid: true}`. `Terminate` sends `SIGTERM` to the group. `Kill` sends `SIGKILL` to the group.
- The Claude Code driver takes a `ProcessManager` via constructor injection.

### 8b. Agent Permission Model

Anthem uses Claude Code's built-in permission system to control what executor agents can do, with a safe default and an opt-in trusted mode.

**Two modes:**

| Mode | Claude Code Flags | Behavior |
|------|-------------------|----------|
| **Safe (default)** | `--permission-mode dontAsk` + `--allowedTools` from config | Agent can only use explicitly whitelisted tools. Everything else is auto-denied without hanging. |
| **Trusted** | `--dangerously-skip-permissions` | Full autonomy, no permission checks. Opt-in via `agent.skip_permissions: true` in WORKFLOW.md. |

The safe default uses Claude Code's `dontAsk` mode, which auto-denies any tool not in the allow list. This is critical for headless execution -- denied tools return an error to Claude (no interactive prompt), so the agent never hangs waiting for input. Claude sees the denial and either tries an alternative approach or reports that it couldn't complete the step.

**WORKFLOW.md configuration:**

```yaml
agent:
  command: "claude"
  permission_mode: "dontAsk"         # default; or "bypassPermissions" for trusted
  skip_permissions: false             # shorthand: when true, overrides to bypassPermissions
  allowed_tools:                      # tools auto-approved in dontAsk mode
    - "Read"
    - "Edit"
    - "Grep"
    - "Glob"
    - "Bash(git *)"
    - "Bash(go test *)"
    - "Bash(go build *)"
  denied_tools:                       # explicit deny (overrides allow)
    - "Bash(git push --force *)"
    - "Bash(rm -rf *)"
```

Tool rules follow Claude Code's permission rule syntax: `Bash(npm run *)` allows any command starting with `npm run`, `Edit(/src/**)` restricts edits to the src directory, `WebFetch(domain:github.com)` allows fetching from GitHub only. Deny rules always take precedence over allow rules.

**Permission-blocked task flow:**

When an agent hits a permission wall in safe mode, the orchestrator detects the blocked state and moves the task to a `needs-permission` status so a human can intervene:

```mermaid
stateDiagram-v2
    todo: TODO
    inProgress: IN_PROGRESS
    needsPerm: NEEDS_PERMISSION
    done: DONE

    todo --> inProgress: Anthem claims task
    inProgress --> done: Agent completes successfully
    inProgress --> needsPerm: Agent reports permission denial
    needsPerm --> todo: User approves, task re-queued
    todo --> inProgress: Anthem resumes session
```

Detection: when Claude Code completes a run in `dontAsk` mode and the result indicates the task is incomplete due to denied tools, the orchestrator:

1. Adds a `needs-permission` label to the issue
2. Posts a comment explaining what was blocked (e.g., "Agent needed `Bash(npm install)` but it's not in allowed_tools")
3. Saves the session ID for later resume
4. Removes `in-progress` label -- the task waits for human action

**Unblocking a permission-blocked task:**

A user can unblock in three ways:

1. **Update allowed_tools** in WORKFLOW.md to permanently whitelist the needed tool (e.g., add `Bash(npm install)`)
2. **Switch to trusted mode** for a specific task by adding a label like `trusted` that the rules engine maps to `skip_permissions: true`
3. **Manually complete** the blocked step and move the card back to `todo` for the agent to continue the remaining work

When the task returns to `todo`, Anthem picks it up and uses `--resume <session_id>` to continue the Claude Code session where it left off, preserving all context from the previous run.

**Layered defense:**

The permission model works alongside (not instead of) the constraints system:

- **Process-level**: Claude Code's `dontAsk` mode + `--allowedTools` enforces which tools the agent can use
- **Prompt-level**: The constraints system injects non-negotiable rules into the prompt (e.g., "Never force-push to main")
- **Workspace-level**: The workspace manager sets `cmd.Dir` to the task's isolated directory, scoping file access

This layered approach means even if the agent tries to work around a prompt-level constraint, the process-level permission system blocks the actual tool invocation.

### 9. Retry and Backoff

- **Continuation (clean exit):** 1 second delay before re-eligibility check
- **Failure:** exponential backoff `min(10s * 2^(attempt-1), max_retry_backoff_ms)`
- On retry: re-fetch task, verify still active, dispatch if slots available, else requeue
- Stall timeout triggers termination + retry with backoff

### 10. Graceful Shutdown

On interrupt (Ctrl+C) or system stop:

- Send termination signal to all active Claude Code processes (they save session state)
  - Windows: `TerminateJobObject` on the Job Object containing the process tree
  - Unix: `SIGTERM` to the process group
- Wait up to 10s for clean exit, then force-kill
- Release all claims on the issue tracker (remove "in-progress" labels)
- Save orchestrator state to `~/.anthem/state.json` (active sessions, retry queues, token totals)
- On restart, load saved state and reconcile against the tracker (tasks may have changed while Anthem was down)

Cross-platform signal handling:

- `os.Interrupt` (Ctrl+C) works on all platforms in Go
- `syscall.SIGTERM` is available on Windows in Go's signal package
- Platform-specific cleanup logic isolated behind build tags

### 11. Concurrent File Safety

Multiple executor agents may attempt to edit shared files (e.g., `WORKFLOW.md`) simultaneously:

- Anthem holds an in-process mutex per protected file (`internal/workspace/lock.go`)
- After an agent run completes, diffs are applied sequentially (not in parallel)
- If two agents both propose `WORKFLOW.md` changes, the second one is queued and re-diffed against the already-applied first change
- `VOICE.md` is only modified by the orchestrator agent (Phase 3), which is a single session -- no concurrent write issues

### 12. Web Dashboard + Status API (Observability Layer)

Embedded Go HTTP server (no separate frontend build step):

- `GET /` -- Dashboard (tech TBD in Phase 3 -- server-rendered HTML + HTMX or embedded SPA)
- `GET /api/v1/state` -- JSON snapshot: running tasks, queued, retry queue, token totals
- `GET /api/v1/tasks/:id` -- Single task details + agent session history
- `POST /api/v1/refresh` -- Force immediate poll tick
- WebSocket `/ws` -- Real-time event stream (subscribes to the EventBus)

Dashboard shows:

- Active agents with live output streaming
- Task queue with priorities and labels
- Token usage and cost estimates
- Retry queue with next attempt times
- Historical runs with outcomes

### 13. Structured Logging

- JSON structured logs via `log/slog` (Go stdlib) to stdout + optional file sink
- Required fields: `task_id`, `task_identifier`, `session_id`, `event_type`
- Log levels: debug, info, warn, error
- Token accounting per session and aggregate

### 14. Cost Tracking

Claude Code's `--output-format json` returns native cost data per session. Anthem parses and aggregates this:

- Per-task: tokens in/out, cost USD, number of turns, duration
- Per-session aggregate: total spend, average cost per task, cost by label/category
- Budget enforcement: `max_cost` rule stops a task if its running total exceeds the budget
- Dashboard displays running cost with estimates (based on average cost per turn x remaining turns)
- Optional: daily/weekly spend alerts via issue comments or webhook

### 15. MCP + Skills Integration

Agents spawned by Anthem are extended through two complementary mechanisms:

**MCP Servers (tools -- the agent's hands):**

The orchestrator configures which MCP servers are available to each agent. These give Claude the ability to interact with external systems (Unity Editor, databases, APIs):

```yaml
agent:
  mcp_servers:
    - name: "unity"
      command: "npx"
      args: ["-y", "@anthropic/unity-mcp-server"]
    - name: "github"
      command: "npx"
      args: ["-y", "@anthropic/github-mcp-server"]
```

These are passed to Claude Code's `--mcp-config` flag. The orchestrator doesn't need to understand the MCP protocol -- it just configures which servers are available.

**Skills (knowledge -- the agent's training):**

Skills are `SKILL.md` files (markdown with YAML frontmatter) that teach the agent *how* to approach tasks. Claude Code discovers them automatically from two locations:

- `~/.claude/skills/` -- user's personal skills (available across all projects)
- `.claude/skills/` -- project-level skills (in the repo's workspace)

Anthem extends this with managed skills:

```yaml
agent:
  skills:
    - "anthem://pr-workflow"      # built-in: how to write good PRs
    - "anthem://plan-first"       # built-in: produce plan.md before coding
    - "./skills/unity-patterns"   # project-local skill directory
```

Built-in skills are copied into each workspace's `.claude/skills/` directory during the `after_create` hook. Project skills already in the repo are discovered automatically.

**Agents creating skills:**

Agents can also create new skills during their work. For example, after noticing a recurring pattern in how the user wants tests written, an agent might create `.claude/skills/test-patterns/SKILL.md`. This pairs with VOICE.md: the voice captures *who* the agent is, skills capture *how* it works. Skill creation follows the same guardrail pattern -- protected by approval if configured.

## Project Structure

```
anthem/
  cmd/
    anthem/             # CLI entrypoint
      main.go
  internal/
    types/              # Shared domain types (Task, RunResult, etc.)
    config/             # WORKFLOW.md parser, validator, hot-reload
    orchestrator/       # Core loop, concurrency, dispatch, shutdown, event bus
    rules/              # Rules engine, approval flow
    tracker/            # IssueTracker interface
      github/           # GitHub adapter (go-github, GITHUB_TOKEN + gh auth fallback, rate limiting)
      local/            # Local JSON adapter
    workspace/          # Workspace manager, hooks, file safety, VOICE.md copy
    agent/              # AgentRunner interface
      claude/           # Claude Code driver (stream-json, session resume, cross-platform process mgmt)
    voice/              # VOICE.md parser, section merge logic, changelog
    dashboard/          # Embedded HTTP server, templates, API, WebSocket
    logging/            # Structured logger (slog)
    cost/               # Token/cost tracking, budget enforcement
  templates/
    dashboard/          # HTML templates for dashboard
  testdata/             # Test fixtures (workflow.md, voice.md, tasks.json)
  WORKFLOW.md.example   # Example workflow file
  VOICE.md.example      # Example personality file
  README.md
  go.mod
  go.sum
  Makefile
  .github/workflows/ci.yml
  .golangci.yml
```

## CLI Interface

```
anthem init                   # Create starter WORKFLOW.md + bootstrap ~/.anthem/VOICE.md
anthem run                    # Start orchestrator (default: ./WORKFLOW.md)
anthem run -w /path/to.md     # Custom workflow file
anthem run --port 8080        # Override dashboard port
anthem validate               # Validate WORKFLOW.md without starting
anthem status                 # Query running orchestrator's /api/v1/state
anthem version                # Print version
```

## Build Phases

### Phase 1: Foundation (COMPLETE)

Single task end-to-end: poll GitHub Issues, render WORKFLOW.md prompt with constraints, spawn Claude Code, update issue on completion. Includes `--output-format stream-json` integration, session management, cost parsing, ETag caching, rate limit throttling, auto-bootstrap, and two-tier constraints system.

### Phase 2: Go Daemon Reliability Layer (COMPLETE)

All six steps implemented and tested:

1. Rules engine -- TitlePattern regex matching (compiled cache), AutoAssign, MaxCost budget enforcement with cost tracker
2. Workspace manager -- production implementation (per-task dirs, hook lifecycle with retry/warn-only, CleanupTerminal)
3. Retry and backoff -- per-task RetryInfo, exponential backoff capped at max_retry_backoff_ms, stall detection in reconcile
4. Graceful shutdown -- WaitGroup drain (10s timeout), claim release with fresh context, state save
5. State persistence -- atomic write to `~/.anthem/state.json`, LoadAndReconcile on startup (skips terminal tasks)
6. Config hot-reload -- fsnotify watcher with debounce, validates before applying, configSnapshot pattern for goroutines

### Phase 3a: Contract + Audit + Orchestrator Core (COMPLETE)

Intelligence layer built using contract-first, orchestrator-as-allocator architecture. The daemon is the authority; the orchestrator proposes actions via a defined contract. If the orchestrator fails, the daemon falls back to Phase 2 mechanical dispatch.

All 9 steps completed:

1. **Tool contract** (`internal/orchestrator/contract.go`) -- 8 action types (dispatch, skip, comment, update_voice, request_approval, close_wave, create_subtasks, promote_knowledge) with risk classification, ValidateAction, IsIdempotent, SchemaOnly. Schema-only actions (create_subtasks, promote_knowledge) log ErrNotImplemented and skip.
2. **SQLite audit log** (`internal/audit/`) -- append-only event log at `~/.anthem/audit.db` via `modernc.org/sqlite` (pure Go, no CGo). AuditLogger interface: Record, Query, RecentByTask, SummaryForWave, Close. WAL mode, mutex-serialized writes. Injected into Orchestrator, closed on shutdown.
3. **Task lifecycle state machine** (`internal/types/task.go`) -- 10 formalized states (queued, planned, running, blocked, retryQueued, needsApproval, completed, failed, canceled, skipped). Transition(from, to) validation. StatusToLabel/LabelToStatus mapping. TerminalReason field. Reconcile applies external tracker changes directly.
4. **Executor prompt fix** -- removed VOICE.md from buildFullPrompt. Executors get constraints + WORKFLOW.md only. Voice is orchestrator-only.
5. **Agent driver fix** -- config-driven PermissionMode (default dontAsk), DeniedTools, ContinueOpts (workspace, stall timeout, allowed tools, permissions). RunResult.Output populated from stream result text.
6. **Orchestrator session manager** (`internal/orchestrator/orchagent.go`) -- OrchestratorAgent with Start/Consult/Refresh. StateSnapshot builder. parseActions with brace-counting JSON extraction. ConsultWithRepair repair loop. Token tracking for refresh threshold.
7. **Tick loop wiring** -- dirty-snapshot gating (SHA256 hash, skip unchanged). Wave tracking (frontier exhaustion). Fallback to mechanical dispatch. OrchestratorConfig in config.go. main.go creates audit logger + orchestrator agent.
8. **Voice self-evolution** -- update_voice action triggers voice.Merge + changelog + audit event. Updates in-memory voiceContent on Orchestrator and OrchestratorAgent.
9. **Documentation** -- all docs updated to reflect Phase 3a completion.

### Phase 3b: Channels + Task Decomposition + Maintenance

Two-way communication between the orchestrator agent and the user, plus audit-driven maintenance:

1. **Channel system** -- modular `Channel` interface (Kind, Start, Send, Incoming, Close), Channel Manager (Register, Start, Broadcast, Incoming, Close), EventBridge subscriber for outbound event-to-channel routing with configurable event filters and EventFormatter
2. **Slack adapter** -- Socket Mode (pure WebSocket, no HTTP server). Inbound message + file handling, outbound markdown messages. Scopes: `chat:write`, `channels:history`, `channels:read`, `files:read`, `app_mentions:read`
3. **Multi-format task decomposition** -- user sends feature descriptions through channels as prompts, markdown, flowcharts, mermaid diagrams, or images. Orchestrator decomposes into GitHub issues via `create_subtasks` (implements the currently schema-only action). `CreateIssue` added to `IssueTracker` interface
4. **Audit-log maintenance scanner** -- periodic queries on `audit.db` for repeated failures (3+ retries), stale tasks (queued/planned > N hours), budget anomalies (2x avg cost), drift (re-opened completed tasks). Emits `maintenance.suggested` events, routed to channels with user approval gate. Configurable auto-approve per maintenance type
5. **`require_plan` via channel conversation** -- orchestrator proposes plan via `ActionReply`, user approves or adjusts through channel, then dispatch begins. No separate rule mechanism needed
6. **New contract actions** -- `ActionReply` (channel response to user, low risk, idempotent), `ActionRequestMaintenance` (maintenance proposal with approval gate, medium risk)
7. **Configuration** -- global credentials in `~/.anthem/channels.yaml`, per-project channel targets + event filters in WORKFLOW.md `channels:` block, maintenance thresholds in WORKFLOW.md `maintenance:` block
8. **Orchestrator integration** -- `HandleUserMessage` method on Orchestrator for inbound channel messages, extended system prompt for multi-format understanding and reply actions
9. **New packages**: `internal/channel/` (interface, manager, bridge, config), `internal/channel/slack/` (adapter), `internal/maintenance/` (scanner)
10. **New dependency**: `github.com/slack-go/slack`

### Phase 4: Dashboard + Polish + Community

1. Dashboard + status API + WebSocket streaming via EventBus (embedded HTTP server)
2. Knowledge promotion -- `promote_knowledge` action implementation, write execution summaries to `docs/exec-plans/completed/`
3. Plans as first-class DAG artifacts with dependency edges
4. WhatsApp channel adapter (uses dashboard HTTP server for inbound webhooks)
5. Example WORKFLOW.md + VOICE.md templates
6. CONTRIBUTING.md
7. Cross-platform release binaries via GoReleaser (Windows/macOS/Linux), code signing for Windows (SignPath.io or Azure Trusted Signing)
8. Demo video

## Future Enhancements (Post Phase 4)

- **GitHub webhook support**: Alternative to polling for instant task detection and lower API usage. The `IssueTracker` interface doesn't need to change -- `GitHubTracker` could internally support both poll and webhook modes. Webhook mode would require a publicly accessible URL (or a tunneling solution like ngrok for local dev).
- **GitHub App authentication**: For production/org use -- fine-grained permissions, separate rate limits per installation, auto-refreshing tokens.
- **Multi-instance Anthem**: Distributed claim locking for running multiple Anthem instances against the same tracker.

## Reference: OpenAI Symphony

Anthem mirrors many of Symphony's proven design patterns (orchestrator loop, tracker adapters, workspace isolation, config parsing from markdown front matter) while adding a personality-aware orchestrator agent layer that Symphony lacks. Symphony's orchestrator is pure Elixir code with no AI -- Anthem's hybrid architecture adds a Claude orchestrator agent on top of the Go daemon for user communication, task decomposition, and self-evolution.

- Repository: https://github.com/openai/symphony
- Language: Elixir (GenServer-based orchestrator)
- Spec: `SPEC.md` in repo root defines the language-agnostic service specification
