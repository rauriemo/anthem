# Anthem

[![CI](https://github.com/rauriemo/anthem/actions/workflows/ci.yml/badge.svg)](https://github.com/rauriemo/anthem/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26.1+-00ADD8?logo=go)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**Anthem** is an agentic loop orchestrator for [Claude Code](https://docs.anthropic.com/en/docs/claude-code). It runs two layers: an **intelligence layer** (an AI orchestrator that plans, decomposes features, and communicates in your chosen voice) and a **harness layer** (headless Claude Code workers that execute tasks inside isolated workspaces with constraints and guardrails).

Describe a feature -- by voice, Slack message, or GitHub issue -- and Anthem breaks it into tasks, dispatches concurrent coding agents, tracks cost and progress, retries failures, and closes issues when done. The orchestrator makes the decisions; the daemon enforces the rules.

[Design docs](docs/plans/architecture.md) | [Build plan](docs/plans/implementation.md) | [WORKFLOW.md schema](#configuration-reference) | [Dispatch](https://github.com/rauriemo/dispatch) (voice-first client)

> **Safety note:** Anthem runs a coding agent that can edit files and execute commands. Start in a trusted repo, keep `permission_mode: dontAsk` (the default) until you're comfortable, and use [constraints](#constraints) to define non-negotiables. See the [safety model](#permission-model) below.

## Quick Start

```bash
# 1. Install
go install github.com/rauriemo/anthem/cmd/anthem@latest

# 2. Initialize (in the repo you want Anthem to work on)
cd /path/to/your-repo
anthem init

# 3. Edit WORKFLOW.md -- set tracker.repo to your GitHub repo
#    (this is the only line you must change)

# 4. Authenticate GitHub
gh auth login          # or: export GITHUB_TOKEN="ghp_..."

# 5. Create a test issue on GitHub with the label "todo"

# 6. Run
anthem run --log-level info
```

**You're done when:**
1. You see a `dispatching task` log line
2. Your issue gets the `in-progress` label while the agent works
3. On completion the issue receives your terminal label (e.g. `done`) and closes
4. A workspace directory appears under `./workspaces/`

<details>
<summary>Expected log output</summary>

```
{"level":"INFO","msg":"starting anthem","tracker":"github"}
{"level":"INFO","msg":"orchestrator started","interval_ms":10000,"max_concurrent":3}
{"level":"INFO","msg":"dispatching task","task_id":"1","identifier":"GH-1","title":"Add a CONTRIBUTING.md file"}
{"level":"INFO","msg":"task completed","task_id":"1","exit_code":0,"cost_usd":0.058,...}
```
</details>

Press `Ctrl+C` to stop. Anthem drains active agents (up to 10s), releases all claims, and saves state for next startup.

## How It Works

```mermaid
flowchart LR
  U["You\n(Voice / Slack / GitHub)"] -->|describe feature| OA["Orchestrator agent\n(Claude + orchestrator.md\n+ VOICE.md)"]
  OA -->|create issues| GH["GitHub Issues"]
  GH -->|poll| D["Anthem daemon\n(Go)"]
  D -->|dispatch| W[Workspace per task]
  W -->|run| EA["Executor agents\n(Claude Code)"]
  EA -->|"result + cost"| D
  D -->|"label + close"| GH
  D -->|events| AUD["audit.db"]
  D -->|notifications| U
```

1. You describe a feature or goal -- by voice command, Slack message, or GitHub issue with a label (e.g. `todo`)
2. The orchestrator agent (intelligence layer) decomposes it into tasks, creates GitHub issues, and plans dispatch in waves
3. Anthem's Go daemon polls for labeled issues, builds a state snapshot, and validates the orchestrator's proposed actions against a typed contract
4. For each dispatched task: create an isolated workspace, run hooks, render the prompt with constraints, spawn a headless Claude Code worker (harness layer)
5. Claude Code runs autonomously. Anthem streams output, tracks cost, and detects stalls
6. On success: labels updated, issue closed, retry state cleared
7. On failure: exponential backoff, retry comment posted
8. Everything is recorded in the SQLite audit log. Events pushed to connected channels (voice, Slack)

If the orchestrator is disabled or fails, Anthem falls back to mechanical dispatch -- every eligible issue gets dispatched directly.

### Label Lifecycle

```mermaid
stateDiagram-v2
  state "todo" as Todo
  state "in-progress" as InProgress
  state "done" as Done
  [*] --> Todo: issue labeled
  Todo --> InProgress: Anthem claims task\n(adds in-progress, removes other active labels)
  InProgress --> Done: success\n(adds terminal label, closes issue)
  InProgress --> Todo: failure/abort\n(removes in-progress)
```

The `in-progress` label is hard-coded. Include it in your `labels.active` list so Anthem can see tasks that were mid-flight if it restarts.

## Installation

**Recommended** (Go install):
```bash
go install github.com/rauriemo/anthem/cmd/anthem@latest
```

**From source** (for contributors):
```bash
git clone https://github.com/rauriemo/anthem
cd anthem
go build -o anthem ./cmd/anthem
```

On Windows, if Smart App Control blocks `go run`, build and run the binary directly (`go build ./cmd/anthem` then `.\anthem.exe`).

**Requires:** Go 1.26.1+, Claude Code CLI (`claude --version`), GitHub auth (`gh auth status` or `GITHUB_TOKEN`).

## Features

- **GitHub issue-driven**: poll by label, claim, dispatch, update status, close on completion
- **AI orchestrator agent**: persistent Claude session that plans dispatch in waves, proposes actions via a validated contract, falls back to mechanical dispatch on failure
- **Voice commands via [Dispatch](https://github.com/rauriemo/dispatch)**: say "hey anthem, build a login page" -- the orchestrator decomposes and dispatches, responds aloud
- **Two-way Slack integration**: send feature requests, commands, and approvals; orchestrator decomposes into subtasks and replies in-thread
- **Multi-format input**: plain text, markdown specs, mermaid diagrams, or images -- decomposed into GitHub issues
- **Concurrent agents**: configurable global and per-label concurrency
- **Rules engine**: label/title matching, approval gates, auto-assign, budget caps
- **Two-tier constraints**: user-level + project-level safety rules injected into every prompt, protected by a meta-constraint agents cannot remove
- **Per-task workspaces**: isolated directories with lifecycle hooks
- **SQLite audit log**: append-only event log for dispatches, retries, wave transitions, orchestrator actions, voice updates
- **Maintenance scanner**: detects repeated failures, stale tasks, budget anomalies, and drift -- notifies via channel
- **Two-file identity model**: orchestrator character lives in project-specific `agents/orchestrator.md`; shared user knowledge in `~/.anthem/VOICE.md`. `update_voice` routes character sections (Identity, Personality, Your Focus, Coordination) to the orchestrator file and user sections to VOICE.md. Changes logged to `~/.anthem/voice-changelog.md`
- **Retry with backoff**: failed tasks retry with exponential delays
- **State persistence**: retry queue and cost data survive restarts
- **Config hot-reload**: edit WORKFLOW.md while running
- **Graceful shutdown**: drains agents, releases claims, saves state on Ctrl+C
- **Cross-platform**: Windows (Job Objects), macOS/Linux (process groups)
- **Plan / Agent / Build modes**: orchestrator supports three channel modes — Plan (markdown-only planning using a dedicated prompt that omits JSON actions and HTML display), Agent (full JSON actions with plan context), and Build (approved plan → GitHub issue creation). Mode selected by `[system:plan]` / `[system:build]` tags; default is agent
- **Plan-card in chat**: after plan finalization, the orchestrator sends a structured plan-card to the channel — rendered in Prism as a UI card with title, task list, "View Plan" link, model dropdown, and "Build" button for one-click dispatch
- **Model selection**: `[model:claude-xxx]` tags in messages select the Claude model for any path (lean, plan, build, agent). Supports Sonnet, Opus, Haiku across versions
- **Plan storage**: markdown plans saved to `~/.anthem/plans/{project-slug}/` with YAML frontmatter. Plan history and latest draft injected into agent-mode context for seamless plan-to-agent handoff
- **Auto-label on subtask creation**: newly created subtasks auto-receive the first configured active label (e.g. `todo`) if not already present, ensuring immediate visibility in kanban and dispatch
- **Dependency ordinal remapping**: `depends_on` in `create_subtasks` uses 1-based ordinals (e.g. `[1, 2]`); the daemon remaps to real GitHub issue IDs after creation

## CLI Commands

| Command | Description |
|---------|-------------|
| `anthem init` | Create starter WORKFLOW.md, `agents/orchestrator.md`, and bootstrap `~/.anthem/` |
| `anthem run` | Start the orchestrator |
| `anthem run -w path/to/WORKFLOW.md` | Use a specific workflow file |
| `anthem run --log-level debug` | Verbose logging |
| `anthem validate` | Check WORKFLOW.md syntax without starting |
| `anthem version` | Print version |

## Configuration Reference

### Minimal WORKFLOW.md

If you only change one line, change `tracker.repo`:

```yaml
---
tracker:
  kind: github
  repo: "owner/repo"
  labels:
    # Issues with ANY of these labels are eligible.
    # Anthem adds "in-progress" while working and removes other active labels.
    active: ["todo", "in-progress"]
    terminal: ["done"]

polling:
  interval_ms: 10000

workspace:
  root: "./workspaces"

hooks:
  after_create: "git clone {{issue.repo_url}} ."
  before_run: "git pull origin main"

agent:
  command: "claude"
  max_turns: 5
  max_concurrent: 3
  stall_timeout_ms: 300000
  max_retry_backoff_ms: 300000

system:
  constraints:
    - "Never commit secrets or credentials"
    - "Run tests before opening a PR"

server:
  port: 8080
---

You are an expert software engineer working on {{.issue.title}}.

Repository: {{.issue.repo_url}}
Branch: anthem/{{.issue.identifier}}

## Task
{{.issue.body}}

## Rules
- Create a branch named `anthem/{{.issue.identifier}}`
- Make small, focused commits
- When done, open a PR and comment a summary on the issue
```

The file has two parts separated by `---`:
- **YAML front matter** -- tracker, polling, agent, rules, constraints, channels
- **Go template body** -- the prompt sent to Claude Code, with access to `{{.issue.title}}`, `{{.issue.body}}`, `{{.issue.identifier}}`, `{{.issue.repo_url}}`, and `{{.issue.labels}}`

The template engine supports [sprig functions](http://masterminds.github.io/sprig/).

### Permission Model

```yaml
agent:
  permission_mode: "dontAsk"    # Safe default: only allowed_tools run
  allowed_tools:
    - "Read"
    - "Edit"
    - "Grep"
    - "Glob"
    - "Bash(git *)"
    - "Bash(go test *)"
  denied_tools:                 # Explicit deny (overrides allow)
    - "Bash(git push --force *)"
```

In `dontAsk` mode (the default), only tools in `allowed_tools` are auto-approved. Everything else is denied -- the agent sees the denial and adapts. Set `skip_permissions: true` for full autonomy.

### Orchestrator

```yaml
orchestrator:
  enabled: true                 # false = mechanical dispatch only
  max_context_tokens: 80000     # Token threshold before session refresh
  max_turns: 10                 # Turn budget for agent/build mode consults
  plan_max_turns: 25            # Fallback turn budget for simple plan requests
  explorer_max_turns: 10        # Turn budget per explorer subagent
  max_explorers: 5              # Max parallel explorer subagents
```

When enabled, the orchestrator agent (a persistent Claude session) plans task dispatch in waves. When disabled or on failure, Anthem falls back to mechanical dispatch.

Plan mode uses a **three-phase explorer architecture** for deep, evidence-based planning:

1. **Scout** (8 turns) -- the plan agent reads the file tree, identifies 1-5 areas needing focused research
2. **Explore** (parallel) -- the Go daemon spawns parallel Claude Code processes, one per area, each with a focused research question and read-only tools
3. **Synthesize** (10 turns) -- the plan agent receives all explorer findings and produces a plan backed by verified evidence

For trivial requests (scout returns 0 explores), plan mode falls back to a single-run consultation using `plan_max_turns`. All plan mode agents are read-only -- write tools (Write, Edit, MultiEdit) are denied.

Plan output is **markdown-only**: plan mode uses `buildPlanSystemPrompt` which omits JSON actions and HTML display instructions, ensuring the LLM produces structured markdown via `anthem-plan` fenced blocks. After finalization, a **plan-card** is sent to the channel for rendering in Prism's chat UI with a "Build" button for one-click dispatch. All plans are saved to `~/.anthem/plans/`, ensuring plan context is always available when switching to agent or build mode.

### Agent Profiles and Harnesses

Anthem uses a three-layer "Registry + Reference" architecture for agent configuration:

```yaml
agent:
  # Layer 1: MCP Server Registry (capabilities — define once)
  mcp_servers:
    # Unity: prefer Unity's official MCP relay (com.unity.ai.assistant 2.x), not npx.
    # Example (Windows path — use your %USERPROFILE%\.unity\relay\relay_win.exe):
    # unity:
    #   command: "C:/Users/You/.unity/relay/relay_win.exe"
    #   args: ["--mcp"]
    unity:
      command: "npx"
      args: ["-y", "@anthropic/unity-mcp-server"]
    semgrep:
      command: "semgrep-mcp"
      args: ["--config", "auto"]

  # Layer 2: Skill Registry (knowledge — define once)
  skills:
    - "anthem://owasp-checklist"
    - "./skills/unity-patterns"

  # Layer 3: Profiles (compose by reference)
  profiles:
    coder:
      prompt_prefix: "You are a coding agent. Write clean, tested code."
    architect:
      prompt_prefix: "You are an architect agent. Analyze and design."
      denied_tools: ["Write", "Edit", "Bash"]
    security-explorer:
      prompt_prefix: "You are a security research agent..."
      mcp_refs: ["semgrep"]
      skill_refs: ["anthem://owasp-checklist"]
      denied_tools: ["Write", "Edit", "MultiEdit"]
    unity-designer:
      prompt_prefix: "You are a Unity game designer agent..."
      mcp_refs: ["unity"]
      skill_refs: ["./skills/unity-patterns"]
      model: "opus"

  review_enabled: true
  review_max_turns: 3
  review_max_retries: 1
```

**MCP servers** are external tools (Unity Editor via [Unity MCP](https://docs.unity3d.com/Packages/com.unity.ai.assistant@2.0/manual/unity-mcp-overview.html), semgrep, databases) registered by name. **Skills** are SKILL.md knowledge packages (OWASP checklists, coding patterns) Claude Code discovers automatically. **Profiles** compose these by reference — `mcp_refs` and `skill_refs` point to registry entries. Adding a new agent type (animator, database agent, image generator) requires only a YAML profile entry.

**Guest agents** (project `agents/*.md`) can declare their own `mcp_servers` and `allowed_tools`; Anthem merges them with this global registry when dispatching a guest and writes the combined **`{workspace}/.mcp.json`** so Claude Code can attach to Unity and other stdio/HTTP MCP servers.

Before each agent launch, Anthem writes `.mcp.json` and copies skills to `.claude/skills/` in the workspace. Claude Code auto-discovers both via its native three-level progressive loading.

#### Built-in Skills

Anthem ships 6 core skills compiled into the binary via `go:embed`. These are automatically available to all agents without any external dependencies:

| Skill | Purpose | Default profiles |
|-------|---------|-----------------|
| `anthem://test-verifier` | Go test pyramid: tier selection, coverage collection, structured reports | tester, test-explorer |
| `anthem://gh-issue-verifier` | Evidence-based GitHub issue verification with verdicts | Global (all agents) |
| `anthem://code-review` | Structured 4-category review (security, performance, quality, tests) | Global (all agents), security-explorer |
| `anthem://tdd-classicist` | Language-agnostic TDD methodology, test-double taxonomy | tester, debugger, test-explorer |
| `anthem://go-cli` | Go CLI doctrine: command grammar, exit codes, agent-friendly design | coder |
| `anthem://commit-hygiene` | Conventional commits, separation of concerns, PR quality | coder |

Global skills apply to every agent. Profile-specific skills are loaded only when that profile is active. User-added skills in `~/.anthem/skills/` are also supported as a filesystem fallback.

### Channels

Two-way communication with the orchestrator via pluggable channel adapters. Global credentials live in `~/.anthem/channels.yaml`; per-project channel targets go in WORKFLOW.md.

#### Dispatch (voice)

[Dispatch](https://github.com/rauriemo/dispatch) is a voice-first command channel that connects to Anthem over WebSocket. Say "hey anthem" followed by a command -- the orchestrator processes it and speaks the response aloud.

Anthem acts as the server; Dispatch connects in and authenticates with a shared token.

`~/.anthem/channels.yaml`:
```yaml
dispatch:
  token: "your-shared-secret"
```

WORKFLOW.md:
```yaml
channels:
  - kind: dispatch
    target: "localhost:8081"     # Address Anthem listens on
    events: [task.completed, task.failed, maintenance.suggested]
```

#### Slack

`~/.anthem/channels.yaml`:
```yaml
slack:
  bot_token: "xoxb-your-bot-token"
  app_token: "xapp-your-app-token"
```

WORKFLOW.md:
```yaml
channels:
  - kind: slack
    target: "C0123456789"       # Channel ID
    events: [task.completed, task.failed, maintenance.suggested]
```

Requires a Slack app with Socket Mode enabled, `message.channels` event subscription, and bot scopes: `channels:history`, `channels:read`, `chat:write`, `files:read`.

Run `anthem init` to generate a `channels.yaml` template with setup instructions for both adapters.

### Maintenance

Periodic audit log analysis detects health issues and notifies via channels:

```yaml
maintenance:
  scan_interval_ms: 600000       # Every 10 min
  failure_threshold: 3           # Alert after 3+ failures in 24h
  stale_threshold_hours: 24
  cost_anomaly_multiplier: 2.0
  auto_approve: ["repeated_failure"]
```

Signal types: `repeated_failure`, `stale_task`, `budget_anomaly`, `drift`.

### Rules

```yaml
rules:
  - match:
      labels: ["planning"]
    action: require_approval
    approval_label: "approved"
  - match:
      labels: ["bug"]
    action: auto_assign
    auto_assignee: "alice"
  - match:
      title_pattern: "^fix:"
    action: max_cost
    max_cost: 5.00
```

### Constraints

Safety guardrails separate from personality, cannot be modified by agents.

**User-level** (`~/.anthem/constraints.yaml`):
```yaml
constraints:
  - "Never force-push to main or master"
  - "Never commit secrets, credentials, API keys, or tokens"
```

**Project-level** (`system.constraints` in WORKFLOW.md):
```yaml
system:
  constraints:
    - "Run tests before opening a PR"
```

Both levels combine into a `## Constraints (non-negotiable)` block in the prompt. Anthem appends a meta-constraint preventing agents from editing constraint definitions.

### Agent Identity (Two-File Model)

Anthem uses a two-file identity model:

- **`agents/orchestrator.md`** -- Project-specific orchestrator character (Identity, Personality, Your Focus, Coordination). Lives alongside guest agent files in the project's `agents/` directory. Each project's orchestrator develops a unique personality over time. Created by `anthem init` or Forge scaffolding.
- **`~/.anthem/VOICE.md`** -- Global user knowledge shared across all projects. Contains facts and stable preferences about the user (communication style, working habits, expertise) -- not project state and not agent self-description. Acts as an onboarding brief so new agents don't start blind.

```markdown
# agents/orchestrator.md
---
name: "MyProject"
description: "Orchestrator for the MyProject workspace"
role: orchestrator
---

## Identity
Name: Aria
Role: Senior engineer

## Personality
- Direct and opinionated. Skip pleasantries.
- Prefer shipping over perfection.
```

```markdown
# ~/.anthem/VOICE.md
## Communication Style
- Prefers concise responses with code examples over walls of text.

## Working Habits
- Likes to review plans before implementation.
```

The orchestrator evolves both files via the `update_voice` action. Character sections (Identity, Personality, Your Focus, Coordination) route to `agents/orchestrator.md`; user-knowledge sections route to `~/.anthem/VOICE.md`. All routing decisions are logged. Changes to VOICE.md are also logged to `~/.anthem/voice-changelog.md`.

**Migration**: On first run after upgrading, Anthem automatically migrates existing Identity/Personality sections from `~/.anthem/VOICE.md` to `agents/orchestrator.md`. If `orchestrator.md` already exists, migration is a no-op (a warning is logged if stale personality sections remain in VOICE.md).

> **Note**: `orchestrator.md` is not a guest agent. It lives in `agents/` for authoring consistency but is explicitly excluded from the `GuestIndex` and never appears in Prism's guest roster.

### State Files

| File | Purpose |
|------|---------|
| `agents/orchestrator.md` | Project-specific orchestrator character (Identity, Personality, Focus, Coordination) |
| `~/.anthem/VOICE.md` | Shared user knowledge (communication style, habits, expertise) |
| `~/.anthem/constraints.yaml` | User-level safety rules |
| `~/.anthem/channels.yaml` | Channel credentials (Slack tokens, Dispatch shared secret) |
| `~/.anthem/state.json` | Persisted retry queue and cost data |
| `~/.anthem/audit.db` | SQLite audit log |
| `~/.anthem/voice-changelog.md` | Log of VOICE.md changes |
| `~/.anthem/plans/` | Stored plan artifacts (markdown with YAML frontmatter) |

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| `anthem: command not found` | Add `$GOPATH/bin` (or `$GOBIN`) to your PATH |
| `claude` not found | Install Claude Code CLI, verify with `claude --version` |
| No tasks picked up | Ensure your issue has a label from `tracker.labels.active` |
| Tasks stuck as `in-progress` after crash | Rerun Anthem -- it reconciles on startup. Include `in-progress` in `labels.active` |
| Agent can't run a command | Add the command pattern to `allowed_tools` in WORKFLOW.md |
| GitHub auth fails | Check `gh auth status` or verify `GITHUB_TOKEN` has `repo` scope |
| Slack not connecting | Verify Socket Mode is enabled on your Slack app and `app_token` starts with `xapp-` |
| Dispatch not connecting | Check that Anthem is running first (it's the server), tokens match in both `channels.yaml` files, and the `target` address/port is reachable |

## Architecture

Anthem uses a **hybrid architecture** inspired by [OpenAI Symphony](https://github.com/openai/symphony):

- **Go daemon** (Phases 1-2): polling, process management, workspace isolation, retry, state persistence, config hot-reload. Validates and executes actions — never makes judgment calls.
- **Orchestrator agent** (Phase 3a+): three modes of operation — **Plan** (markdown-only planning stored to `~/.anthem/plans/`; sends a plan-card to chat with Build button), **Agent** (JSON actions with full plan + project context, can edit code directly via Claude Code), and **Build** (plan → subtask issue creation). Falls back to mechanical dispatch on failure.
- **Channel system** (Phase 3b): two-way communication via pluggable adapters (Slack, [Dispatch](https://github.com/rauriemo/dispatch) voice, and [Prism](https://github.com/rauriemo/prism) visual workstation). Users send feature requests by text, voice, or file attachment; orchestrator decomposes into subtasks.
- **Executor agents**: headless Claude Code workers with specialist profiles (coder, architect, tester, debugger). Post-execution reviewer loop with automatic debugger retry.
- **Audit log + maintenance**: append-only SQLite at `~/.anthem/audit.db` with decision traces. Scanner detects health signals and notifies via channels.

See [architecture.md](docs/plans/architecture.md) for the full system design with component diagrams and interface definitions.

## Project Status

| Phase | Status | Highlights |
|-------|--------|------------|
| **1** | Complete | Core loop, GitHub tracker, Claude Code driver, CLI, ETag caching, constraints |
| **2** | Complete | Rules engine, workspace manager, retry/backoff, shutdown, state persistence, hot-reload |
| **3a** | Complete | Contract actions (10 types), SQLite audit, task state machine, orchestrator agent, wave dispatch |
| **3b** | Complete | Slack + Dispatch (voice) channels, task decomposition, maintenance scanner, project context for orchestrator |
| **4** | Complete | Frontier -- audit fixes, multi-LLM driver, DAG edges, promote_knowledge, reviewer loop, agent profiles, decision traces, orchestrator codebase awareness |

## Development

```bash
go build ./cmd/anthem        # Build
go test ./... -count=1       # Test
go vet ./...                 # Vet
golangci-lint run ./...      # Lint (matches CI)
```

## Contributing

Contributions welcome. If you're fixing a bug or adding a feature, please open an issue first so we can align on behavior -- especially around safety defaults, permissions, and label semantics.

See [architecture.md](docs/plans/architecture.md) and [implementation.md](docs/plans/implementation.md) for the canonical design.

## License

[MIT](LICENSE)
