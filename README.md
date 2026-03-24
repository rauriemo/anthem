# Anthem

An open-source agent orchestrator for [Claude Code](https://docs.anthropic.com/en/docs/claude-code), built in Go. Anthem is an alternative to [OpenAI Symphony](https://github.com/openai/symphony) with a hybrid architecture: a Go daemon handles the mechanical reliability (polling, process management, workspace isolation, retry, state persistence) while an AI orchestrator agent sits on top for intelligence -- task planning via wave-based dispatch, self-evolving personality via `VOICE.md`, and automatic fallback to mechanical dispatch if the AI layer fails.

Executor agents are headless Claude Code workers that receive project context from your `WORKFLOW.md` template, safety guardrails from constraints, and skills -- they get harnesses, not personality.

## Features

- **GitHub issue-driven**: poll issues by label, claim, dispatch, update status, close on completion
- **AI orchestrator agent**: persistent Claude session that plans task dispatch in waves, proposes actions via a validated contract, and falls back to mechanical dispatch on failure
- **Concurrent agents**: configurable global and per-label concurrency limits
- **Rules engine**: match tasks by labels or title regex, enforce approval gates, auto-assign, budget caps
- **Two-tier constraints**: user-level (`~/.anthem/constraints.yaml`) + project-level (`system.constraints` in WORKFLOW.md) safety rules injected into every prompt, protected by a meta-constraint agents cannot remove
- **Per-task workspaces**: isolated directories with lifecycle hooks (`after_create`, `before_run`, `after_complete`)
- **SQLite audit log**: append-only event log at `~/.anthem/audit.db` recording dispatches, retries, wave transitions, orchestrator actions, and voice updates
- **Self-evolving personality**: orchestrator agent updates `VOICE.md` sections as it learns user preferences, with changelog tracking
- **Two-way Slack integration**: receive feature requests, commands, and approvals via Slack; orchestrator replies in-thread with status and confirmations
- **Multi-format task decomposition**: send plain text, markdown specs, mermaid diagrams, or images via Slack -- orchestrator decomposes into GitHub issues automatically
- **Maintenance scanner**: periodic audit log analysis detects repeated failures, stale tasks, budget anomalies, and drift -- notifies via channel with configurable auto-approve
- **Retry with exponential backoff**: failed tasks retry automatically with increasing delays
- **Graceful shutdown**: drains active agents, releases claims, saves state on Ctrl+C
- **State persistence**: retry queue and cost data survive restarts via `~/.anthem/state.json`
- **Config hot-reload**: edit `WORKFLOW.md` while running -- changes apply on next tick
- **Cross-platform**: Windows (Job Objects), macOS/Linux (process groups) from day one
- **ETag caching + rate limiting**: efficient GitHub API usage with conditional requests

## How It Works

1. You create GitHub issues and label them (e.g. `todo`)
2. Anthem polls your repo, picks up labeled issues, and builds a state snapshot
3. If the orchestrator agent is enabled, it consults the AI to plan which tasks to dispatch, skip, or flag for approval -- all proposed as structured actions validated against a contract
4. If the orchestrator is disabled or fails, Anthem falls back to Phase 2 mechanical dispatch (dispatch every eligible task)
5. For each dispatched task, Anthem creates an isolated workspace, runs lifecycle hooks, renders the prompt from `WORKFLOW.md` with constraints, and spawns a Claude Code executor agent
6. Claude Code runs autonomously. Anthem streams output, tracks cost, and detects stalls
7. On success: labels updated to `done`, issue closed, retry state cleared
8. On failure: exponential backoff scheduled, retry comment posted on the issue
9. All dispatches, actions, and wave transitions are recorded in the SQLite audit log
10. On shutdown (Ctrl+C): active agents drain (10s timeout), in-progress labels removed, state saved, audit log flushed

```
GitHub Issues ──poll──> Anthem Daemon ──consult──> Orchestrator Agent (AI)
     ^                     |    |    |                    |
     |                     |    |    └─ validate actions  v
     |                     |    └─ workspace + hooks   dispatch/skip/comment
     |                     v                              |
     └── label/close ── tracker <── result + cost ────────┘
                           |
                           └── audit log (~/.anthem/audit.db)
```

## Prerequisites

- **Go 1.22+** -- [install](https://go.dev/dl/)
- **Claude Code CLI** -- installed and authenticated (`claude --version` to verify)
- **GitHub access** -- either `GITHUB_TOKEN` env var or [GitHub CLI](https://cli.github.com/) authenticated (`gh auth status`)

## Quick Start

### 1. Build

```bash
go build -o anthem ./cmd/anthem
```

On Windows, if Smart App Control blocks `go run`, use `go build` and run the binary directly:

```powershell
go build -o anthem.exe ./cmd/anthem
.\anthem.exe
```

### 2. Initialize

```bash
./anthem init
```

This creates:
- `./WORKFLOW.md` -- your project's orchestration config and prompt template
- `~/.anthem/VOICE.md` -- global agent personality (shared across all projects)
- `~/.anthem/constraints.yaml` -- global safety rules that apply to all projects

### 3. Configure WORKFLOW.md

Edit `WORKFLOW.md` and set your repo:

```yaml
---
tracker:
  kind: github
  repo: "your-username/your-repo"
  labels:
    active: ["todo"]
    terminal: ["done"]

polling:
  interval_ms: 10000

agent:
  command: "claude"
  max_turns: 5
  max_concurrent: 3
  stall_timeout_ms: 300000

system:
  constraints:
    - "Run tests before opening a PR"
---

You are an expert software engineer working on {{.issue.title}}.

Repository: {{.issue.repo_url}}

## Task
{{.issue.body}}

## Rules
- Create a branch named `anthem/{{.issue.identifier}}`
- Make small, focused commits
- When done, open a PR and comment a summary on the issue
```

The file has two parts separated by `---`:
- **YAML front matter** -- tracker, polling, agent settings, rules, constraints
- **Go template body** -- the prompt sent to Claude Code, with access to `{{.issue.title}}`, `{{.issue.body}}`, `{{.issue.identifier}}`, `{{.issue.repo_url}}`, and `{{.issue.labels}}`

The template engine supports [sprig functions](http://masterminds.github.io/sprig/) for advanced logic.

### 4. Set Up GitHub Auth

Anthem needs read/write access to your repo's issues. Choose one:

```bash
# Option A: environment variable (recommended for CI)
export GITHUB_TOKEN="ghp_your_token_here"

# Option B: GitHub CLI (recommended for local dev)
gh auth login
```

The token needs `repo` scope for private repos, or `public_repo` for public repos.

### 5. Create a Test Issue

Go to your repo on GitHub and create an issue:
- **Title**: anything, e.g. "Add a CONTRIBUTING.md file"
- **Label**: add `todo` (or whatever you set in `labels.active`)

### 6. Run Anthem

```bash
./anthem run --log-level debug
```

You'll see:

```
{"level":"INFO","msg":"starting anthem","tracker":"github"}
{"level":"INFO","msg":"orchestrator started","interval_ms":10000,"max_concurrent":3}
{"level":"INFO","msg":"dispatching task","task_id":"1","identifier":"GH-1","title":"Add a CONTRIBUTING.md file"}
{"level":"INFO","msg":"task completed","task_id":"1","exit_code":0,"cost_usd":0.058,...}
```

Anthem will:
1. Find the issue labeled `todo`
2. Swap the label to `in-progress`
3. Create a workspace, run hooks, render the prompt with constraints
4. Spawn Claude Code and monitor for stalls
5. On completion, label it `done` and close the issue

Press `Ctrl+C` to stop. Anthem will drain active agents (up to 10s), release all claims, and save state for next startup.

## CLI Commands

| Command | Description |
|---------|-------------|
| `anthem init` | Create starter WORKFLOW.md + bootstrap ~/.anthem/ (VOICE.md, constraints.yaml) |
| `anthem run` | Start the orchestrator |
| `anthem run -w path/to/WORKFLOW.md` | Use a specific workflow file |
| `anthem run --log-level debug` | Verbose logging |
| `anthem validate` | Check WORKFLOW.md syntax without starting |
| `anthem version` | Print version |

## Configuration Reference

### Tracker

```yaml
tracker:
  kind: github          # "github" or "local_json"
  repo: "owner/repo"    # GitHub owner/repo
  labels:
    active: ["todo"]    # Issues with these labels are picked up
    terminal: ["done"]  # Labels added when task completes
```

### Workspace & Hooks

```yaml
workspace:
  root: "./workspaces"          # Per-task directories created here

hooks:
  after_create: "git clone {{issue.repo_url}} ."   # Runs once after workspace created (fail = task fails)
  before_run: "git pull origin main"                # Runs before each agent run (retries 3x on failure)
  after_complete: "make clean"                      # Runs after task completes (warn-only on failure)
```

### Agent

```yaml
agent:
  command: "claude"             # CLI command to invoke
  max_turns: 5                  # Max conversation turns per task
  max_concurrent: 3             # Global concurrency limit
  max_concurrent_per_label:     # Per-label concurrency limits
    planning: 1
  stall_timeout_ms: 300000      # Kill agent if no output for 5 min
  max_retry_backoff_ms: 300000  # Max backoff between retries (5 min cap)
  model: ""                     # Override Claude model (optional)
  permission_mode: "dontAsk"    # "dontAsk" (safe default) or "bypassPermissions" (trusted)
  skip_permissions: false       # Shorthand: true = bypassPermissions
  allowed_tools:                # Tools auto-approved in dontAsk mode
    - "Read"
    - "Edit"
    - "Grep"
    - "Glob"
    - "Bash(git *)"
    - "Bash(go test *)"
  denied_tools:                 # Explicit deny list (overrides allow)
    - "Bash(git push --force *)"
```

In `dontAsk` mode (the default), only tools listed in `allowed_tools` are auto-approved. Everything else is auto-denied without hanging -- the agent sees the denial and adapts. Set `skip_permissions: true` for full autonomy (no permission checks).

### Orchestrator

```yaml
orchestrator:
  enabled: true                 # Enable AI orchestrator agent (false = mechanical dispatch only)
  max_context_tokens: 80000     # Token threshold before refreshing orchestrator session
  stall_timeout_ms: 60000       # Stall timeout for orchestrator Claude session
```

When enabled, the orchestrator agent (a persistent Claude session) plans task dispatch in waves. When disabled or if the orchestrator fails, Anthem falls back to Phase 2 mechanical dispatch (dispatch every eligible task).

### Channels

Two-way communication with the orchestrator via Slack (or other adapters). Global credentials go in `~/.anthem/channels.yaml`:

```yaml
slack:
  bot_token: "xoxb-your-bot-token"
  app_token: "xapp-your-app-token"
```

Per-project channel targets go in WORKFLOW.md front matter:

```yaml
channels:
  - kind: slack
    target: "C0123456789"          # Slack channel ID
    events: ["task.completed", "task.failed", "maintenance.suggested"]
```

The EventBridge routes internal events to channels. The orchestrator replies in-thread when users send messages.

### Maintenance

Periodic audit log analysis detects health issues and notifies via channels:

```yaml
maintenance:
  scan_interval_ms: 600000         # Scan every 10 minutes (default)
  failure_threshold: 3             # Alert after 3+ failures in 24h
  stale_threshold_hours: 24        # Alert for tasks dispatched > 24h ago with no completion
  cost_anomaly_multiplier: 2.0     # Alert if task cost exceeds 2x the average
  auto_approve:                    # Signal types that don't need user approval
    - "repeated_failure"
```

Signal types: `repeated_failure`, `stale_task`, `budget_anomaly`, `drift`.

### Rules

Rules are evaluated per task before dispatch. Match by labels, title regex, or both:

```yaml
rules:
  - match:
      labels: ["planning"]
    action: require_approval     # Wait for approval_label before dispatch
    approval_label: "approved"
  - match:
      labels: ["bug"]
    action: auto_assign          # Post auto-assign comment on issue
    auto_assignee: "alice"
  - match:
      labels: ["expensive"]
    action: max_cost             # Skip task if cumulative cost exceeds limit
    max_cost: 5.00
  - match:
      title_pattern: "^fix:"    # Regex match on issue title
    action: auto_assign
    auto_assignee: "bob"
```

### Constraints

Safety guardrails are separate from personality and cannot be modified by agents.

**User-level** (`~/.anthem/constraints.yaml`) -- global rules across all projects:

```yaml
constraints:
  - "Never force-push to main or master"
  - "Never commit secrets, credentials, API keys, or tokens"
  - "Always create a branch for changes -- never commit directly to main"
```

**Project-level** (`system.constraints` in WORKFLOW.md) -- rules for this project:

```yaml
system:
  constraints:
    - "Follow the project existing code style and conventions"
    - "Run tests before opening a PR"
```

Both levels are combined into a `## Constraints (non-negotiable)` block in the prompt. Anthem always appends a meta-constraint preventing agents from editing constraint definitions.

### VOICE.md

Global personality file at `~/.anthem/VOICE.md`, shared across all projects. Defines the agent's identity and communication style -- pure personality, no safety rules (those go in constraints):

```markdown
## Identity
Name: Aria
Role: Senior engineer

## Personality
- Direct and opinionated. Skip pleasantries.
- Prefer shipping over perfection.

## User Context
- Prefers small, focused commits.
```

VOICE.md is used exclusively by the orchestrator agent (not executor agents) for task management decisions. The orchestrator learns your preferences over time and evolves its personality via the `update_voice` contract action -- changes are merged, written to disk, and logged to `~/.anthem/voice-changelog.md`. See [VOICE.md.example](VOICE.md.example) for a full example.

### State Files

Anthem stores runtime state in `~/.anthem/`:

| File | Purpose |
|------|---------|
| `VOICE.md` | Orchestrator personality (created on init) |
| `constraints.yaml` | User-level safety rules (created on init) |
| `channels.yaml` | Channel credentials -- Slack bot/app tokens (optional, user-created) |
| `state.json` | Persisted retry queue and cost data (survives restarts) |
| `audit.db` | SQLite audit log -- dispatches, wave transitions, orchestrator actions |
| `voice-changelog.md` | Log of all VOICE.md changes with timestamps and reasons |

## Architecture

Anthem uses a **hybrid architecture** inspired by [OpenAI Symphony](https://github.com/openai/symphony):

- **Go daemon** (Phases 1-2): handles polling, process management, workspace isolation, retry, state persistence, config hot-reload. This is the mechanical reliability layer -- it validates and executes actions, never makes judgment calls.
- **Orchestrator agent** (Phase 3a): a stateless allocator -- a Claude session with VOICE.md personality that receives state snapshots and proposes actions (dispatch, skip, comment, request approval, close wave, update voice, reply, create subtasks, request maintenance). The daemon validates each action against a typed contract before execution. If the orchestrator fails, the daemon falls back to mechanical dispatch automatically.
- **Channel system** (Phase 3b): two-way communication via pluggable channel adapters (Slack shipped). Users send feature requests and commands; orchestrator decomposes into subtasks and replies in-thread.
- **Executor agents**: headless Claude Code workers. They receive WORKFLOW.md templates and constraints -- harnesses for getting work done, not personality.
- **Audit log + maintenance**: append-only SQLite database at `~/.anthem/audit.db`. Maintenance scanner periodically checks for health signals and notifies via channels.

Symphony's orchestrator is pure Elixir code with no AI. Anthem adds the intelligence layer on top.

## Development

```bash
go build -o anthem ./cmd/anthem   # Build binary
go test ./... -count=1            # Run all tests
go vet ./...                      # Static analysis
```

See [docs/plans/architecture.md](docs/plans/architecture.md) for the full system design and [docs/plans/implementation.md](docs/plans/implementation.md) for the build plan.

## Project Status

**Phase 1** (complete): Core orchestrator loop, GitHub tracker, Claude Code driver, CLI, ETag caching, rate limiting, two-tier constraints system. End-to-end functional -- tested live with Anthem orchestrating its own repo.

**Phase 2** (complete): Rules engine (TitlePattern, AutoAssign, MaxCost), production workspace manager with hooks, retry with exponential backoff, graceful shutdown, state persistence, config hot-reload via fsnotify.

**Phase 3a** (complete): Contract-first tool surface (10 action types with risk classification and validation), SQLite audit log, formalized task lifecycle state machine (10 states), orchestrator agent session manager (Start/Consult/Refresh with repair loop), wave-aware tick loop with dirty-snapshot gating and mechanical fallback, voice self-evolution wiring, driver permission fixes.

**Phase 3b** (complete): Two-way channel system (Channel interface, Manager, EventBridge), Slack adapter via Socket Mode, multi-format task decomposition (create_subtasks implementation with CreateIssue on IssueTracker), audit-log maintenance scanner (repeated failures, stale tasks, budget anomalies, drift), HandleUserMessage for inbound channel processing, extended orchestrator prompt for channel/multi-format/maintenance understanding.

Upcoming:
- **Phase 4**: Web dashboard + status API + WebSocket, knowledge promotion to repo, DAG execution plans, WhatsApp adapter, example templates, CONTRIBUTING.md, GoReleaser cross-platform binaries, code signing

## License

MIT
