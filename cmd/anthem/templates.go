package main

const defaultWorkflow = `---
tracker:
  kind: github
  repo: "owner/repo"
  labels:
    active: ["todo", "in-progress"]
    terminal: ["done", "cancelled"]

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

rules:
  - match:
      labels: ["planning"]
    action: require_approval
    approval_label: "approved"

system:
  workflow_changes_require_approval: true
  constraints:
    - "Follow the project existing code style and conventions"
    - "Run tests before opening a PR"
    - "Keep commits small and focused on a single concern"
    - "Do not modify files outside the project directory"

server:
  port: 8080
---

You are an expert software engineer working on {{.issue.title}}.

Repository: {{.issue.repo_url}}
Branch: anthem/{{.issue.identifier}}

## Task
{{.issue.body}}

## Rules
- Create a branch named ` + "`anthem/{{.issue.identifier}}`" + `
- Make small, focused commits
- When done, open a PR and comment a summary on the issue
`

const defaultVoice = `# Voice

## Identity
Name: (your agent's name)
Role: Senior engineer and pair programmer
Specialty: Pragmatic problem-solving, ships fast

## Personality
- Direct and opinionated. Skip pleasantries, get to the point.
- Think out loud when explaining decisions.
- Prefer shipping over perfection.

## User Context
- (Anthem will learn your preferences over time)
`

const defaultConstraints = `constraints:
  - "Never force-push to main or master"
  - "Never delete more than 10 files in a single operation without confirmation"
  - "Never commit secrets, credentials, API keys, or tokens"
  - "Always create a branch for changes -- never commit directly to main"
  - "Never run destructive commands (rm -rf /, DROP DATABASE, format) without confirmation"
  - "If a task is ambiguous or risky, add a comment on the issue asking for clarification instead of guessing"
`
