# Anthem

An open-source agent orchestrator for [Claude Code](https://docs.anthropic.com/en/docs/claude-code), built in Go. Anthem is an alternative to [OpenAI Symphony](https://github.com/openai/symphony) with a hybrid architecture: a Go daemon handles the mechanical reliability (polling, process management, workspace isolation, retry, state persistence) while an AI orchestrator agent (coming in Phase 3) will sit on top for intelligence -- user communication, task decomposition, and self-evolving personality via `VOICE.md`.

Executor agents are headless Claude Code workers that receive project context from your `WORKFLOW.md` template, safety guardrails from constraints, and skills -- they get harnesses, not personality.

## Features

- **GitHub issue-driven**: poll issues by label, claim, dispatch, update status, close on completion
- **Concurrent agents**: configurable global and per-label concurrency limits
- **Rules engine**: match tasks by labels or title regex, enforce approval gates, auto-assign, budget caps
- **Two-tier constraints**: user-level (`~/.anthem/constraints.yaml`) + project-level (`system.constraints` in WORKFLOW.md) safety rules injected into every prompt, protected by a meta-constraint agents cannot remove
- **Per-task workspaces**: isolated directories with lifecycle hooks (`after_create`, `before_run`, `after_complete`)
- **Retry with exponential backoff**: failed tasks retry automatically with increasing delays
- **Graceful shutdown**: drains active agents, releases claims, saves state on Ctrl+C
- **State persistence**: retry queue and cost data survive restarts via `~/.anthem/state.json`
- **Config hot-reload**: edit `WORKFLOW.md` while running -- changes apply on next tick
- **Cross-platform**: Windows (Job Objects), macOS/Linux (process groups) from day one
- **ETag caching + rate limiting**: efficient GitHub API usage with conditional requests

## How It Works

1. You create GitHub issues and label them (e.g. `todo`)
2. Anthem polls your repo, picks up labeled issues, and evaluates rules (approval, budget, assignment)
3. For each eligible task, Anthem creates an isolated workspace, runs lifecycle hooks, renders the prompt from `WORKFLOW.md` with constraints, and spawns a Claude Code agent
4. Claude Code runs autonomously. Anthem streams output, tracks cost, and detects stalls
5. On success: labels updated to `done`, issue closed, retry state cleared
6. On failure: exponential backoff scheduled, retry comment posted on the issue
7. On shutdown (Ctrl+C): active agents drain (10s timeout), in-progress labels removed, state saved to disk

```
GitHub Issues ──poll──> Anthem Orchestrator ──dispatch──> Claude Code CLI
     ^                     |    |    |                         |
     |                     |    |    └─ rules/budget check     v
     |                     |    └─ workspace + hooks      stream-json
     |                     v                                   |
     └── label/close ── tracker <── result + cost ─────────────┘
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
  allowed_tools: []             # Restrict available tools (optional)
```

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

In Phase 3, VOICE.md will be used exclusively by the orchestrator agent for user communication and task management. The orchestrator will learn your preferences over time and evolve its personality. See [VOICE.md.example](VOICE.md.example) for a full example.

## Architecture

Anthem uses a **hybrid architecture** inspired by [OpenAI Symphony](https://github.com/openai/symphony):

- **Go daemon** (Phases 1-2, complete): handles polling, process management, workspace isolation, retry, state persistence, config hot-reload. This is the mechanical reliability layer -- it never makes judgment calls.
- **Orchestrator agent** (Phase 3, upcoming): a persistent Claude session with VOICE.md personality that sits on top of the daemon. Handles user communication, task decomposition, parallel planning, and voice self-evolution. The daemon exposes a tool interface for the orchestrator agent to call.
- **Executor agents**: headless Claude Code workers. They receive WORKFLOW.md templates, constraints, and skills -- harnesses for getting work done, not personality.

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

Upcoming:
- **Phase 3**: Orchestrator agent (persistent Claude session with VOICE.md personality), tool interface, voice self-evolution, task decomposition, web dashboard, WebSocket event stream
- **Phase 4**: Example templates, CONTRIBUTING.md, cross-platform release binaries via GoReleaser, code signing

## License

MIT
