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
  voice_changes_require_approval: false
  voice_core_immutable: true

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

## Boundaries [CORE]
- Never mass-delete files without explicit confirmation.
- Never force-push to main.
- Always create a branch for changes.
- When unsure, ask rather than guess.
`
