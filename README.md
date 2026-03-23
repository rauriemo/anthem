# Anthem

An open-source agent orchestrator for [Claude Code](https://docs.anthropic.com/en/docs/claude-code). Anthem polls your GitHub issues, dispatches Claude Code agents to work on them, and manages the full lifecycle -- from claiming a task to closing the issue.

## How It Works

1. You create GitHub issues and label them (e.g. `todo`)
2. Anthem polls your repo, picks up labeled issues, and renders a prompt from your `WORKFLOW.md` template
3. Claude Code runs autonomously against each task
4. When done, Anthem updates the labels and closes the issue

```
GitHub Issues ──poll──> Anthem Orchestrator ──dispatch──> Claude Code CLI
     ^                        |                                |
     |                        v                                v
     └── label/close ── Issue Tracker <── result ── stream-json output
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

To embed a version string in the binary:

```bash
go build -ldflags "-X main.version=1.0.0" -o anthem ./cmd/anthem
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
- `~/.anthem/VOICE.md` -- your global agent personality (shared across all projects)
- `~/.anthem/constraints.yaml` -- your global safety rules

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
- **YAML front matter** -- tracker config, polling interval, agent settings, rules
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
3. Render the prompt and spawn Claude Code
4. On completion, label it `done` and close the issue

Press `Ctrl+C` to stop (graceful shutdown).

## CLI Commands

| Command | Description |
|---------|-------------|
| `anthem init` | Create starter WORKFLOW.md + bootstrap ~/.anthem/VOICE.md |
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

### Workspace

```yaml
workspace:
  root: "./workspaces"          # Per-task directories created here

hooks:
  after_create: "git clone {{issue.repo_url}} ."   # Runs once after workspace is created
  before_run: "git pull origin main"                # Runs before each agent run (retries 3x)
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
  max_retry_backoff_ms: 300000  # Max backoff between retries (5 min)
  model: ""                     # Override model (optional)
  allowed_tools: []             # Restrict available tools (optional)
```

### Rules

```yaml
rules:
  - match:
      labels: ["planning"]
    action: require_approval     # Wait for approval_label before dispatch
    approval_label: "approved"
  - match:
      labels: ["bug"]
    action: auto_assign          # Post auto-assign comment
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

### VOICE.md

The global personality file at `~/.anthem/VOICE.md` is prepended to every prompt. It defines your agent's identity and communication style -- pure personality, no safety rules:

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

See [VOICE.md.example](VOICE.md.example) for a full example.

### Constraints

Safety guardrails are separate from personality and cannot be modified by agents. Constraints are defined at two levels:

**User-level** (`~/.anthem/constraints.yaml`) -- global rules that apply to all projects:

```yaml
constraints:
  - "Never force-push to main or master"
  - "Never commit secrets, credentials, API keys, or tokens"
  - "Always create a branch for changes -- never commit directly to main"
```

**Project-level** (`system.constraints` in WORKFLOW.md) -- rules specific to this project:

```yaml
system:
  constraints:
    - "Follow the project existing code style and conventions"
    - "Run tests before opening a PR"
```

Both levels are combined into a single `## Constraints (non-negotiable)` block in the prompt. Anthem always appends a meta-constraint: "Do not modify constraint definitions in WORKFLOW.md system.constraints or ~/.anthem/constraints.yaml" to prevent agents from removing their own guardrails.

## Development

```bash
make build          # Build binary
make test           # Run unit tests
make vet            # Run go vet
make lint           # Run golangci-lint
make test-integration  # Run integration tests (requires GitHub API)
```

## Project Status

**Phase 1** (complete): Core orchestrator loop, GitHub tracker, Claude Code driver, CLI, ETag caching, rate limiting, constraints system. End-to-end functional.

**Phase 2** (complete): Rules engine (TitlePattern, AutoAssign, MaxCost), production workspace manager with hooks, retry with exponential backoff, graceful shutdown, state persistence to `~/.anthem/state.json`, config hot-reload via fsnotify.

Upcoming:
- **Phase 3**: Orchestrator agent (persistent Claude session with VOICE.md personality), tool interface, voice self-evolution, task decomposition, web dashboard, WebSocket event stream
- **Phase 4**: Example templates, CONTRIBUTING.md, cross-platform release binaries via GoReleaser, code signing

## License

MIT
